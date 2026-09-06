# Stratum Evaluation 告警 Runbook

评测运行态观测层（Phase 1）告警处置手册。

## 通用处置

- 所有判异类告警先到评测中心（运行态观测视图）按 trace 下钻，确认是否为真实能力退化。
- 告警只做通知与定位，处置动作（回滚/调参/拦截规则调整）走既有操作台与 CD 流程，禁止远端手改。
- 规则护栏（T1/T4）命中即拦截，属 fail closed 预期行为；持续命中说明平台规则需重新评估。

<a id="stratum-eval-behavior-anomaly"></a>

## StratumEvalBehaviorAnomaly

行为异常判异速率升高（judge 跌阈 / 行为放弃 / 行为升级）。

- 定位：查询 `eval_behavior_anomaly_total` 按 signal 分组，定位具体异常维度与 resource。
- 确认：到评测中心下钻对应 trace，核对 judge 分数与行为信号来源（feedback 或 agent 埋点）。
- 处置：真能力退化 → 走参数/版本调整；误报 → 调整判异阈值或信号映射。

<a id="stratum-eval-sample-coverage-low"></a>

## StratumEvalSampleCoverageLow

主动采样覆盖率低于阈值，观测落库可能被静默跳过。

- 语义：`eval_sample_coverage` = 落库观测 / 采样候选（采样通过且 judge 开启）。健康稳态 ≈1.0；
  judge 配置关闭（主动停观测）不计入分母、不触发本告警。
- 定位：先区分「主动停观测」与「故障降级」——核对 `evaluation.observe.enabled` / `evaluation.observe.sample_rate` 与 judge 可用性；
  覆盖率掉低但 judge 正常时，查落库链路（Validate 失败 / Save 失败）与 `eval_judge_failure_total` 的 `reason` 维度。
- 处置：恢复 judge 或修复落库链路；覆盖率长期低说明观测失去代表性（§14 禁止静默跳过某层）。

<a id="stratum-eval-rule-blocked"></a>

## StratumEvalRuleBlocked

规则护栏即时拦截命中（T4 红线级）。

- 定位：按 `rule` label 定位命中规则与工具；查询 `eval_rule_hit_total` 按 tool 分布。
- 处置：T4 强制人工确认——评估该工具是否应继续禁用、denylist 是否需调整，经审批后在平台参数更新
  `evaluation.ruleguard.denylist`，再回归验证。禁止自动放行（fail closed，§14）。

<a id="stratum-eval-judge-degraded"></a>

## StratumEvalJudgeDegraded

judge 外部依赖持续不可用（§11.2 judge 健康）。

- 语义：任一 30 分钟窗口内 `eval_judge_failure_total{reason="judge_unavailable"}` 累计
  ≥ 3 次且持续 15 分钟触发；单次瞬时抖动不告警。恢复后需等失败事件滚出 30 分钟窗口，
  告警自动消除。
- 定位：查询 `eval_judge_failure_total` 的 `reason="judge_unavailable"` 维度，核对 LLM provider 可用性、限额与配置。
- 确认：judge 属异步外部依赖，需有超时预算、有限重试与熔断/隔离；区分瞬时抖动与持续降级。
- 处置：恢复 provider 可用性或调整 judge 配置；恢复后告警自动消除。

<a id="stratum-eval-queue-backlog-high"></a>

## StratumEvalQueueBacklogHigh

评测观测消费队列积压超过阈值。

- 语义：仅评测观测消费队列 `eval_queue_backlog{queue="observation"}` 积压 > 1000 持续
  15 分钟触发；同指标下其它队列不计入本告警。
- 定位：查询 `eval_queue_backlog{queue="observation"}`，观察消费停滞来源（消费速率、落库链路）。
- 确认：消费停滞将延迟观测落库与采样覆盖统计，先确认是消费端故障还是上游突发。
- 处置：修复 observation consumer（重启/扩容/修落库），积压消化后告警自动恢复。

<a id="stratum-eval-judge-below-threshold"></a>

## StratumEvalJudgeBelowThreshold

judge 单维度低于阈值（score < JudgeBelowThreshold）的跌阈判异升高。

- 语义：`eval_gate_action_total{layer="detect",action="flag"}` 15 分钟 increase > 0，或
  `eval_judge_score` 直方图 <0.5 尾部占比 > 30%（10 分钟窗口）触发；第一腿信号现网即燃
  （applyAnomalyVerdict emit detect/flag，§3.2-①）。
- 定位：查询 `eval_gate_action_total{layer="detect",action="flag"}` 与 `eval_judge_score`
  直方图尾部，定位低分维度与 resource；到评测中心按 trace 下钻核对 judge 分数。
- 处置：真能力退化 → 走参数/版本调整并回归验证；误报 → 复核 judge 阈值与评测量纲后调整
  `evaluation.judge.*` 阈值。确认路径 = 评审池人工确认（§3.2-①）。

<a id="stratum-eval-run-regression"></a>

## StratumEvalRunRegression

run 级回归劣化判异（相对基线 run 的维度 delta 跌破 `RunRegressionDeltaThreshold`）。

- 语义：`eval_gate_action_total{layer="l2",action="regression"}` 15 分钟 increase > 0。
  P2 该 label 值仅由发布哨兵判劣 emit（emit 点 Task 5），T13 确认 run 复用——规则先落、当前
  只读待命；`{layer="l2",action="regression"}` 是 P2 新增 label 值，不新增 metric family。
- 定位：查询该 counter 按 resource/suite_revision 分组定位劣化 run；对比 base vs current
  run 的 `metrics.by_dimension`（纯函数 `application.CompareRunRegression`；基线为同 suite
  revision 的同 resource 最近 completed run）。
- 处置：真回归 → 走门禁流程（人工确认/回滚候选）；误报 → 核对基线选择与维度 delta 阈值。

<a id="stratum-eval-rule-disabled"></a>

## StratumEvalRuleDisabled

规则护栏命中但未拦截：`evaluation.ruleguard.enabled=false` 但 `denylist` 非空（O4 检测恒开 + 执行受控的提示告警）。

- 语义：任一 15 分钟窗口内 `eval_rule_hit_total{verdict="detected"}` 命中且持续 5 分钟触发。
  verdict=detected 表示护栏「检测到但未拦截」；`StratumEvalRuleBlocked`（critical，verdict=block）
  不受污染——disabled 命中不会误触 critical。
- 定位：查询 `eval_rule_hit_total{verdict="detected"}` 按 `rule`/`resource` 分组，确认命中工具
  是否本应禁用；比对平台参数 `evaluation.ruleguard.enabled` 与 `evaluation.ruleguard.denylist`
  当前值（registry 平台级 low risk_tier）。
- 确认：enabled=false 时 denylist 命中只产观测（评测侧判 VerdictBlock，属显式接受副作用），
  不拦截执行——这与 O4「未启用规则 = 无规则可命中」语义一致，非执行故障。
- 处置：命中工具应禁用 → 平台参数开启 `evaluation.ruleguard.enabled=true`，走平台参数发布审批，
  随后回归验证命中即拦截；误报 → 收紧 `evaluation.ruleguard.denylist`。禁止远端手改（变更走
  操作台/CD 流程）。

<a id="stratum-eval-multitenant-verify-not-recovered"></a>

## StratumEvalMultiTenantVerifyNotRecovered

平台版本回滚后，multi-tenant verify 判定存在未恢复租户（critical，T4 红线级）。

- 定位：查询 `eval_gate_action_total{layer="l3_multitenant_verify",action="not_recovered"}` 按租户维度下钻；
  确认回滚动作（group_key/from_seq/to_seq）与受影响租户。
- 确认：到该租户核对回滚目标 seq 下的 run 表现（`FindLatestCompletedRunForPlatformSeq` 锚定 to_seq）；
  not_recovered = 回滚后（好版本）run 仍劣于回滚前（坏版本）run（run 级回归 Regressed=true）。
- 处置：平台参数影响全租户，恢复不达标需人工介入——复核回滚目标是否为真「上一好版本」，必要时继续回滚到更早
  版本或调整配置；处置动作走参数操作台与 CD，禁止远端手改。恢复后 not_recovered 计数停止增长即自动消除。

<a id="stratum-eval-platform-multitenant-divergence"></a>

## StratumEvalPlatformMultiTenantDivergence

平台版本多租户验证分化（多数恢复 / 少数未恢复，warning，仅信号不自动处置）。

- 语义：同一验证窗口内 recovered 与 not_recovered 并存 = 分布效应（多数改善、少数劣化），
  可能源于租户规模/tier/流量差异（防辛普森悖论需分层归因）。
- 定位：把 not_recovered 的租户名单按 tier/行业/流量规模分层下钻，找出劣化集中段。
- 处置：仅告警，不自动回滚；人工在归因视图确认劣化是否真实能力退化，走参数调整/定向回滚流程。
