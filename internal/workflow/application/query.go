package application

import (
	"context"
	"fmt"
	"time"

	"github.com/byteBuilderX/stratum/internal/workflow/domain"
	"github.com/byteBuilderX/stratum/internal/workflow/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

type ListDefinitionsQuery struct {
	Query    string
	Page     int
	PageSize int
}

type DefinitionSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Revision    int64     `json:"revision"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type DefinitionPage struct {
	Workflows []DefinitionSummary `json:"workflows"`
	Total     int                 `json:"total"`
	Page      int                 `json:"page"`
	PageSize  int                 `json:"page_size"`
}

type ListVersionsQuery struct {
	Page     int
	PageSize int
}

type VersionSummary struct {
	ID           string `json:"id"`
	DefinitionID string `json:"definition_id"`
	Number       int64  `json:"version"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	// CreatedBy 是发布该版本的原始 actor id（版本历史「操作者」）；CreatedByName
	// 为展示名（display_name > github_login > 无 users 命中留空，前端回退 CreatedBy）。
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
}

type VersionPage struct {
	Versions []VersionSummary `json:"versions"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

type ListRunsQuery struct {
	ActorID      string
	IsAdmin      bool
	DefinitionID string
	Status       domain.RunStatus
	Page         int
	PageSize     int
}

type RunSummary struct {
	ID            string           `json:"id"`
	DefinitionID  string           `json:"definition_id"`
	Name          string           `json:"name"`
	VersionID     string           `json:"version_id"`
	VersionNumber int64            `json:"version"`
	Status        domain.RunStatus `json:"status"`
	CreatedBy     string           `json:"created_by"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
	StartedAt     *time.Time       `json:"started_at,omitempty"`
	FinishedAt    *time.Time       `json:"finished_at,omitempty"`
}

type RunPage struct {
	Runs     []RunSummary `json:"runs"`
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}

func (s *DefinitionService) ListDefinitions(
	ctx context.Context,
	tenantID string,
	query ListDefinitionsQuery,
) (DefinitionPage, error) {
	repository, ok := s.definitions.(port.DefinitionQueryRepository)
	if !ok {
		return DefinitionPage{}, fmt.Errorf("list workflow definitions: query repository unavailable")
	}
	page, pageSize := normalizePage(query.Page, query.PageSize)
	rows, total, err := repository.ListDefinitions(ctx, tenantID, port.DefinitionListQuery{
		Query: query.Query, Offset: (page - 1) * pageSize, Limit: pageSize,
	})
	if err != nil {
		return DefinitionPage{}, fmt.Errorf("list workflow definitions: %w", err)
	}
	summaries := make([]DefinitionSummary, len(rows))
	for i, row := range rows {
		summaries[i] = DefinitionSummary{
			ID: row.ID, Name: row.Name, Description: row.Description, Revision: row.Revision, UpdatedAt: row.UpdatedAt,
		}
	}
	return DefinitionPage{Workflows: summaries, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *DefinitionService) ListVersions(
	ctx context.Context,
	tenantID, definitionID string,
	query ListVersionsQuery,
) (VersionPage, error) {
	repository, ok := s.versions.(port.VersionQueryRepository)
	if !ok {
		return VersionPage{}, fmt.Errorf("list workflow versions: query repository unavailable")
	}
	page, pageSize := normalizePage(query.Page, query.PageSize)
	rows, total, err := repository.ListVersions(ctx, tenantID, definitionID, port.VersionListQuery{
		Offset: (page - 1) * pageSize, Limit: pageSize,
	})
	if err != nil {
		return VersionPage{}, fmt.Errorf("list workflow versions: %w", err)
	}
	names, err := s.resolveVersionActorNames(ctx, rows)
	if err != nil {
		return VersionPage{}, err
	}
	summaries := make([]VersionSummary, len(rows))
	for i, row := range rows {
		summaries[i] = VersionSummary{
			ID: row.ID, DefinitionID: row.DefinitionID, Number: row.Number, Name: row.Name,
			Description: row.Description, CreatedAt: row.CreatedAt,
			CreatedBy: row.CreatedBy, CreatedByName: names[row.CreatedBy],
		}
	}
	return VersionPage{Versions: summaries, Total: total, Page: page, PageSize: pageSize}, nil
}

// resolveVersionActorNames 批量解析版本发布者展示名（display-only）。未注入
// nameResolver 时跳过（操作者列回退原始 id，names 恒空映射）；注入后查询失败
// 必须传播错误（fail-closed：禁止默认名掩盖查询故障）。无 users 命中的 actor
// （system 占位符等）不在映射中 → CreatedByName 留空，前端回退 CreatedBy 原文。
func (s *DefinitionService) resolveVersionActorNames(ctx context.Context, rows []domain.Version) (map[string]string, error) {
	if s.nameResolver == nil {
		return map[string]string{}, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.CreatedBy != "" {
			ids = append(ids, row.CreatedBy)
		}
	}
	names, err := s.nameResolver.ResolveActorNames(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("workflow service resolve actor names: %w", err)
	}
	return names, nil
}

func (s *RunService) ListRuns(ctx context.Context, tenantID string, query ListRunsQuery) (RunPage, error) {
	repository, ok := s.store.(port.RunQueryRepository)
	if !ok {
		return RunPage{}, fmt.Errorf("list workflow runs: query repository unavailable")
	}
	if !query.IsAdmin && query.ActorID == "" {
		return RunPage{}, domain.ErrForbidden
	}
	page, pageSize := normalizePage(query.Page, query.PageSize)
	createdBy := query.ActorID
	if query.IsAdmin {
		createdBy = ""
	}
	rows, total, err := repository.ListRuns(ctx, tenantID, port.RunListQuery{
		CreatedBy: createdBy, DefinitionID: query.DefinitionID, Status: query.Status,
		Offset: (page - 1) * pageSize, Limit: pageSize,
	})
	if err != nil {
		return RunPage{}, fmt.Errorf("list workflow runs: %w", err)
	}
	summaries := make([]RunSummary, len(rows))
	for i, row := range rows {
		summaries[i] = NewRunSummary(row)
	}
	return RunPage{Runs: summaries, Total: total, Page: page, PageSize: pageSize}, nil
}

func NewRunSummary(run domain.Run) RunSummary {
	return RunSummary{
		ID: run.ID, DefinitionID: run.DefinitionID, Name: run.Name, VersionID: run.VersionID, VersionNumber: run.VersionNumber,
		Status: run.Status, CreatedBy: run.CreatedBy, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
	}
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > constants.MaxPageSize {
		pageSize = constants.DefaultPageSize
	}
	return page, pageSize
}
