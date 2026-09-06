package application

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// recordingEvalChatRepo 是 OpenEvalConversation 的最小 ChatStore fake：记录
// CreateConversation 收到的参数，并按脚本返回会话/错误，其余方法 no-op。
type recordingEvalChatRepo struct {
	conv *domain.ChatConversation
	err  error

	tenantID, agentID, userID, name, source string
}

func (r *recordingEvalChatRepo) CreateConversation(_ context.Context, tenantID, agentID, userID, name, source string) (*domain.ChatConversation, error) {
	r.tenantID, r.agentID, r.userID, r.name, r.source = tenantID, agentID, userID, name, source
	return r.conv, r.err
}
func (r *recordingEvalChatRepo) GetConversation(context.Context, string, string) (*domain.ChatConversation, error) {
	return nil, nil
}
func (r *recordingEvalChatRepo) ListConversations(context.Context, string, string, string) ([]*domain.ChatConversation, error) {
	return nil, nil
}
func (r *recordingEvalChatRepo) RenameConversation(context.Context, string, string, string, string) error {
	return nil
}
func (r *recordingEvalChatRepo) DeleteConversation(context.Context, string, string, string) error {
	return nil
}
func (r *recordingEvalChatRepo) AddMessage(context.Context, string, *domain.ChatMessage) error {
	return nil
}
func (r *recordingEvalChatRepo) ListMessages(context.Context, string, string, string) ([]*domain.ChatMessage, error) {
	return nil, nil
}
func (r *recordingEvalChatRepo) CleanupExpired(context.Context, string) error {
	return nil
}
func (r *recordingEvalChatRepo) DeleteByAgent(context.Context, string, string) error {
	return nil
}

var _ port.ChatRepo = (*recordingEvalChatRepo)(nil)

func TestOpenEvalConversation_Success(t *testing.T) {
	chat := &recordingEvalChatRepo{conv: &domain.ChatConversation{ID: "conv-eval-1"}}
	svc := NewAgentService(AgentServiceDeps{ChatStore: chat})

	got, err := svc.OpenEvalConversation(context.Background(), "t1", "agent-1", "user-eval")
	require.NoError(t, err)
	require.Equal(t, "conv-eval-1", got)
	// 透传租户/来源/属主：会话名固定「评测会话」，source 为 evaluation 常量。
	require.Equal(t, "t1", chat.tenantID)
	require.Equal(t, "agent-1", chat.agentID)
	require.Equal(t, "user-eval", chat.userID)
	require.Equal(t, "评测会话", chat.name)
	require.Equal(t, constants.ChatConversationSourceEvaluation, chat.source)
}

func TestOpenEvalConversation_FailClosedWhenChatStoreMissing(t *testing.T) {
	svc := NewAgentService(AgentServiceDeps{}) // ChatStore 未装配（nil）

	got, err := svc.OpenEvalConversation(context.Background(), "t1", "agent-1", "user-eval")
	require.Error(t, err)
	require.Empty(t, got)
	require.Contains(t, err.Error(), "chat store not configured")
}

func TestOpenEvalConversation_PropagatesCreateError(t *testing.T) {
	chat := &recordingEvalChatRepo{err: errors.New("create failed")}
	svc := NewAgentService(AgentServiceDeps{ChatStore: chat})

	got, err := svc.OpenEvalConversation(context.Background(), "t1", "agent-1", "user-eval")
	require.Error(t, err)
	require.Empty(t, got)
	require.Contains(t, err.Error(), "agent service: open eval conversation")
	require.Contains(t, err.Error(), "create failed")
}
