#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
mode=${1:-short}; profile=${STATEFUL_E2E_PROFILE:-}; duration=${STATEFUL_E2E_DURATION_SEC:-}
case "$mode" in
  short) [[ -z "$profile" ]] || { printf 'short mode cannot declare an acceptance profile\n' >&2; exit 2; }; duration=${duration:-600} ;;
  soak) profile=${profile:-test}; [[ "$profile" == test || "$profile" == release ]] || { printf 'unsupported stateful E2E profile: %s\n' "$profile" >&2; exit 2; }; duration=${duration:-$([[ "$profile" == release ]] && printf 3600 || printf 600)} ;;
  *) printf 'unsupported stateful E2E mode: %s\n' "$mode" >&2; exit 2 ;;
esac
[[ "$duration" =~ ^[0-9]+$ ]] && ((duration >= 600 && duration <= 14400)) || { printf 'STATEFUL_E2E_DURATION_SEC must be between 600 and 14400\n' >&2; exit 2; }
[[ "$profile" != release || "$duration" -ge 3600 ]] || { printf 'release profile requires at least 3600 seconds\n' >&2; exit 2; }

all_packs='dashboard,iam,workflow,agent,skill,mcp,agent-skill-mcp,knowledge,memory,audit,evaluation,agent-context,evaluation-promotion,llm-admin,operation-gate,collab,scheduled-task'
packs=${STATEFUL_E2E_PACKS:-all}; [[ "$packs" == all ]] && packs=$all_packs
IFS=',' read -r -a selected_packs <<<"$packs"
for pack in "${selected_packs[@]}"; do [[ ",$all_packs," == *",$pack,"* ]] || { printf 'unknown stateful E2E pack: %s\n' "$pack" >&2; exit 2; }; done

common_git_dir=$(cd "$repo_dir" && git rev-parse --path-format=absolute --git-common-dir)
env_file=${STATEFUL_E2E_ENV_FILE:-$(dirname "$common_git_dir")/.env}
if [[ -r "$env_file" ]]; then set -a; source "$env_file"; set +a; fi
base_dsn=${TEST_DATABASE_URL:-${STRATUM_TEST_POSTGRES_URL:-postgres://stratum:stratum@127.0.0.1:5432/stratum_e2e?sslmode=disable}}
registry_root=${STATEFUL_E2E_REGISTRY_ROOT:-${TMPDIR:-/tmp}/stratum-stateful-e2e}
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/stratum-stateful-run.XXXXXX")
scope_file=$work_dir/scope.json; results_path=${STATEFUL_E2E_RESULTS_PATH:-$work_dir/safe-results.json}
scope_command=${STATEFUL_E2E_SCOPE_COMMAND:-"go run ./cmd/e2e-run-scope"}
oauth_pid= mcp_pid= backend_pid= frontend_pid=; lease_registered=false; database_created=false
database_dropped=false; lease_removed=false; cleanup_done=false
infra_started_unmarked=false
infra_up_command=${STATEFUL_E2E_INFRA_UP_COMMAND:-"make -C '$repo_dir' infra-up"}
infra_wait_command=${STATEFUL_E2E_INFRA_WAIT_COMMAND:-"make -C '$repo_dir' infra-wait"}
infra_down_command=${STATEFUL_E2E_INFRA_DOWN_COMMAND:-"make -C '$repo_dir' infra-down"}
phase=initialization

run_scope() { (cd "$repo_dir" && TEST_DATABASE_URL="$base_dsn" bash -c "$scope_command $*"); }

stop_process() {
  local pid=${1:-} iteration
  [[ -n "$pid" ]] || return 0
  kill -TERM -- "-$pid" 2>/dev/null || true
  for iteration in $(seq 1 $((child_term_timeout_sec * 10))); do
    pgrep -g "$pid" >/dev/null || { wait "$pid" 2>/dev/null || true; return 0; }
    sleep 0.1
  done
  kill -KILL -- "-$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
export_failure_logs() {
  local target=${STATEFUL_E2E_FAILURE_LOG_DIR:-} log
  [[ -n "$target" ]] || return 0
  mkdir -p "$target"; chmod 700 "$target"
  for log in oauth mcp backend frontend; do
    [[ -f "$work_dir/$log.log" ]] && install -m 600 "$work_dir/$log.log" "$target/$log.log"
  done
}
infra_ready() {
  if [[ -n "${STATEFUL_E2E_INFRA_HEALTH_COMMAND:-}" ]]; then
    bash -c "$STATEFUL_E2E_INFRA_HEALTH_COMMAND"
    return
  fi
  local port
  for port in 5432 6379 4222 19530; do
    timeout 1 bash -c "</dev/tcp/127.0.0.1/$port" 2>/dev/null || return 1
  done
}
cleanup_owned() {
  [[ "$cleanup_done" == true ]] && return 0
  local status=0 release_json=$work_dir/release.json
  stop_process "$frontend_pid"; stop_process "$backend_pid"
  stop_process "$mcp_pid"; stop_process "$oauth_pid"
  if [[ "$database_created" == true && "$database_dropped" != true ]]; then
    if ! run_scope "drop-database --scope '$scope_file' --base-dsn-env TEST_DATABASE_URL"; then
      printf 'stateful E2E residual database: %s; lease: %s/runs/%s.json\n' \
        "$database_name" "$registry_root" "$run_id" >&2
      status=1
    fi
    [[ "$status" -ne 0 ]] || database_dropped=true
  else database_dropped=true; fi
  if [[ "$lease_registered" == true && "$database_dropped" == true ]]; then
    exec 9<"$registry_root"; flock 9
    if [[ "$lease_removed" != true ]]; then
      run_scope "release --scope '$scope_file' --registry '$registry_root'" >"$release_json" || status=1
    fi
    if [[ "$status" -eq 0 && "$lease_removed" != true ]]; then
      lease_removed=true
    fi
    if [[ "$status" -eq 0 && -f "$release_json" ]] && \
      jq -e '.stop_owned_infrastructure == true' "$release_json" >/dev/null; then
      bash -c "$infra_down_command" || status=1
      if [[ "$status" -eq 0 ]]; then
        owner=$(jq -er .ownership_run_id "$release_json")
        run_scope "confirm-infrastructure-stopped --ownership-run-id '$owner' --registry '$registry_root'" || status=1
      fi
    fi
    if [[ "$infra_started_unmarked" == true ]]; then
      bash -c "$infra_down_command" || status=1
      [[ "$status" -ne 0 ]] || infra_started_unmarked=false
    fi
    flock -u 9
  elif [[ "$lease_registered" == false ]]; then lease_removed=true
  fi
  [[ "$status" -ne 0 ]] || cleanup_done=true
  return "$status"
}
on_exit() {
  local primary=$? cleanup_status=0 log_status=0
  cleanup_owned || cleanup_status=$?
  ((primary == 0 && cleanup_status == 0)) || export_failure_logs || log_status=$?
  rm -rf "$work_dir"
  ((primary != 0)) && exit "$primary"
  ((cleanup_status != 0)) && exit "$cleanup_status"
  exit "$log_status"
}
trap on_exit EXIT; trap 'exit 130' INT; trap 'exit 143' TERM
trap 'status=$?; printf "stateful E2E failed during %s\n" "$phase" >&2; exit "$status"' ERR

reset_attempt() {
  oauth_pid=; mcp_pid=; backend_pid=; frontend_pid=
  lease_registered=false; database_created=false; database_dropped=false; lease_removed=false; cleanup_done=false
}
allocate_scope() {
  phase=scope-allocation
  exec 9<"$registry_root"; flock 9
  run_scope "reap --registry '$registry_root' --base-dsn-env TEST_DATABASE_URL" >/dev/null
  run_scope "allocate --repository '$repo_dir' --registry '$registry_root' --owner-pid '$$'" >"$scope_file"
  lease_registered=true
  if ! infra_ready; then
    infra_started_unmarked=true
    if ! bash -c "$infra_up_command"; then
      if ! bash -c "$infra_down_command"; then flock -u 9; return 1; fi
      infra_started_unmarked=false
      flock -u 9
      return 1
    fi
    if ! bash -c "$infra_wait_command" || ! infra_ready; then
      printf 'shared E2E infrastructure failed readiness\n' >&2
      if ! bash -c "$infra_down_command"; then flock -u 9; return 1; fi
      infra_started_unmarked=false
      flock -u 9
      return 1
    fi
    if ! run_scope "mark-infrastructure-owned --scope '$scope_file' --registry '$registry_root'"; then
      if ! bash -c "$infra_down_command"; then flock -u 9; return 1; fi
      infra_started_unmarked=false
      flock -u 9
      return 1
    fi
    infra_started_unmarked=false
  fi
  flock -u 9
}
load_scope() {
  phase=scope-validation
  run_scope "validate --scope '$scope_file'"
  jq -e '.schema_version == 2 and (.run_id|type=="string") and (.database_name|type=="string") and ([.ports[]]|length==4) and ([.ports[]]|unique|length==4)' "$scope_file" >/dev/null
  run_id=$(jq -er .run_id "$scope_file"); database_name=$(jq -er .database_name "$scope_file")
  frontend_port=$(jq -er .ports.frontend "$scope_file"); backend_port=$(jq -er .ports.backend "$scope_file")
  oauth_port=$(jq -er .ports.oauth "$scope_file"); fixture_port=$(jq -er .ports.fixture "$scope_file")
  export E2E_API_URL="http://127.0.0.1:$backend_port" E2E_WEB_URL="http://127.0.0.1:$frontend_port"
  export E2E_FIXTURE_URL="http://127.0.0.1:$fixture_port" E2E_RUN_INSTANCE_ID=$run_id
  export GITHUB_CALLBACK_URL="$E2E_API_URL/auth/github/callback"
  export GITHUB_AUTHORIZE_URL="http://127.0.0.1:$oauth_port/login/oauth/authorize"
  export GITHUB_TOKEN_URL="http://127.0.0.1:$oauth_port/login/oauth/access_token" GITHUB_USER_URL="http://127.0.0.1:$oauth_port/user"
  export QWEN_BASE_URL="$E2E_FIXTURE_URL/v1" E2E_GITHUB_LISTEN_ADDRESS="127.0.0.1:$oauth_port" E2E_MCP_LISTEN_ADDRESS="127.0.0.1:$fixture_port"
}
prepare_database() {
  phase=database-creation
  run_scope "create-database --scope '$scope_file' --base-dsn-env TEST_DATABASE_URL"
  database_created=true
  TEST_DATABASE_URL=$(run_scope "database-url --base-dsn-env TEST_DATABASE_URL --database-name '$database_name'")
  export TEST_DATABASE_URL STRATUM_TEST_POSTGRES_URL=$TEST_DATABASE_URL POSTGRES_URL=$TEST_DATABASE_URL
  phase=migration
  migration_command=${STATEFUL_E2E_MIGRATION_COMMAND:-"cd '$repo_dir' && go run ./cmd/migrate-public --sql-dir '$repo_dir/pkg/migration/sql'"}
  bash -c "$migration_command"
  phase=platform-params-seed
  # 提示词平台化（fail-closed）前提：agent.system_prompt / agent.compaction_prompt
  # 未配置时 agent 执行/压缩 fail-closed，与生产一致。E2E 环境在此预置测试值。
  # P1 后读取已切到 production label 快照（platform_config_versions /
  # platform_config_labels），直写 platform_settings 不再生效。seed 填充 agent 组
  # version-1 快照（backfill 生成的空快照），production/latest label 已指向
  # version-1，等价于系统在初始空配置上预置测试提示词。
  seed_command=${STATEFUL_E2E_PLATFORM_PARAMS_SEED_COMMAND:-"pg=\$(docker ps --format '{{.Names}} {{.Ports}}' | awk '/0.0.0.0:5432->/{print \$1; exit}'); [ -n \"\$pg\" ] && docker exec -i \"\$pg\" psql \"$TEST_DATABASE_URL\" -v ON_ERROR_STOP=1"}
  bash -c "$seed_command" <<'SQL' || return 1
UPDATE public.platform_config_versions
SET snapshot = '{
  "agent.system_prompt": "你是 Stratum E2E 测试助手。回答前优先调用可用工具验证，禁止编造。\n\n## 工具调用\n- 回答事实性问题前必须先调用工具。\n- 工具失败时如实说明，禁止声称成功。",
  "agent.compaction_prompt": "你是对话历史压缩器。把以下对话压成要点摘要，保留关键事实、已达成的决定与未解决问题，只输出摘要正文。"
}'::jsonb,
    message = 'stateful-e2e platform params seed',
    created_by = 'stateful-e2e'
WHERE group_key = 'agent'
  AND version_seq = 1
  AND status = 'published';
SQL
}
start_services() {
  phase=service-startup
start_child() { local variable=$1 command=$2 log=$3 pid; setsid bash -c "$command" >"$log" 2>&1 & pid=$!; printf -v "$variable" '%s' "$pid"; }
start_child oauth_pid "${STATEFUL_E2E_OAUTH_COMMAND:-cd '$repo_dir' && go run ./cmd/e2e-github-oauth}" "$work_dir/oauth.log"
start_child mcp_pid "${STATEFUL_E2E_MCP_COMMAND:-cd '$repo_dir' && go run ./cmd/e2e-mcp-server}" "$work_dir/mcp.log"
start_child backend_pid "${STATEFUL_E2E_BACKEND_COMMAND:-cd '$repo_dir' && FRONTEND_URL='$E2E_WEB_URL' OPIK_URL='$E2E_FIXTURE_URL/opik' PORT='$backend_port' SECURE_COOKIES=false MCP_ALLOW_PRIVATE_TARGETS=true TRACE_PAYLOAD_ENDPOINT='127.0.0.1:9000' TRACE_PAYLOAD_ACCESS_KEY='minioadmin' TRACE_PAYLOAD_SECRET_KEY='minioadmin' TRACE_PAYLOAD_BUCKET='stratum-trace-evidence' TRACE_PAYLOAD_USE_TLS=false go run ./cmd/server}" "$work_dir/backend.log"
start_child frontend_pid "${STATEFUL_E2E_FRONTEND_COMMAND:-cd '$repo_dir/web' && CI=1 VITE_API_BASE_URL='$E2E_API_URL' npm run dev -- --host 127.0.0.1 --port '$frontend_port' --strictPort}" "$work_dir/frontend.log"
poll() { local label=$1 command=$2; for _ in $(seq 1 "${STATEFUL_E2E_HEALTH_ATTEMPTS:-120}"); do bash -c "$command" >/dev/null 2>&1 && return 0; sleep 1; done; printf '%s failed health check\n' "$label" >&2; return 1; }
poll oauth "${STATEFUL_E2E_OAUTH_HEALTH_COMMAND:-curl -fsS -D - -H 'X-Stratum-E2E-Instance: $run_id' 'http://127.0.0.1:$oauth_port/health' | grep -Fi 'X-Stratum-E2E-Instance: $run_id'}" || return 1
poll MCP "${STATEFUL_E2E_MCP_HEALTH_COMMAND:-curl -fsS -D - -H 'X-Stratum-E2E-Instance: $run_id' '$E2E_FIXTURE_URL/health' | grep -Fi 'X-Stratum-E2E-Instance: $run_id'}" || return 1
poll backend "${STATEFUL_E2E_BACKEND_HEALTH_COMMAND:-curl -fsS '$E2E_API_URL/health'}" || return 1
poll frontend "${STATEFUL_E2E_FRONTEND_HEALTH_COMMAND:-curl -fsS '$E2E_WEB_URL/'}" || return 1
for pid in "$oauth_pid" "$mcp_pid" "$backend_pid" "$frontend_pid"; do
  kill -0 "$pid" 2>/dev/null || { printf 'stateful E2E child exited before browser execution\n' >&2; return 1; }
done
}

port_attempts=${STATEFUL_E2E_PORT_ALLOCATION_ATTEMPTS:-3}
[[ "$port_attempts" =~ ^[1-9][0-9]*$ ]] && ((port_attempts <= 10)) || {
  printf 'STATEFUL_E2E_PORT_ALLOCATION_ATTEMPTS must be between 1 and 10\n' >&2; exit 2
}
child_term_timeout_sec=${STATEFUL_E2E_CHILD_TERM_TIMEOUT_SEC:-5}
[[ "$child_term_timeout_sec" =~ ^[1-9][0-9]*$ ]] && ((child_term_timeout_sec <= 30)) || {
  printf 'STATEFUL_E2E_CHILD_TERM_TIMEOUT_SEC must be between 1 and 30\n' >&2; exit 2
}
phase=source-digest
run_scope "prepare-registry --registry '$registry_root'"
digest_command=${STATEFUL_E2E_DIGEST_COMMAND:-"go run ./cmd/e2e-attestation digest --root ."}
source_before=$(cd "$repo_dir" && bash -c "$digest_command")
[[ -n "${JWT_PRIVATE_KEY_PEM:-}" ]] || { JWT_PRIVATE_KEY_PEM=$(openssl genrsa 2048 2>/dev/null); export JWT_PRIVATE_KEY_PEM; }
export STRATUM_E2E_MODE=true GITHUB_CLIENT_ID=stratum-stateful-e2e GITHUB_CLIENT_SECRET=${GITHUB_CLIENT_SECRET:-$(openssl rand -hex 32)}
for attempt in $(seq 1 "$port_attempts"); do
  reset_attempt
  allocate_scope
  load_scope
  export E2E_GITHUB_ID=${E2E_GITHUB_ID:-730001} E2E_GITHUB_LOGIN=stateful-oauth-$run_id E2E_GITHUB_EMAIL=stateful-oauth-$run_id@example.test
  prepare_database
  if start_services; then break; fi
  printf 'stateful E2E service startup attempt %d/%d failed\n' "$attempt" "$port_attempts" >&2
  cleanup_owned || { printf 'stateful E2E retry cleanup failed\n' >&2; exit 1; }
  ((attempt < port_attempts)) || { printf 'stateful E2E service startup retries exhausted\n' >&2; exit 1; }
done

phase=browser
export STATEFUL_E2E_MODE=$mode STATEFUL_E2E_DURATION_SEC=$duration STATEFUL_E2E_PACKS=$packs STATEFUL_E2E_RESULTS_PATH=$results_path
[[ "$mode" == soak ]] && export STATEFUL_E2E_PROFILE=$profile || unset STATEFUL_E2E_PROFILE
bash -c "${STATEFUL_E2E_PLAYWRIGHT_COMMAND:-cd '$repo_dir/web' && npx playwright test --config playwright.stateful.config.ts}"
jq -e '.status == "passed" and .cleanup.passed and (.unverified_capabilities|length==0) and all(.packs[]; .status == "passed") and all(.capabilities[]; .status == "passed")' "$results_path" >/dev/null
phase=cleanup
cleanup_owned
[[ "$database_dropped" == true && "$lease_removed" == true ]] || { printf 'owned cleanup incomplete\n' >&2; exit 1; }
source_after=$(cd "$repo_dir" && bash -c "$digest_command")
[[ "$source_before" == "$source_after" ]] || { printf 'covered source changed during stateful E2E execution\n' >&2; exit 1; }
jq --arg run "$run_id" --arg db "$database_name" --argjson fp "$frontend_port" --argjson bp "$backend_port" \
  --argjson op "$oauth_port" --argjson xp "$fixture_port" \
  '. + {run_topology:{run_id:$run,host:"127.0.0.1",ports:{frontend:$fp,backend:$bp,oauth:$op,fixture:$xp},database_name:$db},owned_cleanup:{database_dropped:true,lease_removed:true}}' \
  "$results_path" >"$work_dir/results-v2.json"
mv "$work_dir/results-v2.json" "$results_path"
phase=attestation
attestation_profile=
[[ "$mode" == soak ]] && attestation_profile=" --profile '$profile'"
attestation_command=${STATEFUL_E2E_ATTESTATION_COMMAND:-"cd '$repo_dir' && go run ./cmd/e2e-attestation generate --input '$results_path' --output-dir 'test/e2e/attestations/$run_id'$attestation_profile"}
bash -c "$attestation_command"
trap - EXIT; rm -rf "$work_dir"
