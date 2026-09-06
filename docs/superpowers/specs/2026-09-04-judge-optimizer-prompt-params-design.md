# 评测法官 / 优化器提示词平台参数化 Design

> 日期：2026-09-04
> 状态：已批准（用户确认「没有问题，继续」）
> 范围：`evaluation.judge.rubric`（AI 判定默认准则）与 `evaluation.optimizer.system_prompt`（优化器系统提示词）作为平台参数暴露，在「平台管理 → 平台参数」可见可配；默认值与现状 byte-identical，历史 run 快照缺键不漂移。

## 1. 背景与问题

评测法官（`assertion_mode=judge` 用例的 LLM 判定）与优化器（评测失败驱动的被测 agent 提示词重写）的系统提示词/判定准则当前是**代码内建常量**：

- `api/wiring/evaluation.go:549` `judgeDefaultRubric`（法官默认判定准则，§6.2 三维度：faithfulness/relevance/completeness）
- `api/wiring/evaluation.go:530` 优化器 system `"你是提示词优化器。只生成候选内容，不决定发布。仅输出 JSON 数组。"`

二者**不是平台参数**，因此「平台管理 → 平台参数」页不展示它们。管理员要调判定准则或优化器角色，只能改代码 + 走 CD 发版。这与平台既有模式不一致——`agent.system_prompt`、`memory.extraction_prompt`、`memory.reflection_prompt` 等行为提示词均为平台参数（`internal/parameters/domain/registry.go`），且 `2026-08-22-prompt-platform-params.md` 已有「提示词迁平台参数 + 单一来源 + 未配置语义」的完整样板。

## 2. 目标与非目标

**目标**

1. 两段提示词在平台参数页可见、可配（多行 textarea），编辑走现有平台参数 draft→发布→回滚版本流。
2. 默认与现状 byte-identical：注册表默认值 = 当前内置全文；历史 run 快照缺键或参数留空 → 回退内置常量，无行为漂移。
3. 读取优先级统一：用例自声明 > run 版本快照 > 当前平台值 > 内置兜底。
4. 变更可观测、可回滚；判定变更不产生误放行（fail-closed 方向）。

**非目标**

- 不暴露法官 system 行（「只输出 JSON」JSON 契约固定，代码内不开放）——Q1 用户选择。
- 不暴露用例生成器 `caseGenSystemPrompt`——Q1 用户选择仅两键。
- 不做 run 级只读溯源页（未选）。
- 不改权限模型：平台参数页仍仅 `system_admin` 可写。
- 无 DB DDL / migration。

## 3. 决策

| # | 决策 | 结论 |
|---|---|---|
| D1 | 暴露面 | 两键：`evaluation.judge.rubric` + `evaluation.optimizer.system_prompt`（用户 Q1 选择「判定准则+优化器角色」） |
| D2 | 默认值语义 | 注册表 Default = 内置全文，开箱可见；清空 → 回退内置（用户 Q2 选择）；代码兜底常量保留，行为永不为空态漂移 |
| D3 | 单一来源 | 两段文本上移到 `pkg/constants/evaluation.go` 导出；registry 默认与 wiring 兜底共同引用（`internal/parameters/domain` 不可 import `api/wiring`，二者皆可达 `pkg/`；先例：`pkg/constants/agent.go` 的 `CompactionDefaultPrompt`） |
| D4 | 优化器护栏 | 固定 user-content 追加防御契约行，强制整段合法 JSON 数组（用户 Q3 选择），admin 自由改 system 也不破坏 `parsePromptRewritePatches` |
| D5 | 法官护栏 | system 行保持代码固定不暴露；rubric 改坏 → LLM 非 JSON → `parseJudgeResponse` error，或缺 `passed` 默认 false（fail-closed） |
| D6 | Optimizable | 两键 `Optimizable: false`：不进评测优化候选搜索空间，防止优化器改法官/改自身 |
| D7 | 快照锚定 | 键属 evaluation 平台组 → `snapshotCapturer.captureGroup` 整组快照自动纳入每次 run pin；旧快照缺键 → 平台当前值或常量兜底 |

## 4. 现状事实（证据锚点，main @ 2048fce5）

- 注册表 evaluation 组仅含 judge/optimizer 的 model/temperature/enabled/max_tokens 运行开关，无 prompt 键（`internal/parameters/domain/registry.go` `registerOptimizerParams`/`registerJudgeParams`，约 L405-466）。
- 法官实现 `judgeAdapter`（`api/wiring/evaluation.go:648`）：system `"你是评测法官。只输出 JSON，不输出其他内容。"`（L736，代码固定）；`judgeRubric(_ ctx, requested)`（L713）现忽略 ctx，仅 `requested != "" → requested`，否则 `judgeDefaultRubric`。评测 run 与运行态观测共用此 adapter。
- 法官优先读 run 快照：`Enabled`/`judgeModel`/`judgeTemperature` 均先查 `EvalSnapshotFromCtx(ctx).Evaluation.Values[...]`（L656-711）；rubric 未纳入——需补 ctx。
- 优化器 `gatewayPromptRewriter.Rewrite`（`api/wiring/evaluation.go:505`）：`optimizerLLM` 从 `PlatformValues` 读 model/temperature/max_tokens（空 model 交 llmgateway 解析）；system 与 user 内容均为内联常量。
- `parseJudgeResponse`（`evaluation.go:889`）：非 JSON → error；缺 `passed` → 默认 false（fail-closed）；维度缺失/非法 → 丢弃该维度（fail-open），完全无维度 → nil 聚合跳过。旧 judge 仅返回 passed/reason 仍可工作（注释明示）。
- `parsePromptRewritePatches`（`evaluation.go:960`）：整段必须为合法 JSON 数组（`json.Unmarshal` 全文），长度 1-3，每项过 `ValidatePromptPatch`；否则 error。
- 快照捕获 `snapshotCapturer.captureGroup`（`api/wiring/evaluation_snapshot.go:110`）：`params.Versions(ctx, groupKey)` 整组快照（`platform_config_versions.snapshot` 存整组 key→value，`ListVersions` 按 group_key 取全量），新键自动纳入每次 run pin。
- 平台参数页 schema-driven：新注册平台级键自动渲染；`agent.system_prompt` 同控件（ControlTextarea、ScopePlatform）已在用。
- 平台参数编辑 = 平台级版本流：draft 可编辑态 → 保存产出版本 → 发布指向 production 生效、可回滚（`web/src/modules/parameters/components/VersionHistory.tsx`）。

## 5. 方案设计

### 5.1 参数注册（backend registry）

`internal/parameters/domain/registry.go`，在 `registerJudgeParams` / `registerOptimizerParams` 各追加一键：

| Key | DisplayName | ValueType | Default | Control | Optimizable | Description |
|---|---|---|---|---|---|---|
| `evaluation.judge.rubric` | AI 判定默认准则 | TypeString | `pkg/constants` 常量 | Textarea | false | AI 判定断言（assertion_mode=judge）在用例未单写判定标准时使用的准则；留空回退内置 |
| `evaluation.optimizer.system_prompt` | 优化器系统提示词 | TypeString | `pkg/constants` 常量 | Textarea | false | 提示词优化器的角色/任务设定；留空回退内置 |

`Scope: ScopePlatform`、`Category: "evaluation"`。

### 5.2 单一来源迁移（pkg/constants）

- **新增** `pkg/constants/evaluation.go`，导出两常量：
  - `EvaluationJudgeDefaultRubric`：现 `judgeDefaultRubric` 全文（见 `api/wiring/evaluation.go:549-556`），逐字迁移。
  - `EvaluationOptimizerSystemPrompt`：现 `"你是提示词优化器。只生成候选内容，不决定发布。仅输出 JSON 数组。"`。
- `api/wiring/evaluation.go`：删除本地 `judgeDefaultRubric` const（L549），`judgeRubric` 兜底改引用常量；优化器 system 常量改为引用常量。
- 注释在常量旁标注「与平台参数默认值镜像，改动需同步 registry 默认」。
- 防漂移：单测断言「参数注册表解析出的默认值 == `pkg/constants` 常量」及「空平台值路径解析 == 常量」。

### 5.3 法官消费端（api/wiring `judgeAdapter`）

`judgeRubric(ctx, requested)` 改造为带 ctx 读取，统一优先级：

```
requested（用例自声明 rubric，最强）
  → run 快照 snap.Evaluation.Values["evaluation.judge.rubric"]（string 且非空）
  → 当前平台值 params.PlatformValues["evaluation.judge.rubric"]（string 且非空）
  → pkg/constants.EvaluationJudgeDefaultRubric（兜底）
```

- 评测 run：`RunStored` 注入快照 → run pin 到创建时点平台准则（D7）。
- 运行态观测：同 adapter，无快照时走当前平台值（最新已发布准则）。
- `Judge` 内 system 行不变（代码固定，JSON 契约护栏）。

### 5.4 优化器消费端（api/wiring `gatewayPromptRewriter`）

`Rewrite`：

1. system：`PlatformValues["evaluation.optimizer.system_prompt"]`，string 且非空 → 用之；否则 `pkg/constants.EvaluationOptimizerSystemPrompt`。
2. user-content（保持代码固定）**追加一行防御契约**：

```
\n整段回复必须为合法 JSON 数组，禁止任何解释、Markdown、代码围栏或前后缀。
```

（现有 user 内容：`基线配置：%s\n失败摘要：%s\n输出最多3项，每项格式：{"prompt_patch":{"instructions":"..."},"rationale":"..."}。不得修改 requirements、权限、密钥或网络配置。`）

1. 保留 `optimizerLLM` 的 model/temperature/max_tokens 平台化读取不变。

### 5.5 快照锚定路径

- 键属 evaluation 平台组 → `captureGroup("evaluation")` 整组快照自动纳入每次 run（创建时点 pin），无需额外代码。
- 历史 run 快照（键存在前创建）缺键 → 5.3 优先级落到当前平台值/常量兜底，byte-identical，不漂移。
- 平台管理发布新版本后，新 run pin 到新准则；旧 run 快照保持旧判定依据（溯源语义不变）。

## 6. 权限 / 门禁 / 风险与回滚

| 风险 | 影响 | 缓解 |
|---|---|---|
| 法官准则改坏 → 模型输出非 JSON / 缺 `passed` | judge 返回 error（用例显式失败）或判不过（fail-closed，不误放行）；污染该 run 失败指标 | run 可观测；平台参数版本回滚即恢复；改准则属 `system_admin` 操作，发布留 message |
| 优化器 system 删掉 JSON 数组指令 | 防御契约行（user 侧固定）仍强制整段 JSON；极端违规 → parse error，候选为空 | 界面可观测 + 回滚 |
| 两键文本漂移（registry 默认 vs 常量） | 开箱值 ≠ 兜底值 | 单测断言相等；单一来源常量 |
| 新键进入优化器候选搜索空间 | 优化器改法官/改自身，自指 | D6 `Optimizable: false` |

无 DB DDL：`public.platform_config_versions` 已承载平台参数；存量租户无感，无需 migration。不触碰 `pkg/migration/sql/`。

## 7. 前端

无手写页面。平台参数页 schema-driven 自动渲染两键为 textarea（先例 `agent.system_prompt`）。可选打磨（不阻塞）：textarea rows、Description 文案在注册表内定。

## 8. 测试与验收

**单元测试**

- registry：两新键注册成功，`Default` == `pkg/constants` 常量。
- `judgeRubric` 优先级表：`requested > 快照值 > 平台值 > 常量兜底`（含快照缺键、平台值空串、全空路径）。
- optimizer：system 平台值读取 / 空回退常量；防御契约行存在且不破坏既有 parse 用例。
- 既有 parse（judge/patches）、契约 golden 无回归。

**系统验收**（功能改动 → 完整测试门槛，`.test/verification.yaml` 风险分级；走 `stratum-e2e-development` skill）

- 平台管理发布新的 `evaluation.judge.rubric` → 新建含 judge 用例的评测 run 判定按新准则生效（以 run/观测记录对账）。
- 回滚版本 → 新 run 恢复内置准则。
- 断言平台参数页可见两键并可编辑保存。

## 9. 改动文件清单

| 文件 | 改动 |
|---|---|
| `pkg/constants/evaluation.go` | 新增：`EvaluationJudgeDefaultRubric`、`EvaluationOptimizerSystemPrompt` |
| `internal/parameters/domain/registry.go` | `registerJudgeParams`/`registerOptimizerParams` 各追加一键 |
| `api/wiring/evaluation.go` | 删本地 `judgeDefaultRubric`、`judgeRubric` 带 ctx 读优先级、optimizer system 平台化 + user 防御契约行 |
| 前端 | 无 |
| 测试 | registry / judgeRubric / optimizer 单测；契约 golden 无 diff |

## 10. 验证 / 待确认假设

- [x] 法官/优化器两处 LLM 调用均走 `pkg/constants` 可达（`api/wiring` 与 `internal/parameters/domain` 皆 import `pkg/`）。
- [x] `snapshotCapturer` 整组快照自动纳入新键（已读 `captureGroup`/`groupFromVersion`）。
- [x] 平台参数页 ControlTextarea 渲染已有先例（`agent.system_prompt`）。
- [ ] 实现时复核 `Optimizable` 消费方（参数搜索空间枚举）不会因两键 `false` 产生行为差异；如需确认则仅新增注册，不动既有键。
- [ ] 实现时确认 `PlatformValues`（非 run 路径）返回的 production 快照在从未发布过该键时为缺键（走兜底）而非报错——已读 `judgeModel` 模式为 `values[key].(string)` 缺键容忍，预期一致。

## 附录：知识输入与证据

- obsidian `99-系统/知识输入与证据检索协议.md` 在当前 vault **不存在**（已检索定位，记录在案）；已读 CLAUDE.md/仓库内置协议要求并执行仓库证据优先。
- 相关 obsidian 观点（trail）：memory 系行为提示词（`memory.reflection_prompt`/`extraction_prompt`）为平台级配置先例；Agent 评测体系/LLM Judge 校准属既有知识域。
- 仓库证据（见 §4）：`2026-08-22-prompt-platform-params.md`（提示词平台参数化样板）、`2026-08-28/30/31-evaluation-*.md`（judgeDefaultRubric 演进史，均为近期新增的代码常量，无「刻意不外配」旧决策）。
