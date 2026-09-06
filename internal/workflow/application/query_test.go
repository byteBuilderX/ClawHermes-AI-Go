package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/workflow/application"
	"github.com/byteBuilderX/stratum/internal/workflow/domain"
	"github.com/byteBuilderX/stratum/internal/workflow/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type queryStore struct {
	*memoryStore
	definitionQuery port.DefinitionListQuery
	versionQuery    port.VersionListQuery
	runQuery        port.RunListQuery
}

func newQueryStore() *queryStore {
	return &queryStore{memoryStore: newMemoryStore()}
}

func (s *queryStore) ListDefinitions(_ context.Context, _ string, query port.DefinitionListQuery) ([]domain.Definition, int, error) {
	s.definitionQuery = query
	return []domain.Definition{{ID: "wf-1", Name: "研究", Revision: 3}}, 41, nil
}

func (s *queryStore) ListVersions(_ context.Context, _, _ string, query port.VersionListQuery) ([]domain.Version, int, error) {
	s.versionQuery = query
	return []domain.Version{{ID: "version-2", DefinitionID: "wf-1", Number: 2, CreatedBy: "user-a"}}, 2, nil
}

// stubNameResolver 固定 actor→昵称映射，供 ListVersions 展示名解析用例使用。
type stubNameResolver struct{ names map[string]string }

func (r stubNameResolver) ResolveActorNames(_ context.Context, actorIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(actorIDs))
	for _, id := range actorIDs {
		if n, ok := r.names[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

func (s *queryStore) ListRuns(_ context.Context, _ string, query port.RunListQuery) ([]domain.Run, int, error) {
	s.runQuery = query
	return []domain.Run{{ID: "run-1", DefinitionID: "wf-1", Status: domain.RunStatusRunning, CreatedBy: "user-a"}}, 1, nil
}

func (s *queryStore) AppendEvent(_ context.Context, _ string, event domain.Event) (domain.Event, error) {
	return event, nil
}

func (s *queryStore) ListEvents(context.Context, string, string, int64, int) ([]domain.Event, error) {
	return nil, nil
}

func TestDefinitionServiceListsDefinitionsWithNormalizedPagination(t *testing.T) {
	store := newQueryStore()
	service := application.NewDefinitionService(store, store, (&ids{}).NewID)

	page, err := service.ListDefinitions(context.Background(), "tenant-1", application.ListDefinitionsQuery{
		Query: "研究", Page: 0, PageSize: constants.MaxPageSize + 1,
	})
	require.NoError(t, err)
	require.Equal(t, port.DefinitionListQuery{Query: "研究", Offset: 0, Limit: constants.DefaultPageSize}, store.definitionQuery)
	require.Equal(t, 41, page.Total)
	require.Equal(t, 1, page.Page)
	require.Equal(t, constants.DefaultPageSize, page.PageSize)
	require.Equal(t, "wf-1", page.Workflows[0].ID)
}

func TestDefinitionServiceListsVersionsWithStablePageOffset(t *testing.T) {
	store := newQueryStore()
	service := application.NewDefinitionService(store, store, (&ids{}).NewID)

	page, err := service.ListVersions(context.Background(), "tenant-1", "wf-1", application.ListVersionsQuery{
		Page: 2, PageSize: 10,
	})
	require.NoError(t, err)
	require.Equal(t, port.VersionListQuery{Offset: 10, Limit: 10}, store.versionQuery)
	require.Equal(t, 2, page.Total)
	require.Equal(t, int64(2), page.Versions[0].Number)
	// 未注入解析器：保留 raw actor（CreatedByName 空，前端回退 CreatedBy）。
	require.Equal(t, "user-a", page.Versions[0].CreatedBy)
	require.Empty(t, page.Versions[0].CreatedByName)
}

// TestDefinitionServiceListsVersionsResolvesOperatorName 锁定 ListVersions 对
// 发布者昵称的批量解析：注入解析器后 CreatedByName 取展示名，raw CreatedBy 保留。
func TestDefinitionServiceListsVersionsResolvesOperatorName(t *testing.T) {
	store := newQueryStore()
	service := application.NewDefinitionService(store, store, (&ids{}).NewID)
	service.SetActorNameResolver(stubNameResolver{names: map[string]string{"user-a": "Alice"}})

	page, err := service.ListVersions(context.Background(), "tenant-1", "wf-1", application.ListVersionsQuery{
		Page: 1, PageSize: 10,
	})
	require.NoError(t, err)
	require.Equal(t, "user-a", page.Versions[0].CreatedBy)
	require.Equal(t, "Alice", page.Versions[0].CreatedByName)
}

func TestRunServiceListsMemberRunsWithOwnershipFilter(t *testing.T) {
	store := newQueryStore()
	service := application.NewRunServiceWithRegistry(store, store, &scriptedRegistry{}, (&ids{}).NewID, zap.NewNop())

	page, err := service.ListRuns(context.Background(), "tenant-1", application.ListRunsQuery{
		ActorID: "user-a", DefinitionID: "wf-1", Status: domain.RunStatusRunning, Page: 1, PageSize: 20,
	})
	require.NoError(t, err)
	require.Equal(t, port.RunListQuery{
		CreatedBy: "user-a", DefinitionID: "wf-1", Status: domain.RunStatusRunning, Offset: 0, Limit: 20,
	}, store.runQuery)
	require.Equal(t, 1, page.Total)
	require.Equal(t, "user-a", page.Runs[0].CreatedBy)
}

func TestRunServiceListsAllTenantRunsForAdmin(t *testing.T) {
	store := newQueryStore()
	service := application.NewRunServiceWithRegistry(store, store, &scriptedRegistry{}, (&ids{}).NewID, zap.NewNop())

	_, err := service.ListRuns(context.Background(), "tenant-1", application.ListRunsQuery{
		ActorID: "admin-a", IsAdmin: true, Page: 1, PageSize: 20,
	})
	require.NoError(t, err)
	require.Empty(t, store.runQuery.CreatedBy)
}

func TestRunServiceRejectsMemberListWithoutActor(t *testing.T) {
	store := newQueryStore()
	service := application.NewRunServiceWithRegistry(store, store, &scriptedRegistry{}, (&ids{}).NewID, zap.NewNop())

	_, err := service.ListRuns(context.Background(), "tenant-1", application.ListRunsQuery{Page: 1, PageSize: 20})
	require.ErrorIs(t, err, domain.ErrForbidden)
}

func TestWorkflowQuerySummariesPreservePublicTiming(t *testing.T) {
	now := time.Now().UTC()
	run := domain.Run{ID: "run-1", CreatedAt: now, UpdatedAt: now}
	summary := application.NewRunSummary(run)
	require.Equal(t, now, summary.CreatedAt)
	require.Equal(t, now, summary.UpdatedAt)
}
