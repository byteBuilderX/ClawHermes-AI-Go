// Package handler — agent_dto.go.
//
// Wire DTOs for agent HTTP endpoints. Field shapes are frozen by
// api/http/contract_test.go + testdata/contracts/*.golden.json.
package handler

import (
	agent "github.com/byteBuilderX/stratum/internal/agent/application"
	"github.com/byteBuilderX/stratum/internal/agent/domain"
)

type CreateAgentRequest struct {
	Name                  string   `json:"name" binding:"required"`
	Type                  string   `json:"type"`
	Description           string   `json:"description"`
	SystemPrompt          string   `json:"systemPrompt"`
	LLMModel              string   `json:"llmModel" binding:"required"`
	MaxIterations         int      `json:"maxIterations" binding:"required"`
	MaxContextTokens      int      `json:"maxContextTokens"`
	Temperature           float32  `json:"temperature"`
	ReasoningEffort       string   `json:"reasoning_effort"`
	MaxTokens             int      `json:"max_tokens"`
	AllowedSkills         []string `json:"allowedSkills"`
	MCPToolIDs            []string `json:"mcpToolIds"`
	KnowledgeWorkspaceIDs []string `json:"knowledgeWorkspaceIds"`
	MemoryScope           string   `json:"memoryScope" binding:"required"`
	// DelegateEnabled 开启 stratum_delegate 子 agent 派发；DelegateMaxDepth /
	// DelegateDefaultMaxSteps 0=unset → 运行时回落全局默认。
	DelegateEnabled         bool `json:"delegateEnabled"`
	DelegateMaxDepth        int  `json:"delegateMaxDepth"`
	DelegateDefaultMaxSteps int  `json:"delegateDefaultMaxSteps"`
	// Parameters carries registry resource-scope values as a flat object; only
	// the memory.* dotted keys persist on the agent (sampling keys stay on the
	// explicit fields). Same merge semantics as UpdateAgentRequest.Parameters.
	Parameters map[string]any `json:"parameters"`
	Editors    []string       `json:"editors"`
}

// embedding model is immutable post-create.
type UpdateAgentRequest struct {
	Name                  string   `json:"name" binding:"required"`
	Type                  string   `json:"type"`
	Description           string   `json:"description"`
	SystemPrompt          string   `json:"systemPrompt"`
	LLMModel              string   `json:"llmModel" binding:"required"`
	MaxIterations         int      `json:"maxIterations"`
	MaxContextTokens      int      `json:"maxContextTokens"`
	Temperature           float32  `json:"temperature"`
	ReasoningEffort       string   `json:"reasoning_effort"`
	MaxTokens             int      `json:"max_tokens"`
	AllowedSkills         []string `json:"allowedSkills"`
	MCPToolIDs            []string `json:"mcpToolIds"`
	KnowledgeWorkspaceIDs []string `json:"knowledgeWorkspaceIds"`
	MemoryScope           string   `json:"memoryScope" binding:"required"`
	// 委托配置同 CreateAgentRequest；DelegateEnabled 为 *bool:缺省(nil)保留现有值,
	// 显式 false 才关闭(存量默认关闭,Update 全量列写不能把缺省当显式 false 覆盖);
	// 深度/默认步数 0=unset 不覆盖已存值。
	DelegateEnabled         *bool `json:"delegateEnabled"`
	DelegateMaxDepth        int   `json:"delegateMaxDepth"`
	DelegateDefaultMaxSteps int   `json:"delegateDefaultMaxSteps"`
	// Parameters carries the registry sampling parameters as a flat object
	// (temperature/max_tokens/reasoning_effort；压缩配置已迁平台参数)。
	// 压缩三值（提示词/温度/模型）为平台级参数，不在 agent 上暴露/保存。
	// Merge semantics: only keys present in this map are written; a 0 value is
	// unset and never overwrites a persisted value.
	Parameters map[string]any `json:"parameters"`
}

type AgentResponse struct {
	ID                      string   `json:"id"`
	Name                    string   `json:"name"`
	Type                    string   `json:"type"`
	Description             string   `json:"description"`
	SystemPrompt            string   `json:"systemPrompt"`
	LLMModel                string   `json:"llmModel"`
	MaxIterations           int      `json:"maxIterations"`
	MaxContextTokens        int      `json:"maxContextTokens"`
	Temperature             float32  `json:"temperature"`
	ReasoningEffort         string   `json:"reasoning_effort"`
	MaxTokens               int      `json:"max_tokens"`
	AllowedSkills           []string `json:"allowedSkills"`
	MCPToolIDs              []string `json:"mcpToolIds"`
	KnowledgeWorkspaceIDs   []string `json:"knowledgeWorkspaceIds"`
	CreatedAt               string   `json:"createdAt"`
	MemoryScope             string   `json:"memoryScope"`
	DelegateEnabled         bool     `json:"delegateEnabled"`
	DelegateMaxDepth        int      `json:"delegateMaxDepth"`
	DelegateDefaultMaxSteps int      `json:"delegateDefaultMaxSteps"`
	// Parameters echoes the persisted sampling parameters (0=unset keys
	// omitted), symmetric with UpdateAgentRequest.parameters.
	Parameters map[string]any `json:"parameters"`
	// Editors is the current granted editor set, for form prefill.
	Editors []string `json:"editors"`
}

type ExecuteAgentRequest struct {
	Query          string                 `json:"query"`
	ConversationID string                 `json:"conversation_id"`
	ExecutionID    string                 `json:"execution_id"`
	UserID         string                 `json:"user_id"`
	Context        map[string]interface{} `json:"context"`
	Options        map[string]interface{} `json:"options"`
}

type AgentExecutionResult struct {
	AgentID    string                     `json:"agentId"`
	Input      string                     `json:"input"`
	Output     string                     `json:"output"`
	Steps      int                        `json:"steps"`
	TokensUsed int                        `json:"tokensUsed"`
	Duration   string                     `json:"duration"`
	Thoughts   []agent.Thought            `json:"thoughts"`
	ToolCalls  []agent.ToolCall           `json:"toolCalls"`
	Metadata   map[string]interface{}     `json:"metadata"`
	Error      string                     `json:"error,omitempty"`
	Artifacts  []domain.ExecutionArtifact `json:"artifacts"`
}

// AgentVersionResponse mirrors agent.VersionDTO for the wire. Field shapes
// are frozen by contract tests; do not rename.
type AgentVersionResponse struct {
	ID            string         `json:"id"`
	VersionNo     int            `json:"versionNo"`
	Status        string         `json:"status"`
	Source        string         `json:"source"`
	ContentHash   string         `json:"contentHash"`
	CreatedBy     string         `json:"createdBy"`
	CreatedByName string         `json:"createdByName"`
	CreatedAt     string         `json:"createdAt"`
	PublishedAt   string         `json:"publishedAt"`
	IsCurrent     bool           `json:"isCurrent"`
	SafeSummary   map[string]any `json:"safeSummary"`
	// ParentVersionID 指向直父版本 ID（首版为空串）；前端「详情」Drawer 以父版本
	// 整份 payload 为 before 基线。见 spec §4.3。
	ParentVersionID string `json:"parentVersionId"`
}

// AgentVersionsResponse wraps the version history list (newest first),
// matching the skill revisions response shape for frontend symmetry.
type AgentVersionsResponse struct {
	Versions []AgentVersionResponse `json:"versions"`
}

// RollbackAgentRequest carries the target historical version to restore.
type RollbackAgentRequest struct {
	VersionID string `json:"versionId" binding:"required"`
}

// agentVersionToResponse maps the service-side VersionDTO to the wire shape.
func agentVersionToResponse(v agent.VersionDTO) AgentVersionResponse {
	return AgentVersionResponse{
		ID:              v.ID,
		VersionNo:       v.VersionNo,
		Status:          v.Status,
		Source:          v.Source,
		ContentHash:     v.ContentHash,
		CreatedBy:       v.CreatedBy,
		CreatedByName:   v.CreatedByName,
		CreatedAt:       v.CreatedAt,
		PublishedAt:     v.PublishedAt,
		IsCurrent:       v.IsCurrent,
		SafeSummary:     v.SafeSummary,
		ParentVersionID: v.ParentVersionID,
	}
}

// AgentVersionContentResponse is the single-version content wire shape: the
// list fields plus the full edit-surface payload snapshot (snake_case keys),
// which the "detail" drawer diffs against the direct parent version's payload.
// AgentVersionResponse is embedded to keep both responses field-aligned.
type AgentVersionContentResponse struct {
	AgentVersionResponse
	Payload map[string]any `json:"payload"`
}

// agentVersionContentToResponse extends agentVersionToResponse with payload.
func agentVersionContentToResponse(v agent.VersionContentDTO) AgentVersionContentResponse {
	return AgentVersionContentResponse{
		AgentVersionResponse: agentVersionToResponse(v.VersionDTO),
		Payload:              v.Payload,
	}
}

// dtoToResponse maps the service-side AgentDTO to the wire AgentResponse.
func dtoToResponse(d agent.AgentDTO) AgentResponse {
	return AgentResponse{
		ID:                      d.ID,
		Name:                    d.Name,
		Type:                    d.Type,
		Description:             d.Description,
		SystemPrompt:            d.SystemPrompt,
		LLMModel:                d.LLMModel,
		MaxIterations:           d.MaxIterations,
		MaxContextTokens:        d.MaxContextTokens,
		Temperature:             d.Temperature,
		ReasoningEffort:         d.ReasoningEffort,
		MaxTokens:               d.MaxTokens,
		AllowedSkills:           d.AllowedSkills,
		MCPToolIDs:              d.MCPToolIDs,
		KnowledgeWorkspaceIDs:   d.KnowledgeWorkspaceIDs,
		CreatedAt:               d.CreatedAt,
		MemoryScope:             d.MemoryScope,
		DelegateEnabled:         d.DelegateEnabled,
		DelegateMaxDepth:        d.DelegateMaxDepth,
		DelegateDefaultMaxSteps: d.DelegateDefaultMaxSteps,
		Parameters:              d.Parameters,
		Editors:                 d.Editors,
	}
}
