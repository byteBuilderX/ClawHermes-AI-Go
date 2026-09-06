#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

labels=(
  architecture
  migration
  deployment
  auth-http
  knowledge
  memory
  mcp
  runtime-governance
  frontend-auth
  frontend-supply-chain
  tool-permissions
  code-quality
)
declare -A selected=()
acceptance_mode=short
# go 工具链并行度：负载感知降级，多 worktree 并行 guard 时防止 CPU 打满
PARALLELISM="$(bash scripts/quality/go-parallelism.sh)"

classify_acceptance_path() {
  case "${1#./}" in
    internal/iam/*|api/http/handler/auth_*|pkg/migration/*|pkg/storage/postgres/*schema*|internal/platform/messaging/*|\
    internal/llmgateway/*|internal/mcp/*|pkg/storage/milvus/*|pkg/httpclient/*|\
    monitoring|monitoring/remote|monitoring/remote/*|internal/platform/alerting|internal/platform/alerting/*|\
	    cmd/feishu-alert-adapter|cmd/feishu-alert-adapter/*|cmd/remote-health-monitor|cmd/remote-health-monitor/*|\
	    cmd/e2e-*|cmd/e2e-*/*|internal/platform/e2eattestation/*|internal/platform/e2erunscope/*|\
	    scripts/e2e/*|scripts/quality/test-verification-*|.agents/skills/stratum-e2e-development/*|test/e2e/*|\
	    scripts/deploy-remote-monitoring.sh|.github/workflows/deploy.yml|.github/workflows/reconcile-monitoring.yml|\
	    .github/workflows/remote-health-monitor.yml)
      acceptance_mode=soak
      ;;
  esac
}

select_all() {
  local label
  for label in "${labels[@]}"; do
    selected["${label}"]=1
  done
}

select_for_path() {
  local path="$1"
  case "${path}" in
    *.go|.golangci.yml|scripts/quality/code-quality-*|.pre-commit-config.yaml|Makefile|.github/workflows/ci.yml)
      selected[code-quality]=1
      ;;
  esac
  case "${path}" in
    api/wiring/*|.golangci.yml)
      selected[architecture]=1
      ;;
  esac
  case "${path}" in
    internal/migration/*|pkg/migration/*|pkg/storage/postgres/*schema*|scripts/quality/check-migration-boundaries*)
      selected[migration]=1
      ;;
  esac
  case "${path}" in
    helm/*|k8s/*|.github/workflows/deploy.yml|.github/workflows/reconcile-monitoring.yml|scripts/quality/check-deployment-safety*)
      selected[deployment]=1
      ;;
  esac
  case "${path}" in
    api/http/*|internal/iam/*)
      selected[auth-http]=1
      ;;
  esac
  case "${path}" in
    internal/knowledge/*|pkg/storage/milvus/*|pkg/vector/*)
      selected[knowledge]=1
      ;;
  esac
  case "${path}" in
    internal/memory/*)
      selected[memory]=1
      ;;
  esac
  case "${path}" in
    internal/mcp/*)
      selected[mcp]=1
      ;;
  esac
  case "${path}" in
    api/middleware/*|cmd/server/*)
      selected[runtime-governance]=1
      ;;
  esac
  case "${path}" in
    web/src/modules/iam/*)
      selected[frontend-auth]=1
      ;;
  esac
  case "${path}" in
    web/package.json|web/package-lock.json)
      selected[frontend-supply-chain]=1
      ;;
  esac
  case "${path}" in
    internal/agent/*|internal/mcp/*|internal/skill/*|internal/iam/*|api/http/*|api/wiring/agent.go|web/src/modules/agent/*)
      selected[tool-permissions]=1
      ;;
  esac
}

run_check() {
  local label="$1"
  shift
  printf 'risk regression guard: %s\n' "${label}"
  if [[ -n "${RISK_GUARD_EXECUTOR:-}" ]]; then
    /bin/bash "${RISK_GUARD_EXECUTOR}" "${label}" "$@"
    return
  fi
  "$@"
}

if [[ "${1:-}" == "--acceptance" ]]; then
  shift
  for path in "$@"; do
    classify_acceptance_path "$path"
  done
  printf '%s\n' "$acceptance_mode"
  exit 0
fi

if [[ "${1:-}" == "--explain" ]]; then
  if [[ "$#" -ne 1 ]]; then
    echo 'usage: risk-regression-guard.sh --explain' >&2
    exit 2
  fi
  cat <<'EOF'
高风险编码检查表：
- 授权、租户状态或外部依赖查询失败时必须 fail closed，禁止默认角色或默认放行。
- bearer credential 不得进入 URL、Web Storage、通用请求日志或下游错误正文。
- tenant-scoped 操作必须显式携带并校验 tenant ID，数据库访问必须经过租户边界封装。
- 请求和启动路径禁止自动执行 DropCollection、不可逆清理或其他破坏性数据修复。
- 持久化失败必须向上传播；失败状态写回失败也必须暴露。
- 替换连接、client 或 worker 时必须关闭旧资源，并等待所属 goroutine 退出。
- 认证、租户、迁移、消息、向量库或外部依赖改动必须增加失败路径和真实链路验证。

自动报告只是候选证据，必须按当前代码、测试和运行结果复核。
提交前运行：make risk-guardrails
本地系统验收：普通功能改动要求 short；认证、租户迁移、消息、向量库或外部依赖改动要求 soak。
EOF
  exit 0
fi

if [[ "${1:-}" == "--all" ]]; then
  if [[ "$#" -ne 1 ]]; then
    echo 'usage: risk-regression-guard.sh [--all | changed-file ...]' >&2
    exit 2
  fi
  select_all
elif [[ "$#" -gt 0 ]]; then
  for path in "$@"; do
    select_for_path "${path#./}"
  done
else
  while IFS= read -r path; do
    [[ -n "${path}" ]] && select_for_path "${path#./}"
  done < <(git diff --cached --name-only --diff-filter=ACMR)
fi

if [[ "${#selected[@]}" -eq 0 ]]; then
  echo 'risk regression guard: no relevant changes'
  exit 0
fi

for label in "${labels[@]}"; do
  [[ -n "${selected[${label}]:-}" ]] || continue
  case "${label}" in
    architecture)
      run_check "${label}" /bin/bash -c \
        'bash scripts/quality/arch-guard-test.sh && bash scripts/quality/arch-guard.sh api/wiring/*.go'
      ;;
    migration)
      run_check "${label}" /bin/bash -c \
        "bash scripts/quality/check-migration-boundaries-test.sh && bash scripts/quality/check-migration-boundaries.sh && go test -p ${PARALLELISM} ./pkg/storage/postgres ./pkg/tenantdb"
      ;;
    deployment)
      run_check "${label}" /bin/bash -c \
        'bash scripts/quality/check-deployment-safety-test.sh && bash scripts/quality/release-verification-test.sh'
      ;;
    auth-http)
      run_check "${label}" go test -p "${PARALLELISM}" ./api/http/... ./internal/iam/...
      ;;
    knowledge)
      run_check "${label}" go test -p "${PARALLELISM}" ./internal/knowledge/... ./pkg/storage/milvus
      ;;
    memory)
      run_check "${label}" go test -p "${PARALLELISM}" ./internal/memory/...
      ;;
    mcp)
      run_check "${label}" go test -p "${PARALLELISM}" ./internal/mcp/...
      ;;
    runtime-governance)
      run_check "${label}" go test -p "${PARALLELISM}" ./api/middleware ./api/http ./cmd/server
      ;;
    frontend-auth)
      run_check "${label}" /bin/bash -c \
        'npm --prefix web run typecheck && if command -v stratum-verify >/dev/null 2>&1; then stratum-verify frontend-test; else npm --prefix web test -- --run --maxWorkers=2; fi'
      ;;
    frontend-supply-chain)
      # npm 官方 2026-09 退役 registry 的 quick-audit 端点(/-/npm/v1/security/
      # audits/quick → 400)；Node 22 自带 npm 10 仍打该端点。用 npm@latest
      # 跑 audit(走 bulk advisory 端点),等价审计语义,不依赖 runner npm 版本。
      run_check "${label}" npx --yes npm@latest --prefix web audit --audit-level=high
      ;;
    tool-permissions)
      run_check "${label}" make tool-permission-test
      ;;
    code-quality)
      run_check "${label}" /bin/bash scripts/quality/code-quality-ratchet.sh --all
      ;;
  esac
done

echo 'risk regression guard: passed'
