package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentapp "github.com/byteBuilderX/stratum/internal/agent/application"
	agentdomain "github.com/byteBuilderX/stratum/internal/agent/domain"
	llmgatewaydomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	mcpdomain "github.com/byteBuilderX/stratum/internal/mcp/domain"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestPublicErrorDescribesWrappedAssistantModelUnavailable(t *testing.T) {
	err := fmt.Errorf("resolve platform assistant: %w", agentdomain.ErrAssistantModelUnavailable)

	got := DescribePublicError(err, http.StatusServiceUnavailable)
	want := PublicErrorDescriptor{
		Message: "该 Agent 尚未配置可用模型",
		Code:    CodeAssistantModelUnavailable,
	}
	if got != want {
		t.Fatalf("DescribePublicError() = %#v, want %#v", got, want)
	}
}

func TestPublicErrorHidesUnknownServerError(t *testing.T) {
	got := DescribePublicError(errors.New("provider secret=hidden"), http.StatusInternalServerError)
	want := PublicErrorDescriptor{Message: "internal server error"}
	if got != want {
		t.Fatalf("DescribePublicError() = %#v, want %#v", got, want)
	}
}

func TestPublicErrorRetainsNonServerError(t *testing.T) {
	got := DescribePublicError(errors.New("invalid request"), http.StatusBadRequest)
	want := PublicErrorDescriptor{Message: "invalid request"}
	if got != want {
		t.Fatalf("DescribePublicError() = %#v, want %#v", got, want)
	}
}

func TestPublicErrorHidesUpstreamBaseURL(t *testing.T) {
	// The wrapped error carries the internal provider BaseURL; the public
	// message must be a fixed string that never echoes it back.
	err := fmt.Errorf("anthropic: POST http://10.0.0.5:8080/v1/messages 返回 401，"+
		"请检查 API Key 与 Base URL 是否正确: %w", llmgatewaydomain.ErrUpstreamRequestFailed)

	got := DescribePublicError(err, http.StatusBadGateway)
	want := PublicErrorDescriptor{Message: "上游模型服务请求失败，请稍后重试"}
	if got != want {
		t.Fatalf("DescribePublicError() = %#v, want %#v", got, want)
	}
	if strings.Contains(got.Message, "10.0.0.5") || strings.Contains(got.Message, "http") {
		t.Fatalf("public message leaks internal URL: %q", got.Message)
	}
}

func TestPublicErrorDescribesMCPTransportFailure(t *testing.T) {
	err := fmt.Errorf("discover MCP resources: %w", mcpdomain.ErrTransportFailed)
	got := DescribePublicError(err, http.StatusBadGateway)
	if got.Message != "连接 MCP 服务器失败：服务器未响应或协议不兼容，请检查服务器地址与认证配置" {
		t.Fatalf("DescribePublicError() = %#v", got)
	}
	if got.Code != "" {
		t.Fatalf("unexpected code: %#v", got)
	}
}

func TestErrorHandlerDoesNotLeakUpstreamBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ErrorHandler(zap.NewNop()))
	router.GET("/complete", func(c *gin.Context) {
		_ = c.Error(fmt.Errorf("anthropic: POST http://10.0.0.5:8080/v1/messages 返回 401，"+
			"请检查 API Key 与 Base URL 是否正确: %w", llmgatewaydomain.ErrUpstreamRequestFailed))
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/complete", nil))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	wantBody := "{\"error\":\"上游模型服务请求失败，请稍后重试\"}"
	if response.Body.String() != wantBody {
		t.Fatalf("body = %q, want %q", response.Body.String(), wantBody)
	}
	if strings.Contains(response.Body.String(), "10.0.0.5") {
		t.Fatalf("response leaks internal BaseURL: %s", response.Body.String())
	}
}

func TestErrorHandlerReturnsAssistantModelUnavailableContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ErrorHandler(zap.NewNop()))
	router.GET("/assistant", func(c *gin.Context) {
		_ = c.Error(fmt.Errorf("resolve platform assistant: %w", agentdomain.ErrAssistantModelUnavailable))
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assistant", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	wantBody := "{\"code\":\"ASSISTANT_MODEL_UNAVAILABLE\",\"error\":\"该 Agent 尚未配置可用模型\"}"
	if response.Body.String() != wantBody {
		t.Fatalf("body = %q, want %q", response.Body.String(), wantBody)
	}
}

// D7/D8：审批工作台与聊天页依赖可解释的中文终态/操作消息。approval sentinel
// 经 DescribePublicError 必须映射为固定中文（不泄 payload/内部 detail），status 由
// MapErrorToStatus 单独守卫（410/409 等）。
func TestPublicErrorDescribesApprovalSentinels(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"expired", agentapp.ErrApprovalExpired, "审批已过期"},
		{"policy_changed", agentdomain.ErrApprovalPolicyChanged, "权限策略已变更，请重新发起"},
		{"conversation_gone", agentdomain.ErrApprovalConversationGone, "会话已删除，审批已失效"},
		{"self_decision", agentdomain.ErrApprovalSelfDecision, "不能审批自己发起的请求"},
		{"role_denied", agentdomain.ErrApprovalRoleDenied, "需要管理员权限"},
		{"already_decided", agentdomain.ErrApprovalAlreadyDecided, "该审批已处理"},
		{"already_executed", agentdomain.ErrApprovalAlreadyExecuted, "该工具已执行"},
		{"invalidated", agentdomain.ErrApprovalInvalidated, "审批已失效"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribePublicError(fmt.Errorf("approval: %w", tc.err), http.StatusConflict)
			if got.Message != tc.want {
				t.Fatalf("DescribePublicError(%v).Message = %q, want %q", tc.err, got.Message, tc.want)
			}
		})
	}
}

func TestErrorHandlerDoesNotLeakUnknownServerError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ErrorHandler(zap.NewNop()))
	router.GET("/provider", func(c *gin.Context) {
		_ = c.Error(errors.New("provider secret=hidden"))
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/provider", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	wantBody := "{\"error\":\"internal server error\"}"
	if response.Body.String() != wantBody {
		t.Fatalf("body = %q, want %q", response.Body.String(), wantBody)
	}
}

// 平台 fail-closed 参数未配置（system_prompt / compaction_prompt）必须映射为
// 具体中文与专用 code，而非笼统的 "internal server error"——用户可读、管理员可定位。
func TestPublicErrorDescribesFailClosedPromptSentinels(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want PublicErrorDescriptor
	}{
		{
			name: "system_prompt",
			err:  fmt.Errorf("agent: %w", agentdomain.ErrSystemPromptNotConfigured),
			want: PublicErrorDescriptor{
				Message: "平台未配置全局系统提示词（agent.system_prompt），请联系平台管理员在参数配置中补全后重试",
				Code:    CodeSystemPromptNotConfigured,
			},
		},
		{
			name: "compaction_prompt",
			err:  fmt.Errorf("history compactor: %w", agentdomain.ErrCompactionPromptNotConfigured),
			want: PublicErrorDescriptor{
				Message: "平台未配置对话历史压缩提示词（agent.compaction_prompt），请联系平台管理员在参数配置中补全后重试",
				Code:    CodeCompactionPromptNotConfigured,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribePublicError(tc.err, http.StatusServiceUnavailable)
			if got != tc.want {
				t.Fatalf("DescribePublicError() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestMapErrorToStatusMapsFailClosedPromptToServiceUnavailable(t *testing.T) {
	if got := MapErrorToStatus(agentdomain.ErrSystemPromptNotConfigured); got != http.StatusServiceUnavailable {
		t.Fatalf("MapErrorToStatus(system) = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := MapErrorToStatus(agentdomain.ErrCompactionPromptNotConfigured); got != http.StatusServiceUnavailable {
		t.Fatalf("MapErrorToStatus(compaction) = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

// SSE/HTTP 错误契约：fail-closed 未配置经 ErrorHandler 透出 code + 具体中文，
// 前端据此渲染可读错误而非笼统 500。
func TestErrorHandlerReturnsFailClosedPromptContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ErrorHandler(zap.NewNop()))
	router.GET("/assistant", func(c *gin.Context) {
		_ = c.Error(fmt.Errorf("agent: %w", agentdomain.ErrSystemPromptNotConfigured))
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assistant", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	wantBody := "{\"code\":\"SYSTEM_PROMPT_NOT_CONFIGURED\",\"error\":\"平台未配置全局系统提示词（agent.system_prompt），请联系平台管理员在参数配置中补全后重试\"}"
	if response.Body.String() != wantBody {
		t.Fatalf("body = %q, want %q", response.Body.String(), wantBody)
	}
}

// 建档（评测登记）被测 agent 自身缺 system_prompt：AgentRevision 领域校验失败不再是
// 裸 500，而应映射为 422 + 固定中文 + 专用 code——前端登记 toast 直接展示后端消息，
// 提示用户先为被测 agent 配置系统提示词再重试。
func TestPublicErrorDescribesSubjectAgentPromptRequired(t *testing.T) {
	err := fmt.Errorf("evaluation baseline: create published revision: agent adapter: snapshot baseline: %w",
		agentdomain.ErrAgentSystemPromptRequired)

	got := DescribePublicError(err, http.StatusUnprocessableEntity)
	want := PublicErrorDescriptor{
		Message: "该被测 Agent 尚未配置系统提示词，无法建档。请先在 Agent 配置中填写系统提示词后再登记",
		Code:    CodeAgentSystemPromptRequired,
	}
	if got != want {
		t.Fatalf("DescribePublicError() = %#v, want %#v", got, want)
	}
}

func TestMapErrorToStatusMapsSubjectAgentPromptToUnprocessable(t *testing.T) {
	if got := MapErrorToStatus(agentdomain.ErrAgentSystemPromptRequired); got != http.StatusUnprocessableEntity {
		t.Fatalf("MapErrorToStatus(subject prompt) = %d, want %d", got, http.StatusUnprocessableEntity)
	}
}

func TestErrorHandlerReturnsSubjectAgentPromptContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(ErrorHandler(zap.NewNop()))
	router.GET("/baseline", func(c *gin.Context) {
		_ = c.Error(fmt.Errorf("evaluation baseline: create published revision: agent adapter: snapshot baseline: %w",
			agentdomain.ErrAgentSystemPromptRequired))
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/baseline", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	wantBody := "{\"code\":\"AGENT_SYSTEM_PROMPT_REQUIRED\",\"error\":\"该被测 Agent 尚未配置系统提示词，无法建档。请先在 Agent 配置中填写系统提示词后再登记\"}"
	if response.Body.String() != wantBody {
		t.Fatalf("body = %q, want %q", response.Body.String(), wantBody)
	}
}
