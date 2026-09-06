//go:build integration

package persistence

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newSuiteRepoTestPool provisions a throwaway tenant for integration tests and
// registers cleanup. The returned tenantID scopes every repository call via
// execTenant; each test uses a distinct tenantID so parallel runs never collide.
func newSuiteRepoTestPool(t *testing.T, tenantID string) (*pgxpool.Pool, context.Context, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.ProvisionTenantSchema(ctx, pool, tenantID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS "tenant_%s" CASCADE`, tenantID)); err != nil {
			t.Logf("cleanup tenant %s: %v", tenantID, err)
		}
		pool.Close()
	})
	return pool, ctx, tenantID
}

// TestPgSuitePublishInheritsFullDraft covers the S1-1 publish behavior: the
// draft just published becomes active, and a successor draft is opened that
// inherits every case under a fresh id, faithfully carrying content, judge
// spec, process assertions, provenance and the session script.
func TestPgSuitePublishInheritsFullDraft(t *testing.T) {
	pool, ctx, tenantID := newSuiteRepoTestPool(t, "eval_publish_inherit")
	repo := NewPgSuiteRepository(pool)
	suiteID := "suite-inherit"
	suite := domain.EvalSuite{ID: suiteID, Name: "发布继承", DraftRevisionID: "rev-1"}
	sessionScript := &domain.EvalSessionScript{Goal: "退款", Turns: []domain.SessionTurn{
		{User: "没发货", ToolSpec: &domain.ToolSpec{MustCall: []string{"refund"}}},
	}}
	revision := domain.EvalSuiteRevision{
		ID: "rev-1", SuiteID: suiteID, Status: domain.SuiteRevisionDraft,
		ResourceKind: domain.ResourceKindAgent, CreatedBy: "alice",
		Cases: []domain.EvalCase{
			{ID: "case-rule", Name: "规则", Input: "输入", ExpectedOutput: "输出", AssertionMode: domain.AssertionExact, Enabled: true},
			{ID: "case-judge", Name: "法官", Input: "问题", ExpectedOutput: "结论", AssertionMode: domain.AssertionJudge, Enabled: false,
				JudgeSpec: &domain.JudgeSpec{Model: "qwen-max", Rubric: "rubric"},
				ToolSpec:  &domain.ToolSpec{MustNotCall: []string{"delete"}}, StepJudge: &domain.StepJudge{Criteria: "逐步"},
				SourceTraceID: "trace-1", FeedbackRef: "fb-1", GenerateReason: "负反馈"},
			{ID: "case-session", Name: "会话", Input: nil, ExpectedOutput: "已退款", AssertionMode: domain.AssertionContains,
				Enabled: true, Session: sessionScript},
		},
	}
	if err := repo.CreateSuite(ctx, tenantID, suite, revision); err != nil {
		t.Fatal(err)
	}
	published, err := repo.PublishRevision(ctx, tenantID, suiteID, revision.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if published.ID != "rev-1" || published.Status != domain.SuiteRevisionPublished || len(published.Cases) != 3 {
		t.Fatalf("unexpected published revision: %+v", published)
	}

	// 发布后自动开启继承草稿：active 指向 rev-1、draft 指向新草稿，全部 case 拷贝且 id 全新。
	draft, ok, err := repo.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil || !ok {
		t.Fatalf("GetDraftRevision after publish: ok=%v err=%v", ok, err)
	}
	if draft.Status != domain.SuiteRevisionDraft || draft.ParentID != "rev-1" {
		t.Fatalf("draft not chained to published revision: %+v", draft)
	}
	if draft.ResourceKind != domain.ResourceKindAgent || draft.CreatedBy != "alice" {
		t.Fatalf("draft must inherit kind/created_by from published revision: %+v", draft)
	}
	if len(draft.Cases) != 3 {
		t.Fatalf("draft must inherit all 3 cases, got %d", len(draft.Cases))
	}
	seen := map[string]bool{}
	for _, c := range draft.Cases {
		if seen[c.ID] {
			t.Fatalf("duplicate case id in inherited draft: %s", c.ID)
		}
		seen[c.ID] = true
	}
	rule := caseByName(t, draft.Cases, "规则")
	if rule.Input != "输入" || rule.ExpectedOutput != "输出" {
		t.Fatalf("rule case content lost on inherit: %+v", draft.Cases)
	}
	judge := findJudgeCase(t, draft.Cases)
	if judge.JudgeSpec == nil || judge.JudgeSpec.Model != "qwen-max" || judge.JudgeSpec.Rubric != "rubric" {
		t.Fatalf("judge spec lost on inherit: %+v", judge)
	}
	if judge.ToolSpec == nil || judge.StepJudge == nil || len(judge.ToolSpec.MustNotCall) != 1 {
		t.Fatalf("process assertions lost on inherit: %+v", judge)
	}
	if judge.SourceTraceID != "trace-1" || judge.FeedbackRef != "fb-1" || judge.GenerateReason != "负反馈" {
		t.Fatalf("provenance lost on inherit: %+v", judge)
	}
	if judge.Enabled {
		t.Fatalf("enabled=false must be preserved on inherit: %+v", judge)
	}
	sess := findSessionCase(t, draft.Cases)
	if sess.Session == nil || sess.Session.Goal != "退款" || len(sess.Session.Turns) != 1 ||
		sess.Session.Turns[0].ToolSpec == nil || len(sess.Session.Turns[0].ToolSpec.MustCall) != 1 {
		t.Fatalf("session script lost on inherit: %+v", sess.Session)
	}

	// active 仍是原 rev-1，带原始 case id（load 排序按 created_at,id，故按集合断言）。
	active, found, err := repo.GetActiveRevision(ctx, tenantID, suiteID)
	if err != nil || !found {
		t.Fatalf("GetActiveRevision: found=%v err=%v", found, err)
	}
	activeIDs := map[string]bool{}
	for _, c := range active.Cases {
		activeIDs[c.ID] = true
	}
	if active.ID != "rev-1" || active.Status != domain.SuiteRevisionPublished ||
		!activeIDs["case-rule"] || !activeIDs["case-judge"] || !activeIDs["case-session"] {
		t.Fatalf("active revision must stay untouched (original case ids intact): %+v", active)
	}
	next, err := repo.NextVersionNo(ctx, tenantID, suiteID)
	if err != nil {
		t.Fatal(err)
	}
	if next != 2 {
		t.Fatalf("expected next version 2, got %d", next)
	}
}

// TestPgSuitePublishTwiceProgression publishes a draft, edits the inherited
// draft and publishes again, asserting the version chain advances and each
// publish keeps an editable draft open (v1 → v2 → successor draft).
func TestPgSuitePublishTwiceProgression(t *testing.T) {
	pool, ctx, tenantID := newSuiteRepoTestPool(t, "eval_publish_progression")
	repo := NewPgSuiteRepository(pool)
	suiteID := "suite-progress"
	suite := domain.EvalSuite{ID: suiteID, Name: "演进", DraftRevisionID: "rev-a1"}
	revision := domain.EvalSuiteRevision{
		ID: "rev-a1", SuiteID: suiteID, Status: domain.SuiteRevisionDraft,
		ResourceKind: domain.ResourceKindSkill,
		Cases:        []domain.EvalCase{{ID: "c1", Name: "v1 用例", Input: "x", ExpectedOutput: "y", AssertionMode: domain.AssertionExact, Enabled: true}},
	}
	if err := repo.CreateSuite(ctx, tenantID, suite, revision); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PublishRevision(ctx, tenantID, suiteID, revision.ID, 1); err != nil {
		t.Fatal(err)
	}
	draft, _, err := repo.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		t.Fatal(err)
	}
	inheritedID := draft.Cases[0].ID
	// 在继承草稿上新增一条，形成 v2 内容。
	added := domain.EvalCase{ID: "c2", Name: "v2 新增", Input: "x2", ExpectedOutput: "y2", AssertionMode: domain.AssertionExact, Enabled: true}
	if err := repo.AddDraftCases(ctx, tenantID, draft.ID, []domain.EvalCase{added}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PublishRevision(ctx, tenantID, suiteID, draft.ID, 2); err != nil {
		t.Fatal(err)
	}
	active, found, err := repo.GetActiveRevision(ctx, tenantID, suiteID)
	if err != nil || !found {
		t.Fatalf("GetActiveRevision v2: found=%v err=%v", found, err)
	}
	if active.ID != draft.ID || active.VersionNo != 2 || len(active.Cases) != 2 {
		t.Fatalf("v2 must hold inherited + added case: %+v", active)
	}
	next, err := repo.NextVersionNo(ctx, tenantID, suiteID)
	if err != nil {
		t.Fatal(err)
	}
	if next != 3 {
		t.Fatalf("expected next version 3, got %d", next)
	}
	// 每发布一次都留一个可编辑草稿，其 parent 指向刚发布版本。
	nextDraft, ok, err := repo.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil || !ok {
		t.Fatalf("GetDraftRevision after v2 publish: ok=%v err=%v", ok, err)
	}
	if nextDraft.ParentID != draft.ID || len(nextDraft.Cases) != 2 {
		t.Fatalf("v2 publish must open inherited draft: %+v", nextDraft)
	}
	// v2 active 就是刚发布的草稿本身：沿用草稿的 id（F1=inheritedID 与 c2），publish 不重排
	// 自身 cases；原始 v1 的 c1 已退出链路，不得再出现在 active。
	for _, c := range active.Cases {
		if c.ID == "c1" {
			t.Fatalf("original v1 case id must retire from the active chain: %+v", active.Cases)
		}
	}
	gotInherited, gotAdded := false, false
	for _, c := range active.Cases {
		if c.ID == inheritedID {
			gotInherited = true
		}
		if c.ID == "c2" {
			gotAdded = true
		}
	}
	if !gotInherited || !gotAdded {
		t.Fatalf("v2 active must keep the published draft's own case ids: %+v", active.Cases)
	}
	// 后继草稿以全新 id 从 v2 拷贝，不得复用 F1 或 c2。
	for _, c := range nextDraft.Cases {
		if c.ID == inheritedID || c.ID == "c2" {
			t.Fatalf("successor draft must not reuse parent case ids: %+v", nextDraft.Cases)
		}
	}
}

// TestPgSuiteCreateDraftRevisionSeedsFromActiveForLegacy covers legacy suites
// (published before S1-1, draft cleared): CreateDraftRevision seeds a fresh
// draft from the active revision's cases and refuses to create a second draft
// while one exists.
func TestPgSuiteCreateDraftRevisionSeedsFromActiveForLegacy(t *testing.T) {
	pool, ctx, tenantID := newSuiteRepoTestPool(t, "eval_legacy_seed")
	repo := NewPgSuiteRepository(pool)
	suiteID := "suite-legacy"
	suite := domain.EvalSuite{ID: suiteID, Name: "遗留", DraftRevisionID: "rev-l1"}
	revision := domain.EvalSuiteRevision{
		ID: "rev-l1", SuiteID: suiteID, Status: domain.SuiteRevisionDraft,
		ResourceKind: domain.ResourceKindSkill, CreatedBy: "bob",
		Cases:        []domain.EvalCase{{ID: "legacy-case", Name: "遗留用例", Input: "a", ExpectedOutput: "b", AssertionMode: domain.AssertionContains, Enabled: true}},
	}
	if err := repo.CreateSuite(ctx, tenantID, suite, revision); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PublishRevision(ctx, tenantID, suiteID, revision.ID, 1); err != nil {
		t.Fatal(err)
	}
	// S1-1 之后 publish 自动开草稿；模拟 S1-1 之前的遗留态：清空 draft 行与指针。
	auto, _, err := repo.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		fmt.Sprintf(`DELETE FROM "tenant_%s".eval_suite_revisions WHERE id=$1`, tenantID), auto.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		fmt.Sprintf(`UPDATE "tenant_%s".eval_suites SET draft_revision_id=NULL WHERE id=$1`, tenantID), suiteID); err != nil {
		t.Fatal(err)
	}

	draft, err := repo.CreateDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		t.Fatalf("CreateDraftRevision on legacy suite: %v", err)
	}
	if draft.ResourceKind != domain.ResourceKindSkill || draft.ParentID != "rev-l1" || draft.CreatedBy != "bob" {
		t.Fatalf("legacy draft must inherit kind/parent/created_by: %+v", draft)
	}
	if len(draft.Cases) != 1 || draft.Cases[0].ID == "legacy-case" || draft.Cases[0].Name != "遗留用例" {
		t.Fatalf("legacy draft must seed active cases under fresh ids: %+v", draft.Cases)
	}
	// 已有草稿时再建 → 拒绝，保护"至多一个草稿"不变式。
	if _, err := repo.CreateDraftRevision(ctx, tenantID, suiteID); err == nil {
		t.Fatal("expected error creating a draft while one exists")
	}
}

// TestPgSuitePublishGuards covers the idempotency rails around publish: a
// second publish of the same revision fails, and a suite never published
// cannot seed a draft.
func TestPgSuitePublishGuards(t *testing.T) {
	pool, ctx, tenantID := newSuiteRepoTestPool(t, "eval_publish_guards")
	repo := NewPgSuiteRepository(pool)
	suiteID := "suite-guard"
	suite := domain.EvalSuite{ID: suiteID, Name: "护栏", DraftRevisionID: "rev-g1"}
	revision := domain.EvalSuiteRevision{
		ID: "rev-g1", SuiteID: suiteID, Status: domain.SuiteRevisionDraft,
		ResourceKind: domain.ResourceKindAgent,
		Cases:        []domain.EvalCase{{ID: "gc1", Name: "用例", Input: "i", ExpectedOutput: "o", AssertionMode: domain.AssertionExact, Enabled: true}},
	}
	if err := repo.CreateSuite(ctx, tenantID, suite, revision); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PublishRevision(ctx, tenantID, suiteID, revision.ID, 1); err != nil {
		t.Fatal(err)
	}
	// 已发布的 revision 再 publish（旧 revision 二次发布）→ status='draft' 0 行失败。
	if _, err := repo.PublishRevision(ctx, tenantID, suiteID, revision.ID, 2); err == nil {
		t.Fatal("expected error publishing an already-published revision")
	}
	// 未发布套件无 active revision，CreateDraftRevision 必须失败。
	if _, err := repo.CreateDraftRevision(ctx, tenantID, "never-published-suite"); err == nil {
		t.Fatal("expected error seeding draft for never-published suite")
	}
}

func caseByName(t *testing.T, cases []domain.EvalCase, name string) domain.EvalCase {
	t.Helper()
	for _, c := range cases {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no case named %q in %+v", name, cases)
	return domain.EvalCase{}
}

func findJudgeCase(t *testing.T, cases []domain.EvalCase) domain.EvalCase {
	t.Helper()
	for _, c := range cases {
		if c.JudgeSpec != nil {
			return c
		}
	}
	t.Fatalf("no judge case in %+v", cases)
	return domain.EvalCase{}
}

func findSessionCase(t *testing.T, cases []domain.EvalCase) domain.EvalCase {
	t.Helper()
	for _, c := range cases {
		if c.Session != nil {
			return c
		}
	}
	t.Fatalf("no session case in %+v", cases)
	return domain.EvalCase{}
}
