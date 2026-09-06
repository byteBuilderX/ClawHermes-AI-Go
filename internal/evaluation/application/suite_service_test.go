package application

import (
	"context"
	"errors"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
)

func TestSuiteServiceCreatesDraftAndPublishesImmutableRevision(t *testing.T) {
	repo := &fakeSuiteRepo{}
	svc := NewSuiteService(repo)

	suite, revision, err := svc.Create(context.Background(), "tenant-1", CreateSuiteInput{
		Name: "投诉分类基线", ResourceKind: domain.ResourceKindSkill,
		Cases: []domain.EvalCase{{Name: "物流", Input: "快递没更新", ExpectedOutput: "物流", AssertionMode: domain.AssertionContains, Enabled: true}},
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if revision.Status != domain.SuiteRevisionDraft || suite.DraftRevisionID != revision.ID || revision.Cases[0].ID == "" {
		t.Fatalf("unexpected draft: suite=%+v revision=%+v", suite, revision)
	}

	published, err := svc.Publish(context.Background(), "tenant-1", suite.ID)
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if published.Status != domain.SuiteRevisionPublished || published.VersionNo != 1 {
		t.Fatalf("unexpected published revision: %+v", published)
	}
}

// TestSuiteServiceCreateCarriesJudgeSpecIntoRevision verifies the judge
// authoring path: a judge case's JudgeSpec set at create time survives the
// service unchanged into the persisted revision (the repository's
// insertEvalCase then writes it into evaluator_config via ToConfig).
func TestSuiteServiceCreateCarriesJudgeSpecIntoRevision(t *testing.T) {
	repo := &fakeSuiteRepo{}
	svc := NewSuiteService(repo)

	suite, revision, err := svc.Create(context.Background(), "tenant-1", CreateSuiteInput{
		Name: "judge 基线", ResourceKind: domain.ResourceKindAgent,
		Cases: []domain.EvalCase{{
			Name: "j1", Input: "帮我总结", ExpectedOutput: "要点",
			AssertionMode: domain.AssertionJudge, Enabled: true,
			JudgeSpec: &domain.JudgeSpec{Model: "judge-v1", Rubric: "总结要点覆盖度"},
		}},
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if revision.Cases[0].AssertionMode != domain.AssertionJudge {
		t.Fatalf("assertion mode=%q, want judge", revision.Cases[0].AssertionMode)
	}
	if got := revision.Cases[0].JudgeSpec; got == nil || got.Model != "judge-v1" || got.Rubric != "总结要点覆盖度" {
		t.Fatalf("judge spec lost through Create: %+v", got)
	}
	// ToConfig must be non-nil so insertEvalCase persists the spec.
	if cfg := revision.Cases[0].ToConfig(); cfg == nil || cfg.JudgeSpec == nil || cfg.JudgeSpec.Model != "judge-v1" {
		t.Fatalf("ToConfig does not carry judge spec: %+v", cfg)
	}
	if suite.ID == "" || revision.Cases[0].ID == "" {
		t.Fatalf("expected generated IDs, suite=%+v revision=%+v", suite, revision)
	}
}

type fakeSuiteRepo struct {
	suite    domain.EvalSuite
	revision domain.EvalSuiteRevision
}

func (f *fakeSuiteRepo) CreateSuite(_ context.Context, _ string, suite domain.EvalSuite, revision domain.EvalSuiteRevision) error {
	f.suite, f.revision = suite, revision
	return nil
}

func (f *fakeSuiteRepo) GetDraftRevision(_ context.Context, _ string, suiteID string) (domain.EvalSuiteRevision, bool, error) {
	return f.revision, f.revision.SuiteID == suiteID && f.revision.Status == domain.SuiteRevisionDraft, nil
}

func (f *fakeSuiteRepo) PublishRevision(_ context.Context, _ string, suiteID, revisionID string, versionNo int) (domain.EvalSuiteRevision, error) {
	f.revision.Status = domain.SuiteRevisionPublished
	f.revision.VersionNo = versionNo
	f.suite.ActiveRevisionID = revisionID
	f.suite.DraftRevisionID = ""
	return f.revision, nil
}

func (f *fakeSuiteRepo) NextVersionNo(_ context.Context, _ string, _ string) (int, error) {
	return 1, nil
}

func (f *fakeSuiteRepo) GetRevision(_ context.Context, _ string, revisionID string) (domain.EvalSuiteRevision, bool, error) {
	return f.revision, f.revision.ID == revisionID, nil
}

func (f *fakeSuiteRepo) GetActiveRevision(_ context.Context, _ string, suiteID string) (domain.EvalSuiteRevision, bool, error) {
	return f.revision,
		f.suite.ID == suiteID && f.suite.ActiveRevisionID == f.revision.ID && f.revision.Status == domain.SuiteRevisionPublished,
		nil
}

func (f *fakeSuiteRepo) GetSuite(_ context.Context, _ string, suiteID string) (domain.EvalSuite, bool, error) {
	if f.suite.ID != suiteID {
		return domain.EvalSuite{}, false, nil
	}
	return f.suite, true, nil
}

func (f *fakeSuiteRepo) ListSuiteRevisions(_ context.Context, _ string, _ string) ([]domain.SuiteRevisionMeta, error) {
	meta := domain.SuiteRevisionMeta{
		ID: f.revision.ID, VersionNo: f.revision.VersionNo, Status: f.revision.Status,
		ResourceKind: f.revision.ResourceKind, CreatedBy: f.revision.CreatedBy,
	}
	for _, c := range f.revision.Cases {
		if c.Enabled {
			meta.EnabledCaseCount++
		}
	}
	if meta.ID == "" {
		return nil, nil
	}
	return []domain.SuiteRevisionMeta{meta}, nil
}

// The four draft-management methods exist so the fake satisfies
// port.SuiteRepository; UpdateDraftCase mutates the fake's revision to
// exercise UpdateDraftCase on the service.
func (f *fakeSuiteRepo) CreateDraftRevision(_ context.Context, _ string, _ string) (domain.EvalSuiteRevision, error) {
	return domain.EvalSuiteRevision{}, nil
}

func (f *fakeSuiteRepo) AddDraftCases(_ context.Context, _ string, _ string, _ []domain.EvalCase) error {
	return nil
}

func (f *fakeSuiteRepo) UpdateDraftCase(_ context.Context, _ string, _ string, testCase domain.EvalCase) error {
	for i := range f.revision.Cases {
		if f.revision.Cases[i].ID == testCase.ID {
			f.revision.Cases[i] = testCase
			return nil
		}
	}
	// Mirrors the real repository: an UPDATE that matches no row is an error.
	return errors.New("draft case not found")
}

func (f *fakeSuiteRepo) DeleteDraftCase(_ context.Context, _ string, _ string, _ string) error {
	return nil
}

func TestSuiteServiceGetDraftAndUpdateDraftCase(t *testing.T) {
	repo := &fakeSuiteRepo{}
	svc := NewSuiteService(repo)
	suite, _, err := svc.Create(context.Background(), "tenant-1", CreateSuiteInput{
		Name: "投诉分类", ResourceKind: domain.ResourceKindSkill,
		Cases: []domain.EvalCase{
			{Name: "物流", Input: "快递没更新", ExpectedOutput: "物流", AssertionMode: domain.AssertionContains, Enabled: true},
			{Name: "退款", Input: "要退款", ExpectedOutput: "退款", AssertionMode: domain.AssertionContains, Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	ctx := context.Background()

	draft, err := svc.GetDraft(ctx, "tenant-1", suite.ID)
	if err != nil {
		t.Fatalf("GetDraft returned error: %v", err)
	}
	if len(draft.Cases) != 2 || draft.Status != domain.SuiteRevisionDraft {
		t.Fatalf("unexpected draft: %+v", draft)
	}

	// Edit: full field replacement.
	edited, err := svc.UpdateDraftCase(ctx, "tenant-1", suite.ID, draft.Cases[0].ID, domain.EvalCase{
		ID: draft.Cases[0].ID, Name: "物流改", Input: "物流进度查询", ExpectedOutput: "物流查询",
		AssertionMode: domain.AssertionExact, Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpdateDraftCase edit returned error: %v", err)
	}
	if edited.Name != "物流改" || edited.AssertionMode != domain.AssertionExact || edited.Input != "物流进度查询" {
		t.Fatalf("edited case not persisted: %+v", edited)
	}

	// Reject: enabled=false keeps the case in the draft for later approval.
	rejected, err := svc.UpdateDraftCase(ctx, "tenant-1", suite.ID, draft.Cases[0].ID, domain.EvalCase{
		ID: draft.Cases[0].ID, Name: "物流改", Input: "物流进度查询", ExpectedOutput: "物流查询",
		AssertionMode: domain.AssertionExact, Enabled: false,
	})
	if err != nil {
		t.Fatalf("UpdateDraftCase reject returned error: %v", err)
	}
	if rejected.Enabled {
		t.Fatalf("expected rejected case to be disabled: %+v", rejected)
	}

	// Unknown case: error propagates from the repository path.
	if _, err := svc.UpdateDraftCase(ctx, "tenant-1", suite.ID, "missing", domain.EvalCase{
		ID: "missing", Name: "x", Input: "x", ExpectedOutput: "x", AssertionMode: domain.AssertionExact, Enabled: true,
	}); err == nil {
		t.Fatal("expected error for unknown draft case")
	}
}

func TestSuiteServiceGetDraftNotFound(t *testing.T) {
	svc := NewSuiteService(&fakeSuiteRepo{})
	if _, err := svc.GetDraft(context.Background(), "tenant-1", "missing"); !errors.Is(err, ErrSuiteNotFound) {
		t.Fatalf("expected ErrSuiteNotFound, got %v", err)
	}
}

func TestSuiteServiceGetActiveRevision(t *testing.T) {
	repo := &fakeSuiteRepo{}
	svc := NewSuiteService(repo)

	t.Run("published suite returns active revision", func(t *testing.T) {
		suite, revision, err := svc.Create(context.Background(), "tenant-1", CreateSuiteInput{
			Name: "技能基准集", ResourceKind: domain.ResourceKindSkill,
			Cases: []domain.EvalCase{{Name: "抽取", Input: "x", AssertionMode: domain.AssertionJudge, Enabled: true}},
		})
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		if _, err := svc.Publish(context.Background(), "tenant-1", suite.ID); err != nil {
			t.Fatalf("Publish returned error: %v", err)
		}
		active, err := svc.GetActiveRevision(context.Background(), "tenant-1", suite.ID)
		if err != nil {
			t.Fatalf("GetActiveRevision returned error: %v", err)
		}
		if active.ID != revision.ID || active.Status != domain.SuiteRevisionPublished {
			t.Fatalf("expected published active revision %s, got %+v", revision.ID, active)
		}
	})

	t.Run("unpublished suite returns ErrSuiteNotFound", func(t *testing.T) {
		svc := NewSuiteService(&fakeSuiteRepo{})
		if _, err := svc.GetActiveRevision(context.Background(), "tenant-1", "missing"); !errors.Is(err, ErrSuiteNotFound) {
			t.Fatalf("expected ErrSuiteNotFound, got %v", err)
		}
	})
}

// sessionScriptFixture 构造一个合法的双轮会话剧本 case（阶段 B §5.4 authoring
// round-trip 用）：首轮纯 user 消息、末轮带工具过程断言。
func sessionScriptFixture() domain.EvalCase {
	return domain.EvalCase{
		Name: "会话投诉", ExpectedOutput: "已给用户可执行处理", AssertionMode: domain.AssertionContains,
		Enabled: true,
		Session: &domain.EvalSessionScript{
			Goal: "用户投诉快递未收到：定位物流状态并给出签收异常处理",
			Turns: []domain.SessionTurn{
				{User: "快递一直没到，帮我看看", Probe: "识别物流查询意图"},
				{User: "物流显示已签收但我没收到", Probe: "进入签收异常处理",
					ToolSpec: &domain.ToolSpec{MustCall: []string{"track_package"}, MaxCalls: 2}},
			},
		},
	}
}

func TestSuiteServiceCreateCarriesSessionScriptIntoRevision(t *testing.T) {
	repo := &fakeSuiteRepo{}
	svc := NewSuiteService(repo)

	suite, revision, err := svc.Create(context.Background(), "tenant-1", CreateSuiteInput{
		Name: "会话基线", ResourceKind: domain.ResourceKindAgent,
		Cases: []domain.EvalCase{sessionScriptFixture()},
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if suite.ID == "" || revision.Cases[0].ID == "" {
		t.Fatalf("expected generated IDs, suite=%+v revision=%+v", suite, revision)
	}
	got := revision.Cases[0].Session
	if got == nil {
		t.Fatal("session script lost through Create")
	}
	if got.Goal != "用户投诉快递未收到：定位物流状态并给出签收异常处理" || len(got.Turns) != 2 {
		t.Fatalf("session script not preserved verbatim: %+v", got)
	}
	if got.Turns[1].ToolSpec == nil || len(got.Turns[1].ToolSpec.MustCall) != 1 ||
		got.Turns[1].ToolSpec.MustCall[0] != "track_package" || got.Turns[1].ToolSpec.MaxCalls != 2 {
		t.Fatalf("per-turn tool spec not preserved: %+v", got.Turns[1])
	}
	// 会话 case 的 input 无执行语义，Create 应放行（服务层不要求单轮输入）。
	if !revision.Cases[0].IsSession() {
		t.Fatal("expected case to report IsSession()=true")
	}
}

func TestSuiteServiceRejectsInvalidSessionScript(t *testing.T) {
	repo := &fakeSuiteRepo{}
	svc := NewSuiteService(repo)

	tests := []struct {
		name string
		wrap func() error
	}{
		{name: "zero turns",
			wrap: func() error {
				_, _, err := svc.Create(context.Background(), "tenant-1", CreateSuiteInput{
					Name: "x", ResourceKind: domain.ResourceKindAgent,
					Cases: []domain.EvalCase{{Name: "c", ExpectedOutput: "e", AssertionMode: domain.AssertionContains,
						Enabled: true, Session: &domain.EvalSessionScript{Goal: "g", Turns: nil}}},
				})
				return err
			}},
		{name: "empty turn user",
			wrap: func() error {
				_, _, err := svc.Create(context.Background(), "tenant-1", CreateSuiteInput{
					Name: "x", ResourceKind: domain.ResourceKindAgent,
					Cases: []domain.EvalCase{{Name: "c", ExpectedOutput: "e", AssertionMode: domain.AssertionContains,
						Enabled: true, Session: &domain.EvalSessionScript{Goal: "g",
							Turns: []domain.SessionTurn{{User: "  "}}}}},
				})
				return err
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.wrap(); !errors.Is(err, ErrSuiteCaseInvalidScript) {
				t.Fatalf("expected ErrSuiteCaseInvalidScript, got %v", err)
			}
		})
	}
}

func TestSuiteServiceRejectsSingleTurnCaseWithoutInput(t *testing.T) {
	svc := NewSuiteService(&fakeSuiteRepo{})

	// Create：单轮 case（session nil）缺 input → 400 哨兵。
	if _, _, err := svc.Create(context.Background(), "tenant-1", CreateSuiteInput{
		Name: "x", ResourceKind: domain.ResourceKindSkill,
		Cases: []domain.EvalCase{{Name: "c", ExpectedOutput: "e", AssertionMode: domain.AssertionExact, Enabled: true}},
	}); !errors.Is(err, ErrSuiteCaseInputRequired) {
		t.Fatalf("expected ErrSuiteCaseInputRequired on Create, got %v", err)
	}

	// UpdateDraftCase 同规则：编辑不能把单轮 case 的 input 清空。
	repo := &fakeSuiteRepo{}
	createSvc := NewSuiteService(repo)
	_, revision, err := createSvc.Create(context.Background(), "tenant-1", CreateSuiteInput{
		Name: "x", ResourceKind: domain.ResourceKindSkill,
		Cases: []domain.EvalCase{{Name: "c", Input: "q", ExpectedOutput: "e", AssertionMode: domain.AssertionExact, Enabled: true}},
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	svc = NewSuiteService(repo)
	if _, err := svc.UpdateDraftCase(context.Background(), "tenant-1", revision.SuiteID, revision.Cases[0].ID, domain.EvalCase{
		ID: revision.Cases[0].ID, Name: "c", ExpectedOutput: "e",
		AssertionMode: domain.AssertionExact, Enabled: true,
	}); !errors.Is(err, ErrSuiteCaseInputRequired) {
		t.Fatalf("expected ErrSuiteCaseInputRequired on UpdateDraftCase, got %v", err)
	}
}

func TestSuiteServiceUpdateDraftCaseSessionRoundTrip(t *testing.T) {
	repo := &fakeSuiteRepo{}
	svc := NewSuiteService(repo)
	_, revision, err := svc.Create(context.Background(), "tenant-1", CreateSuiteInput{
		Name: "会话基线", ResourceKind: domain.ResourceKindAgent,
		Cases: []domain.EvalCase{sessionScriptFixture()},
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	caseID := revision.Cases[0].ID

	// 编辑会话剧本：goal 与轮次内容可随 draft case full replacement 更新。
	edited, err := svc.UpdateDraftCase(context.Background(), "tenant-1", revision.SuiteID, caseID, domain.EvalCase{
		ID: caseID, Name: "会话投诉改", ExpectedOutput: "已给用户可执行处理",
		AssertionMode: domain.AssertionContains, Enabled: true,
		Session: &domain.EvalSessionScript{
			Goal: "快递签收异常：先核实再给处理",
			Turns: []domain.SessionTurn{
				{User: "签收异常怎么处理", Probe: "进入异常处理"},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateDraftCase returned error: %v", err)
	}
	if edited.Session == nil || edited.Session.Goal != "快递签收异常：先核实再给处理" || len(edited.Session.Turns) != 1 {
		t.Fatalf("session edit not persisted: %+v", edited.Session)
	}

	// 清空 session（会话 case 转回单轮）：nil Session 被接受，必须补 input。
	reverted, err := svc.UpdateDraftCase(context.Background(), "tenant-1", revision.SuiteID, caseID, domain.EvalCase{
		ID: caseID, Name: "会话投诉改", Input: "签收异常怎么处理", ExpectedOutput: "已给用户可执行处理",
		AssertionMode: domain.AssertionContains, Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpdateDraftCase reverted to single-turn returned error: %v", err)
	}
	if reverted.IsSession() {
		t.Fatalf("expected session cleared, got %+v", reverted.Session)
	}
}

// ---- S1-3 suite 详情/版本/草稿编辑命令 ----

// suiteMgmtRepo 是 S1-3 草稿/详情命令单测的录制型 repo 桩，显式建模
// suite + published + draft 三态（对应 S1-1 之后仓库不变量：publish 自动开
// 继承草稿、legacy 套件经 CreateDraftRevision 补建）。原 fakeSuiteRepo 以单
// 个 revision 模拟流程，表达不了「已发布与继承草稿并存」，故独立成桩。
type suiteMgmtRepo struct {
	suite            domain.EvalSuite
	published        *domain.EvalSuiteRevision // active 已发布 revision；legacy 从未发布为 nil
	draft            *domain.EvalSuiteRevision // 当前草稿
	createDraftCalls int
	addedCases       []domain.EvalCase
	deletedCaseID    string
	deleteDraftErr   error
}

func (r *suiteMgmtRepo) CreateSuite(_ context.Context, _ string, suite domain.EvalSuite, revision domain.EvalSuiteRevision) error {
	r.suite = suite
	d := revision
	r.draft = &d
	return nil
}

func (r *suiteMgmtRepo) GetSuite(_ context.Context, _ string, suiteID string) (domain.EvalSuite, bool, error) {
	if r.suite.ID == "" || r.suite.ID != suiteID {
		return domain.EvalSuite{}, false, nil
	}
	return r.suite, true, nil
}

func (r *suiteMgmtRepo) GetDraftRevision(_ context.Context, _ string, _ string) (domain.EvalSuiteRevision, bool, error) {
	if r.draft == nil {
		return domain.EvalSuiteRevision{}, false, nil
	}
	return *r.draft, true, nil
}

func (r *suiteMgmtRepo) GetActiveRevision(_ context.Context, _ string, _ string) (domain.EvalSuiteRevision, bool, error) {
	if r.published == nil {
		return domain.EvalSuiteRevision{}, false, nil
	}
	return *r.published, true, nil
}

func (r *suiteMgmtRepo) GetRevision(_ context.Context, _ string, revisionID string) (domain.EvalSuiteRevision, bool, error) {
	if r.published != nil && r.published.ID == revisionID {
		return *r.published, true, nil
	}
	if r.draft != nil && r.draft.ID == revisionID {
		return *r.draft, true, nil
	}
	return domain.EvalSuiteRevision{}, false, nil
}

func (r *suiteMgmtRepo) NextVersionNo(_ context.Context, _ string, _ string) (int, error) {
	return 1, nil
}

func (r *suiteMgmtRepo) ListSuiteRevisions(_ context.Context, _ string, _ string) ([]domain.SuiteRevisionMeta, error) {
	var metas []domain.SuiteRevisionMeta
	if r.published != nil {
		metas = append(metas, suiteRevisionMetaOf(r.published))
	}
	if r.draft != nil {
		metas = append(metas, suiteRevisionMetaOf(r.draft))
	}
	return metas, nil
}

func suiteRevisionMetaOf(rev *domain.EvalSuiteRevision) domain.SuiteRevisionMeta {
	meta := domain.SuiteRevisionMeta{ID: rev.ID, VersionNo: rev.VersionNo, Status: rev.Status,
		ResourceKind: rev.ResourceKind, CreatedBy: rev.CreatedBy}
	for _, tc := range rev.Cases {
		if tc.Enabled {
			meta.EnabledCaseCount++
		}
	}
	return meta
}

// PublishRevision 忠实 S1-1：发布当前草稿并开启继承草稿（published 承载对外
// kind/status，draft 继承 cases 但 version_no 未分配）。
func (r *suiteMgmtRepo) PublishRevision(_ context.Context, _ string, suiteID, _ string, versionNo int) (domain.EvalSuiteRevision, error) {
	if r.draft == nil {
		return domain.EvalSuiteRevision{}, errors.New("no draft to publish")
	}
	published := *r.draft
	published.Status = domain.SuiteRevisionPublished
	published.VersionNo = versionNo
	r.published = &published
	child := domain.EvalSuiteRevision{ID: "draft-next", SuiteID: suiteID, ParentID: published.ID,
		Status: domain.SuiteRevisionDraft, ResourceKind: published.ResourceKind, CreatedBy: published.CreatedBy}
	for _, tc := range published.Cases {
		tc.ID = "child-" + tc.ID
		child.Cases = append(child.Cases, tc)
	}
	r.draft = &child
	r.suite.ActiveRevisionID = published.ID
	r.suite.DraftRevisionID = child.ID
	return published, nil
}

// CreateDraftRevision 供 legacy 补建：镜像仓库护栏（已有草稿/无 active 均失败），
// 从 active revision 全量拷贝 cases。
func (r *suiteMgmtRepo) CreateDraftRevision(_ context.Context, _ string, suiteID string) (domain.EvalSuiteRevision, error) {
	r.createDraftCalls++
	if r.draft != nil {
		return domain.EvalSuiteRevision{}, errors.New("draft already exists")
	}
	if r.published == nil {
		return domain.EvalSuiteRevision{}, errors.New("no active revision to seed draft")
	}
	child := domain.EvalSuiteRevision{ID: "draft-from-active", SuiteID: suiteID, ParentID: r.published.ID,
		Status: domain.SuiteRevisionDraft, ResourceKind: r.published.ResourceKind, CreatedBy: r.published.CreatedBy}
	for _, tc := range r.published.Cases {
		tc.ID = "child-" + tc.ID
		child.Cases = append(child.Cases, tc)
	}
	r.draft = &child
	r.suite.DraftRevisionID = child.ID
	return child, nil
}

func (r *suiteMgmtRepo) AddDraftCases(_ context.Context, _ string, _ string, cases []domain.EvalCase) error {
	r.addedCases = append(r.addedCases, cases...)
	return nil
}

func (r *suiteMgmtRepo) UpdateDraftCase(_ context.Context, _ string, _ string, testCase domain.EvalCase) error {
	if r.draft == nil {
		return errors.New("no draft")
	}
	for i := range r.draft.Cases {
		if r.draft.Cases[i].ID == testCase.ID {
			r.draft.Cases[i] = testCase
			return nil
		}
	}
	return errors.New("draft case not found")
}

func (r *suiteMgmtRepo) DeleteDraftCase(_ context.Context, _ string, _ string, caseID string) error {
	r.deletedCaseID = caseID
	if r.deleteDraftErr != nil {
		return r.deleteDraftErr
	}
	if r.draft != nil {
		for _, tc := range r.draft.Cases {
			if tc.ID == caseID {
				return nil
			}
		}
	}
	return errors.New("draft case not found")
}

// publishedSkillSuiteRepo 构造一个已发布 skill 套件 + 继承草稿的桩状态。
func publishedSkillSuiteRepo() *suiteMgmtRepo {
	repo := &suiteMgmtRepo{
		suite: domain.EvalSuite{ID: "s1", Name: "投诉分类", Description: "d",
			ActiveRevisionID: "r-pub", DraftRevisionID: "r-draft", CreatedBy: "alice"},
		published: &domain.EvalSuiteRevision{ID: "r-pub", SuiteID: "s1", VersionNo: 3,
			Status: domain.SuiteRevisionPublished, ResourceKind: domain.ResourceKindSkill, CreatedBy: "alice",
			Cases: []domain.EvalCase{
				{ID: "c1", Name: "启用", Enabled: true},
				{ID: "c2", Name: "停用", Enabled: false},
			}},
		draft: &domain.EvalSuiteRevision{ID: "r-draft", SuiteID: "s1", VersionNo: 0,
			Status: domain.SuiteRevisionDraft, ResourceKind: domain.ResourceKindSkill, CreatedBy: "alice",
			Cases: []domain.EvalCase{{ID: "c3", Name: "草稿", Enabled: true}}},
	}
	return repo
}

func TestSuiteServiceGetSuiteDetailAggregatesVersions(t *testing.T) {
	svc := NewSuiteService(publishedSkillSuiteRepo())

	detail, err := svc.GetSuiteDetail(context.Background(), "t1", "s1")
	if err != nil {
		t.Fatalf("GetSuiteDetail returned error: %v", err)
	}
	if detail.Name != "投诉分类" || detail.ID != "s1" || detail.Description != "d" || detail.CreatedBy != "alice" {
		t.Fatalf("base fields not carried: %+v", detail)
	}
	// 对外 kind/status 与版本号/启用 case 数取自已发布版本。
	if detail.Status != "published" || detail.ResourceKind != domain.ResourceKindSkill ||
		detail.ActiveVersionNo != 3 || detail.ActiveCaseCount != 1 {
		t.Fatalf("published aggregation wrong: %+v", detail)
	}
	if detail.ActiveRevisionID != "r-pub" || detail.DraftRevisionID != "r-draft" ||
		detail.DraftVersionNo != 0 || detail.DraftCaseCount != 1 {
		t.Fatalf("draft aggregation wrong: %+v", detail)
	}
}

func TestSuiteServiceGetSuiteDetailMissing(t *testing.T) {
	svc := NewSuiteService(&suiteMgmtRepo{})
	if _, err := svc.GetSuiteDetail(context.Background(), "t1", "missing"); !errors.Is(err, ErrSuiteNotFound) {
		t.Fatalf("expected ErrSuiteNotFound, got %v", err)
	}
}

func TestSuiteServiceListVersionsOrderedPublishedFirst(t *testing.T) {
	svc := NewSuiteService(publishedSkillSuiteRepo())

	metas, err := svc.ListVersions(context.Background(), "t1", "s1")
	if err != nil {
		t.Fatalf("ListVersions returned error: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("expected 2 metas, got %d", len(metas))
	}
	if metas[0].ID != "r-pub" || metas[0].VersionNo != 3 || metas[0].Status != domain.SuiteRevisionPublished ||
		metas[0].EnabledCaseCount != 1 {
		t.Fatalf("published meta wrong: %+v", metas[0])
	}
	if metas[1].ID != "r-draft" || metas[1].Status != domain.SuiteRevisionDraft || metas[1].EnabledCaseCount != 1 {
		t.Fatalf("draft meta wrong: %+v", metas[1])
	}

	if _, err := svc.ListVersions(context.Background(), "t1", "missing"); !errors.Is(err, ErrSuiteNotFound) {
		t.Fatalf("expected ErrSuiteNotFound for missing suite, got %v", err)
	}
}

func TestSuiteServiceStartNextDraftIdempotentWithExistingDraft(t *testing.T) {
	repo := publishedSkillSuiteRepo()
	svc := NewSuiteService(repo)

	draft, err := svc.StartNextDraft(context.Background(), "t1", "s1")
	if err != nil {
		t.Fatalf("StartNextDraft returned error: %v", err)
	}
	if draft.ID != "r-draft" {
		t.Fatalf("expected existing draft returned, got %+v", draft)
	}
	if repo.createDraftCalls != 0 {
		t.Fatalf("idempotent start must not call CreateDraftRevision, got %d calls", repo.createDraftCalls)
	}
}

func TestSuiteServiceStartNextDraftSeedsFromActive(t *testing.T) {
	// legacy 套件：已发布、无草稿。
	repo := &suiteMgmtRepo{
		suite: domain.EvalSuite{ID: "s1", Name: "legacy", ActiveRevisionID: "r-pub", CreatedBy: "bob"},
		published: &domain.EvalSuiteRevision{ID: "r-pub", SuiteID: "s1", VersionNo: 2,
			Status: domain.SuiteRevisionPublished, ResourceKind: domain.ResourceKindAgent, CreatedBy: "bob",
			Cases: []domain.EvalCase{{ID: "c1", Name: "旧", Input: "q", ExpectedOutput: "a", Enabled: true}}},
	}
	svc := NewSuiteService(repo)

	draft, err := svc.StartNextDraft(context.Background(), "t1", "s1")
	if err != nil {
		t.Fatalf("StartNextDraft returned error: %v", err)
	}
	if repo.createDraftCalls != 1 {
		t.Fatalf("expected one CreateDraftRevision call, got %d", repo.createDraftCalls)
	}
	if draft.ID == "r-pub" || draft.Status != domain.SuiteRevisionDraft || draft.ParentID != "r-pub" {
		t.Fatalf("inherited draft wrong: %+v", draft)
	}
	if draft.ResourceKind != domain.ResourceKindAgent || len(draft.Cases) != 1 {
		t.Fatalf("draft must inherit kind and copy active cases: %+v", draft)
	}
	// 再次调用幂等：复用已建草稿。
	if _, err := svc.StartNextDraft(context.Background(), "t1", "s1"); err != nil {
		t.Fatalf("second StartNextDraft returned error: %v", err)
	}
	if repo.createDraftCalls != 1 {
		t.Fatalf("second start must be idempotent, got %d calls", repo.createDraftCalls)
	}
}

func TestSuiteServiceStartNextDraftErrors(t *testing.T) {
	t.Run("never published suite cannot inherit", func(t *testing.T) {
		repo := &suiteMgmtRepo{suite: domain.EvalSuite{ID: "s1", Name: "未发布"}}
		svc := NewSuiteService(repo)
		if _, err := svc.StartNextDraft(context.Background(), "t1", "s1"); !errors.Is(err, ErrSuiteDraftMissing) {
			t.Fatalf("expected ErrSuiteDraftMissing, got %v", err)
		}
	})
	t.Run("missing suite", func(t *testing.T) {
		svc := NewSuiteService(&suiteMgmtRepo{})
		if _, err := svc.StartNextDraft(context.Background(), "t1", "missing"); !errors.Is(err, ErrSuiteNotFound) {
			t.Fatalf("expected ErrSuiteNotFound, got %v", err)
		}
	})
}

func TestSuiteServiceAddDraftCaseErrors(t *testing.T) {
	t.Run("single-turn missing input rejected before repo", func(t *testing.T) {
		repo := publishedSkillSuiteRepo()
		svc := NewSuiteService(repo)
		if _, err := svc.AddDraftCase(context.Background(), "t1", "s1", domain.EvalCase{
			Name: "x", ExpectedOutput: "e", AssertionMode: domain.AssertionContains, Enabled: true,
		}); !errors.Is(err, ErrSuiteCaseInputRequired) {
			t.Fatalf("expected ErrSuiteCaseInputRequired, got %v", err)
		}
		if len(repo.addedCases) != 0 {
			t.Fatalf("invalid case must not reach repo, got %d", len(repo.addedCases))
		}
	})
	t.Run("legacy published suite without draft", func(t *testing.T) {
		repo := &suiteMgmtRepo{
			suite:     domain.EvalSuite{ID: "s1", ActiveRevisionID: "r-pub"},
			published: &domain.EvalSuiteRevision{ID: "r-pub", SuiteID: "s1", VersionNo: 1, Status: domain.SuiteRevisionPublished},
		}
		svc := NewSuiteService(repo)
		if _, err := svc.AddDraftCase(context.Background(), "t1", "s1", domain.EvalCase{
			Name: "x", Input: "q", ExpectedOutput: "e", AssertionMode: domain.AssertionContains, Enabled: true,
		}); !errors.Is(err, ErrSuiteDraftMissing) {
			t.Fatalf("expected ErrSuiteDraftMissing, got %v", err)
		}
	})
	t.Run("missing suite", func(t *testing.T) {
		svc := NewSuiteService(&suiteMgmtRepo{})
		if _, err := svc.AddDraftCase(context.Background(), "t1", "missing", domain.EvalCase{
			Name: "x", Input: "q", ExpectedOutput: "e", AssertionMode: domain.AssertionContains, Enabled: true,
		}); !errors.Is(err, ErrSuiteNotFound) {
			t.Fatalf("expected ErrSuiteNotFound, got %v", err)
		}
	})
}

func TestSuiteServiceAddDraftCaseAppendsToRepo(t *testing.T) {
	repo := publishedSkillSuiteRepo()
	svc := NewSuiteService(repo)

	added, err := svc.AddDraftCase(context.Background(), "t1", "s1", domain.EvalCase{
		Name: "新加", Input: "查单", ExpectedOutput: "物流查询", AssertionMode: domain.AssertionContains, Enabled: true,
	})
	if err != nil {
		t.Fatalf("AddDraftCase returned error: %v", err)
	}
	if added.ID == "" {
		t.Fatal("service must assign a case id when none provided")
	}
	if len(repo.addedCases) != 1 || repo.addedCases[0].Name != "新加" || repo.addedCases[0].ID != added.ID {
		t.Fatalf("case not appended to draft: %+v", repo.addedCases)
	}
}

func TestSuiteServiceDeleteDraftCase(t *testing.T) {
	t.Run("removes case present in draft", func(t *testing.T) {
		repo := publishedSkillSuiteRepo()
		svc := NewSuiteService(repo)
		if err := svc.DeleteDraftCase(context.Background(), "t1", "s1", "c3"); err != nil {
			t.Fatalf("DeleteDraftCase returned error: %v", err)
		}
		if repo.deletedCaseID != "c3" {
			t.Fatalf("delete not delegated to repo, recorded=%q", repo.deletedCaseID)
		}
	})
	t.Run("case not in current draft rejected", func(t *testing.T) {
		repo := publishedSkillSuiteRepo()
		svc := NewSuiteService(repo)
		if err := svc.DeleteDraftCase(context.Background(), "t1", "s1", "c1"); !errors.Is(err, ErrSuiteNotFound) {
			t.Fatalf("expected ErrSuiteNotFound for published-only case, got %v", err)
		}
		if repo.deletedCaseID != "" {
			t.Fatalf("repo delete must not be reached for foreign case, got %q", repo.deletedCaseID)
		}
	})
	t.Run("legacy published suite without draft", func(t *testing.T) {
		repo := &suiteMgmtRepo{
			suite:     domain.EvalSuite{ID: "s1", ActiveRevisionID: "r-pub"},
			published: &domain.EvalSuiteRevision{ID: "r-pub", SuiteID: "s1", VersionNo: 1, Status: domain.SuiteRevisionPublished},
		}
		svc := NewSuiteService(repo)
		if err := svc.DeleteDraftCase(context.Background(), "t1", "s1", "c1"); !errors.Is(err, ErrSuiteDraftMissing) {
			t.Fatalf("expected ErrSuiteDraftMissing, got %v", err)
		}
	})
	t.Run("missing suite", func(t *testing.T) {
		svc := NewSuiteService(&suiteMgmtRepo{})
		if err := svc.DeleteDraftCase(context.Background(), "t1", "missing", "c1"); !errors.Is(err, ErrSuiteNotFound) {
			t.Fatalf("expected ErrSuiteNotFound, got %v", err)
		}
	})
}

func TestSuiteServiceDeleteDraftCaseRepoErrorPropagates(t *testing.T) {
	repo := publishedSkillSuiteRepo()
	repo.deleteDraftErr = errors.New("persist failed")
	svc := NewSuiteService(repo)
	if err := svc.DeleteDraftCase(context.Background(), "t1", "s1", "c3"); !errors.Is(err, repo.deleteDraftErr) {
		t.Fatalf("expected repo error to propagate, got %v", err)
	}
}
