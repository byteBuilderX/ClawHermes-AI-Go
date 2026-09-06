package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

const (
	defaultCenterLimit = 20
	maxCenterLimit     = 100
)

type QueryService struct{ repo port.CenterQueryRepository }

func NewQueryService(repo port.CenterQueryRepository) *QueryService { return &QueryService{repo: repo} }
func (s *QueryService) Overview(ctx context.Context, tenantID string) (domain.CenterOverview, error) {
	return s.repo.Overview(ctx, tenantID)
}

func normalizeCenterFilter(filter port.CenterFilter) (port.CenterFilter, error) {
	if err := validateCenterResourceKinds(filter.ResourceKind); err != nil {
		return filter, domain.ErrInvalidCenterQuery
	}
	allowedStatus := map[string]bool{"": true, "draft": true, "published": true, "queued": true, "running": true, "succeeded": true, "failed": true, "cancelled": true, "active": true, "paused": true, "completed": true, "stopped": true, "rolled_back": true, "proposed": true, "promoted": true, "rejected": true}
	if !allowedStatus[filter.Status] || filter.Limit < 0 {
		return filter, domain.ErrInvalidCenterQuery
	}
	if filter.Cursor != "" {
		if _, err := domain.DecodeCenterCursor(filter.Cursor); err != nil {
			return filter, err
		}
	}
	if filter.Limit == 0 {
		filter.Limit = defaultCenterLimit
	}
	if filter.Limit > maxCenterLimit {
		filter.Limit = maxCenterLimit
	}
	return filter, nil
}

// validateCenterResourceKinds 校验中心被测类型筛选：空串=全部；逗号分隔的每个 token
// 都必须是合法 ResourceKind（skill/agent/mcp/knowledge）。默认双轨 CSV 'agent,knowledge'
// 由此放行，与仓库 ANY(string_to_array($1, ',')) 谓词及 handler 逐 token 语义一致。
func validateCenterResourceKinds(kind string) error {
	if kind == "" {
		return nil
	}
	for _, token := range strings.Split(kind, ",") {
		if token == "" || domain.ResourceKind(token).Validate() != nil {
			return fmt.Errorf("unsupported resource kind in center filter: %q", kind)
		}
	}
	return nil
}

func mapCenterError(err error) error {
	if errors.Is(err, port.ErrCenterResourceNotFound) {
		return domain.ErrCenterResourceNotFound
	}
	return err
}

func (s *QueryService) ListResources(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.ResourcePage, error) {
	f, e := normalizeCenterFilter(filter)
	if e != nil {
		return domain.ResourcePage{}, e
	}
	p, e := s.repo.ListResources(ctx, tenantID, f)
	if p.Items == nil {
		p.Items = []domain.ResourceSummary{}
	}
	return p, mapCenterError(e)
}
func (s *QueryService) ListSuites(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.SuitePage, error) {
	f, e := normalizeCenterFilter(filter)
	if e != nil {
		return domain.SuitePage{}, e
	}
	p, e := s.repo.ListSuites(ctx, tenantID, f)
	if p.Items == nil {
		p.Items = []domain.SuiteSummary{}
	}
	return p, mapCenterError(e)
}
func (s *QueryService) ListRuns(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.RunPage, error) {
	f, e := normalizeCenterFilter(filter)
	if e != nil {
		return domain.RunPage{}, e
	}
	p, e := s.repo.ListRuns(ctx, tenantID, f)
	if p.Items == nil {
		p.Items = []domain.RunSummary{}
	}
	return p, mapCenterError(e)
}
func (s *QueryService) ListCandidates(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.CandidatePage, error) {
	f, e := normalizeCenterFilter(filter)
	if e != nil {
		return domain.CandidatePage{}, e
	}
	p, e := s.repo.ListCandidates(ctx, tenantID, f)
	if p.Items == nil {
		p.Items = []domain.CandidateSummary{}
	}
	return p, mapCenterError(e)
}
func (s *QueryService) ListExperiments(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.ExperimentPage, error) {
	f, e := normalizeCenterFilter(filter)
	if e != nil {
		return domain.ExperimentPage{}, e
	}
	p, e := s.repo.ListExperiments(ctx, tenantID, f)
	if p.Items == nil {
		p.Items = []domain.ExperimentSummary{}
	}
	return p, mapCenterError(e)
}
func (s *QueryService) Timeline(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.TimelinePage, error) {
	f, e := normalizeCenterFilter(filter)
	if e != nil {
		return domain.TimelinePage{}, e
	}
	if strings.TrimSpace(f.ResourceKind) == "" || strings.TrimSpace(f.ResourceID) == "" {
		return domain.TimelinePage{}, fmt.Errorf("%w: resource required", domain.ErrInvalidCenterQuery)
	}
	p, e := s.repo.Timeline(ctx, tenantID, f)
	if p.Items == nil {
		p.Items = []domain.TimelineEvent{}
	}
	return p, mapCenterError(e)
}

func normalizeMonitorFilter(filter port.MonitorFilter) (port.MonitorFilter, error) {
	if err := validateMonitorFilter(filter); err != nil {
		return filter, err
	}
	if filter.Limit < 0 {
		return filter, fmt.Errorf("%w: negative limit", domain.ErrInvalidCenterQuery)
	}
	filter = defaultMonitorWindow(filter)
	if filter.Limit == 0 {
		filter.Limit = constants.EvalMonitorResourceLimitDefault
	}
	if filter.Limit > constants.EvalMonitorResourceLimitMax {
		filter.Limit = constants.EvalMonitorResourceLimitMax
	}
	return filter, nil
}

// validateMonitorFilter 校验 kind 白名单、resource_id 依赖与窗口顺序。
func validateMonitorFilter(filter port.MonitorFilter) error {
	if filter.ResourceKind != "" && domain.ResourceKind(filter.ResourceKind).Validate() != nil {
		return domain.ErrInvalidCenterQuery
	}
	if filter.ResourceKind == "" && strings.TrimSpace(filter.ResourceID) != "" {
		return fmt.Errorf("%w: resource_id requires resource_kind", domain.ErrInvalidCenterQuery)
	}
	if filter.From != nil && filter.To != nil && filter.To.Before(*filter.From) {
		return fmt.Errorf("%w: from after to", domain.ErrInvalidCenterQuery)
	}
	return nil
}

// defaultMonitorWindow 窗口缺省为近 EvalMonitorWindowDays 天（now-窗口, now]。
func defaultMonitorWindow(filter port.MonitorFilter) port.MonitorFilter {
	if filter.From == nil {
		from := time.Now().UTC().Add(-time.Duration(constants.EvalMonitorWindowDays) * 24 * time.Hour)
		filter.From = &from
	}
	if filter.To == nil {
		to := time.Now().UTC()
		filter.To = &to
	}
	return filter
}

func (s *QueryService) MonitorResources(ctx context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	f, e := normalizeMonitorFilter(filter)
	if e != nil {
		return domain.MonitorResourcesPage{}, e
	}
	p, e := s.repo.MonitorResources(ctx, tenantID, f)
	if p.Items == nil {
		p.Items = []domain.MonitorResourceSummary{}
	}
	p.Window = domain.MonitorWindow{From: *f.From, To: *f.To}
	return p, mapCenterError(e)
}

func (s *QueryService) MonitorTrend(ctx context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	if strings.TrimSpace(filter.ResourceKind) == "" || strings.TrimSpace(filter.ResourceID) == "" {
		return domain.MonitorTrendSeries{}, fmt.Errorf("%w: resource required", domain.ErrInvalidCenterQuery)
	}
	f, e := normalizeMonitorFilter(filter)
	if e != nil {
		return domain.MonitorTrendSeries{}, e
	}
	trend, e := s.repo.MonitorTrend(ctx, tenantID, f)
	if trend.Series == nil {
		trend.Series = []domain.MonitorTrendPoint{}
	}
	if trend.Runs == nil {
		trend.Runs = []domain.RunProcessPoint{}
	}
	return trend, mapCenterError(e)
}
