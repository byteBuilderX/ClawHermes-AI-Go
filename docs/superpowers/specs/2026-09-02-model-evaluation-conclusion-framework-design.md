# 模型评测结论产出框架设计（结论 + 归因 + 改进建议总纲）

> 文件建议名：`2026-09-02-model-evaluation-conclusion-framework-design.md`
> 内部卡：#109「模型评测结论 + 归因分析 + 后训练建议」（卡 F）
> 类型：**结论产出框架 / 方法论 spec**（定义产出标准与流程，非功能设计——不写 Go 接口、不写 DDL）
> 日期：2026-09-02
> 状态：**定稿（主会话已裁 A-1/A-2，待提交）**
> 前置文档：
>
> - `docs/superpowers/specs/2026-08-28-evaluation-metrics-design.md`（下称「主 spec」）
> - `docs/superpowers/specs/2026-08-30-evaluation-review-pool-design.md`（下称「评审池 spec」）
> - `docs/superpowers/specs/2026-09-02-evaluation-two-track-versioning-design.md`（下称「双轨 spec」）
> - `/tmp/card-f-recon.md`（范围与数据盘点，2026-09-02）

---

## 1. 背景与目标

### 1.1 本 spec 的定位

平台的运行态评测与评测集评测能力正在按主 spec 分阶段上线。评测能力回答「怎么测、测出什么指标」；但「测完 **对被测模型** 形成什么结论、为什么差、下一步改什么」——即**结论产出层**——尚无统一标准。本 spec 是评测能力上线后的**结论产出 + 归因分析 + 改进建议总纲**，独立于任一功能 spec，先行冻结方法与产出标准，真实评测数据到位后按此执行。

评测对象是**平台在用的被测 LLM 模型**：以 `public.models` 模型目录为事实源，当前被测候选为 GLM-5.x 系 chat 能力模型 + embedding 模型（见 §2.1 与 recon）。结论服务于「当前选用的模型在平台真实任务形态（agent / skill / knowledge-RAG）上是否胜任、哪里失分、怎么改进」的决策支持。

### 1.2 与功能 spec 的边界（关键）

| 既有通道 / 功能 | 定位 | 本 spec 的关系 |
|---|---|---|
| `cmd/e2e-eval-check` + `test/e2e/<kind>/golden/cases.yaml` | 开发态/测试态/CI 离线回归（主 spec §1.2） | **不替代**。作为方法学演练与最小样本 dry-run 的载体；结论级数据不以 dev golden 冒充生产结论 |
| 评测集评测 `EnqueueRun` → worker（主 spec §2.1/§6） | 判定闸门，产出多维指标 | **承接消费**。结论级评测的入口；双轨 spec 保证 run 可复现 |
| 人工评审池（评审池 spec，主 spec §6.6） | 低置信/分歧判定的人工复核与回写 | **承接消费**。失败→归因条目、judge 误判→校准样本、promote→评测集；本 spec 定义这些沉淀如何上升为「模型结论/归因建议」 |
| §9 归因分析引擎（三级根因 / TunableEvalProfile / 改进闭环，主 spec §13 Phase3） | 参数归因自动化功能 | **先于功能冻结方法学**。功能未实现时，本框架规定人工分析者按同一方法学、同一证据门槛产出；功能实现后由引擎接管 |
| 双轨版本快照（双轨 spec） | 评测 run 全链路锚定创建时点 | **本 spec 的可出结论前提之一**：无锚定的 run 不可用于归因（§3.5/§4.5） |

**本 spec 不承诺**：不实现任何评测功能、不新增证据存储、不产出任何「无数据支撑的结论」。（详见 §8）

### 1.3 关键现实约束（截至 2026-09-02 recon，作为 §6 数据门槛的依据）

1. **生产库 `eval_*` 评测运行数据全部为 0 行**：`eval_runs / eval_case_results / eval_suites / eval_suite_revisions / eval_cases / eval_observations / eval_review_items / eval_calibration_samples / eval_attribution_entries / evaluation_experiments / evaluation_feedback / evaluation_jobs / evaluation_deployments` 均为空。
2. 模型目录 `public.models`：26 个 GLM 系 chat/vision/reasoning + 2 embedding；`enabled` = glm-5 / 5.1 / 5.2 / 5.3 + embedding-3；单一 provider。
3. 评测器平台参数快照记录 `judge.model = glm-4-flash` 且 `judge.enabled = false`；`evaluation.judge.model` 走平台参数，独立于被测模型（主 spec §3.6）。
4. 平台参数版本 10 个（agent×5 / evaluation×1 / memory×2 / trace×2）。

**推论**：现时点**不存在**可出「有数据支撑结论」的评测运行；judge 判定通道默认关闭，judge 无任何人工校准样本。因此本 spec 的可执行部分 = 定义数据到位判据、结论语义、证据门槛与产出流程；**首份结论的执行预案见 §9**。

### 1.4 目标与成功（框架层）

本 spec 成功 = 下面每一项都有可判定标准、无「待定/后续再说」占位：

- 定义「什么算一次可出结论的评测」（§2.3，判据 C1–C5）。
- 定义评测结论的口径、分层、门禁语义与可信度标签（§3）。
- 定义归因分析框架与**禁止伪归因的证据门槛**（§4，承接主 spec §14）。
- 定义改进建议产出规则；「后训练 / SFT」相关内容**仅作归因维度**、不产出 SFT 建议或流程（§5，主会话已裁，见附录 A-1）。
- 定义数据来源、跑批入口与**出结论的最低数据门槛**（§6）。
- 定义报告模板、沉淀与 meta 指标（§7）。
- 给出真实数据到位后的首份结论执行预案与责任方（§9）。

---

## 2. 评测对象与边界

### 2.1 被测模型目录分层（用途分层）

模型目录事实源 = `public.models`（`internal/llmgateway` Model：`capabilities ∈ {chat, embedding, rerank, vision, tool_use, reasoning}`、`enabled`、`defaultEmbedding`、context_window / max_tokens / 价格）。按**平台用途**分四层，只有前两层是被测对象，后两层是**测量仪器 / 生成器**（不进被测结论，但决定可测性）：

| 用途层 | 用途 | 现用模型（生产目录，截至 recon） | 是否被测 | 说明 |
|---|---|---|---|---|
| **chat 执行层** | agent / skill 对话与多步执行（含 vision / reasoning 子能力） | glm-5 / 5.1 / 5.2 / 5.3（enabled） | ✅ | 本 spec 主要被测对象；模型挂在 agent / skill 资源版本上 |
| **embedding 层** | knowledge / memory 检索向量化 | embedding-3（enabled，defaultEmbedding 以目录为准） | ✅ | 经 knowledge 检索评测（recall@k / mrr / 命中率）间接评估 |
| judge 判定层 | 评测集 / 观测的 LLM 判定 | 平台参数 `evaluation.judge.model`（recon：glm-4-flash，`judge.enabled=false`） | ❌ | **测量仪器**；需启用 + 人工校准基线才可支撑语义维结论（§6.4） |
| optimizer 生成层 | 评测 case 批量生成 | 平台参数 `evaluation.optimizer.model` | ❌ | 不进被测结论；生成物永不经其自动 publish（主 spec §5.2） |

**规则**：

- 「被测模型」的权威 id = `public.models.id`；结论报告必须记录该 id、`name`、capabilities 与生效的 context_window / max_tokens 策略（`EffectiveModelPolicy`），避免同名不同窗。
- judge / optimizer 模型变化**不视为被测对象结论**，但 judge 模型不一致会使两次 run 的语义维不可比 → 归因前必须校验 judge spec 一致（§4.5）。
- 当前 enabled 候选：chat 层 4 个同系（glm-5.x），embedding 层仅 1 个（embedding-3）。**相对选型结论（“B 优于 A”）需要 ≥2 个 enabled 候选**；单候选只能做绝对能力评估（见 §5.2）。

### 2.2 评测点（模型 × 资源 × 参数版本）

承接主 spec §3.5「评测点 = 平台参数快照 × 租户资源配置快照」与双轨 spec「全链路版本快照」。本 spec 把被测模型**显式提升为一等归因维度**（主 spec 中模型归属资源配置内，此处扩展但不改变双锚点基础）：

```
EvaluationPoint(Model) =
  被测模型 { public.models.id, name, capabilities, 生效窗口/输出策略 }
  × 承载资源 { resource_kind(agent|skill|knowledge-workspace), resource_id, resource_revision }
  × 平台参数版本 platform_seq（evaluation 组 + 被测执行相关组，取自 run 快照）
  × suite_revision_id + tier
```

- 一次 run 的双版本锚点（主 spec §6.2 `run.metrics.version` / 双轨 spec `EvaluationContextSnapshot`）必须完整记录 `platform_seq + resource_version + revision_id + 被测模型 id`；任一缺失按 §4.5 处理（该观察/run 不参与版本对比归因）。
- **结论聚合粒度**：结论默认按「被测模型 × 用途层」声明（如「glm-5.2 作为 agent 执行模型」），**内部证据按「模型 × 承载资源 × 分层」组织**。只有当各资源、各分层证据方向一致且各自覆盖达标时才允许上卷到模型层；否则只报告到资源层（防辛普森，主 spec §3.3；同 §3.2 本节）。

### 2.3 什么算「一次可出结论的评测」

一条评测运行满足以下全部判据，才可作为结论的证据单元（判据 C1–C5，供 §6 门槛引用）：

| 判据 | 要求 | 来源 |
|---|---|---|
| C1 run 成功 | run `status=succeeded`，`metrics` 聚合完成，无 `error_message` | 评测系统既有 |
| C2 锚点完整 | `run.metrics.version` 双锚点 + 被测模型 id 齐全；`context_snapshot` 存在（双轨机制上线前旧 run 视为不可用） | 主 §6.2 / 双轨 §6.1 |
| C3 覆盖达标 | 样本量 ≥ 对应结论级门槛（§6.3）；声明的分层 ≥2/3 有样本 | §3.3 / §6.3 |
| C4 判定通道可用且口径一致 | 语义维结论要求 `evaluation.judge.enabled=true`，且对比组间 judge 模型 / rubric / 阈值一致；否则该 run 的语义维仅可出「受限结论」 | 主 §3.6 / §6.4 本节 |
| C5 suite 可追溯 | `suite_revision_id` + 版本链可回溯到具体 case 集；generator 未自动 publish 污染 | 主 §5.2 |

不满足 C1–C5 的运行**不是证据**：可作为候选 case 池/观测信号，但禁止进入结论与归因。

---

## 3. 评测结论的定义

### 3.1 多维指标口径（承接主 spec §6.2）

结论基于 `run.metrics` 的多维聚合，报告必含以下口径；维度可用范围受 C4 限制：

| 组 | 指标 | 语义维结论的前提 |
|---|---|---|
| by_dimension | faithfulness / relevance / completeness（judge） | judge.enabled + 校准基线（§6.4） |
| by_dimension（确定性） | safety / format / tool_pass / step_reasoning / process | 规则断言，天然确定 |
| by_dimension（行为） | retry_rate / escalation_rate / abandonment_rate | 评测集行为信号（主 §3.1） |
| by_category | normal / boundary / adversarial | — |
| cost / latency | total_usd / avg_usd / p50 / p95 / max | — |
| version | suite_revision_id / platform_seq / resource_version / 被测模型 id | C2 |

单 case 层面引用 `result.dimensions`（含 confidence）、`result.process_pass / process_failure / tool_sequence`、`failure_reason`（主 spec §6.2/§6.5）做下钻与失败归类。

### 3.2 分层报告（辛普森防护，承接主 spec §3.3）

- 所有指标按 `资源 × 难度分层(tier) × tenant-tier × 参数版本` 聚合报告，**禁止只看模型层整体均值**。
- 模型级结论报告必须附分层表；任一分层样本 <3（§6.3）时该层标注「coverage 不足」，该层结论降为信号级，不参与模型层上卷。
- 平台参数发布对多租户的分布效应（主 spec §9.4）：若结论涉及「平台参数改动 × 多模型」，按资源特征分层识别交互，劣化租户名单单独列出，不与整体混同。

### 3.3 判定 / 门禁写回语义（承接主 spec §8）

- 评测集 run 的判定结果已由评测系统写回（`run.passed` / 门禁动作层）。本框架在结论报告层区分**判定结论**与**归因/建议结论**两层，不混写：
  - 判定结论 = 该模型在「该评测点 × 该 suite」上 **pass / conditional_pass / fail / no_conclusion**。写入报告 §7.1，并作为门禁信号（L2 告警 / L3 人工确认语义，主 spec §8）。
  - **被测模型不是平台 tunable**（不在 TunableRegistry 声明集合内，主 spec §9.2），因此模型级判异**永不触发自动回滚**；「换模型/切版本」一律走资源变更 + 人工决策流程（§5.2）。
- no_conclusion 时的门禁语义 = 保持现状、不动作、上升人工复核；**禁止把 no_conclusion 解读为通过**（fail-closed，主 spec §14）。

### 3.4 结论可信度标签（样本量 / 覆盖 / 校准 / 置信）

每条结论（判定、归因、建议）必须带可信度标签，标签由下述门决定，**不可人工自封**：

| 标签 | 判定条件（全部满足才升级） | 可产出的结论形态 |
|---|---|---|
| **no_conclusion** | 不满足 C1–C5；或声明语义维但 judge 禁用/未校准；或 n < 判异级门槛 | 不输出判定/归因；只输出「数据不足说明」（§6.6） |
| **low** | 满足判异级门槛但未达正式结论级；或样本达级但 judge 中位 confidence < 0.7；或语义维无校准基线 | 仅信号/方向性表述 +「需扩大样本复核」，不得作为决策依据 |
| **medium** | 满足正式结论级门槛 + 覆盖达标；语义维有校准基线或明确限定为确定性维度 | 可作判定结论与方向性建议 |
| **high** | 满足 medium 全部 + 存在同集对照基线且差值超证据门槛（§4.5）+ 分层方向一致 + judge 置信/校准达标 | 可作决策级结论（切换/发布/选型） |

标签规则固化为报告「数据充分性」块的机器可判定汇总（§6.6），并写入每张结论表头（例：`判定结论[high]`、`归因[medium]`）。

### 3.5 结论的粒度与有效期

- 结论对「被测模型 × suite_revision × resource_revision × platform_seq × judge spec」**五元组**有效；任一变化后旧结论失效，需重评或标注「基于旧锚点，仅供参考」。
- 归因时间窗遵循版本边界：版本内 drift 不等于参数/模型因素（主 spec §7.2）；同一锚点内出现劣化 → 先查外部依赖/内容/流量，不归因到模型。

---

## 4. 归因分析框架

### 4.1 归因对象

归因回答两类问题，本框架都覆盖：

1. **失败归因**：本次评测中某模型为什么在维度 X 失分 / 某些 case 失败。
2. **差异归因**：两次评测之间（模型 A→B，或平台版本 V1→V2，或资源配置 Y1→Y2）为什么指标变化。

### 4.2 三级根因（承接主 spec §9.1）

| 层 | 问题 | 方法 | 证据形态 |
|---|---|---|---|
| case 级 | 这次单个 case 为什么差 | trace 组件级下钻：哪个 span 劣化（retriever 召回 / tool 失败 / LLM 截断 / 上下文丢失） | `eval_case_results.trace_evidence` + Opik trace 只读回查 |
| 参数/配置级 | 是不是参数或配置差异导致 | 同集版本对比优先 + 扰动式（一次改一个） | 版本对比差值表 / 单变量扰动 run |
| 模式级 | 是不是系统性根因 | 跨 case 失败聚类（按 failure_reason / 维度 / 资源） | 失败聚类 ≥3 条同簇 |

### 4.3 方法选择优先级（承接主 spec §6.3 / §9.1）

评测集归因有受控性优势（case 固定），是第一优先级证据。选择顺序：

1. **同集版本对比归因**（优先采信）：同一 `suite_revision_id` 上跑模型/配置 A 与 B，逐维度差值表。用于「是不是换模型/改参数导致的」。**模型切换结论必须同集对比**，禁止用两次不同 suite 的 run 对比下归因。
2. **case 聚类归因**：失败 case 按 `失败类型 × 维度 × 资源` 聚类 → 系统性根因。
3. **trace 组件级下钻**：单 case 定位劣化 span。
4. **扰动式**（假设验证，不作首采）：一次只改一个变量（参数/上下文/工具），n 足够时验证因果假设（承接主 spec §9.1 参数级）。

### 4.4 参数 × 指标因果矩阵与模型维度的关系

- 参数归因沿用主 spec §3.2 因果矩阵 + §9.2 `TunableEvalProfile`（声明影响维度/归因策略/改进方向）；参数纳入归因的三个必要条件（可观测、可归因、可调节）不变（主 spec §9.2）。
- **被测模型本身不在该矩阵**（模型不是已登记 Tunable，平台参数 7 项 tunable 中无 model）。模型差异的归因走「同集版本对比 + 资源特征分层」，在报告里单独成节「模型级差异归因」，与参数归因并列但不共用 Tunable 路由。
- 归因先按双锚点分组做正交分离（主 spec §9.4）：同一资源配置下比模型 = 模型净效应；同一模型下比配置 = 配置净效应；平台与资源/模型效应不混同。

### 4.5 证据不足 → 禁止伪归因（承接主 spec §14）

以下任一情形，该条归因**禁止成立**，只能输出「归因假设（待验证）」或「证据不足」：

| # | 证据门槛（可判定） | 不满足时的处置 |
|---|---|---|
| E1 | 样本量：差异双方各 ≥10 case（失败维样本） | 标 evidence_insufficient，不上归因 |
| E2 | 同集对照：A/B 使用同一 suite_revision_id；非同一 suite 不得做差值归因 | 无对照 → 只做失败描述，不做差异归因 |
| E3 | 效应显著：比率类 `pass_rate \|Δ\| ≥ 10pp`，或维度均分 `\|Δ\| ≥ 0.5`（0–1 量纲；0–5 量纲按比例 2.5）；低于阈值视为噪声 | 报告「无显著差异」，禁止硬归因 |
| E4 | 锚点完整：参与对比的 run 满足 C2；锚点缺失标 `unknown` 并排除（主 spec §14） | 排除该观察，不参与版本对比 |
| E5 | 判定通道一致：对比组 judge 模型 / rubric / 阈值一致；judge 有校准基线（语义维） | 不一致 → 语义维差异不可归因 |
| E6 | 混杂排除：同窗内存在外部依赖/内容/流量变化（版本内 drift）时，先排除非模型因素（主 spec §7.2） | 无法排除 → 标假设 |
| E7 | 聚类模式级：≥3 条同簇失败 case 才可命名「系统性根因」；单 case 只能 case 级 | 不足 → 降级到 case 级或假设 |

报告强制区分两类表述：**归因（attribution，E1–E7 全过）** 与 **假设（hypothesis，待验证）**；「可能/或许」式表述一律归入假设，不允许充当归因结论。归因证据不足的条目按主 spec §6.6 语义进人工复核路径（评审池当前触发枚举未含 evidence_insufficient，见附录 A-3，本门禁作为报告层门禁独立执行）。

### 4.6 归因结果沉淀

- `fail` 判定 → 归因条目（`eval_attribution_entries`：resource/dimension/snapshot/status open→closed）；`judge 误判` → 校准样本（`eval_calibration_samples`）；`case_revision` → case 修正走评测集编辑流程（评审池 spec §8.1 语义；落库差异见附录 A-4）。
- 归因条目的 `snapshot` 必须携带证据链引用：run_id / suite_revision_id / case_id 聚类 / 维度 / 差值 / 标签（§4.5 判定表）+ 报告结论 id，保证「一条归因 = 一条可回查证据」。
- 若评审池/归因引擎功能未上线，按同字段口径以人工台账沉淀，**字段与状态语义保持一致**，后续可机械迁入表。

---

## 5. 改进建议产出规则（「后训练/SFT」仅归因维度，A-1 已裁）

### 5.1 三类改进建议（承接主 spec §9.3）

指标劣化经归因筛选后产出三类建议；**每条建议必须携带归因证据链与预期影响**（§5.4），无证据链不产出：

| 类 | 形态 | 流向 | 产出条件 |
|---|---|---|---|
| ① 连续参数调优 | 因果矩阵命中维度的连续参数（temperature / max_tokens / max_context_tokens 等）→ SearchSpace 出候选值 | 候选/审批/金丝雀验证 → promote/回滚（复用现有候选与实验链路） | E1–E7 中参数级归因成立 |
| ② prompt 级建议 | 归因命中 prompt/指令 → 基于失败 case 生成修改建议 | 同①（金丝雀验证） | 参数级归因指向 prompt |
| ③ 非参数因素 → 代码变更 | 归因命中外部依赖/内容/检索/工具集等非参数因素 → **不硬调参** | 走代码/资源变更流程 | 归因排除价值（主 spec §9.3 第③条） |

### 5.2 模型级维度建议（选型 / 切换；本卡新增，承接主 spec §9.3 模型配置为归因维度）

「建议换模型」只在下列全部成立时产出（否则不产出选型建议）：

1. 归因结论指向**模型自身能力边界**（同配置下调参/prompt 无法弥补、同集对照中候选模型证明改善）；
2. 对照是**同集 A/B**（E2），且差值超 E3 门槛，分层方向一致（§3.2）；
3. 候选已在目录启用（或先启用再测）；成本/时延在预算内（cost/latency 报告一并给出）。

约束：

- chat 层 4 个候选同系（glm-5.x）→ 同系内切换建议基于真实同集对比；跨系候选不在当前目录则只给「需引入候选后再评」的方向性建议，不假装做过对比。
- embedding 层当前仅 embedding-3 enabled → **只能做绝对能力评估**（对 knowledge 基线的 recall@k/mrr/命中率），无法产出「相对选型」结论；相对选型需先启用 ≥1 个对比 embedding 候选。
- 切换类建议附回滚条件（新模型上线后同集回归/金丝雀验证期），但执行走资源变更流程，本框架只出建议与验证方案。

### 5.3 「后训练 / SFT」仅归因维度（A-1 已裁）

> **裁决（已确认）**：主会话裁定卡面用词「后训练建议」中涉及 SFT/微调的**只作为归因维度**处理（附录 A-1）。平台当前无模型权重、无训练数据管线、全仓库无 SFT/微调功能证据（recon A 节）——本框架**不产出、不定义任何 SFT/微调改进建议或清单**，也不把「后训练」当独立建议类型。模型行为缺陷一律翻译为平台上可执行的改进：

| 模型行为缺陷 | 翻译去向 | 说明 |
|---|---|---|
| 能力边界（领域知识/推理上限） | ② 模型选型/切换（§5.2） | 换更强/更合适模型或引入候选对照 |
| 指令遵循 / 格式 / 上下文利用差 | ② prompt 级 + ① 参数（max_context_tokens/temperature） | 先调可控变量 |
| 知识不足 / 幻觉 | ③ RAG / 知识库补全 + 检索配置 | 非参数因素，走代码/内容变更 |
| 工具使用差 | ③ 工具配置 / 工具说明 / 步骤约束 | 走资源配置变更 |
| **疑似仅微调可根治的缺陷** | 归因维度处理（见下），**不产建议** | 只归因，不进改进闭环 |

当归因证据（E1–E7 全过）指向「缺陷根因 = 模型自身能力边界、平台内参数/prompt/检索/选型均无法覆盖」时，该缺陷作为归因条目标记 `needs_model_training`（交模型供应商/模型选择决策方）。它**不**升级为改进建议、**不**承诺平台内解决、**不**进入参数优化闭环；归因条目仍按 §4.6 沉淀并回查证据。该标记是「不假装平台能做微调」的显式出口。

### 5.4 每条建议的强制结构

每条建议 = `{ 类型(①/②/③/选型) | 归因证据链(§4.5 判定表 + run/suite/case 引用) | 目标指标与预期方向量级 | 验证路径(金丝雀/复跑/同集对照) | 回滚/退出条件 | 责任方 }`。缺证据链或缺验证路径的建议不产出（对建议层同样 fail-closed）。疑似需 SFT 的缺陷不产建议，只按 §5.3 归因标记（`needs_model_training`）。

---

## 6. 数据来源与跑批门槛

### 6.1 现状（recon，2026-09-02）

生产 `eval_*` 全 0 行 → **现时点无结论级数据**；judge.enabled=false → 语义维不可用；`eval_calibration_samples` 0 行 → judge 无人工校准基线。以下门槛定义「数据到位」与「可出结论」的判据，不改变「不立即造数」的裁决（§8）。

### 6.2 数据来源（只读）

| 源 | 内容 | 用途 |
|---|---|---|
| eval 表（评测 DB） | `eval_runs / eval_case_results / eval_observations / eval_review_items / eval_attribution_entries / eval_calibration_samples` | 结论、归因、沉淀的主体（只读 SELECT；取数路径见 recon C 节：ssh → psql） |
| Opik + MinIO（证据权威，主 spec §1.4） | trace / payload | case 级下钻证据；**只读回查，不双写** |
| Prometheus（`eval_*` 指标） | 运行态健康/队列/校准 | 运行态画像，provisional；**不作评测集判定**（recon C 节） |

禁止把观测信号（EvalObservation，非权威判定）当评测集判定写入结论（主 spec §1.2）。

### 6.3 出结论的最低数据门槛（每「被测模型 × 承载资源」）

| 结论级 | 样本门槛 | 结论形态 | 依据 |
|---|---|---|---|
| **判异 / 信号级** | 哨兵集 **5–15 case** 全量跑（C1–C4） | flag / 需复核 / 护栏结论；**默认 low 或 no_conclusion**，统计显著性不承诺 | 主 spec §3.6 哨兵 |
| **正式结论级** | 标准集 **≥30 case**；失败/判 fail 维度样本 ≥10；每分层 ≥3 且覆盖 ≥2/3 声明层 | pass / conditional_pass / fail + 方向性归因/建议（medium） | 主 spec §3.6 标准集下界 |
| **决策级（含模型切换）** | 正式结论级全部 + **同集对照 A/B** 且差值超 E3 | 决策级结论 / 选型建议（high） | §4.5 + §5.2 |

- 数值默认承接主 spec §3.6 金字塔下界；**主会话已确认接受为框架默认**（附录 A-2），正文标注「默认值」，首份真实数据 dry-run（§9）后按实际校准锁值。
- 任一资源样本不足该级门槛 → 该资源不得上卷到模型级；模型层只能给「coverage 不足」声明。

### 6.4 语义维可用性门槛（judge 通道）

- 语义维（faithfulness/relevance/completeness/step_reasoning）结论要求：`evaluation.judge.enabled=true` **且** judge 具备人工校准基线（≥50 人工样本、约 85% 一致性，主 spec §11.2；校准样本即 `eval_calibration_samples` 沉淀）。
- judge 关闭/未校准 → 语义维只能标 low / no_conclusion；此时结论退化为确定性维度（safety/format/process/tool_pass）+ cost/latency，并显式声明「语义维未评估」。
- judge 用便宜模型独立于执行模型（主 spec §3.6）；同批对比 run 必须同 judge spec（C4）。

### 6.5 跑批入口与评测环境

| 用途 | 入口 | 说明 |
|---|---|---|
| 方法学演练 / 最小 dry-run / 回归 | `cmd/e2e-eval-check` + `test/e2e/{agent,skill,knowledge,mcp}/golden/cases.yaml` | 真实 LLM judge；CI 标记 `not_run`，需本地/dev 带 key 运行；**dev 态结果不作生产结论** |
| 判异 / 快速回归 | 在线 `EnqueueRun` 哨兵 suite | 需 suite publish；run 走双轨快照 |
| 正式 / 决策级结论 | 在线 `EnqueueRun` 标准/深度 suite | 需 suite publish + judge 启用 + 候选启用（选型时） |

评测环境选择：

- 结论级评测在**评测/测试环境 + 被测资源当前 published revision 快照**上跑（双轨 spec 保证 run 复现）；run 幂等可重跑，结论复现成本低。
- 使用生产快照跑不代表污染生产：评测 run 只读生产配置快照、写评测表，不改任何生产资源。
- 跑批预算显式化：正式结论级 ≈ 30+ case × (执行模型 + judge) 的真实 LLM 成本，报告必须附 `total_usd` 与预算对照。

### 6.6 数据充分性声明（机器可判定）

每份结论报告自带「数据充分性」块：n / 分层覆盖 / 锚点完整率 / judge 校准与置信汇总 / 各证据门槛(E1–E7) 命中情况 / 语义维是否受限。该块是 §3.4 标签与 §7.1 结论表的输入，缺失该块视为报告不合格。

---

## 7. 交付物与成功标准

### 7.1 结论报告格式模板

一份「模型评测结论报告」= 单一 markdown/JSON 双形态文档，结构固定如下（各节字段可判定、可回查）：

1. **评测快照**：被测模型(id/name/capabilities/生效窗口策略)；承载资源清单(kind/id/revision)；suite_revision_id + tier；双版本锚点(platform_seq/resource_version)；judge spec(model/rubric/enabled/校准状态)；run_id 列表；执行时间窗。
2. **总体判定**：按「模型 × 用途层」给出判定结论 + 可信度标签（§3.4），引用 §6.6 数据充分性。
3. **多维指标表**：by_dimension / by_category / cost / latency（承接主 spec §6.2 口径），含双版本锚点列。
4. **分层报告**：资源 × tier × tenant-tier 分层表；coverage 不足层显式标注（§3.2）。
5. **失败归因清单**：每条 = dimension + case 聚类 + 三级根因层 + E1–E7 证据判定 + 归因/假设标记 + 证据引用（§4）。
6. **改进建议清单**：按 §5.1/§5.2 结构（①/②/③/选型），含证据链/预期影响/验证路径/责任方；疑似需 SFT 缺陷不产建议，归因条目标记 `needs_model_training`（§5.3/§7.2）。
7. **数据充分性与限制声明**（§6.6）+ 有效期（§3.5）。
8. **meta**：run 数、总成本、judge 置信汇总、分层覆盖率。
9. **签署**：分析者 / reviewer / 日期。

### 7.2 归因条目与校准样本沉淀

- 报告内「失败归因清单」机械映射 `eval_attribution_entries`（open → 处置后 closed，附 reviewer/reason）；「judge 误判」映射 `eval_calibration_samples`；case 修正映射 case 编辑流程（评审池 spec §8.1）。
- 沉淀 = 评审池闭环的消费方：归因条目成为改进建议（§5）的输入，校准样本成为 judge 校准基线（§6.4）的增量。

### 7.3 评审池闭环（承接评审池 spec + 主 spec §6.6）

- 模型结论分析中发现的低置信/维度分歧/judge-rule 冲突/needs_review/过程-结果冲突条目 → 进入评审池人工复核；人工金标准回写后**同 case 不被自动覆盖**。
- 评审池积压（`eval_review_backlog`）与校准一致性进 §7.4 meta；积压超标告警复用既有规则（评审池 spec §12）。

### 7.4 meta 指标（结论体系自身的健康）

| 指标 | 口径 | 建议落点 |
|---|---|---|
| 结论报告数 / 判定分布 | pass/conditional/fail/no_conclusion 计数 | 报告台账 |
| no_conclusion 占比 | 数据不足结论占总产出比例（**伪归因防线的效果度量**） | §6.6 汇总 |
| 归因解析率 | attribution open → closed 的比例与周期 | `eval_attribution_entries.status` |
| 建议采纳率 | 建议 → 已执行/已验证/已回滚 的比例 | §5 建议台账 |
| judge 校准一致性 | judge vs 人工一致率 | 主 spec §11.2 `eval_judge_calibration_agreement` |
| 覆盖不足告警 | 某资源样本持续 < 门槛 | 主 spec §11.1 `eval_sample_coverage` |

新增指标需经监控/埋点流程（主 spec §11/§12）评审，本框架只列口径不实现。

### 7.5 成功标准（本框架验收，不依赖真实评测数据）

1. 有数据时能按 §7.1 模板产出结论报告；无数据/低置信时能显式标 no_conclusion / low，不空转、不硬结论。
2. E1–E7 证据门槛可判定：任何归因条目都能标注其证据判定，无证据不归因。
3. 建议规则（①/②/③/选型）+ SFT 仅归因维度（§5.3）清晰，无「微调能解决一切」式占位。
4. 模板/门槛/清单无占位符、无内部矛盾（自检 §7.6）。
5. 可选 dry-run：用 dev golden 最小哨兵样本跑一次 cmd/e2e-eval-check（本地带 key），产出报告骨架样例验证模板可填——非本卡强制交付，属方法学演练。

### 7.6 自检项（发布前过一遍）

- grep 无 `TBD / 待定 / XXX / 后续再说` 占位。
- 门槛数值与 §3.4 标签、§4.5 证据表、§6.3 门槛互相引用一致，无自相矛盾。
- 每条「必须/禁止」均有可判定对象（数量/条件/来源章节）。

---

## 8. 明确不做

- **不新建平行证据存储**：Opik/MinIO 为证据权威，评测 DB 只存控制面聚合（主 spec §1.4）；本框架只读，不复制 payload。
- **不替代 `cmd/e2e-eval-check`**（开发态回归）与**不实现任何评测功能**（多维指标、版本绑定、评审池、归因引擎分别由主 spec §13 Phase1–3 与评审池 spec 承接）。
- **不立即造数 / 不伪造评测结果**：现时点 eval_* 0 行是事实；本框架不编造基准、不把 provisional 观测当判定（主 spec §1.2）。
- **不做自动回滚 / 自动改代码 / 不可逆清理**（主 spec §1.4）：模型级判异只上升人工。
- **不产出任何 SFT / 微调改进建议或流程**：SFT 相关内容仅作归因维度（主会话已裁，§5.3 / 附录 A-1）；疑似需 SFT 缺陷只标记 `needs_model_training` 归因，交模型决策方，不进参数优化闭环。
- **不做前端视图**：归因对比视图/改进建议操作台属主 spec §10 与 §13 Phase2/3 的功能交付，不在本框架。
- **不改动任何生产配置与仓库文件**：本框架产出物 = /tmp 草稿与后续报告文档。

---

## 9. 后续执行预案（真实数据到位后的首份结论）

先决（已裁）：① SFT 仅归因维度，不产 SFT 建议/流程（A-1，已确认）；② 门槛数值接受框架默认，dry-run 后校准（A-2，已确认）。执行前仍需主会话决策：③ 是否启用线上 judge 与跑批预算；④ 选型类是否需先启用对比候选。

| 步 | 动作 | 责任方 |
|---|---|---|
| R1 | 选定首份结论的目标：被测对象（如 glm-5.2 的 agent 执行层 / embedding-3 的 knowledge 检索）与承载资源 | 评测分析师 + 主会话 |
| R2 | 数据盘点确认：目标资源的 published revision、可用 suite / 需从 golden 或现有 case 建 suite（哨兵子集优先），确认 judge.enabled 与校准状态 | 评测分析师 |
| R3 | 跑批：按 §6.5 选入口（判异→哨兵；正式→标准在线 suite；选型→同集 A/B），run 走双轨快照 | 评测执行方（评测/平台工程师） |
| R4 | 数据充分性核验（§6.6 门槛 + §3.4 标签）；不达门槛 → 输出 no_conclusion 声明并停止 | 评测分析师 |
| R5 | 归因分析：按 §4 三级根因 + E1–E7 证据门槛，产出失败归因清单 | 评测分析师（必要时资源 owner 提供上下文） |
| R6 | 建议产出：按 §5 三类 + 选型建议，每条带证据链与验证路径；疑似需 SFT 缺陷仅标记归因 `needs_model_training`（§5.3） | 评测分析师 + 资源 owner |
| R7 | 成稿：按 §7.1 模板出报告，归因/校准沉淀入库（§7.2），进评审池闭环（§7.3），meta 记入 §7.4 | 评测分析师 + reviewer |
| R8 | 评审与复核：报告 + 数据充分性 + 证据链由独立 reviewer 复核，判异/切换类上升主会话决策 | 独立 reviewer + 主会话 |

首份结论是**本框架的一次端到端演练**，其 output 反过来校验门槛数值是否合理（附录 A-2 的校准输入）。

---

## 附录 A：承接冲突与开放点记录

| # | 类型 | 内容 | 处置 |
|---|---|---|---|
| A-1 | 分歧（**已裁：仅归因**） | 卡面「后训练建议」是否含真模型 SFT/微调：平台无权重、无训练管线、无功能证据。 | **主会话裁决：SFT 相关内容仅作归因维度**——不产出 SFT/微调改进建议或流程（§5.3）；疑似需 SFT 缺陷只标记归因 `needs_model_training` 交模型决策方；真微调立项另作主题 |
| A-2 | 数值（**已裁：接受默认值**） | §6.3 出结论门槛默认值（判异 5–15、正式 ≥30、失败维 ≥10、每分层 ≥3、覆盖 ≥2/3）与 E3 显著差（比率 10pp / 均分 0.5@0–1 量纲）为框架默认，承接主 spec §3.6 下界但主 spec 未定义为「结论门槛」。 | **主会话确认接受为默认值**；正文保留「默认值」标注，首份真实数据 dry-run（§9 预案）后按实际校准锁值 |
| A-3 | 承接缺口 | 主 spec §6.6 评审触发第 5 条「归因证据不足 → 进池」，但评审池触发枚举（评审池 spec §7 四条 + tenant_schema 增补 `process_output_conflict`）**无 evidence_insufficient**。 | 本框架的 E1–E7 证据不足门禁**独立于评审池触发实现**，作为报告层门禁执行（§4.5），不阻塞评审池既有功能；如需入池触发另行排期 |
| A-4 | 措辞差异 | 评审池 spec §8.1：`fail`→归因条目、`case_revision`→仅回写；tenant_schema.sql 注释「fail/case_revision 落归因」。 | 以评审池 spec §8.1 Decision 副作用表为基准；若实现层 case_revision 也落归因条目，按 source_type 区分归类，并在报告中注明来源（§4.6） |
| A-5 | 无实质冲突 | 评审池 spec §5 DDL 与 tenant_schema 实际枚举扩展（`process_output_conflict`、`created_by` backfill）。 | schema 为准，仅记录 |
| A-6 | 概念扩展 | 主 spec §3.5 评测点未把「模型」提为一等维度（模型在资源配置内）；本框架显式提升为评测点第一维（§2.2）。 | 不改变双锚点基础；模型作为 resource_revision 的显式记录，归因时按正交分离处理（§4.4） |
