// Behavioral constants — no UI styling numbers here.

export const API_DEFAULT_TIMEOUT_MS = 10_000;
// 注册创建租户需 provisioning 完整 tenant schema（百级 DDL），中等负载即可超 10s；
// 低频重操作单独给更长预算，避免中途被 abort 留下半 provisioned 租户。
export const AUTH_REGISTER_TIMEOUT_MS = 30_000;
export const AGENT_DEFAULT_MAX_ITERATIONS = 10;
export const AGENT_MIN_MAX_ITERATIONS = 1;
export const AGENT_MAX_MAX_ITERATIONS = 90;
export const AGENT_MAX_TOKENS_MIN = 0;
export const AGENT_MAX_TOKENS_MAX = 131072;
export const AGENT_MAX_TOKENS_STEP = 256;
export const AGENT_MAX_CONTEXT_TOKENS_MIN = 0;
export const AGENT_MAX_CONTEXT_TOKENS_MAX = 128000;
export const AGENT_MAX_CONTEXT_TOKENS_STEP = 1000;
// 0 = 自动按模型窗口解析；窗口未知时回落后端 constants.DefaultAgentContextTokens。
export const AGENT_DEFAULT_MAX_CONTEXT_TOKENS = 0;
// 与后端 pkg/constants/agent.go DefaultContextWindowRatio 同源，后端为权威。
export const AGENT_CONTEXT_WINDOW_RATIO = 0.85;
// 与后端 pkg/constants/evaluation.go TunableTemperatureMin/Max 同源，后端为权威。
// provider（Qwen/Zhipu）拒绝 >1，越界会在执行期 500。
export const AGENT_TEMPERATURE_MIN = 0;
export const AGENT_TEMPERATURE_MAX = 1;
export const AGENT_TEMPERATURE_STEP = 0.1;
// temperature = 0（未设置）时回落后端 registry agent.temperature Default
// （pkg/constants/agent.go，运行时权威；这里只用于提示展示）。
export const AGENT_DEFAULT_TEMPERATURE = 0.7;
// max_tokens = 0 时回落后端 constants.DefaultOutputReserveTokens：模型 registry
// 无 maxOut 或模型未知时的平台兜底输出上限，后端为权威。
export const AGENT_DEFAULT_MAX_OUTPUT_TOKENS = 4096;
// 子 Agent 委托（stratum_delegate）：与后端 pkg/constants/agent.go 同源，后端为权威。
// delegateMaxDepth 0=unset → 运行时回落 DefaultDelegateMaxDepth(1)，clamp 到 MaxDelegateDepth(2)；
// 表单允许显式输入 0 表达「未设置」（update 语义 0=unset 不覆盖已存值）。
export const AGENT_DELEGATE_DEFAULT_MAX_DEPTH = 1;
export const AGENT_DELEGATE_MAX_DEPTH = 2;
// delegateDefaultMaxSteps 0=unset → 回落 DefaultDelegateMaxLLMSteps(5)；
// max_steps 参数硬上限 10（schema maximum + 运行时 clamp）。
export const AGENT_DELEGATE_DEFAULT_MAX_STEPS = 5;
export const AGENT_DELEGATE_MAX_STEPS = 10;

export const DEFAULT_PAGE_SIZE = 20;
export const COMPACT_PAGE_SIZE = 10;
export const PAGE_SIZE_OPTIONS = ['10', '20', '50'];

// 平台管理员候选用户搜索条数（GET /admin/users）。
export const ADMIN_USER_SEARCH_LIMIT = 10;

export const WORKFLOW_DEFAULT_PAGE_SIZE = 20;
export const SCHEDULED_TASK_DEFAULT_PAGE_SIZE = 20;
export const SCHEDULED_TASK_MAX_NAME_LENGTH = 64;
export const SCHEDULED_TASK_WORKFLOW_SELECT_SIZE = 20;
export const WORKFLOW_VALIDATION_FOCUS_MS = 320;
export const WORKFLOW_NODE_WIDTH = 224;
export const WORKFLOW_NODE_HEIGHT = 88;
export const WORKFLOW_LAYOUT_LAYER_GAP_X = 96;
export const WORKFLOW_LAYOUT_NODE_GAP_Y = 48;
export const WORKFLOW_LAYOUT_MARGIN = 80;
export const WORKFLOW_STREAM_RECONNECT_BASE_MS = 1000;
export const WORKFLOW_STREAM_RECONNECT_MAX_MS = 10000;
export const WORKFLOW_OUTPUT_MAX_CHARS = 100000;
// Agent 流断点续接退避:断线/5xx 携带 execution_id 重发,指数退避从 1s 到 10s,
// 最多 5 次重试仍不完整则报错(对齐 WORKFLOW_STREAM_RECONNECT 命名/数值风格)。
export const AGENT_STREAM_RECONNECT_BASE_MS = 1000;
export const AGENT_STREAM_RECONNECT_MAX_MS = 10000;
export const AGENT_STREAM_RECONNECT_MAX_ATTEMPTS = 5;
// 审批铃铛待办轮询周期（角标数据复用 ListPending，无独立端点）。
export const APPROVAL_POLL_MS = 30000;
// 审批等待态卡片轮询 active-execution 的周期：批准后自动流式续跑的判定源。
export const ACTIVE_EXECUTION_POLL_MS = 2000;

export const MCP_DEFAULT_TIMEOUT_SEC = 30;
export const MCP_MAX_TIMEOUT_SEC = 300;
export const MCP_RETRY_INITIAL_DELAY_MS = 1000;
export const MCP_RETRY_MAX_DELAY_MS = 30000;
export const MCP_RETRY_MAX_RETRIES = 5;
export const MCP_RETRY_BACKOFF_FACTOR = 2.0;

export const SKILL_DEFAULT_TEMPERATURE = 0.7;
export const SKILL_DEFAULT_MAX_TOKENS = 2048;
export const SKILL_DEFAULT_TIMEOUT_SEC = 30;
export const EVALUATION_JOB_POLL_INTERVAL_MS = 1000;
export const EVALUATION_JOB_MAX_WAIT_MS = 120000;
// 评测集生成采样上限（与后端 pkg/constants DefaultCaseSampleLimit/MaxCaseSampleLimit 一致）。
export const EVALUATION_GENERATE_DEFAULT_MAX_CASES = 20;
export const EVALUATION_GENERATE_MAX_CASES = 50;
// 工具序列过程断言 max_calls 上限（与后端 pkg/constants evaluation.go 同步，
// 前端表单 InputNumber 硬上限；仅约束表单输入，运行时后端为权威）。
export const EVALUATION_MAX_CALLS_LIMIT = 50;
// 评测运行通过率趋势面板拉取最近运行的分页条数（listRuns limit）。仅展示最近 N 次，
// 后端返回 next_cursor 时前端显式标注「存在更早记录」，不滚动加载暗示全量。
export const EVALUATION_TREND_RUN_LIMIT = 100;
// 评测监控面板（EvaluationCenterPage「监控」tab）行为边界（spec 2026-09-03 §4.3）：
// 默认窗口天数前端自持（后端 pkg/constants EvalMonitorWindowDays=7 为权威兜底，
// 两端各持默认值并在 UI 明示）；资源行 limit 与后端默认 20 保持一致，用于显式传参
// 并推断「仅显示观测最多的前 N 资源」截断。
export const EVALUATION_MONITOR_DEFAULT_WINDOW_DAYS = 7;
export const EVALUATION_MONITOR_RESOURCE_LIMIT = 20;
// RangePicker 快捷预设（近 N 天含端点）；N 本身即行为数字，禁止散落组件。
export const EVALUATION_MONITOR_WINDOW_PRESETS_DAYS = [7, 14, 30] as const;
// 失败归因报告「系统性模式」的批次数阈值：失败 case 数 ≥ 该值才标「系统性」。
// §9 服务端落地后改由服务端 systematic 信号替代。
export const EVALUATION_ATTRIBUTION_SYSTEMATIC_MIN_CASES = 2;

export const MEMORY_SEARCH_LIMIT = 20;
// 记忆事实分类白名单（与后端 internal/memory/domain/fact.go factCategoryAllowSet 一致）。
export const FACT_CATEGORIES = ['preference', 'skill', 'event', 'state', 'relationship', 'other'] as const;
// Memory 快照编辑限制（与后端 pkg/constants/memory.go ActiveSnapshot* 一致）。
export const MEMORY_SNAPSHOT_SECTION_MAX_ITEMS = 8;
export const MEMORY_SNAPSHOT_ITEM_MAX_RUNES = 240;

export const PROMPT_TEXTAREA_MAX_LENGTH = 16000; // 机制模板 TextArea 上限，与后端全局 10MB body 双保险

export const LLM_DEFAULT_PAGE_SIZE = 20;

export const KNOWLEDGE_DEFAULT_CHUNK_SIZE = 512;
export const KNOWLEDGE_DEFAULT_CHUNK_OVERLAP = 64;
export const KNOWLEDGE_DEFAULT_TOP_K = 5;
// query_mode 未设置时的运行时默认（workspace.go DefaultQueryMode，后端为权威；
// 与 rag.top_k 平台参数 10 的评测搜索空间语义无关）。
export const KNOWLEDGE_DEFAULT_QUERY_MODE = 'hybrid';
export const KNOWLEDGE_MIN_CHUNK_SIZE = 64;
export const KNOWLEDGE_MAX_CHUNK_SIZE = 2048;
export const KNOWLEDGE_MIN_CHUNK_OVERLAP = 0;
export const KNOWLEDGE_MAX_CHUNK_OVERLAP = 512;
export const KNOWLEDGE_MIN_TOP_K = 1;
export const KNOWLEDGE_MAX_TOP_K = 20;
// rerank_top_k 合法上下限（0 = 跟随 Top-K；与 pkg/constants MaxRerankTopK、proto
// WorkspaceConfig.rerank_top_k binding min=0,max=20 同步，修改须三处一致）。
export const KNOWLEDGE_MIN_RERANK_TOP_K = 0;
export const KNOWLEDGE_MAX_RERANK_TOP_K = 20;
export const KNOWLEDGE_MIN_SCORE_THRESHOLD = 0;
export const KNOWLEDGE_MAX_SCORE_THRESHOLD = 1;
// 评分指令附加段 rune 上限（TextArea maxLength；与 pkg/constants
// MaxRerankScoringInstructionsRunes / MaxJudgeScoringInstructionsRunes 同步，
// 修改须两处一致；rune 数 ≤ UTF-16 单元数，同值安全）。
export const KNOWLEDGE_MAX_SCORING_INSTRUCTIONS_RUNES = 2000;
export const KNOWLEDGE_MAX_UPLOAD_SIZE_BYTES = 10 * 1024 * 1024; // 10MB，与 UI 提示一致（后端上限 100MB）
export const AVATAR_MAX_UPLOAD_SIZE_BYTES = 2 * 1024 * 1024; // 2MB，与 UI 提示一致

// Memory v2
export const MEMORY_SCOPE_OPTIONS = [
  { value: 'user', label: '用户级' },
  { value: 'agent', label: 'Agent 级' },
];

export const MEMORY_DIAGNOSTICS_REFRESH_INTERVAL_MS = 30000; // 30s
export const MEMORY_TOP_ENTITIES_LIMIT = 10;
// 记忆嵌入模型迁移进行中的轮询间隔：migrating 期间前端轮询进度，避免高频打后端。

// 上下文压缩温度控件 bounds；后端 CompactionDefaultTemperature=0.3，写路径钳制
// [0,1]（Qwen/Zhipu 拒收 >1）。0 = unset，回落 0.3。
export const COMPACTION_TEMP_MIN = 0;
export const COMPACTION_TEMP_MAX = 1;
export const COMPACTION_DEFAULT_TEMPERATURE = 0.3;
// 压缩安全比例 0 = unset 时回落 LoopCompactionSafetyRatio（pkg/constants/agent.go）。
export const COMPACTION_SAFETY_RATIO_DEFAULT = 0.8;
export const CHUNKING_STRATEGY_OPTIONS = [
  { value: 'structure_recursive', label: '结构感知（推荐）— Markdown 标题分层 + 递归分块' },
  { value: 'recursive', label: '递归分块 — 按字符边界递归切分' },
  { value: 'semantic', label: '语义分块 — 按语义相似度切分（需嵌入模型）' },
];

// 思考强度档位 Options；与后端 agents.parameters JSONB 的 reasoning_effort 键
// 及 pkg/constants 枚举 low/medium/high 一致。高档位 token 消耗放大，无
// max_tokens_per_execution 联动属成本 DoS 风险（后端仅文档化，不联动）。
export const REASONING_EFFORT_OPTIONS = [
  { value: 'low', label: '低 — 更快的响应，更少 token 消耗' },
  { value: 'medium', label: '中 — 平衡质量与成本' },
  { value: 'high', label: '高 — 更深推理，token 消耗放大' },
];

// 资源变更审计的资源类型（与 internal/audit/domain/change_audit.go 对齐）。
export const RESOURCE_KIND_OPTIONS: Array<{ value: string; label: string }> = [
  { value: 'agent', label: 'Agent' },
  { value: 'skill', label: '技能' },
  { value: 'mcp', label: 'MCP 服务器' },
  { value: 'knowledge', label: '知识库' },
  { value: 'workflow', label: '工作流' },
  { value: 'evaluation', label: '评测' },
];

// 平台级审计的资源类型（与 internal/audit/domain/change_audit.go 对齐）。
// 平台管理面覆盖租户/管理员/模型/厂商/平台参数等 public 目录变更。
export const PLATFORM_RESOURCE_KIND_OPTIONS: Array<{ value: string; label: string }> = [
  { value: 'tenant', label: '租户' },
  { value: 'admin', label: '平台管理员' },
  { value: 'model', label: '模型' },
  { value: 'provider', label: '厂商' },
  { value: 'platform_config', label: '平台配置' },
];
