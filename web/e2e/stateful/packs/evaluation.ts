import { expect, type Locator, type Page } from '@playwright/test';
import type { QueryResultRow } from 'pg';

import type { BrowserActor } from '../core/actors';
import {
  configureManagedModels, requireUUID, withTenantMutation, withTenantQuery, type DatabasePool,
} from '../core/database';
import type { EvidenceRecord } from '../core/evidence';
import { runCleanupTasks } from '../core/errors';

interface EvaluationPackContext { actor: BrowserActor; pool: DatabasePool; evidence: EvidenceRecord; webURL: string; fixtureURL: string; backendURL: string }
const waitFor = async (page: Page, path: string | RegExp, method: string) => {
  try {
    return await page.waitForResponse((response) => {
      const pathname = new URL(response.url()).pathname;
      return (typeof path === 'string' ? pathname === path : path.test(pathname))
        && response.request().method() === method;
    });
  } catch (error) {
    throw new Error(`waiting for ${method} ${String(path)}: ${error instanceof Error ? error.message : String(error)}`);
  }
};
const rows = async <R extends QueryResultRow>(pool: DatabasePool, tenantID: string, text: string, values: unknown[]) => (
  await withTenantQuery<R>(pool, tenantID, { text, values })
).rows;
const mutate = async (pool: DatabasePool, tenantID: string, text: string, values: unknown[]) => (
  await withTenantMutation(pool, tenantID, { text, values })
);
const openEvolution = async (page: Page, url: string) => {
  // 命令（reject/promote/rollback/pause）后列表异步 reload：先等 reload 稳定、
  // 再关闭残留遮罩，避免后续点击被拦截。自进化工作区独立成页（Batch 3+），
  // 候选/金丝雀操作全部落到 /evaluations/evolution 页面（默认候选版本 tab）。
  await page.goto(url);
  await expect(page.getByRole('heading', { name: '自进化工作区' })).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('.ant-spin-spinning')).toHaveCount(0, { timeout: 15_000 });
  await closeDrawerIfOpen(page);
  await page.getByRole('button', { name: /进化操作/ }).click();
  return page.getByRole('dialog', { name: '进化操作' });
};
// EvolutionPage 自身是 Tabs（候选版本/金丝雀实验），隐藏 pane 仍在 DOM（display:none）；
// 一律在活动 pane 内取行，避免 Playwright strict 命中隐藏重复行。
const activeTableRow = (page: Page, text: string) =>
  page.locator('.ant-tabs-tabpane-active .ant-table-row').filter({ hasText: text }).first();
const closeDrawerIfOpen = async (page: Page) => {
  // 单次 count 判断会漏在 drawer 瞬关窗口上，残留 mask 拦截后续点击
  // （soak 实测 45s 仍被 ant-drawer-mask 拦截、候选版本 tab 点击超时）。
  // 循环关闭直到 drawer 与其 mask 都消失。
  for (let attempt = 0; attempt < 5; attempt++) {
    const drawer = page.locator('.ant-drawer:visible');
    const mask = page.locator('.ant-drawer-mask:visible');
    if (!(await drawer.count()) && !(await mask.count())) return;
    await page.keyboard.press('Escape');
    await mask.click({ position: { x: 5, y: 5 }, timeout: 5_000 }).catch(() => {});
    await expect(drawer).toBeHidden({ timeout: 10_000 });
    await expect(mask).toHaveCount(0, { timeout: 10_000 });
  }
};

const pickSuite = async (scope: Page | Locator, page: Page, suiteName: string) => {
  // SuitePicker 两级选择：先选评测集（combobox aria-label="评测集"），发布套件会自动
  // 装载版本链并把当前 active revision 设为默认；下拉选项按唯一 suiteName 过滤。
  await scope.getByRole('combobox', { name: '评测集' }).click();
  await page.locator('.ant-select-item-option-content').filter({ hasText: suiteName }).click();
};

// 决策夹具的假资源被测类型必须是中心默认两轨（agent/knowledge）内，才能出现在
// 「金丝雀实验」列表被 UI 驱动 promote/rollback。skill/mcp 已退出中心默认视图，故从
// 原 mcp 收敛为 agent：直插 resource_revisions(kind=agent) + running 实验 + 部署。
const seedDecisionFixtures = async (
  pool: DatabasePool, tenantID: string, suiteRevisionID: string, suffix: string,
) => {
  const policy = JSON.stringify({ stages: [5, 20, 50, 100], min_samples: 100, min_observation_minutes: 60,
    max_cost_regression: 0.15, max_latency_regression: 0.2, max_error_rate_increase: 0.01 });
  const evidence = JSON.stringify({ metrics: { samples: 100, observed_minutes: 60, quality_improvement: 0.2,
    quality_significant: true, cost_regression: 0, p95_latency_regression: 0, error_rate_increase: 0,
    security_violation: false } });
  const fixtures = [
    { action: 'promote', resource: `e2e-promote-${suffix}`, experiment: `e2e-promote-experiment-${suffix}`,
      recommendation: 'promote', snapshot: evidence },
    { action: 'rollback', resource: `e2e-rollback-${suffix}`, experiment: `e2e-rollback-experiment-${suffix}`,
      recommendation: 'hold', snapshot: '{}' },
  ];
  for (const fixture of fixtures) {
    const stable = `${fixture.resource}-stable`;
    const canary = `${fixture.resource}-canary`;
    const optimizationJob = `${fixture.resource}-optimization`;
    const candidate = `${fixture.resource}-candidate`;
    await mutate(pool, tenantID, `
      INSERT INTO resource_revisions
        (id,resource_kind,resource_id,source,status,content_hash,payload_hash,payload_ref,safe_summary,published_at)
      VALUES ($1,'agent',$2,'manual','published',$3,$3,$4,$5::jsonb,now()),
             ($6,'agent',$2,'optimization','draft',$7,$7,$8,$9::jsonb,NULL)`,
    [stable, fixture.resource, `hash-${stable}`, `fixture://${stable}`,
      JSON.stringify({ name: fixture.resource, revision: 'stable' }), canary, `hash-${canary}`, `fixture://${canary}`,
      JSON.stringify({ name: fixture.resource, revision: 'canary' })]);
    await mutate(pool, tenantID, `
      INSERT INTO optimization_jobs
        (id,resource_kind,resource_id,baseline_revision_id,suite_revision_id,status,completed_at)
      VALUES ($1,'agent',$2,$3,$4,'succeeded',now())`,
    [optimizationJob, fixture.resource, stable, suiteRevisionID]);
    await mutate(pool, tenantID, `
      INSERT INTO optimization_candidates
        (id,optimization_job_id,revision_id,parent_revision_id,source,status)
      VALUES ($1,$2,$3,$4,'optimization','proposed')`,
    [candidate, optimizationJob, canary, stable]);
    await mutate(pool, tenantID, `
      INSERT INTO evaluation_experiments
        (id,resource_kind,resource_id,stable_revision_id,canary_revision_id,suite_revision_id,status,stage_percent,
         policy,decision_snapshot,state_version,recommendation,safety_stopped)
      VALUES ($1,'agent',$2,$3,$4,$5,'running',5,$6::jsonb,$7::jsonb,1,$8,false)`,
    [fixture.experiment, fixture.resource, stable, canary, suiteRevisionID, policy, fixture.snapshot,
      fixture.recommendation]);
    await mutate(pool, tenantID, `
      INSERT INTO evaluation_deployments
        (resource_kind,resource_id,stable_revision_id,canary_revision_id,canary_percent,experiment_id)
      VALUES ('agent',$1,$2,$3,5,$4)`, [fixture.resource, stable, canary, fixture.experiment]);
  }
  return fixtures.map((fixture) => ({ ...fixture, stable: `${fixture.resource}-stable`, canary: `${fixture.resource}-canary` }));
};

export const executeEvaluationPack = async ({
  actor, pool, evidence, webURL, fixtureURL, backendURL,
}: EvaluationPackContext): Promise<string[]> => {
  const tenantID = requireUUID(actor.tenantID ?? '', 'tenant_id');
  const userID = requireUUID(actor.userID ?? '', 'user_id');
  await configureManagedModels(pool, tenantID, fixtureURL, actor.accessToken ?? '', backendURL);
  const page = await actor.context.newPage();
  const suffix = String(Date.now());
  const skillName = `E2E-Evaluation-Skill-${suffix}`;
  const agentName = `E2E-Evaluation-Agent-${suffix}`;
  const suiteName = `E2E Stateful Suite ${suffix}`;
  let skillID = '';
  let agentID = '';
  let agentStableRevisionID = '';
  let suiteID = '';
  let suiteRevisionID = '';
  let runID = '';
  let experimentID = '';
  let fixtureIDs: string[] = [];
  let reviewID = '';
  try {
    // 被测收敛后主被测轨为 agent：先建一个可激活的绑定 skill（/skills/create 即出
    // published active revision），再建绑定该技能的 agent，最后在评测中心「登记被测
    // 资源」建档 agent 成为被测主体（skill 不再独立建档/发起评测）。
    await page.goto(`${webURL}/skills/create`);
    await page.getByLabel('名称').fill(skillName);
    await page.getByLabel('描述').fill('返回可核验的 stateful 评测结果');
    await page.getByLabel('执行指令').fill('返回 stateful sync completed。');
    // 三字段模型（3d037c86 简化）：保存即生效，create 响应 active.id 即 stable revision。
    const skillResponse = waitFor(page, '/skills', 'POST');
    await page.getByRole('button', { name: /创\s*建/ }).click();
    const createdSkill = await skillResponse;
    expect(createdSkill.status()).toBe(201);
    const skillBody = await createdSkill.json() as { skill: { id: string }; active: { id: string } };
    skillID = skillBody.skill.id;
    const skillActiveRevisionID = skillBody.active.id;

    const agentSkillsResponse = waitFor(page, '/skills', 'GET');
    await page.goto(`${webURL}/agents/create`);
    const listedSkills = await agentSkillsResponse;
    expect(listedSkills.status()).toBe(200);
    expect((await listedSkills.json() as { skills: Array<{ id: string; name: string }> }).skills)
      .toEqual(expect.arrayContaining([expect.objectContaining({ id: skillID, name: skillName })]));
    await page.getByLabel('名称').fill(agentName);
    await page.getByLabel('系统提示词').fill('执行激活的 Skill，并返回确定的 stateful 结果。');
    const modelInput = page.getByRole('combobox', { name: 'LLM 模型' });
    await modelInput.fill('qwen-max');
    await modelInput.press('Enter');
    const agentResponse = waitFor(page, '/agents', 'POST');
    await page.getByRole('button', { name: '创建 Agent' }).click();
    const createdAgent = await agentResponse;
    expect(createdAgent.status()).toBe(201);
    agentID = (await createdAgent.json() as { id: string }).id;
    await mutate(pool, tenantID,
      'INSERT INTO agent_skill_links(agent_id,skill_id,revision_id) VALUES ($1,$2,$3)',
      [agentID, skillID, skillActiveRevisionID]);
    expect(await rows<{ skill_id: string }>(pool, tenantID,
      'SELECT skill_id FROM agent_skill_links WHERE agent_id=$1', [agentID])).toEqual([{ skill_id: skillID }]);

    // 建档收敛到评测中心的统一登记入口（POST /evaluations/resources/agent/:id/baseline，
    // 产出一条 published revision 作为被测 agent 的稳定基线）。不再直插 skill/mcp 的
    // resource_revisions 或 deployment——skill/mcp 已退出建档。hub 拆除后登记落到被测
    // 资源详情页：未建档 Alert 就地提供「登记该资源」CTA（URL 深链直达，无 ?action= 残留）。
    const resourceListResponse = waitFor(page, '/evaluations/resources', 'GET');
    await page.goto(`${webURL}/evaluations/resources/agent/${agentID}`);
    expect((await resourceListResponse).status()).toBe(200);
    // 未建档详情页以 URL 资源 id 作页头主文案（resource 未建档无 resource_name）。
    await expect(page.getByRole('heading', { name: agentID })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole('button', { name: '登记该资源' })).toBeVisible({ timeout: 15_000 });
    await page.getByRole('button', { name: '登记该资源' }).click();
    const registerDialog = page.getByRole('dialog', { name: '登记被测资源' });
    // 详情页登记框由 URL 预填 kind+resource_id；仍按唯一名在资源下拉搜索确认建档对象
    // （资源下拉加载线上 agent：GET /agents）。
    const resourceCombobox = registerDialog.getByRole('combobox', { name: '被测资源' });
    await resourceCombobox.click();
    await resourceCombobox.fill(agentName);
    await page.locator('.ant-select-item-option-content').filter({ hasText: agentName }).click();
    const baselineResponse = waitFor(page, /\/evaluations\/resources\/[^/]+\/[^/]+\/baseline$/, 'POST');
    // 详情页登记框 footer 仅含「取消」+ 主按钮「登记」（无「登记并新建评测」快捷）；
    // antd 对两汉字按钮插空格（登记→登 记），用锚定正则精确匹配主按钮。
    await registerDialog.getByRole('button', { name: /^登\s*记$/ }).click();
    const baseline = await baselineResponse;
    expect(baseline.status()).toBe(201);
    agentStableRevisionID = (await baseline.json() as { revision_id: string }).revision_id;
    await expect(registerDialog).toBeHidden();
    // 登记成功触发详情页 reload：稳定版本标头即建档完成信号（stable_revision_id 为基线 revision）。
    await expect(page.getByText(new RegExp(`稳定版本\\s*${agentStableRevisionID}`))).toBeVisible({ timeout: 15_000 });
    expect((await rows<{ id: string; status: string }>(pool, tenantID,
      'SELECT id,status FROM resource_revisions WHERE id=$1 AND resource_kind=$2 AND resource_id=$3',
      [agentStableRevisionID, 'agent', agentID]))[0]).toEqual({ id: agentStableRevisionID, status: 'published' });
    // 建档行回落资源列表页（/evaluations/resources）：资源表每行恒渲染 resource_id，
    // 以 agentID 作唯一建档持久化信号（safe_summary.name 未必等于 agentName）。
    await page.goto(`${webURL}/evaluations/resources`);
    await expect(page.getByRole('heading', { name: '被测资源' })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole('row').filter({ hasText: agentID })).toHaveCount(1, { timeout: 15_000 });

    // 离线运行页统一承载「新建评测」（原 hub 入口下沉到 runs 子页）。
    await page.goto(`${webURL}/evaluations/runs`);
    await expect(page.getByRole('heading', { name: '离线运行' })).toBeVisible({ timeout: 15_000 });
    await page.getByRole('button', { name: /新建评测/ }).click();
    const createDialog = page.getByRole('dialog', { name: '新建评测' });
    await createDialog.getByRole('combobox', { name: '目标资源' }).click();
    await page.locator('.ant-select-item-option-content').filter({ hasText: agentID }).click();
    // 两模式 radio（已有评测集 / 新建评测集）：切到「新建评测集」才渲染 create 表单。
    await createDialog.locator('.ant-radio-button-wrapper').filter({ hasText: '新建评测集' }).click();
    await createDialog.getByLabel('评测集名称').fill(suiteName);
    await createDialog.getByLabel('评测集说明').fill('真实浏览器发起的 stateful evaluation');
    await createDialog.getByLabel('用例名称').fill('确定性 Agent 输出');
    await createDialog.getByLabel('测试输入').fill('执行 stateful evaluation');
    await createDialog.getByLabel('期望输出').fill('stateful sync completed');
    const suiteResponse = waitFor(page, '/evaluations/suites', 'POST');
    const publishSuiteResponse = waitFor(page, /\/evaluations\/suites\/[^/]+\/publish$/, 'POST');
    const runResponse = waitFor(page, '/evaluations/runs', 'POST');
    await createDialog.getByRole('button', { name: '创建并运行' }).click();
    const suiteCreated = await suiteResponse;
    expect(suiteCreated.status()).toBe(201);
    suiteID = (await suiteCreated.json() as { suite: { id: string } }).suite.id;
    const suitePublished = await publishSuiteResponse;
    expect(suitePublished.status()).toBe(200);
    suiteRevisionID = (await suitePublished.json() as { id: string }).id;
    expect((await runResponse).status()).toBe(202);
    await expect.poll(async () => (await rows<{ id: string; status: string }>(pool, tenantID,
      `SELECT id,status FROM eval_runs WHERE resource_id=$1 AND suite_revision_id=$2 ORDER BY created_at DESC LIMIT 1`,
    [agentID, suiteRevisionID]))[0]?.status, { timeout: 120_000 }).toBe('succeeded');
    const run = (await rows<{ id: string; trace_id: string; error_message: string; actual_output: string }>(pool, tenantID, `
      SELECT r.id,
             cr.trace_id,
             COALESCE(cr.error_message, '') AS error_message,
             COALESCE(cr.actual_output::text, '') AS actual_output
      FROM eval_runs r
      JOIN eval_case_results cr ON cr.run_id = r.id
      WHERE r.resource_id=$1 AND r.suite_revision_id=$2 ORDER BY r.created_at DESC LIMIT 1`,
    [agentID, suiteRevisionID]))[0];
    if (!run) throw new Error('evaluation run result was not persisted');
    runID = run.id;
    expect(run.trace_id,
      `evaluation trace missing; error=${run.error_message.slice(0, 240)} actual=${run.actual_output.slice(0, 240)}`).not.toBe('');

    // ── 评测集管理页闭环：套件列表 / 详情 / 草稿用例增删 / legacy 补建草稿 ──────────
    // 覆盖 5 个新 surface 能力：route /evaluations/suites、/evaluations/suites/:id，
    // mutation POST draft、POST draft/cases、DELETE draft/cases/:caseId。
    const suiteListResponse = waitFor(page, '/evaluations/suites', 'GET');
    await page.goto(`${webURL}/evaluations/suites`);
    expect((await suiteListResponse).status()).toBe(200);
    await expect(page.getByRole('heading', { name: '评测集' })).toBeVisible();
    const suiteRow = page.getByRole('row').filter({ hasText: suiteName });
    await expect(suiteRow).toHaveCount(1, { timeout: 15_000 });

    const suiteDetailResponse = waitFor(page, `/evaluations/suites/${suiteID}`, 'GET');
    await suiteRow.getByRole('button', { name: /详\s*情/ }).click();
    expect((await suiteDetailResponse).status()).toBe(200);
    await expect(page.getByRole('heading', { name: suiteName })).toBeVisible();

    // 添加草稿用例 → POST /evaluations/suites/:suiteId/draft/cases (201)
    await page.getByRole('button', { name: '添加用例' }).click();
    const addCaseDialog = page.getByRole('dialog', { name: '添加草稿用例' });
    const addedCaseName = `E2E-Managed-Case-${suffix}`;
    await addCaseDialog.getByLabel('用例名称').fill(addedCaseName);
    await addCaseDialog.getByLabel('测试输入').fill('stateful management add case input');
    await addCaseDialog.getByLabel('期望输出').fill('stateful management add case expected');
    // Ant 会在恰好两个汉字的按钮文本间自动插入空格（添加 → 添 加），
    // 故用 \s* 正则匹配，避免可访问名称不匹配导致点击超时。
    const addCaseResponse = waitFor(page, `/evaluations/suites/${suiteID}/draft/cases`, 'POST');
    await addCaseDialog.getByRole('button', { name: /添\s*加/ }).click();
    const addedCase = await addCaseResponse;
    expect(addedCase.status()).toBe(201);
    const addedCaseID = (await addedCase.json() as { id: string }).id;
    await expect.poll(async () => Number((await rows<{ count: string }>(pool, tenantID, `
      SELECT count(*)::text AS count FROM eval_cases ec
      JOIN eval_suite_revisions esr ON esr.id = ec.suite_revision_id
      WHERE esr.suite_id=$1 AND esr.status='draft'`, [suiteID]))[0]?.count),
    { timeout: 15_000 }).toBe(2);

    // 重挂载草稿折叠面板使其全部展开，再删除新增用例 → DELETE .../draft/cases/:caseId (204)
    await page.reload();
    await expect(page.getByRole('heading', { name: suiteName })).toBeVisible();
    const managedCasePanel = page.locator('.ant-collapse-item').filter({ hasText: addedCaseName });
    await expect(managedCasePanel).toHaveCount(1, { timeout: 15_000 });
    const deleteCaseResponse = waitFor(page, `/evaluations/suites/${suiteID}/draft/cases/${addedCaseID}`, 'DELETE');
    await managedCasePanel.getByRole('button', { name: /删\s*除/ }).click();
    const deleteCaseDialog = page.locator('.ant-modal-confirm').filter({ hasText: addedCaseName });
    await deleteCaseDialog.getByRole('button', { name: /删\s*除/ }).click();
    expect((await deleteCaseResponse).status()).toBe(204);
    await expect.poll(async () => Number((await rows<{ count: string }>(pool, tenantID, `
      SELECT count(*)::text AS count FROM eval_cases ec
      JOIN eval_suite_revisions esr ON esr.id = ec.suite_revision_id
      WHERE esr.suite_id=$1 AND esr.status='draft'`, [suiteID]))[0]?.count),
    { timeout: 15_000 }).toBe(1);
    await expect(managedCasePanel).toHaveCount(0, { timeout: 15_000 });

    // 模拟 legacy 套件（已发布但无草稿）：删除当前 draft revision 并置空
    // draft_revision_id，触发「从此版本新建草稿」→ POST /evaluations/suites/:suiteId/draft (200)
    const legacyDraft = (await rows<{ draft_revision_id: string | null }>(pool, tenantID,
      'SELECT draft_revision_id FROM eval_suites WHERE id=$1', [suiteID]))[0];
    if (!legacyDraft?.draft_revision_id) throw new Error('suite expected an open draft before legacy simulation');
    await mutate(pool, tenantID, 'DELETE FROM eval_suite_revisions WHERE id=$1', [legacyDraft.draft_revision_id]);
    await mutate(pool, tenantID, 'UPDATE eval_suites SET draft_revision_id=NULL WHERE id=$1', [suiteID]);
    await page.reload();
    await expect(page.getByRole('heading', { name: suiteName })).toBeVisible();
    await expect(page.getByRole('button', { name: '从此版本新建草稿' })).toBeVisible({ timeout: 15_000 });
    const startDraftResponse = waitFor(page, `/evaluations/suites/${suiteID}/draft`, 'POST');
    await page.getByRole('button', { name: '从此版本新建草稿' }).click();
    expect((await startDraftResponse).status()).toBe(200);
    await expect.poll(async () => (await rows<{ draft_revision_id: string | null }>(pool, tenantID,
      'SELECT draft_revision_id FROM eval_suites WHERE id=$1', [suiteID]))[0]?.draft_revision_id,
    { timeout: 15_000 }).toBeTruthy();
    await expect(page.getByRole('button', { name: '添加用例' })).toBeVisible({ timeout: 15_000 });

    await page.goto(`${webURL}/evaluations`);
    // /evaluations 已整体重定向到离线运行页（hub 拆除，见 Batch 3），路由收敛后无中心 hub。
    await expect(page).toHaveURL(/\/evaluations\/runs$/);
    await expect(page.getByRole('heading', { name: '离线运行' })).toBeVisible();
    evidence.ui.push('Evaluation suite list, detail, draft case add/delete, and legacy draft start completed through Chromium');
    evidence.http.push('Suite list/detail GET, draft case POST/DELETE, and legacy draft POST returned successful browser-observed responses');
    evidence.database.push('Suite draft revision and eval_cases reconciled after add, delete, and legacy start');

    let dialog = await openEvolution(page, `${webURL}/evaluations/evolution`);
    let panel = dialog.locator('.ant-tabs-tabpane-active');
    await panel.getByRole('combobox', { name: '资源类型' }).click();
    await page.locator('.ant-select-item-option-content').filter({ hasText: 'Agent' }).click();
    await panel.getByLabel('资源 ID').fill(agentID);
    await panel.getByLabel('稳定 Revision ID').fill(agentStableRevisionID);
    await panel.getByLabel('失败摘要').fill('输出需要更明确且可核验');
    // SuitePicker 两级选择评测集并自动选 active 版本；选完前「生成候选」保持禁用。
    await pickSuite(panel, page, suiteName);
    const optimizationResponse = waitFor(page, '/evaluations/optimizations', 'POST');
    await expect(dialog.getByRole('button', { name: '生成候选' })).toBeEnabled({ timeout: 15_000 });
    await dialog.getByRole('button', { name: '生成候选' }).click();
    const optimized = await optimizationResponse;
    expect(optimized.status()).toBe(201);
    const candidates = (await optimized.json() as { candidates: Array<{ id: string; revision: { revision_id: string } }> }).candidates;
    expect(candidates).toHaveLength(2);
    // EvolutionPage 候选表不展示候选 id，行定位改用唯一可显示的候选 revision。
    const evaluateRevision = candidates[1].revision.revision_id;
    const rejectRevision = candidates[0].revision.revision_id;
    await expect(dialog).toBeHidden();

    await expect(activeTableRow(page, evaluateRevision)).toHaveCount(1, { timeout: 15_000 });
    await activeTableRow(page, evaluateRevision).getByRole('button', { name: /详\s*情/ }).click();
    const candidateDrawer = page.locator('.ant-drawer:visible');
    await candidateDrawer.getByRole('button', { name: '运行离线评测' }).click();
    const evaluationDialog = page.getByRole('dialog', { name: '运行候选离线评测' });
    await pickSuite(evaluationDialog, page, suiteName);
    const candidateRunResponse = waitFor(page, '/evaluations/runs', 'POST');
    await expect(evaluationDialog.getByRole('button', { name: '开始评测' })).toBeEnabled({ timeout: 15_000 });
    await evaluationDialog.getByRole('button', { name: '开始评测' }).click();
    expect((await candidateRunResponse).status()).toBe(202);
    await expect(evaluationDialog).toBeHidden();
    await expect.poll(async () => (await rows<{ status: string; passed: boolean }>(pool, tenantID, `
      SELECT status,passed FROM eval_runs WHERE resource_id=$1 AND revision_id=$2 AND suite_revision_id=$3
      ORDER BY created_at DESC LIMIT 1`, [agentID, evaluateRevision, suiteRevisionID]))[0],
    { timeout: 120_000 }).toEqual({ status: 'succeeded', passed: true });
    await closeDrawerIfOpen(page);
    await page.reload();
    await expect(page.getByRole('heading', { name: '自进化工作区' })).toBeVisible({ timeout: 15_000 });

    await expect(activeTableRow(page, rejectRevision)).toHaveCount(1, { timeout: 15_000 });
    await activeTableRow(page, rejectRevision).getByRole('button', { name: /详\s*情/ }).click();
    const rejectResponse = waitFor(page, `/evaluations/candidates/${candidates[0].id}/reject`, 'POST');
    await page.getByRole('button', { name: '拒绝候选' }).click();
    const rejectDialog = page.getByRole('dialog', { name: '确认拒绝此候选版本？' });
    await rejectDialog.getByRole('button', { name: '拒绝候选' }).click();
    expect((await rejectResponse).status()).toBe(200);
    await expect(rejectDialog).toBeHidden();
    await closeDrawerIfOpen(page);

    dialog = await openEvolution(page, `${webURL}/evaluations/evolution`);
    await dialog.getByRole('tab', { name: '创建金丝雀' }).click();
    panel = dialog.locator('.ant-tabs-tabpane-active');
    await panel.getByRole('combobox', { name: '资源类型' }).click();
    await page.locator('.ant-select-item-option-content').filter({ hasText: 'Agent' }).click();
    await panel.getByLabel('资源 ID').fill(agentID);
    await panel.getByLabel('稳定 Revision ID').fill(agentStableRevisionID);
    await panel.getByLabel('候选 Revision ID').fill(candidates[1].revision.revision_id);
    await pickSuite(panel, page, suiteName);
    const experimentResponse = waitFor(page, '/evaluations/experiments', 'POST');
    await expect(dialog.getByRole('button', { name: '创建金丝雀' })).toBeEnabled({ timeout: 15_000 });
    await dialog.getByRole('button', { name: '创建金丝雀' }).click();
    const experimentCreated = await experimentResponse;
    if (experimentCreated.status() !== 201) {
      throw new Error(`experiment status ${experimentCreated.status()}: ${await experimentCreated.text()}`);
    }
    experimentID = (await experimentCreated.json() as { experiment: { id: string } }).experiment.id;
    await expect(dialog).toBeHidden();

    const evidenceRegistration = await page.request.post(`${fixtureURL}/e2e/opik/register`, { data: {
      trace_id: run.trace_id, tenant_id: tenantID, user_id: userID,
      resource_kind: 'agent', resource_id: agentID, revision_id: agentStableRevisionID,
    } });
    expect(evidenceRegistration.status()).toBe(204);
    dialog = await openEvolution(page, `${webURL}/evaluations/evolution`);
    await dialog.getByRole('tab', { name: '记录反馈' }).click();
    panel = dialog.locator('.ant-tabs-tabpane-active');
    await panel.getByRole('combobox', { name: '资源类型' }).click();
    await page.locator('.ant-select-item-option-content').filter({ hasText: 'Agent' }).click();
    await panel.getByLabel('Trace ID').fill(run.trace_id);
    await panel.getByLabel('反馈资源 ID').fill(agentID);
    await panel.getByLabel('分数').fill('0.9');
    const feedbackResponse = waitFor(page, '/evaluations/feedback', 'POST');
    await dialog.getByRole('button', { name: '提交反馈' }).click();
    const feedback = await feedbackResponse;
    if (feedback.status() !== 201) {
      throw new Error(`feedback status ${feedback.status()}: ${await feedback.text()}`);
    }
    await expect(dialog).toBeHidden();

    await page.getByRole('tab', { name: /金丝雀实验/ }).click();
    // EvolutionPage 实验表只展示 canary_revision_id 等版本列，不展示实验 id；用金丝雀
    // revision（=evaluateRevision）定位刚创建、stage=5 running 的那一行。
    await expect(activeTableRow(page, evaluateRevision)).toHaveCount(1, { timeout: 15_000 });
    await activeTableRow(page, evaluateRevision).getByRole('button', { name: /详\s*情/ }).click();
    const experimentDrawer = page.locator('.ant-drawer:visible');
    await expect(experimentDrawer).toBeVisible();
    const pauseResponse = waitFor(page, `/evaluations/experiments/${experimentID}/pause`, 'POST');
    await experimentDrawer.getByRole('button', { name: '暂停实验' }).click();
    const pauseDialog = page.getByRole('dialog', { name: '确认暂停金丝雀实验？' });
    await expect(pauseDialog).toBeVisible();
    await pauseDialog.getByRole('button', { name: /暂\s*停/ }).click();
    expect((await pauseResponse).status()).toBe(200);
    await expect.poll(async () => (await rows<{ status: string }>(pool, tenantID,
      'SELECT status FROM evaluation_experiments WHERE id=$1', [experimentID]))[0]?.status,
    { timeout: 15_000 }).toBe('paused');
    await closeDrawerIfOpen(page);

    const fixtures = await seedDecisionFixtures(pool, tenantID, suiteRevisionID, suffix);
    fixtureIDs = fixtures.map(({ experiment }) => experiment);
    await page.reload();
    for (const fixture of fixtures) {
      await page.getByRole('tab', { name: /金丝雀实验/ }).click();
      // 与暂停块同理：实验表不显示实验 id，按各夹具唯一的 canary revision 定位行。
      await expect(activeTableRow(page, fixture.canary)).toHaveCount(1, { timeout: 15_000 });
      await activeTableRow(page, fixture.canary).getByRole('button', { name: /详\s*情/ }).click();
      const decisionDrawer = page.locator('.ant-drawer:visible');
      await expect(decisionDrawer).toBeVisible();
      const commandResponse = waitFor(page,
        `/evaluations/experiments/${fixture.experiment}/${fixture.action}`, 'POST');
      const label = fixture.action === 'promote' ? '晋级' : '回滚';
      const actionName = new RegExp(label.split('').join('\\s*'));
      await decisionDrawer.getByRole('button', { name: actionName }).click();
      const decisionDialog = page.getByRole('dialog', { name: `确认${label}此实验？` });
      await decisionDialog.getByRole('button', { name: actionName }).click();
      const commandResult = await commandResponse;
      if (commandResult.status() !== 200) {
        throw new Error(`${fixture.action} status ${commandResult.status()}: ${await commandResult.text()}`);
      }
      await expect(decisionDialog).toBeHidden();
      const expectedStatus = fixture.action === 'promote' ? 'completed' : 'rolled_back';
      await expect.poll(async () => (await rows<{ status: string }>(pool, tenantID,
        'SELECT status FROM evaluation_experiments WHERE id=$1', [fixture.experiment]))[0]?.status,
      { timeout: 15_000 }).toBe(expectedStatus);
      const deployment = (await rows<{ stable_revision_id: string; canary_revision_id: string | null }>(pool, tenantID,
        'SELECT stable_revision_id,canary_revision_id FROM evaluation_deployments WHERE resource_id=$1',
      [fixture.resource]))[0];
      expect(deployment.stable_revision_id).toBe(fixture.action === 'promote' ? fixture.canary : fixture.stable);
      expect(deployment.canary_revision_id).toBeNull();
      await closeDrawerIfOpen(page);
      await page.reload();
    }

    // ── P1c 人工评审池：列表 / 详情 / 决策 3 端点 ─────────────────────────────
    reviewID = `e2e-review-${suffix}`;
    const reviewSourceID = `e2e-review-source-${suffix}`;
    await mutate(pool, tenantID, `
      INSERT INTO eval_review_items
        (id,source_type,source_id,run_id,resource_kind,resource_id,trigger_reason,snapshot,status)
      VALUES ($1,'observation',$2,$3,'agent',$4,'low_confidence',$5::jsonb,'pending')`,
    [reviewID, reviewSourceID, runID, agentID,
      JSON.stringify({ signals: { judge: [{ dimension: 'faithfulness', score: 0.6, confidence: 0.3 }] },
        verdict: 'pass', stratum: 'evaluation', cost_usd: 0.0 })]);
    const reviewListResponse = waitFor(page, '/evaluations/review', 'GET');
    // 人工评审池独立成页（Batch 3）：直接导航到 ReviewPoolPage，首屏 GET 即评审列表。
    await page.goto(`${webURL}/evaluations/review`);
    await expect(page.getByRole('heading', { name: '人工评审池' })).toBeVisible({ timeout: 15_000 });
    const reviewList = await reviewListResponse;
    expect(reviewList.status()).toBe(200);
    const reviewListBody = await reviewList.json() as { items: Array<{ id: string }>; total: number };
    expect(reviewListBody.items.some((item) => item.id === reviewID)).toBe(true);
    expect(reviewListBody.total).toBeGreaterThanOrEqual(1);
    // ReviewPoolPage 无 Tabs 包裹，直接用页面级表格行。
    const reviewRow = page.locator('.ant-table-row').filter({ hasText: agentID });
    await expect(reviewRow).toHaveCount(1, { timeout: 15_000 });

    // 详情 Drawer 仅展示行数据不发请求，端点用页面 fetch 直接覆盖（带 JWT，走真实浏览器）。
    const reviewDetailResponse = waitFor(page, `/evaluations/review/${reviewID}`, 'GET');
    const detailStatus = await page.evaluate(async ({ url, token }) => {
      const res = await fetch(url, { method: 'GET', credentials: 'include',
        headers: { Authorization: `Bearer ${token}` } });
      return res.status;
    }, { url: `${backendURL}/evaluations/review/${reviewID}`, token: actor.accessToken ?? '' });
    expect(detailStatus).toBe(200);
    expect((await reviewDetailResponse).status()).toBe(200);

    await reviewRow.getByRole('button', { name: /评\s*审/ }).click();
    const reviewDialog = page.getByRole('dialog', { name: '人工评审' });
    await reviewDialog.getByRole('combobox', { name: '评审结论' }).click();
    await page.locator('.ant-select-item-option-content').filter({ hasText: /^通过/ }).click();
    await reviewDialog.getByLabel('评审理由').fill('E2E: 判定通过，无需修正');
    const decisionResponse = waitFor(page, `/evaluations/review/${reviewID}/decision`, 'POST');
    await reviewDialog.getByRole('button', { name: '提交评审' }).click();
    const decision = await decisionResponse;
    if (decision.status() !== 200) {
      throw new Error(`review decision status ${decision.status()}: ${await decision.text()}`);
    }
    await expect(reviewDialog).toBeHidden();
    const reviewed = (await rows<{ status: string; human_verdict: string; review_reason: string; reviewer: string }>(pool, tenantID, `
      SELECT status, human_verdict, review_reason, reviewer FROM eval_review_items WHERE id=$1`, [reviewID]))[0];
    expect(reviewed.status).toBe('reviewed');
    expect(reviewed.human_verdict).toBe('pass');
    expect(reviewed.review_reason).toBeTruthy();
    expect(reviewed.reviewer).toBeTruthy();
    evidence.ui.push('Review pool list, detail, and decision completed through Chromium');
    evidence.http.push('Review pool GET list, GET detail, and POST decision returned successful browser-observed responses');
    evidence.database.push('Review item reconciled to reviewed with verdict pass');

    // P0 route surface 新增：在线观测独立成页（Batch 3），经 observability route 访问一次，
    // 使 manifest route.evaluations.observability 有对应 produced action。
    await page.goto(`${webURL}/evaluations/observability`);
    await expect(page.getByRole('heading', { name: '在线观测' })).toBeVisible({ timeout: 15_000 });
    evidence.ui.push('Online observability page reached through Chromium');

    expect(await rows<{ status: string }>(pool, tenantID,
      'SELECT status FROM optimization_candidates WHERE id=$1', [candidates[0].id])).toEqual([{ status: 'rejected' }]);
    expect(await rows<{ status: string }>(pool, tenantID,
      'SELECT status FROM evaluation_experiments WHERE id=$1', [experimentID])).toEqual([{ status: 'paused' }]);
    expect(await rows<{ count: string }>(pool, tenantID,
      'SELECT count(*)::text AS count FROM evaluation_feedback WHERE trace_id=$1', [run.trace_id]))
      .toEqual([{ count: '1' }]);
    evidence.ui.push('Evaluation suite, run, optimization, experiment, decisions, and feedback completed through Chromium');
    evidence.http.push('All Evaluation mutations returned successful browser-observed responses');
    evidence.database.push('Evaluation run, candidates, experiments, decisions, and feedback reconciled');
  } finally {
    const cleanupTasks: Array<() => Promise<unknown>> = [];
    if (suiteID) {
      const fixtureResource = [`e2e-promote-${suffix}`, `e2e-rollback-${suffix}`];
      const cleanup = [
        { text: 'DELETE FROM evaluation_feedback WHERE resource_id=$1 OR resource_id=$2 OR resource_id=$3',
          values: [agentID, ...fixtureResource] },
        { text: 'DELETE FROM experiment_decisions WHERE experiment_id=$1 OR experiment_id=ANY($2::text[])',
          values: [experimentID || 'none', fixtureIDs] },
        { text: 'DELETE FROM evaluation_deployments WHERE resource_id=$1 OR resource_id=$2 OR resource_id=$3 OR experiment_id=ANY($4::text[])',
          values: [agentID, ...fixtureResource, fixtureIDs] },
        { text: 'DELETE FROM evaluation_experiments WHERE id=$1 OR id=ANY($2::text[])',
          values: [experimentID || 'none', fixtureIDs] },
        { text: 'DELETE FROM optimization_candidates WHERE optimization_job_id IN (SELECT id FROM optimization_jobs WHERE resource_id=$1 OR resource_id=$2 OR resource_id=$3)',
          values: [agentID, ...fixtureResource] },
        { text: 'DELETE FROM optimization_jobs WHERE resource_id=$1 OR resource_id=$2 OR resource_id=$3',
          values: [agentID, ...fixtureResource] },
        { text: "DELETE FROM evaluation_jobs WHERE result_id=$1 OR payload->'resource'->>'resource_id'=$2 OR payload->'resource'->>'resource_id'=$3 OR payload->'resource'->>'resource_id'=$4",
          values: [runID || 'none', agentID, ...fixtureResource] },
        { text: 'DELETE FROM eval_case_results WHERE run_id IN (SELECT id FROM eval_runs WHERE resource_id=$1 OR resource_id=$2 OR resource_id=$3)',
          values: [agentID, ...fixtureResource] },
        { text: 'DELETE FROM eval_runs WHERE resource_id=$1 OR resource_id=$2 OR resource_id=$3',
          values: [agentID, ...fixtureResource] },
        { text: 'DELETE FROM eval_suites WHERE id=$1', values: [suiteID] },
        { text: 'DELETE FROM resource_revisions WHERE resource_id=$1 OR resource_id=$2 OR resource_id=$3',
          values: [agentID, ...fixtureResource] },
      ];
      cleanupTasks.push(...cleanup.map((query) => async () => mutate(pool, tenantID, query.text, query.values)));
    }
    if (reviewID) {
      cleanupTasks.push(async () => mutate(pool, tenantID,
        'DELETE FROM eval_review_items WHERE id=$1', [reviewID]));
    }
    cleanupTasks.push(
      async () => {
        if (!agentID) return;
				await page.goto(`${webURL}/agents`);
        const card = page.locator('.ant-card').filter({ hasText: agentName });
        // goto 后立即 count 会在列表未渲染完时误判 0 并静默跳过删除，
        // 导致 agent 跨轮残留累积（下拉虚拟滚动随之被撑爆）——先等目标卡片
        // 渲染（页面有平台助手卡，waitFor 可成功）再走 count 守卫
        await card.first().waitFor({ state: 'visible', timeout: 15_000 }).catch(() => {});
        if (await card.count()) {
          await card.getByRole('button', { name: '删除 Agent' }).click();
          await page.locator('.ant-popconfirm').getByRole('button', { name: /删\s*除/ }).click();
        }
      },
      async () => {
        if (!skillID) return;
        await page.goto(`${webURL}/skills`);
        const card = page.locator('.ant-card').filter({ hasText: skillName });
        if (await card.count()) {
          await card.getByRole('button', { name: '删除技能' }).click();
          await page.locator('.ant-popconfirm').getByRole('button', { name: /删\s*除/ }).click();
        }
      },
      async () => page.close(),
    );
    await runCleanupTasks(cleanupTasks);
  }
  return [
    'evaluation.route.evaluations',
    'evaluation.route.evaluations.suites',
    'evaluation.route.evaluations.suites.id',
    'evaluation.route.evaluations.runs',
    'evaluation.route.evaluations.evolution',
    'evaluation.route.evaluations.resources',
    'evaluation.route.evaluations.observability',
    'evaluation.route.evaluations.review',
    'evaluation.mutation.post.evaluations.suites',
    'evaluation.mutation.post.evaluations.suites.suiteid.publish',
    'evaluation.mutation.post.evaluations.suites.suiteid.draft',
    'evaluation.mutation.post.evaluations.suites.suiteid.draft.cases',
    'evaluation.mutation.delete.evaluations.suites.suiteid.draft.cases.caseid',
    'evaluation.mutation.post.evaluations.runs',
    'evaluation.mutation.post.evaluations.optimizations',
    'evaluation.mutation.post.evaluations.experiments',
    'evaluation.mutation.post.evaluations.feedback',
    'evaluation.mutation.post.evaluations.candidates.candidateid.reject',
    'evaluation.mutation.post.evaluations.experiments.experimentid.pause',
    'evaluation.mutation.post.evaluations.experiments.experimentid.promote',
    'evaluation.mutation.post.evaluations.experiments.experimentid.rollback',
    'evaluation.mutation.get.evaluations.review',
    'evaluation.mutation.get.evaluations.review.id',
    'evaluation.mutation.post.evaluations.review.id.decision',
  ];
};
