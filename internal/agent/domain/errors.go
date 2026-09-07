package domain

import "errors"

// Sentinel errors shared across the Agent domain. Application aliases these
// where callers must preserve errors.Is checks across layers.
var (
	ErrNotFound                      = errors.New("agent not found")
	ErrNameConflict                  = errors.New("agent name already exists")
	ErrInvalidSkill                  = errors.New("skill not found")
	ErrForbidden                     = errors.New("resource ownership forbidden")
	ErrEditorNotEligible             = errors.New("editor must hold admin or owner role")
	ErrInvalidOfficialEvidenceQuery  = errors.New("official evidence query is empty")
	ErrOfficialEvidenceNotFound      = errors.New("official evidence not found")
	ErrDiagnosticForbidden           = errors.New("diagnostic forbidden")
	ErrDiagnosticEvidenceUnavailable = errors.New("diagnostic evidence unavailable")
	ErrKnowledgeRevisionUnavailable  = errors.New("knowledge revision unavailable")
	ErrAssistantModelUnavailable     = errors.New("system assistant model unavailable")
	ErrInvalidAgentModel             = errors.New("invalid system assistant model")
	ErrInvalidSamplingParameters     = errors.New("invalid sampling parameters")
	ErrInvalidMaxIterations          = errors.New("invalid max iterations")
	ErrProposalInvalid               = errors.New("proposal invalid")
	ErrProposalNotFound              = errors.New("proposal not found")
	ErrProposalStale                 = errors.New("proposal stale")
	ErrProposalExpired               = errors.New("proposal expired")
	ErrProposalForbidden             = errors.New("proposal forbidden")
	ErrProposalAlreadyClaimed        = errors.New("proposal already claimed")
	ErrProposalApplyFailed           = errors.New("proposal apply failed")
	ErrProposalUnknownOutcome        = errors.New("proposal outcome unknown")
	ErrOperationProposalNotFound     = errors.New("operation proposal not found")
	ErrOperationProposalResolved     = errors.New("operation proposal already resolved")
	ErrOperationProposalPending      = errors.New("operation proposal already pending")
	ErrOperationProposalExpired      = errors.New("operation proposal approval expired")
	// ErrSystemPromptNotConfigured 标记平台全局系统提示词 agent.system_prompt 未配置
	// （fail-closed：禁止空后缀静默执行）。错误文本保留 "not configured (fail-closed)"
	// 后缀以兼容既有日志检索；作为 sentinel 供 middleware 映射为可读中文。
	ErrSystemPromptNotConfigured = errors.New("agent.system_prompt not configured (fail-closed)")
	// ErrCompactionPromptNotConfigured 标记平台压缩提示词 agent.compaction_prompt 未配置
	// （fail-closed：禁止空 system prompt 静默调用 LLM）。
	ErrCompactionPromptNotConfigured = errors.New("agent.compaction_prompt not configured (fail-closed)")
	// ErrAgentSystemPromptRequired 标记某被测/发布 agent 自身的 system_prompt 为空，
	// 不满足 AgentRevision 领域不变量（agent_revision.go Validate）。区别于平台全局
	// ErrSystemPromptNotConfigured（503 fail-closed 提示管理员补平台参数）：这是该
	// agent 自身配置缺失，属用户可修正的资源状态问题，middleware 映射为 4xx 可读中文，
	// 提示先为被测 agent 配置系统提示词再建档。
	ErrAgentSystemPromptRequired = errors.New("agent revision: system prompt required")
)
