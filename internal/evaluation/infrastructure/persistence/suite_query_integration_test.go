//go:build integration

package persistence

import (
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
)

// TestPgSuiteQueryMethodsAndEnhancedList covers the S1-2 suite read surface:
// GetSuite returns suite meta with created_at, ListSuiteRevisions returns the
// full version chain (published v1 + inherited draft) with enabled case counts,
// and the center query repo's ListSuites carries the same active/draft
// version + enabled-case-count enrichment on its summary rows.
func TestPgSuiteQueryMethodsAndEnhancedList(t *testing.T) {
	pool, ctx, tenantID := newSuiteRepoTestPool(t, "eval_query_methods")
	repo := NewPgSuiteRepository(pool)
	center := NewPgCenterQueryRepository(pool)

	suiteID := "suite-query"
	suite := domain.EvalSuite{ID: suiteID, Name: "查询能力", Description: "desc", CreatedBy: "carol"}
	revision := domain.EvalSuiteRevision{
		ID: "rev-q1", SuiteID: suiteID, Status: domain.SuiteRevisionDraft,
		ResourceKind: domain.ResourceKindSkill, CreatedBy: "carol",
		Cases: []domain.EvalCase{
			{ID: "c-enabled", Name: "启用", Input: "问", ExpectedOutput: "答", AssertionMode: domain.AssertionContains, Enabled: true},
			{ID: "c-disabled", Name: "停用", Input: "问2", ExpectedOutput: "答2", AssertionMode: domain.AssertionContains, Enabled: false},
		},
	}
	if err := repo.CreateSuite(ctx, tenantID, suite, revision); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PublishRevision(ctx, tenantID, suiteID, revision.ID, 1); err != nil {
		t.Fatal(err)
	}

	// GetSuite：元信息 + 非零 created_at + active/draft 指针。
	got, found, err := repo.GetSuite(ctx, tenantID, suiteID)
	if err != nil || !found {
		t.Fatalf("GetSuite: found=%v err=%v", found, err)
	}
	if got.Name != "查询能力" || got.Description != "desc" || got.CreatedBy != "carol" {
		t.Fatalf("GetSuite meta mismatch: %+v", got)
	}
	if got.ActiveRevisionID != "rev-q1" || got.DraftRevisionID == "" {
		t.Fatalf("GetSuite active/draft pointers wrong: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("GetSuite created_at not loaded: %+v", got)
	}
	if _, found, _ := repo.GetSuite(ctx, tenantID, "missing"); found {
		t.Fatal("GetSuite must report not-found for unknown suite")
	}

	// ListSuiteRevisions：published v1（启用计数只算 enabled）+ 继承草稿（version_no 0）。
	metas, err := repo.ListSuiteRevisions(ctx, tenantID, suiteID)
	if err != nil {
		t.Fatalf("ListSuiteRevisions: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("expected 2 revision metas, got %d: %+v", len(metas), metas)
	}
	if metas[0].ID != "rev-q1" || metas[0].VersionNo != 1 || metas[0].Status != domain.SuiteRevisionPublished {
		t.Fatalf("published meta wrong: %+v", metas[0])
	}
	if metas[0].EnabledCaseCount != 1 {
		t.Fatalf("enabled count must exclude disabled case, got %d", metas[0].EnabledCaseCount)
	}
	if metas[0].PublishedAt == nil {
		t.Fatalf("published meta must carry published_at: %+v", metas[0])
	}
	if metas[0].ResourceKind != domain.ResourceKindSkill || metas[0].CreatedBy != "carol" {
		t.Fatalf("published meta kind/created_by wrong: %+v", metas[0])
	}
	if metas[1].ID != got.DraftRevisionID || metas[1].Status != domain.SuiteRevisionDraft || metas[1].VersionNo != 0 {
		t.Fatalf("draft meta wrong: %+v", metas[1])
	}
	if metas[1].EnabledCaseCount != 1 || metas[1].PublishedAt != nil {
		t.Fatalf("draft meta count/published_at wrong: %+v", metas[1])
	}

	// ListSuites（center query）：行携带 active/draft 版本号与启用 case 数。
	page, err := center.ListSuites(ctx, tenantID, port.CenterFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListSuites: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 suite row, got %d", len(page.Items))
	}
	s := page.Items[0]
	if s.ID != suiteID || s.Name != "查询能力" || s.Description != "desc" || s.CreatedBy != "carol" {
		t.Fatalf("suite row meta mismatch: %+v", s)
	}
	if s.ResourceKind != domain.ResourceKindSkill || s.Status != "published" {
		t.Fatalf("suite row kind/status mismatch: %+v", s)
	}
	if s.ActiveRevisionID != "rev-q1" || s.DraftRevisionID != got.DraftRevisionID {
		t.Fatalf("suite row active/draft pointer mismatch: %+v", s)
	}
	if s.ActiveVersionNo != 1 || s.DraftVersionNo != 0 || s.ActiveCaseCount != 1 || s.DraftCaseCount != 1 {
		t.Fatalf("suite row version/count enrichment mismatch: %+v", s)
	}
	if s.CreatedAt.IsZero() {
		t.Fatalf("suite row created_at missing: %+v", s)
	}
}
