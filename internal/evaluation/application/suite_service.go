package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/google/uuid"
)

var (
	ErrSuiteNameRequired      = errors.New("evaluation suite name required")
	ErrSuiteCasesRequired     = errors.New("evaluation suite requires at least one enabled case")
	ErrSuiteNotFound          = errors.New("evaluation suite not found")
	ErrSuiteCaseInputRequired = errors.New("single-turn evaluation case requires a test input")
	ErrSuiteCaseInvalidScript = errors.New("evaluation session script invalid")
)

// validateCaseShape 校验 draft case 形态结构（阶段 B §5.4 authoring）：会话剧本 case
// （Session 非 nil）必须通过 EvalSessionScript.Validate（至少一轮、每轮 user 非空），
// 让结构错误在 authoring 阶段即拦截，而不是拖到 runCaseSession 执行 preflight 才失败；
// 单轮 case（Session nil）必须有测试输入——input 在请求层不再 binding required，缺
// 字段时 Input==nil 在此拒绝，保持旧契约「单轮 case 缺 input → 400」不变。会话 case
// 的 Input 无执行语义（多轮 runner 只读 Session），允许 nil。错误 wrap 哨兵供统一错误
// 中间件映射 400。
func validateCaseShape(testCase domain.EvalCase) error {
	if testCase.Session == nil {
		if testCase.Input == nil {
			return ErrSuiteCaseInputRequired
		}
		return nil
	}
	if reason, ok := testCase.Session.Validate(); !ok {
		return fmt.Errorf("%w: %s", ErrSuiteCaseInvalidScript, reason)
	}
	return nil
}

type CreateSuiteInput struct {
	Name         string
	Description  string
	ResourceKind domain.ResourceKind
	Cases        []domain.EvalCase
	ActorID      string
}

type SuiteService struct {
	repo port.SuiteRepository
}

func NewSuiteService(repo port.SuiteRepository) *SuiteService {
	return &SuiteService{repo: repo}
}

func (s *SuiteService) Create(ctx context.Context, tenantID string, input CreateSuiteInput) (domain.EvalSuite, domain.EvalSuiteRevision, error) {
	if strings.TrimSpace(input.Name) == "" {
		return domain.EvalSuite{}, domain.EvalSuiteRevision{}, ErrSuiteNameRequired
	}
	hasEnabled := false
	for i := range input.Cases {
		if err := validateCaseShape(input.Cases[i]); err != nil {
			return domain.EvalSuite{}, domain.EvalSuiteRevision{}, err
		}
		if input.Cases[i].ID == "" {
			input.Cases[i].ID = uuid.Must(uuid.NewV7()).String()
		}
		hasEnabled = hasEnabled || input.Cases[i].Enabled
	}
	if !hasEnabled {
		return domain.EvalSuite{}, domain.EvalSuiteRevision{}, ErrSuiteCasesRequired
	}
	suiteID := uuid.Must(uuid.NewV7()).String()
	revisionID := uuid.Must(uuid.NewV7()).String()
	suite := domain.EvalSuite{
		ID: suiteID, Name: input.Name, Description: input.Description, DraftRevisionID: revisionID,
		CreatedBy: input.ActorID,
	}
	revision := domain.EvalSuiteRevision{
		ID: revisionID, SuiteID: suiteID, Status: domain.SuiteRevisionDraft,
		ResourceKind: input.ResourceKind, Cases: input.Cases, CreatedBy: input.ActorID,
	}
	if err := s.repo.CreateSuite(ctx, tenantID, suite, revision); err != nil {
		return domain.EvalSuite{}, domain.EvalSuiteRevision{}, err
	}
	return suite, revision, nil
}

func (s *SuiteService) Publish(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error) {
	revision, ok, err := s.repo.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	if !ok {
		return domain.EvalSuiteRevision{}, ErrSuiteNotFound
	}
	next, err := s.repo.NextVersionNo(ctx, tenantID, suiteID)
	if err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	return s.repo.PublishRevision(ctx, tenantID, suiteID, revision.ID, next)
}

// GetDraft returns the suite's current draft revision (the review queue for
// generated cases) or ErrSuiteNotFound when none exists.
func (s *SuiteService) GetDraft(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error) {
	revision, ok, err := s.repo.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	if !ok {
		return domain.EvalSuiteRevision{}, ErrSuiteNotFound
	}
	return revision, nil
}

// UpdateDraftCase applies an approve (Enabled=true), reject (Enabled=false)
// or edit (full field replacement) to one draft case, then returns the
// updated case read back from the persisted draft.
func (s *SuiteService) UpdateDraftCase(ctx context.Context, tenantID, suiteID, caseID string, testCase domain.EvalCase) (domain.EvalCase, error) {
	revision, ok, err := s.repo.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		return domain.EvalCase{}, err
	}
	if !ok {
		return domain.EvalCase{}, ErrSuiteNotFound
	}
	if err := validateCaseShape(testCase); err != nil {
		return domain.EvalCase{}, err
	}
	if err := s.repo.UpdateDraftCase(ctx, tenantID, revision.ID, testCase); err != nil {
		return domain.EvalCase{}, err
	}
	updated, ok, err := s.repo.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		return domain.EvalCase{}, err
	}
	if !ok {
		return domain.EvalCase{}, ErrSuiteNotFound
	}
	for i := range updated.Cases {
		if updated.Cases[i].ID == caseID {
			return updated.Cases[i], nil
		}
	}
	return domain.EvalCase{}, ErrSuiteNotFound
}

// GetActiveRevision 返回套件当前已发布 revision；套件不存在或从未发布
// 时返回 ErrSuiteNotFound。矩阵评测 seed 用：已有发布基准集直接复用。
func (s *SuiteService) GetActiveRevision(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error) {
	revision, ok, err := s.repo.GetActiveRevision(ctx, tenantID, suiteID)
	if err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	if !ok {
		return domain.EvalSuiteRevision{}, ErrSuiteNotFound
	}
	return revision, nil
}

func (s *SuiteService) GetRevision(ctx context.Context, tenantID, revisionID string) (domain.EvalSuiteRevision, error) {
	revision, ok, err := s.repo.GetRevision(ctx, tenantID, revisionID)
	if err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	if !ok {
		return domain.EvalSuiteRevision{}, ErrSuiteNotFound
	}
	return revision, nil
}

// GetSuiteDetail 返回评测集详情页顶部元信息：套件自身字段叠加当前 active/draft
// revision 的 kind/status/版本号/启用 case 数（从 ListSuiteRevisions 的 meta 聚合，
// 不装载 case 正文）。套件不存在时 ErrSuiteNotFound。无任何 revision 的异常套件
// 返回空 kind/status。
func (s *SuiteService) GetSuiteDetail(ctx context.Context, tenantID, suiteID string) (domain.SuiteDetail, error) {
	suite, ok, err := s.repo.GetSuite(ctx, tenantID, suiteID)
	if err != nil {
		return domain.SuiteDetail{}, err
	}
	if !ok {
		return domain.SuiteDetail{}, ErrSuiteNotFound
	}
	metas, err := s.repo.ListSuiteRevisions(ctx, tenantID, suiteID)
	if err != nil {
		return domain.SuiteDetail{}, err
	}
	detail := domain.SuiteDetail{
		ID: suite.ID, Name: suite.Name, Description: suite.Description,
		ActiveRevisionID: suite.ActiveRevisionID, DraftRevisionID: suite.DraftRevisionID,
		CreatedBy: suite.CreatedBy, CreatedAt: suite.CreatedAt,
	}
	for _, meta := range metas {
		if meta.Status == domain.SuiteRevisionPublished {
			// 已发布版本承载套件的对外 kind/status；草稿与其同源。
			detail.Status = string(meta.Status)
			detail.ResourceKind = meta.ResourceKind
			detail.ActiveVersionNo = meta.VersionNo
			detail.ActiveCaseCount = meta.EnabledCaseCount
			continue
		}
		if detail.ResourceKind == "" {
			detail.ResourceKind = meta.ResourceKind
		}
		if detail.Status == "" {
			detail.Status = string(meta.Status)
		}
		detail.DraftVersionNo = meta.VersionNo
		detail.DraftCaseCount = meta.EnabledCaseCount
	}
	return detail, nil
}

// ListVersions 返回套件全部版本的轻量 meta（不含 cases，版本页只读入口），
// 已发布在前、草稿垫底。套件不存在时 ErrSuiteNotFound。
func (s *SuiteService) ListVersions(ctx context.Context, tenantID, suiteID string) ([]domain.SuiteRevisionMeta, error) {
	if _, ok, err := s.repo.GetSuite(ctx, tenantID, suiteID); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrSuiteNotFound
	}
	return s.repo.ListSuiteRevisions(ctx, tenantID, suiteID)
}

// AddDraftCase appends one hand-authored case to the suite's draft revision.
// 复用 Create 的 authoring 校验（单轮缺 input / 会话剧本结构在 authoring 即拦截，
// 与 UpdateDraftCase 同规则）；空 id 补 uuid。发布态无草稿的 legacy 套件须先经
// StartNextDraft 开启继承草稿，否则 ErrSuiteDraftMissing。
func (s *SuiteService) AddDraftCase(ctx context.Context, tenantID, suiteID string, testCase domain.EvalCase) (domain.EvalCase, error) {
	if err := validateCaseShape(testCase); err != nil {
		return domain.EvalCase{}, err
	}
	if testCase.ID == "" {
		testCase.ID = uuid.Must(uuid.NewV7()).String()
	}
	suite, ok, err := s.repo.GetSuite(ctx, tenantID, suiteID)
	if err != nil {
		return domain.EvalCase{}, err
	}
	if !ok {
		return domain.EvalCase{}, ErrSuiteNotFound
	}
	if suite.DraftRevisionID == "" {
		return domain.EvalCase{}, ErrSuiteDraftMissing
	}
	if err := s.repo.AddDraftCases(ctx, tenantID, suite.DraftRevisionID, []domain.EvalCase{testCase}); err != nil {
		return domain.EvalCase{}, err
	}
	return testCase, nil
}

// DeleteDraftCase removes one case from the suite's draft revision. 草稿的
// case 列表来自当前草稿实体，case 不属于当前草稿时以 ErrSuiteNotFound 拒绝
// （删除对象不存在），避免静默"删了 0 行"。
func (s *SuiteService) DeleteDraftCase(ctx context.Context, tenantID, suiteID, caseID string) error {
	draft, ok, err := s.repo.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		return err
	}
	if !ok {
		// 无草稿：区分套件不存在（404）与发布态 legacy 套件（409 提示先开草稿）。
		if _, found, e := s.repo.GetSuite(ctx, tenantID, suiteID); e != nil {
			return e
		} else if !found {
			return ErrSuiteNotFound
		}
		return ErrSuiteDraftMissing
	}
	for _, testCase := range draft.Cases {
		if testCase.ID == caseID {
			return s.repo.DeleteDraftCase(ctx, tenantID, draft.ID, caseID)
		}
	}
	return ErrSuiteNotFound
}

// StartNextDraft 确保套件存在可编辑草稿：已有草稿幂等返回当前草稿；发布态但
// 无草稿的 legacy 套件从 active revision 开启继承草稿（CreateDraftRevision 内部
// 全量拷贝 cases）。套件不存在 ErrSuiteNotFound；从未发布且无草稿无法继承 →
// ErrSuiteDraftMissing。并发开启时仓库侧以「draft already exists」错误暴露，不静默覆盖。
func (s *SuiteService) StartNextDraft(ctx context.Context, tenantID, suiteID string) (domain.EvalSuiteRevision, error) {
	draft, ok, err := s.repo.GetDraftRevision(ctx, tenantID, suiteID)
	if err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	if ok {
		return draft, nil
	}
	suite, ok, err := s.repo.GetSuite(ctx, tenantID, suiteID)
	if err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	if !ok {
		return domain.EvalSuiteRevision{}, ErrSuiteNotFound
	}
	if suite.ActiveRevisionID == "" {
		return domain.EvalSuiteRevision{}, ErrSuiteDraftMissing
	}
	return s.repo.CreateDraftRevision(ctx, tenantID, suiteID)
}
