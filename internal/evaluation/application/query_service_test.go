package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

func TestQueryServiceNormalizesLimitsAndDelegates(t *testing.T) {
	repo := &queryRepoStub{}
	svc := NewQueryService(repo)

	if _, err := svc.ListResources(context.Background(), "tenant-1", port.CenterFilter{}); err != nil {
		t.Fatal(err)
	}
	if repo.filter.Limit != 20 {
		t.Fatalf("default limit = %d, want 20", repo.filter.Limit)
	}
	if _, err := svc.ListResources(context.Background(), "tenant-1", port.CenterFilter{Limit: 999}); err != nil {
		t.Fatal(err)
	}
	if repo.filter.Limit != 100 {
		t.Fatalf("maximum limit = %d, want 100", repo.filter.Limit)
	}
}

// TestQueryServiceListRunsPassesRevisionFilter Batch 6 (b)：revision_id 是资源详情回归
// 视图的服务端过滤口，service 层透传到仓库（不改谓词语义），空值放行全量并回落默认分页。
func TestQueryServiceListRunsPassesRevisionFilter(t *testing.T) {
	tests := []struct {
		name         string
		in           port.CenterFilter
		wantRevision string
		wantLimit    int
	}{
		{name: "revision passthrough", in: port.CenterFilter{ResourceKind: "skill", ResourceID: "shared",
			RevisionID: "rev-a", Limit: 5}, wantRevision: "rev-a", wantLimit: 5},
		{name: "empty revision defaults limit", in: port.CenterFilter{}, wantRevision: "", wantLimit: 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &queryRepoStub{}
			svc := NewQueryService(repo)
			if _, err := svc.ListRuns(context.Background(), "tenant-1", tc.in); err != nil {
				t.Fatal(err)
			}
			if repo.filter.RevisionID != tc.wantRevision || repo.filter.Limit != tc.wantLimit {
				t.Fatalf("ListRuns filter=%+v, want revision=%q limit=%d", repo.filter, tc.wantRevision, tc.wantLimit)
			}
		})
	}
}

func TestQueryServiceRejectsInvalidFilters(t *testing.T) {
	svc := NewQueryService(&queryRepoStub{})
	tests := []port.CenterFilter{
		{ResourceKind: "invalid"},
		{ResourceKind: "agent,workflow"},
		{ResourceKind: "agent,"},
		{Status: "invalid"},
		{Cursor: "not-a-cursor"},
		{Limit: -1},
	}
	for _, filter := range tests {
		if _, err := svc.ListResources(context.Background(), "tenant-1", filter); !errors.Is(err, domain.ErrInvalidCenterQuery) {
			t.Errorf("filter %+v error = %v, want invalid center query", filter, err)
		}
	}
}

// TestQueryServiceAcceptsCommaSeparatedResourceKinds 默认双轨 CSV 'agent,knowledge' 必须
// 放行到仓库的 ANY(string_to_array) 谓词；单值四类保持可用（被测收敛回归：阻断曾出现在
// 此层整串校验导致 agent,knowledge 被拒 400）。
func TestQueryServiceAcceptsCommaSeparatedResourceKinds(t *testing.T) {
	svc := NewQueryService(&queryRepoStub{})
	tests := []string{"agent,knowledge", "agent", "knowledge", "skill", "mcp", "skill,mcp,agent,knowledge"}
	for _, kinds := range tests {
		if _, err := svc.ListResources(context.Background(), "tenant-1", port.CenterFilter{ResourceKind: kinds}); err != nil {
			t.Errorf("ListResources resource_kind=%q error = %v, want accepted", kinds, err)
		}
	}
}

func TestQueryServiceTimelineRequiresResourceAndHidesNotFound(t *testing.T) {
	repo := &queryRepoStub{}
	svc := NewQueryService(repo)
	if _, err := svc.Timeline(context.Background(), "tenant-1", port.CenterFilter{ResourceKind: "skill"}); !errors.Is(err, domain.ErrInvalidCenterQuery) {
		t.Fatalf("missing resource error = %v", err)
	}
	repo.err = port.ErrCenterResourceNotFound
	if _, err := svc.Timeline(context.Background(), "tenant-1", port.CenterFilter{
		ResourceKind: "skill", ResourceID: "same-id",
	}); !errors.Is(err, domain.ErrCenterResourceNotFound) {
		t.Fatalf("not found error = %v", err)
	}
}

func TestQueryServicePreservesCandidateSafeDiff(t *testing.T) {
	repo := &queryRepoStub{candidates: domain.CandidatePage{Items: []domain.CandidateSummary{{
		ID: "candidate-1", SafeDiff: domain.CandidateSafeDiff{ChangedFields: []string{"label"},
			Changes: map[string]domain.SafeFieldChange{"label": {Before: "old", After: "new"}}},
	}}}}
	page, err := NewQueryService(repo).ListCandidates(context.Background(), "tenant-1", port.CenterFilter{})
	if err != nil || page.Items[0].SafeDiff.Changes["label"].After != "new" {
		t.Fatalf("candidate page=%+v err=%v", page, err)
	}
}

func TestQueryServiceReturnsEmptyCollectionsAsArrays(t *testing.T) {
	svc := NewQueryService(&queryRepoStub{})
	ctx := context.Background()

	resources, err := svc.ListResources(ctx, "tenant-1", port.CenterFilter{})
	if err != nil || resources.Items == nil {
		t.Fatalf("resources items = %#v, err = %v; want non-nil empty slice", resources.Items, err)
	}
	suites, err := svc.ListSuites(ctx, "tenant-1", port.CenterFilter{})
	if err != nil || suites.Items == nil {
		t.Fatalf("suites items = %#v, err = %v; want non-nil empty slice", suites.Items, err)
	}
	runs, err := svc.ListRuns(ctx, "tenant-1", port.CenterFilter{})
	if err != nil || runs.Items == nil {
		t.Fatalf("runs items = %#v, err = %v; want non-nil empty slice", runs.Items, err)
	}
	candidates, err := svc.ListCandidates(ctx, "tenant-1", port.CenterFilter{})
	if err != nil || candidates.Items == nil {
		t.Fatalf("candidates items = %#v, err = %v; want non-nil empty slice", candidates.Items, err)
	}
	experiments, err := svc.ListExperiments(ctx, "tenant-1", port.CenterFilter{})
	if err != nil || experiments.Items == nil {
		t.Fatalf("experiments items = %#v, err = %v; want non-nil empty slice", experiments.Items, err)
	}
	timeline, err := svc.Timeline(ctx, "tenant-1", port.CenterFilter{ResourceKind: "skill", ResourceID: "resource-1"})
	if err != nil || timeline.Items == nil {
		t.Fatalf("timeline items = %#v, err = %v; want non-nil empty slice", timeline.Items, err)
	}
}

// TestQueryServiceMonitorNormalizesWindowAndLimit 兜底：未给窗口 → 填近 EvalMonitorWindowDays 天；
// limit 默认/封顶；window 与空切片由 service 保证。
func TestQueryServiceMonitorNormalizesWindowAndLimit(t *testing.T) {
	repo := &queryRepoStub{}
	svc := NewQueryService(repo)
	page, err := svc.MonitorResources(context.Background(), "tenant-1", port.MonitorFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if repo.monFilter.From == nil || repo.monFilter.To == nil {
		t.Fatal("window not defaulted")
	}
	if !repo.monFilter.To.After(*repo.monFilter.From) {
		t.Fatalf("window invalid: %v -> %v", repo.monFilter.From, repo.monFilter.To)
	}
	// 缺省窗口语义：近 EvalMonitorWindowDays 天。From/To 各自取 time.Now 有亚毫秒级先后差，
	// 用 ≤1s 容差锁定真实跨度，防回归把兜底改成 1 小时等短窗仍通过。
	wantSpan := time.Duration(constants.EvalMonitorWindowDays) * 24 * time.Hour
	if got := repo.monFilter.To.Sub(*repo.monFilter.From); got < wantSpan || got-wantSpan > time.Second {
		t.Fatalf("window span = %v, want ~%v", got, wantSpan)
	}
	if repo.monFilter.Limit != constants.EvalMonitorResourceLimitDefault {
		t.Fatalf("default limit = %d", repo.monFilter.Limit)
	}
	if page.Items == nil || page.Window.From.IsZero() {
		t.Fatalf("page items/window not normalized: %+v", page)
	}
}

// TestQueryServiceMonitorRejectsBadFilters kind 非法 / 单传 resource_id / from>to → invalid。
func TestQueryServiceMonitorRejectsBadFilters(t *testing.T) {
	svc := NewQueryService(&queryRepoStub{})
	now := time.Now().UTC()
	before, after := now.Add(-time.Hour), now
	cases := []port.MonitorFilter{
		{ResourceKind: "invalid"},
		{ResourceID: "only-id"},
		{From: &after, To: &before},
		{Limit: -1},
	}
	for _, filter := range cases {
		if _, err := svc.MonitorResources(context.Background(), "tenant-1", filter); !errors.Is(err, domain.ErrInvalidCenterQuery) {
			t.Errorf("filter %+v error = %v, want invalid", filter, err)
		}
	}
}

// TestQueryServiceMonitorTrendRequiresResource trend 缺 kind/id → invalid；repo not-found 透传。
func TestQueryServiceMonitorTrendRequiresResource(t *testing.T) {
	repo := &queryRepoStub{}
	svc := NewQueryService(repo)
	if _, err := svc.MonitorTrend(context.Background(), "tenant-1", port.MonitorFilter{}); !errors.Is(err, domain.ErrInvalidCenterQuery) {
		t.Fatalf("missing resource error = %v", err)
	}
	repo.err = port.ErrCenterResourceNotFound
	if _, err := svc.MonitorTrend(context.Background(), "tenant-1", port.MonitorFilter{
		ResourceKind: "skill", ResourceID: "sk1",
	}); !errors.Is(err, domain.ErrCenterResourceNotFound) {
		t.Fatalf("not found error = %v", err)
	}
}

type queryRepoStub struct {
	filter     port.CenterFilter
	err        error
	candidates domain.CandidatePage
	monFilter  port.MonitorFilter
	monPage    domain.MonitorResourcesPage
}

func (r *queryRepoStub) Overview(context.Context, string) (domain.CenterOverview, error) {
	return domain.CenterOverview{}, r.err
}
func (r *queryRepoStub) ListResources(_ context.Context, _ string, filter port.CenterFilter) (domain.ResourcePage, error) {
	r.filter = filter
	return domain.ResourcePage{}, r.err
}
func (r *queryRepoStub) ListSuites(context.Context, string, port.CenterFilter) (domain.SuitePage, error) {
	return domain.SuitePage{}, r.err
}
func (r *queryRepoStub) ListRuns(_ context.Context, _ string, filter port.CenterFilter) (domain.RunPage, error) {
	r.filter = filter
	return domain.RunPage{}, r.err
}
func (r *queryRepoStub) ListCandidates(context.Context, string, port.CenterFilter) (domain.CandidatePage, error) {
	return r.candidates, r.err
}
func (r *queryRepoStub) ListExperiments(context.Context, string, port.CenterFilter) (domain.ExperimentPage, error) {
	return domain.ExperimentPage{}, r.err
}
func (r *queryRepoStub) Timeline(_ context.Context, _ string, filter port.CenterFilter) (domain.TimelinePage, error) {
	r.filter = filter
	return domain.TimelinePage{}, r.err
}
func (r *queryRepoStub) MonitorResources(_ context.Context, _ string, filter port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	r.monFilter = filter
	return r.monPage, r.err
}
func (r *queryRepoStub) MonitorTrend(_ context.Context, _ string, filter port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	r.monFilter = filter
	return domain.MonitorTrendSeries{}, r.err
}
