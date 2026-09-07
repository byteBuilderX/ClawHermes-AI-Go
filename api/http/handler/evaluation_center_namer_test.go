package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/byteBuilderX/stratum/api/middleware"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// resourceNamerQueries 复用包内 fakeEvaluationQueries（满足 evaluationQueryService
// 全部 12 个方法），仅覆写 ListRuns 返回带被测资源键的行。
type resourceNamerQueries struct {
	fakeEvaluationQueries
	runItems []domain.RunSummary
}

func (s *resourceNamerQueries) ListRuns(_ context.Context, tenantID string, filter port.CenterFilter) (domain.RunPage, error) {
	s.fakeEvaluationQueries.tenantID, s.fakeEvaluationQueries.filter = tenantID, filter
	return domain.RunPage{Items: s.runItems}, nil
}

// resourceNamerStub 记录收到的解析键并返回预设真名 map（可注错验证非阻断语义）。
type resourceNamerStub struct {
	names map[domain.CenterResourceKey]string
	err   error
	got   []domain.CenterResourceKey
}

func (s *resourceNamerStub) ResolveCenterNames(_ context.Context, _ string, keys []domain.CenterResourceKey) (map[domain.CenterResourceKey]string, error) {
	s.got = append(s.got, keys...)
	return s.names, s.err
}

func key(kind domain.ResourceKind, id string) domain.CenterResourceKey {
	return domain.CenterResourceKey{Kind: kind, ResourceID: id}
}

func namerRunPageRequest(t *testing.T, h *EvaluationHandler, items []domain.RunSummary) domain.RunPage {
	t.Helper()
	r := gin.New()
	r.Use(middleware.ErrorHandler(zap.NewNop()))
	r.GET("/evaluations/runs", withTenant("tenant-1"), h.ListRuns)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluations/runs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var page domain.RunPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != len(items) {
		t.Fatalf("typed page response=%s err=%v", rec.Body.String(), err)
	}
	return page
}

func TestEvaluationHandlerEnrichRunNamesDedupsAndFills(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queries := &resourceNamerQueries{runItems: []domain.RunSummary{
		{ID: "run-1", ResourceKind: domain.ResourceKindAgent, ResourceID: "agent-1"},
		{ID: "run-2", ResourceKind: domain.ResourceKindAgent, ResourceID: "agent-1"}, // 同键去重
		{ID: "run-3", ResourceKind: domain.ResourceKindSkill, ResourceID: "skill-2"},
		{ID: "run-4", ResourceKind: domain.ResourceKindAgent}, // 无被测 id → 不触真名解析
	}}
	namer := &resourceNamerStub{names: map[domain.CenterResourceKey]string{
		key(domain.ResourceKindAgent, "agent-1"): "客服 Agent",
		key(domain.ResourceKindSkill, "skill-2"): "意图分类技能",
	}}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop()).
		WithCenterResourceNamer(namer)

	page := namerRunPageRequest(t, h, queries.runItems)

	if page.Items[0].ResourceName != "客服 Agent" || page.Items[1].ResourceName != "客服 Agent" ||
		page.Items[2].ResourceName != "意图分类技能" || page.Items[3].ResourceName != "" {
		t.Fatalf("resource_name not enriched: %+v", page.Items)
	}
	if len(namer.got) != 2 {
		t.Fatalf("distinct keys = %d, want 2 (agent-1 dedup, agent-4 无 id 跳过): %+v", len(namer.got), namer.got)
	}
}

func TestEvaluationHandlerNilNamerReturnsUnenriched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queries := &resourceNamerQueries{runItems: []domain.RunSummary{
		{ID: "run-1", ResourceKind: domain.ResourceKindAgent, ResourceID: "agent-1"},
	}}
	// 未装配 WithCenterResourceNamer → contract harness 形态：读查询照常 200，resource_name 为空。
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop())

	page := namerRunPageRequest(t, h, queries.runItems)

	if page.Items[0].ResourceName != "" {
		t.Fatalf("nil namer must not enrich: %+v", page.Items[0])
	}
}

func TestEvaluationHandlerNamerErrorDoesNotBlockRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queries := &resourceNamerQueries{runItems: []domain.RunSummary{
		{ID: "run-1", ResourceKind: domain.ResourceKindAgent, ResourceID: "agent-1"},
		{ID: "run-2", ResourceKind: domain.ResourceKindSkill, ResourceID: "skill-2"},
	}}
	namer := &resourceNamerStub{
		names: map[domain.CenterResourceKey]string{key(domain.ResourceKindAgent, "agent-1"): "客服 Agent"},
		err:   errors.New("skill service down"),
	}
	h := NewEvaluationHandler(nil, nil, nil, nil, nil, nil, queries, nil, zap.NewNop()).
		WithCenterResourceNamer(namer)

	// 解析错误仅 Warn + 以已解析子集继续：绝不 5xx 阻断只读查询。
	page := namerRunPageRequest(t, h, queries.runItems)

	if page.Items[0].ResourceName != "客服 Agent" {
		t.Fatalf("resolved row must still enrich despite partial error: %+v", page.Items[0])
	}
	if page.Items[1].ResourceName != "" {
		t.Fatalf("unresolved row must stay empty: %+v", page.Items[1])
	}
}
