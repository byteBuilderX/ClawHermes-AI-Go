package middleware

import (
	"errors"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	llmgatewaydomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	mcpdomain "github.com/byteBuilderX/stratum/internal/mcp/domain"
)

const (
	CodeAssistantModelUnavailable = "ASSISTANT_MODEL_UNAVAILABLE"
	// CodeSystemPromptNotConfigured 平台全局系统提示词 fail-closed 未配置。
	CodeSystemPromptNotConfigured = "SYSTEM_PROMPT_NOT_CONFIGURED"
	// CodeCompactionPromptNotConfigured 平台压缩提示词 fail-closed 未配置。
	CodeCompactionPromptNotConfigured = "COMPACTION_PROMPT_NOT_CONFIGURED"
	// CodeAgentSystemPromptRequired 被测/发布 agent 自身未配置 system_prompt，
	// AgentRevision 领域校验拒绝建档（422）。区别于平台全局 SYSTEM_PROMPT_NOT_CONFIGURED
	// （503 平台参数缺失）——此为该 agent 配置缺失，用户补全后重试即可。
	CodeAgentSystemPromptRequired = "AGENT_SYSTEM_PROMPT_REQUIRED"
)

type PublicErrorDescriptor struct {
	Message string
	Code    string
}

func DescribePublicError(err error, status int) PublicErrorDescriptor {
	if errors.Is(err, agentdomain.ErrAssistantModelUnavailable) {
		return PublicErrorDescriptor{
			Message: "该 Agent 尚未配置可用模型",
			Code:    CodeAssistantModelUnavailable,
		}
	}
	// 平台 fail-closed 参数未配置：全局系统提示词/压缩提示词缺失是部署/配置回归
	// （如迁移后 DB 空串），对客户端暴露固定中文与专用 code，便于管理员定位。
	if errors.Is(err, agentdomain.ErrSystemPromptNotConfigured) {
		return PublicErrorDescriptor{
			Message: "平台未配置全局系统提示词（agent.system_prompt），请联系平台管理员在参数配置中补全后重试",
			Code:    CodeSystemPromptNotConfigured,
		}
	}
	if errors.Is(err, agentdomain.ErrCompactionPromptNotConfigured) {
		return PublicErrorDescriptor{
			Message: "平台未配置对话历史压缩提示词（agent.compaction_prompt），请联系平台管理员在参数配置中补全后重试",
			Code:    CodeCompactionPromptNotConfigured,
		}
	}
	// 被测/发布 agent 自身缺 system_prompt：建档（评测登记）对该被测对象快照做领域校验
	// 失败。这是可被用户修正的资源状态问题（非 5xx 服务故障），固定中文引导先配置被测
	// agent 的系统提示词再重试。
	if errors.Is(err, agentdomain.ErrAgentSystemPromptRequired) {
		return PublicErrorDescriptor{
			Message: "该被测 Agent 尚未配置系统提示词，无法建档。请先在 Agent 配置中填写系统提示词后再登记",
			Code:    CodeAgentSystemPromptRequired,
		}
	}
	// ErrUpstreamRequestFailed 的 wrap 链含内部 BaseURL/上游响应细节，
	// 只对客户端暴露固定消息；内部细节保留在 ERROR 日志（middleware 记录完整 err）。
	if errors.Is(err, llmgatewaydomain.ErrUpstreamRequestFailed) {
		return PublicErrorDescriptor{Message: "上游模型服务请求失败，请稍后重试"}
	}
	// MCP 连接/发现失败：错误链只含安全 sentinel，对外固定中文消息，不暴露
	// 上游地址与响应细节。发现阶段失败（如服务器未实现 resources/list）在
	// 客户端能力感知修复后不再出现，此分支覆盖真实不可达/协议不兼容场景。
	if errors.Is(err, mcpdomain.ErrTransportFailed) {
		return PublicErrorDescriptor{
			Message: "连接 MCP 服务器失败：服务器未响应或协议不兼容，请检查服务器地址与认证配置",
		}
	}
	if errors.Is(err, mcpdomain.ErrSessionMissing) {
		return PublicErrorDescriptor{Message: "MCP 连接已断开，请重新连接后再试"}
	}
	if msg, ok := approvalPublicMessage(err); ok {
		return PublicErrorDescriptor{Message: msg}
	}
	if status >= 500 {
		return PublicErrorDescriptor{Message: "internal server error"}
	}
	return PublicErrorDescriptor{Message: err.Error()}
}

// approvalPublicMessage 映射审批终态/操作 sentinel 为固定中文消息（D7/D8 工作台与
// 聊天页可解释文案）。仅 errors.Is 命中才返回 ok=true；未命中回退默认 err.Error()。
func approvalPublicMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, agentapp.ErrApprovalExpired):
		return "审批已过期", true
	case errors.Is(err, agentdomain.ErrApprovalPolicyChanged):
		return "权限策略已变更，请重新发起", true
	case errors.Is(err, agentdomain.ErrApprovalConversationGone):
		return "会话已删除，审批已失效", true
	case errors.Is(err, agentdomain.ErrApprovalSelfDecision):
		return "不能审批自己发起的请求", true
	case errors.Is(err, agentdomain.ErrApprovalRoleDenied):
		return "需要管理员权限", true
	case errors.Is(err, agentdomain.ErrApprovalAlreadyDecided):
		return "该审批已处理", true
	case errors.Is(err, agentdomain.ErrApprovalAlreadyExecuted):
		return "该工具已执行", true
	case errors.Is(err, agentdomain.ErrApprovalInvalidated):
		return "审批已失效", true
	}
	return "", false
}
