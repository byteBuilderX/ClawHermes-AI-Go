# 法官/优化器提示词平台参数化 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把评测法官默认判定准则与优化器系统提示词暴露为平台参数（`evaluation.judge.rubric`、`evaluation.optimizer.system_prompt`），在「平台管理 → 平台参数」可见可配，默认与现状 byte-identical。

**Architecture:** 文本单一来源上移 `pkg/constants/evaluation.go`；注册表两键默认值引用常量；消费端（`judgeAdapter.judgeRubric`、`gatewayPromptRewriter.Rewrite`）按「用例自声明 > run 快照 > 当前平台值 > 常量兜底」读取；优化器 user 侧追加固定 JSON 数组防御契约行。spec：`docs/superpowers/specs/2026-09-04-judge-optimizer-prompt-params-design.md`。

**Tech Stack:** Go（parameters registry / pkg/constants / api/wiring）、现有参数测试桩（`fakePlatformStore`、`agentLLMStub`）。

## Global Constraints

- 无 DB DDL / migration / `pkg/migration/sql/`；无 `.proto` 契约改动 → 无需 `make proto-gen`。
- 无前端改动（平台参数页 schema-driven 自动渲染新键）。若 `make fe-lint`/`make fe-build` 出 diff 是异常，停下排查。
- 两键均 `Scope: ScopePlatform`、`Category: "evaluation"`、`ValueType: TypeString`、`VisualHint{Control: ControlTextarea}`、`Optimizable: false`。
- 默认值与内置文本 byte-identical（逐字迁移，禁止改写措辞）；空值/缺键一律回退常量，永不 fail-closed 空态漂移。
- 法官 system 行（`你是评测法官。只输出 JSON，不输出其他内容。`）保持代码内联、**不**参数化。
- 读取优先级固定：用例自声明 > run 快照 > 当前平台值 > `pkg/constants` 兜底。
- 新增代码须满足质量门禁：圈复杂度 ≤10、认知复杂度 ≤15、行数 ≤120、嵌套 ≤4；文本常量/提示词内容不做数值常量改造。
- 遵循既有测试风格：读 `api/wiring/evaluation_judge_adapter_test.go`（优先级表驱动）、`internal/parameters/domain/registry_test.go` 模板，复用 `fakePlatformStore`/`evalSnapshotWith` helper，禁止复制粘贴断言。
- Commit 在 `feat/judge-optimizer-prompt-params` 分支；禁止提交到 main。

---

### Task 1: 提示词文本单一来源上移 `pkg/constants/evaluation.go`

**Files:**

- Modify: `pkg/constants/evaluation.go`（追加两常量）
- Modify: `api/wiring/evaluation.go`（删本地 const + 改引用）
- Modify: `api/wiring/evaluation_judge_adapter_test.go`（3 处引用改常量）

**Interfaces:**

- Produces: `constants.EvaluationJudgeDefaultRubric string`、`constants.EvaluationOptimizerSystemPrompt string`——Task 2/3/4 共同引用。

- [ ] **Step 1: 新增常量**。在 `pkg/constants/evaluation.go` 文件内（保留既有 Judge 数值常量）追加：

```go
// EvaluationJudgeDefaultRubric 是评测法官的内置默认判定准则（assertion_mode=judge
// 用例未单写判定标准时使用）。与平台参数 evaluation.judge.rubric 默认值镜像：
// 改动必须同步 internal/parameters/domain/registry.go 的同名默认值。
const EvaluationJudgeDefaultRubric = `你是一名严谨的评测法官。根据以下标准判断实际输出是否通过：
1. 实际输出是否直接、完整地回答了输入要求；
2. 与期望输出的一致性（期望输出为 null 或空时忽略该项）；
3. 是否存在明显的事实错误或逻辑矛盾。
只输出 JSON：{"passed": true 或 false, "reason": "一句话理由", "confidence": 0-1 之间的小数表示判定置信度,
"dimensions": [{"name": "faithfulness", "score": 0-1, "passed": true 或 false, "reason": "一句话理由", "confidence": 0-1},
{"name": "relevance", "score": 0-1, "passed": true 或 false, "reason": "一句话理由", "confidence": 0-1},
{"name": "completeness", "score": 0-1, "passed": true 或 false, "reason": "一句话理由", "confidence": 0-1}]}。`

// EvaluationOptimizerSystemPrompt 是提示词优化器内置系统提示词。与平台参数
// evaluation.optimizer.system_prompt 默认值镜像：改动必须同步 registry 默认值。
const EvaluationOptimizerSystemPrompt = "你是提示词优化器。只生成候选内容，不决定发布。仅输出 JSON 数组。"
```

> 迁移正文前先 `git show main:api/wiring/evaluation.go` 对当前 L544-556 逐字核对；上面反引号文本必须与现状完全一致。

- [ ] **Step 2: 删除 wiring 本地 const 并改引用**。`api/wiring/evaluation.go`：
  - 删除 L544-556 `judgeDefaultRubric` const（连同其注释块）。
  - `judgeRubric` 兜底（L717）`return judgeDefaultRubric` → `return constants.EvaluationJudgeDefaultRubric`。
  - Rewrite system（L530）`{Role: "system", Content: "你是提示词优化器。只生成候选内容，不决定发布。仅输出 JSON 数组。"}` → `{Role: "system", Content: constants.EvaluationOptimizerSystemPrompt}`。

- [ ] **Step 3: 更新测试引用**。`api/wiring/evaluation_judge_adapter_test.go` 中 3 处 `judgeDefaultRubric`（L64、L80、L145 附近）→ `constants.EvaluationJudgeDefaultRubric`。

- [ ] **Step 4: 编译 + 跑覆盖测试**。确认 `api/wiring` 已 import `pkg/constants`（该文件已用 `constants.JudgeMaxTokens` 等，import 已存在）。

Run: `go test ./api/wiring/ -run 'TestJudgeAdapter|TestParseJudge' -count=1`
Expected: PASS（全部既有法官/parse 测试转绿）。

- [ ] **Step 5: Commit**

```bash
git add pkg/constants/evaluation.go api/wiring/evaluation.go api/wiring/evaluation_judge_adapter_test.go
git commit -m "refactor(evaluation): 法官默认判定准则与优化器系统提示词上移 pkg/constants"
```

---

### Task 2: 注册表新增两键（默认值 == 常量）

**Files:**

- Modify: `internal/parameters/domain/registry.go`（`registerJudgeParams` L440、`registerOptimizerParams` L409 各追加一键）
- Test: `internal/parameters/domain/registry_test.go`

**Interfaces:**

- Consumes: `constants.EvaluationJudgeDefaultRubric`、`constants.EvaluationOptimizerSystemPrompt`（Task 1）。
- Produces: 平台参数键 `evaluation.judge.rubric`、`evaluation.optimizer.system_prompt`（自动被 `snapshotCapturer` 整组快照、平台参数页 schema-driven 消费）。

- [ ] **Step 1: 写失败测试**。`registry_test.go` 追加表驱动用例：对两键断言 `registry.Get(key)` 存在，且 `Default == constants.EvaluationJudgeDefaultRubric / constants.EvaluationOptimizerSystemPrompt`、`Scope == ScopePlatform`、`Category == "evaluation"`、`Optimizable == false`、`VisualHint.Control == ControlTextarea`。若文件内已有「注册键集合计数/枚举」类断言，同步补上新键（运行后按失败信息调整）。

- [ ] **Step 2: 注册两键**。`registerJudgeParams` 的 `[]ParameterDefinition` 内追加（保持既有字段风格）：

```go
{
	Key: "evaluation.judge.rubric", Scope: ScopePlatform, Category: "evaluation",
	DisplayName: "AI 判定默认准则",
	Description: "AI 判定断言（assertion_mode=judge）在用例未单写判定标准时使用的准则；留空回退内置",
	ValueType: TypeString, Default: constants.EvaluationJudgeDefaultRubric,
	VisualHint:  VisualHint{Control: ControlTextarea},
	Optimizable: false,
},
```

`registerOptimizerParams` 内追加：

```go
{
	Key: "evaluation.optimizer.system_prompt", Scope: ScopePlatform, Category: "evaluation",
	DisplayName: "优化器系统提示词",
	Description: "提示词优化器的角色/任务设定；留空回退内置",
	ValueType: TypeString, Default: constants.EvaluationOptimizerSystemPrompt,
	VisualHint:  VisualHint{Control: ControlTextarea},
	Optimizable: false,
},
```

> `registry.go` 已 import `pkg/constants`（import 区 L8，L801 已有 `constants.EnricherSummaryTokenThreshold` 用法），无需改动 import。

- [ ] **Step 3: 跑测试**。

Run: `go test ./internal/parameters/domain/ -run TestRegister -count=1 && go test ./internal/parameters/... -count=1`
Expected: PASS（含既有注册全集断言）。

- [ ] **Step 4: Commit**

```bash
git add internal/parameters/domain/registry.go internal/parameters/domain/registry_test.go
git commit -m "feat(evaluation): 注册 evaluation.judge.rubric / evaluation.optimizer.system_prompt 平台参数"
```

---

### Task 3: 法官默认准则按 ctx 优先级读取

**Files:**

- Modify: `api/wiring/evaluation.go`（`judgeRubric`，现 L713）
- Test: `api/wiring/evaluation_judge_adapter_test.go`

**Interfaces:**

- Consumes: `constants.EvaluationJudgeDefaultRubric`（Task 1）；`domain.EvalSnapshotFromCtx`/`domain.WithEvalSnapshot`；`evalSnapshotWith` helper（本文件已有）；`fakePlatformStore`（`embedding_model_test.go`）；`parametersapp.NewService`。
- Produces: 评测 run（快照注入）与运行态观测（无快照走平台当前值）共用同一判定准则来源。

- [ ] **Step 1: 写失败测试**。在本文件 `TestJudgeAdapterSnapshotPreferred`（L173）附近追加（镜像其 model 优先级表写法）。helper 与 `newJudgeParamsWithModel`（L165）同构，store 写入 `evaluation.judge.rubric`：

```go
// newJudgeParamsWithRubric 构造真实 parameters 服务，平台快照写入
// evaluation.judge.rubric（空串 = 已发布但留空，应回退常量）。
func newJudgeParamsWithRubric(t *testing.T, rubric string) *parametersapp.Service {
	t.Helper()
	encoded, err := json.Marshal(rubric)
	require.NoError(t, err)
	store := &fakePlatformStore{values: map[string]string{"evaluation.judge.rubric": string(encoded)}}
	return parametersapp.NewService(parametersdomain.NewParametersRegistry(), store)
}

func TestJudgeAdapterRubricPriority(t *testing.T) {
	cases := []struct {
		name     string
		request  string // 用例自声明 rubric
		ctx      context.Context
		params   *parametersapp.Service
		want     string
	}{
		{name: "requested wins over snapshot", request: "case-rubric",
			ctx: evalSnapshotWith(t, map[string]any{"evaluation.judge.rubric": "snap-rubric"}), want: "case-rubric"},
		{name: "snapshot wins over platform",
			ctx: evalSnapshotWith(t, map[string]any{"evaluation.judge.rubric": "snap-rubric"}),
			params: newJudgeParamsWithRubric(t, "platform-rubric"), want: "snap-rubric"},
		{name: "snapshot missing rubric falls through to platform",
			ctx: evalSnapshotWith(t, map[string]any{"evaluation.judge.model": "m"}),
			params: newJudgeParamsWithRubric(t, "platform-rubric"), want: "platform-rubric"},
		{name: "platform fallback when no snapshot",
			ctx: context.Background(), params: newJudgeParamsWithRubric(t, "platform-rubric"), want: "platform-rubric"},
		{name: "empty platform falls back to constant",
			ctx: context.Background(), params: newJudgeParamsWithRubric(t, ""), want: constants.EvaluationJudgeDefaultRubric},
		{name: "nothing configured falls back to constant",
			ctx: context.Background(), want: constants.EvaluationJudgeDefaultRubric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := judgeAdapter{completer: nil, params: tc.params}
			got := j.judgeRubric(tc.ctx, tc.request)
			require.Equal(t, tc.want, got)
		})
	}
}
```

> 空串 case 语义：store 写入 `""`（键存在但留空）→ `PlatformValues` 返回空串 → 回退常量；若某实现让 store 缺键走 registry default（=常量）也得到同一期望。两路径均指向常量，断言稳定。`evalSnapshotWith`/`fakeJudgeCompleter`/`fakePlatformStore`/`parametersdomain` 均已在本文件或同包测试文件定义，无需新 import。

- [ ] **Step 2: 实现 `judgeRubric(ctx, requested)` 优先级**。替换现函数体（现仅 `requested != ""` + 常量）：

```go
// judgeRubric 解析法官判定准则，优先级：用例自声明 > run 版本快照 > 当前平台
// 值 > 内置常量兜底（D2/D7：默认=内置全文，空/缺键不漂移）。params 与快照任一
// 缺失均降级到下一层，绝不空态返回。
func (j judgeAdapter) judgeRubric(ctx context.Context, requested string) string {
	if requested != "" {
		return requested
	}
	if snap := evaldomain.EvalSnapshotFromCtx(ctx); snap != nil {
		if s, ok := snap.Evaluation.Values["evaluation.judge.rubric"].(string); ok && s != "" {
			return s
		}
	}
	if j.params != nil {
		if values, err := j.params.PlatformValues(ctx); err == nil {
			if s, ok := values["evaluation.judge.rubric"].(string); ok && s != "" {
				return s
			}
		}
	}
	return constants.EvaluationJudgeDefaultRubric
}
```

> 确认 `Judge` 内调用点 `j.judgeRubric(ctx, req.Rubric)`（现 L726 附近）仍传 ctx——已是 ctx 首参，无需改动调用。

- [ ] **Step 3: 跑测试**。

Run: `go test ./api/wiring/ -run 'TestJudgeAdapter|TestObservation' -count=1`
Expected: PASS（新增优先级表 + 既有全绿）。

- [ ] **Step 4: Commit**

```bash
git add api/wiring/evaluation.go api/wiring/evaluation_judge_adapter_test.go
git commit -m "feat(evaluation): 法官判定准则支持 run 快照/平台参数覆盖，内置常量兜底"
```

---

### Task 4: 优化器系统提示词平台化 + user 侧 JSON 防御契约

**Files:**

- Modify: `api/wiring/evaluation.go`（`gatewayPromptRewriter`，现 L471-542）
- Test: `api/wiring/evaluation_judge_adapter_test.go`（已 import `context`/`encoding/json`/`parametersapp`/`parametersdomain`/`pkg/constants`/`require`，且同包 `fakePlatformStore` 可复用——本任务新测试直接落此文件，无需新增 import）

**Interfaces:**

- Consumes: `constants.EvaluationOptimizerSystemPrompt`（Task 1）。
- Produces: `optimizerSystemPrompt(ctx context.Context) string`、`optimizerMessages(ctx context.Context, snapshotJSON, failuresJSON []byte) []agentport.LLMMessage`——供 `Rewrite`（现 L507）改调；单测直接对二者断言，不搭 resolver/gateway stub。

- [ ] **Step 1: 写失败测试**。`api/wiring/evaluation_judge_adapter_test.go` 追加 helper 与两组测试（镜像 `newJudgeParamsWithModel` L165；空串 = 已发布但留空）：

```go
// newOptimizerParamsWithSystem 构造带 evaluation.optimizer.system_prompt 平台值的
// 真实 parameters 服务（值为空串表示已发布但留空）。
func newOptimizerParamsWithSystem(t *testing.T, system string) *parametersapp.Service {
	t.Helper()
	encoded, err := json.Marshal(system)
	require.NoError(t, err)
	store := &fakePlatformStore{values: map[string]string{"evaluation.optimizer.system_prompt": string(encoded)}}
	return parametersapp.NewService(parametersdomain.NewParametersRegistry(), store)
}

func TestOptimizerSystemPromptPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		params *parametersapp.Service
		want   string
	}{
		{name: "platform value wins", params: newOptimizerParamsWithSystem(t, "你是专门的改写器"), want: "你是专门的改写器"},
		{name: "empty platform falls back to constant", params: newOptimizerParamsWithSystem(t, ""), want: constants.EvaluationOptimizerSystemPrompt},
		{name: "nil params falls back to constant", want: constants.EvaluationOptimizerSystemPrompt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gatewayPromptRewriter{params: tc.params}
			require.Equal(t, tc.want, r.optimizerSystemPrompt(context.Background()))
		})
	}
}

func TestOptimizerMessagesCarrySystemAndJSONContract(t *testing.T) {
	r := gatewayPromptRewriter{params: newOptimizerParamsWithSystem(t, "你是专门的改写器")}
	msgs := r.optimizerMessages(context.Background(), []byte(`{"m":1}`), []byte(`[{"s":"x"}]`))
	require.Len(t, msgs, 2)
	require.Equal(t, "system", msgs[0].Role)
	require.Equal(t, "你是专门的改写器", msgs[0].Content)
	require.Equal(t, "user", msgs[1].Role)
	require.Contains(t, msgs[1].Content, `基线配置：{"m":1}`)
	require.Contains(t, msgs[1].Content, `失败摘要：[{"s":"x"}]`)
	require.Contains(t, msgs[1].Content, "整段回复必须为合法 JSON 数组，禁止任何解释、Markdown、代码围栏或前后缀。")
}
```

- [ ] **Step 2: 跑测试验证失败**。

Run: `go test ./api/wiring/ -run 'TestOptimizerSystemPromptPrecedence|TestOptimizerMessagesCarrySystemAndJSONContract' -count=1`
Expected: FAIL（`optimizerSystemPrompt`/`optimizerMessages` undefined）。

- [ ] **Step 3: 实现**。`gatewayPromptRewriter` 追加两方法（镜像 `optimizerLLM` L481 的 nil/空值容忍风格）：

```go
// optimizerSystemPrompt 解析优化器系统提示词：平台值 string 且非空用之，否则
// 内置常量兜底（空/缺键不产生空 system，parsePromptRewritePatches 契约不漂移）。
func (r gatewayPromptRewriter) optimizerSystemPrompt(ctx context.Context) string {
	if r.params == nil {
		return constants.EvaluationOptimizerSystemPrompt
	}
	values, err := r.params.PlatformValues(ctx)
	if err == nil {
		if s, ok := values["evaluation.optimizer.system_prompt"].(string); ok && s != "" {
			return s
		}
	}
	return constants.EvaluationOptimizerSystemPrompt
}

// optimizerMessages 组装优化器 LLM 消息：system 取平台配置（空回退常量）；
// user 模板保持代码固定并追加 JSON 数组防御契约行——admin 自由改 system 也不会
// 破坏 parsePromptRewritePatches 对「整段合法 JSON 数组」的强制。
func (r gatewayPromptRewriter) optimizerMessages(ctx context.Context, snapshotJSON, failuresJSON []byte) []agentport.LLMMessage {
	return []agentport.LLMMessage{
		{Role: "system", Content: r.optimizerSystemPrompt(ctx)},
		{Role: "user", Content: fmt.Sprintf(
			"基线配置：%s\n失败摘要：%s\n输出最多3项，每项格式：{\"prompt_patch\":{\"instructions\":\"...\"},\"rationale\":\"...\"}。不得修改 requirements、权限、密钥或网络配置。\n整段回复必须为合法 JSON 数组，禁止任何解释、Markdown、代码围栏或前后缀。",
			string(snapshotJSON), string(failuresJSON),
		)},
	}
}
```

`Rewrite`（现 L523-537）改为调 `optimizerMessages`，删除内联的 system/user 两条 `Messages` 字面量（Task 1 已把 system 常量引用放在此处；重构后随消息块一并移除，user 内容移动进方法）：

```go
	response, err := gateway.Route(ctx, agentport.CapabilityRequest{
		TenantID: request.TenantID,
		Type:     agentport.CapLLM,
		Timeout:  60 * time.Second,
		LLM: &agentport.LLMCapRequest{
			Model: model, Temperature: temperature, MaxTokens: maxTokens,
			Messages: r.optimizerMessages(ctx, snapshotJSON, failuresJSON),
		},
	})
```

- [ ] **Step 4: 跑测试**。

Run: `go test ./api/wiring/ -run 'TestOptimizer|TestParsePromptRewritePatches' -count=1`
Expected: PASS（新增两组 + 既有 parse 全绿）。

- [ ] **Step 5: Commit**

```bash
git add api/wiring/evaluation.go api/wiring/evaluation_judge_adapter_test.go
git commit -m "feat(evaluation): 优化器系统提示词平台参数化，user 侧追加 JSON 数组防御契约"
```

---

### Task 5: 门禁与回归验证（PR 前）

**Files:** 无新改动；跑门禁。

- [ ] **Step 1: 快速验证**。Run: `go vet ./... && go test -short ./...`；补跑本改动三包：`go test ./pkg/constants/ ./internal/parameters/domain/ ./api/wiring/ -count=1`。Expected: PASS。
- [ ] **Step 2: 契约不变**。Run: `go test ./api/http/ -run TestContract -count=1`（golden 应无 diff；本改动无 HTTP/参数契约变更）。
- [ ] **Step 3: 代码质量**。Run: `make code-quality`（新函数不超门禁；文本常量不触发数值常量规则）。
- [ ] **Step 4: 全量竞态**。Run: `go test -v -race -timeout 30s ./...`。Expected: PASS。
- [ ] **Step 5: 系统验收**。这是功能改动 → 完整测试门槛：派发 `stratum-e2e-tester` agent（封装 `stratum-e2e-development`），按 `.test/verification.yaml` 风险分级跑。核心场景：
  - 平台管理 → 平台参数可见 `AI 判定默认准则`/`优化器系统提示词` 两键并可编辑保存；
  - 发布新 `evaluation.judge.rubric` 后新建含 `assertion_mode=judge` 用例的评测 run，判定按新准则生效（run/观测记录对账）；
  - 回滚平台参数版本后新 run 恢复内置准则；
  - 运行时 mock/契约断言回归无 diff。
- [ ] **Step 6: 推送与 PR**。从主仓库入口流程：

```bash
git fetch origin main
git push -u origin feat/judge-optimizer-prompt-params
gh pr create --base main --title "feat(evaluation): 法官/优化器提示词平台参数化" --body "<What/Why/HowToTest>"
```

## 自评清单（实现前已核）

- 覆盖 spec §5.1（注册）§5.2（单一来源）§5.3（法官优先级）§5.4（优化器+契约）§5.5（快照自动）——各对应 Task 2/1/3/4/自动路径。
- 非目标（法官 system、casegen、溯源页、DB DDL、前端、权限）均未入任务。
- 占位检查：所有常量/文本/优先级/文件行号已具名；引用既有 helper，无「按上文」「类似 Task」式跳步。
- 类型一致：常量名 `EvaluationJudgeDefaultRubric`/`EvaluationOptimizerSystemPrompt` 全计划一致；优先级次序全计划一致。
