package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgSuiteRepository struct {
	pool poolIface
}

func NewPgSuiteRepository(pool *pgxpool.Pool) *PgSuiteRepository {
	return &PgSuiteRepository{pool: pool}
}

func (r *PgSuiteRepository) CreateSuite(
	ctx context.Context,
	tenantID string,
	suite domain.EvalSuite,
	revision domain.EvalSuiteRevision,
) error {
	return r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO eval_suites (id, name, description, draft_revision_id, created_by) VALUES ($1,$2,$3,$4,$5)`,
			suite.ID, suite.Name, suite.Description, revision.ID, suite.CreatedBy,
		); err != nil {
			return fmt.Errorf("evaluation suite repository: insert suite: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO eval_suite_revisions (id, suite_id, parent_id, version_no, status, resource_kind, created_by)
			 VALUES ($1,$2,NULLIF($3,''),NULLIF($4,0),$5,$6,$7)`,
			revision.ID, revision.SuiteID, revision.ParentID, revision.VersionNo,
			string(revision.Status), string(revision.ResourceKind), revision.CreatedBy,
		); err != nil {
			return fmt.Errorf("evaluation suite repository: insert revision: %w", err)
		}
		for _, testCase := range revision.Cases {
			if err := insertEvalCase(ctx, tx, revision.ID, testCase); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *PgSuiteRepository) GetDraftRevision(
	ctx context.Context,
	tenantID, suiteID string,
) (domain.EvalSuiteRevision, bool, error) {
	var revision domain.EvalSuiteRevision
	found := false
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		revision, found, err = loadSuiteRevision(ctx, tx,
			`SELECT id, suite_id, COALESCE(parent_id, ''), COALESCE(version_no, 0), status, resource_kind, created_by
			 FROM eval_suite_revisions WHERE suite_id=$1 AND status='draft'`, suiteID)
		return err
	})
	return revision, found, err
}

func (r *PgSuiteRepository) GetRevision(
	ctx context.Context,
	tenantID, revisionID string,
) (domain.EvalSuiteRevision, bool, error) {
	var revision domain.EvalSuiteRevision
	found := false
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		revision, found, err = loadSuiteRevision(ctx, tx,
			`SELECT id, suite_id, COALESCE(parent_id, ''), COALESCE(version_no, 0), status, resource_kind, created_by
			 FROM eval_suite_revisions WHERE id=$1`, revisionID)
		return err
	})
	return revision, found, err
}

// GetActiveRevision 返回套件当前已发布（active）revision，用于矩阵评测
// seed 幂等复用：已发布的基准集直接复用，不重复创建。套件不存在或
// 从未发布（active_revision_id 为 NULL）时 found=false。
func (r *PgSuiteRepository) GetActiveRevision(
	ctx context.Context,
	tenantID, suiteID string,
) (domain.EvalSuiteRevision, bool, error) {
	var revision domain.EvalSuiteRevision
	found := false
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var activeRevisionID string
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(active_revision_id, '') FROM eval_suites WHERE id=$1`, suiteID,
		).Scan(&activeRevisionID); err != nil {
			if err == pgx.ErrNoRows {
				return nil // 套件不存在：保持 found=false
			}
			return fmt.Errorf("evaluation suite repository: load active revision id: %w", err)
		}
		if activeRevisionID == "" {
			return nil // 从未发布
		}
		var err error
		revision, found, err = loadSuiteRevision(ctx, tx,
			`SELECT id, suite_id, COALESCE(parent_id, ''), COALESCE(version_no, 0), status, resource_kind, created_by
			 FROM eval_suite_revisions WHERE id=$1`, activeRevisionID)
		return err
	})
	return revision, found, err
}

// GetSuite 返回套件自身元信息（含 created_at 与 active/draft revision 指针）；
// 套件不存在时 found=false。
func (r *PgSuiteRepository) GetSuite(
	ctx context.Context,
	tenantID, suiteID string,
) (domain.EvalSuite, bool, error) {
	var suite domain.EvalSuite
	found := false
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT id, name, description, COALESCE(active_revision_id, ''), COALESCE(draft_revision_id, ''),
			        created_by, created_at
			 FROM eval_suites WHERE id=$1`, suiteID,
		).Scan(&suite.ID, &suite.Name, &suite.Description, &suite.ActiveRevisionID,
			&suite.DraftRevisionID, &suite.CreatedBy, &suite.CreatedAt)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return fmt.Errorf("evaluation suite repository: load suite: %w", err)
		}
		found = true
		return nil
	})
	return suite, found, err
}

// ListSuiteRevisions 返回套件全部 revision 的轻量 meta（版本列表页 / 详情元信息
// 聚合用），不装载 cases。已发布版本按 version_no 降序在前，草稿（version_no
// 为 NULL）垫底；published_at 未发布的 revision 为 NULL。
func (r *PgSuiteRepository) ListSuiteRevisions(
	ctx context.Context,
	tenantID, suiteID string,
) ([]domain.SuiteRevisionMeta, error) {
	metas := []domain.SuiteRevisionMeta{}
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, COALESCE(version_no, 0), status, resource_kind, created_by, published_at,
			        (SELECT count(*)::int FROM eval_cases c WHERE c.suite_revision_id = r.id AND c.enabled)
			 FROM eval_suite_revisions r WHERE suite_id=$1
			 ORDER BY version_no DESC NULLS LAST, created_at DESC, id DESC`, suiteID)
		if err != nil {
			return fmt.Errorf("evaluation suite repository: list revisions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var meta domain.SuiteRevisionMeta
			var status, kind string
			if err := rows.Scan(&meta.ID, &meta.VersionNo, &status, &kind, &meta.CreatedBy,
				&meta.PublishedAt, &meta.EnabledCaseCount); err != nil {
				return fmt.Errorf("evaluation suite repository: scan revision meta: %w", err)
			}
			meta.Status = domain.SuiteRevisionStatus(status)
			meta.ResourceKind = domain.ResourceKind(kind)
			metas = append(metas, meta)
		}
		return rows.Err()
	})
	return metas, err
}

func (r *PgSuiteRepository) NextVersionNo(ctx context.Context, tenantID, suiteID string) (int, error) {
	next := 0
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(version_no), 0) + 1 FROM eval_suite_revisions WHERE suite_id=$1`, suiteID,
		).Scan(&next)
	})
	return next, err
}

func (r *PgSuiteRepository) PublishRevision(
	ctx context.Context,
	tenantID, suiteID, revisionID string,
	versionNo int,
) (domain.EvalSuiteRevision, error) {
	var revision domain.EvalSuiteRevision
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// 同一套件并发发布串行化：事务级 advisory 锁避免竞态；配合下方
		// status='draft' 的 0 行护栏，重复/并发 publish 必有一方以错误失败。
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, suiteID); err != nil {
			return fmt.Errorf("evaluation suite repository: lock suite for publish: %w", err)
		}
		tag, err := tx.Exec(ctx,
			`UPDATE eval_suite_revisions
			 SET status='published', version_no=$3, published_at=NOW()
			 WHERE id=$1 AND suite_id=$2 AND status='draft'`, revisionID, suiteID, versionNo)
		if err != nil {
			return fmt.Errorf("evaluation suite repository: publish revision: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("evaluation suite repository: draft revision not found")
		}
		var found bool
		revision, found, err = loadSuiteRevision(ctx, tx,
			`SELECT id, suite_id, COALESCE(parent_id, ''), COALESCE(version_no, 0), status, resource_kind, created_by
			 FROM eval_suite_revisions WHERE id=$1`, revisionID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("evaluation suite repository: published revision not found")
		}
		// 发布后自动开启继承草稿（版本演进 S1-1）：新草稿继承刚发布 revision 的
		// kind/created_by、parent 指向它，并把其全部 case 以全新 uuid 拷贝进来
		// （内容字段原样、判定/过程配置与来源经 ToConfig 重写），草稿由此永续存在。
		draftID := uuid.Must(uuid.NewV7()).String()
		if _, err := tx.Exec(ctx,
			`INSERT INTO eval_suite_revisions (id, suite_id, parent_id, version_no, status, resource_kind, created_by)
			 VALUES ($1,$2,$3,NULL,$4,$5,$6)`,
			draftID, suiteID, revisionID, string(domain.SuiteRevisionDraft),
			string(revision.ResourceKind), revision.CreatedBy,
		); err != nil {
			return fmt.Errorf("evaluation suite repository: insert inherited draft: %w", err)
		}
		if err := copyCasesToDraft(ctx, tx, draftID, revision.Cases); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE eval_suites SET active_revision_id=$2, draft_revision_id=$3, updated_at=NOW() WHERE id=$1`,
			suiteID, revisionID, draftID,
		); err != nil {
			return fmt.Errorf("evaluation suite repository: activate revision: %w", err)
		}
		return nil
	})
	return revision, err
}

// CreateDraftRevision opens a fresh draft for a legacy suite whose draft is
// missing (pre publish-inherits-draft suites, or a draft that was manually
// cleared), pointing eval_suites.draft_revision_id at it. The new draft
// inherits resource_kind and created_by from the active revision and copies
// its cases under fresh ids; suites never published have no active revision
// and fail. Creating a draft while one already exists is rejected to preserve
// the at-most-one-draft-per-suite invariant.
// ensureNoOpenDraft rejects suites that already own an editable draft, preserving the
// at-most-one-draft-per-suite invariant that StartNextDraft depends on.
func ensureNoOpenDraft(ctx context.Context, tx pgx.Tx, suiteID string) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM eval_suite_revisions WHERE suite_id=$1 AND status='draft')`,
		suiteID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("evaluation suite repository: check existing draft: %w", err)
	}
	if exists {
		return fmt.Errorf("evaluation suite repository: draft revision already exists")
	}
	return nil
}

// resolveActiveRevision loads the suite's active (published) revision. Suites without a
// published revision cannot seed an inherited draft, so the lookup is fail-closed.
func resolveActiveRevision(ctx context.Context, tx pgx.Tx, suiteID string) (domain.EvalSuiteRevision, error) {
	var activeID string
	if err := tx.QueryRow(ctx,
		`SELECT sr.id
		 FROM eval_suite_revisions sr
		 JOIN eval_suites s ON s.active_revision_id = sr.id
		 WHERE s.id = $1`, suiteID,
	).Scan(&activeID); err == pgx.ErrNoRows {
		return domain.EvalSuiteRevision{}, fmt.Errorf("evaluation suite repository: suite has no published revision")
	} else if err != nil {
		return domain.EvalSuiteRevision{}, fmt.Errorf("evaluation suite repository: resolve active revision: %w", err)
	}
	active, found, err := loadSuiteRevision(ctx, tx,
		`SELECT id, suite_id, COALESCE(parent_id, ''), COALESCE(version_no, 0), status, resource_kind, created_by
		 FROM eval_suite_revisions WHERE id=$1`, activeID)
	if err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	if !found {
		return domain.EvalSuiteRevision{}, fmt.Errorf("evaluation suite repository: active revision not found")
	}
	return active, nil
}

// createInheritedDraft opens a fresh draft inheriting resource_kind, created_by and the
// case set from the active revision, points eval_suites.draft_revision_id at it, and
// reloads the revision with its cases for direct consumption.
func createInheritedDraft(ctx context.Context, tx pgx.Tx, suiteID string,
	active domain.EvalSuiteRevision) (domain.EvalSuiteRevision, error) {
	revision := domain.EvalSuiteRevision{
		ID: uuid.Must(uuid.NewV7()).String(), SuiteID: suiteID,
		ParentID: active.ID, Status: domain.SuiteRevisionDraft,
		ResourceKind: active.ResourceKind, CreatedBy: active.CreatedBy,
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO eval_suite_revisions (id, suite_id, parent_id, version_no, status, resource_kind, created_by)
		 VALUES ($1,$2,$3,NULL,$4,$5,$6)`,
		revision.ID, revision.SuiteID, revision.ParentID,
		string(revision.Status), string(revision.ResourceKind), revision.CreatedBy,
	); err != nil {
		return domain.EvalSuiteRevision{}, fmt.Errorf("evaluation suite repository: insert draft revision: %w", err)
	}
	if err := copyCasesToDraft(ctx, tx, revision.ID, active.Cases); err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE eval_suites SET draft_revision_id=$2, updated_at=NOW() WHERE id=$1`,
		suiteID, revision.ID,
	)
	if err != nil {
		return domain.EvalSuiteRevision{}, fmt.Errorf("evaluation suite repository: point draft revision: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.EvalSuiteRevision{}, fmt.Errorf("evaluation suite repository: suite not found")
	}
	// 返回装载好 cases 的新草稿：StartNextDraft 与测试直接消费返回值，省一次再查。
	loaded, found, err := loadSuiteRevision(ctx, tx,
		`SELECT id, suite_id, COALESCE(parent_id, ''), COALESCE(version_no, 0), status, resource_kind, created_by
		 FROM eval_suite_revisions WHERE id=$1`, revision.ID)
	if err != nil {
		return domain.EvalSuiteRevision{}, err
	}
	if !found {
		return domain.EvalSuiteRevision{}, fmt.Errorf("evaluation suite repository: draft revision not found after insert")
	}
	return loaded, nil
}

func (r *PgSuiteRepository) CreateDraftRevision(
	ctx context.Context,
	tenantID, suiteID string,
) (domain.EvalSuiteRevision, error) {
	var revision domain.EvalSuiteRevision
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := ensureNoOpenDraft(ctx, tx, suiteID); err != nil {
			return err
		}
		active, err := resolveActiveRevision(ctx, tx, suiteID)
		if err != nil {
			return err
		}
		revision, err = createInheritedDraft(ctx, tx, suiteID, active)
		return err
	})
	return revision, err
}

// AddDraftCases inserts generated cases into a draft revision atomically.
func (r *PgSuiteRepository) AddDraftCases(ctx context.Context, tenantID, revisionID string, cases []domain.EvalCase) error {
	return r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		for i := range cases {
			if err := insertEvalCase(ctx, tx, revisionID, cases[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateDraftCase replaces the editable fields of one draft case. The case
// ID and suite_revision_id are verified together so cross-revision writes
// are impossible. evaluator_config is deliberately untouched: generation
// provenance and the judge spec are facts recorded when the case entered the
// draft, and the approval form does not carry them, so editing the case
// must not wipe them.
func (r *PgSuiteRepository) UpdateDraftCase(ctx context.Context, tenantID, revisionID string, testCase domain.EvalCase) error {
	inputJSON, err := json.Marshal(testCase.Input)
	if err != nil {
		return fmt.Errorf("evaluation suite repository: marshal input: %w", err)
	}
	expectedJSON, err := json.Marshal(testCase.ExpectedOutput)
	if err != nil {
		return fmt.Errorf("evaluation suite repository: marshal expected output: %w", err)
	}
	// session 与 input/expected 同属可编辑内容：会话剧本随编辑 full replacement
	// （nil Session 写回 '{}'，语义回退单轮）。evaluator_config 仍刻意不动：provenance
	// 与 judge spec 是 case 入 draft 时的事实。
	sessionJSON := []byte("{}")
	if testCase.Session != nil {
		if sessionJSON, err = json.Marshal(testCase.Session); err != nil {
			return fmt.Errorf("evaluation suite repository: marshal session script: %w", err)
		}
	}
	return r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE eval_cases
			 SET name=$3, input=$4, expected_output=$5, assertion_mode=$6, enabled=$7, session=$8
			 WHERE id=$1 AND suite_revision_id=$2`,
			testCase.ID, revisionID, testCase.Name, string(inputJSON), string(expectedJSON),
			string(testCase.AssertionMode), testCase.Enabled, string(sessionJSON),
		)
		if err != nil {
			return fmt.Errorf("evaluation suite repository: update draft case: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("evaluation suite repository: draft case not found")
		}
		return nil
	})
}

// DeleteDraftCase removes a rejected draft case.
func (r *PgSuiteRepository) DeleteDraftCase(ctx context.Context, tenantID, revisionID, caseID string) error {
	return r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM eval_cases WHERE id=$1 AND suite_revision_id=$2`,
			caseID, revisionID,
		)
		if err != nil {
			return fmt.Errorf("evaluation suite repository: delete draft case: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("evaluation suite repository: draft case not found")
		}
		return nil
	})
}

func (r *PgSuiteRepository) execTenant(
	ctx context.Context,
	tenantID string,
	fn func(context.Context, pgx.Tx) error,
) error {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	return execTenantTx(ctx, r.pool, tenantID, fn)
}

func insertEvalCase(ctx context.Context, tx pgx.Tx, revisionID string, testCase domain.EvalCase) error {
	inputJSON, err := json.Marshal(testCase.Input)
	if err != nil {
		return fmt.Errorf("evaluation suite repository: marshal input: %w", err)
	}
	expectedJSON, err := json.Marshal(testCase.ExpectedOutput)
	if err != nil {
		return fmt.Errorf("evaluation suite repository: marshal expected output: %w", err)
	}
	// evaluator_config packs the judge spec and generation provenance; the
	// column is NOT NULL DEFAULT '{}', so hand-authored rule cases write an
	// empty object.
	evaluatorConfig := []byte("{}")
	if cfg := testCase.ToConfig(); cfg != nil {
		evaluatorConfig, err = json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("evaluation suite repository: marshal evaluator config: %w", err)
		}
	}
	// session 列承载会话剧本（阶段 B）；'{}' = 旧单轮 case。
	sessionJSON := []byte("{}")
	if testCase.Session != nil {
		sessionJSON, err = json.Marshal(testCase.Session)
		if err != nil {
			return fmt.Errorf("evaluation suite repository: marshal session script: %w", err)
		}
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO eval_cases
		 (id, suite_revision_id, name, input, expected_output, assertion_mode, enabled, session, evaluator_config)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		testCase.ID, revisionID, testCase.Name, string(inputJSON), string(expectedJSON),
		string(testCase.AssertionMode), testCase.Enabled, string(sessionJSON), string(evaluatorConfig),
	)
	if err != nil {
		return fmt.Errorf("evaluation suite repository: insert case: %w", err)
	}
	return nil
}

func loadSuiteRevision(
	ctx context.Context,
	tx pgx.Tx,
	query string,
	arg string,
) (domain.EvalSuiteRevision, bool, error) {
	var revision domain.EvalSuiteRevision
	var status, kind, createdBy string
	err := tx.QueryRow(ctx, query, arg).Scan(
		&revision.ID, &revision.SuiteID, &revision.ParentID, &revision.VersionNo, &status, &kind, &createdBy,
	)
	if err == pgx.ErrNoRows {
		return domain.EvalSuiteRevision{}, false, nil
	}
	if err != nil {
		return domain.EvalSuiteRevision{}, false, err
	}
	revision.Status = domain.SuiteRevisionStatus(status)
	revision.ResourceKind = domain.ResourceKind(kind)
	revision.CreatedBy = createdBy
	cases, err := loadRevisionCases(ctx, tx, revision.ID)
	if err != nil {
		return domain.EvalSuiteRevision{}, false, err
	}
	revision.Cases = cases
	return revision, true, nil
}

// loadRevisionCases loads the full case list of one revision, restoring
// content (input/expected/session), judge spec, process assertions and
// provenance. It is shared by loadSuiteRevision and publish/copy paths so an
// inherited draft is seeded from the same faithfully-decoded cases.
func loadRevisionCases(ctx context.Context, tx pgx.Tx, revisionID string) ([]domain.EvalCase, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, name, input, expected_output, assertion_mode, enabled, session, evaluator_config
		 FROM eval_cases WHERE suite_revision_id=$1 ORDER BY created_at, id`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cases []domain.EvalCase
	for rows.Next() {
		var testCase domain.EvalCase
		var inputJSON, expectedJSON, evaluatorConfig, sessionJSON []byte
		var mode string
		if err := rows.Scan(&testCase.ID, &testCase.Name, &inputJSON, &expectedJSON, &mode, &testCase.Enabled, &sessionJSON, &evaluatorConfig); err != nil {
			return nil, err
		}
		testCase.AssertionMode = domain.AssertionMode(mode)
		_ = json.Unmarshal(inputJSON, &testCase.Input)
		_ = json.Unmarshal(expectedJSON, &testCase.ExpectedOutput)
		// session '{}' = 旧单轮 case：保持 nil 走旧执行路径。
		if len(sessionJSON) > 0 && string(sessionJSON) != "{}" {
			var script domain.EvalSessionScript
			if err := json.Unmarshal(sessionJSON, &script); err == nil {
				testCase.Session = &script
			}
		}
		// evaluator_config carries the judge spec and generation provenance;
		// NULL for hand-authored rule cases stays empty.
		testCase.ApplyConfig(evaluatorConfig)
		cases = append(cases, testCase)
	}
	return cases, rows.Err()
}

// copyCasesToDraft clones cases into the given draft revision, each under a
// fresh id so the global case PK never collides across revisions. Content
// (input/expected/session) round-trips as-is and insertEvalCase re-derives the
// evaluator_config (judge spec, process assertions, provenance) from the
// struct, so an inherited draft is a faithful continuation of its parent.
func copyCasesToDraft(ctx context.Context, tx pgx.Tx, draftID string, cases []domain.EvalCase) error {
	for _, testCase := range cases {
		testCase.ID = uuid.Must(uuid.NewV7()).String()
		if err := insertEvalCase(ctx, tx, draftID, testCase); err != nil {
			return err
		}
	}
	return nil
}

// GetSuiteCreatedBy 返回套件创建者；未命中 found=false。
func (r *PgSuiteRepository) GetSuiteCreatedBy(ctx context.Context, tenantID, suiteID string) (string, bool, error) {
	var createdBy string
	found := false
	err := r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT created_by FROM eval_suites WHERE id=$1`, suiteID).Scan(&createdBy)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return fmt.Errorf("evaluation suite repository: load created by: %w", err)
		}
		found = true
		return nil
	})
	return createdBy, found, err
}

// DeleteSuite 删除套件：任一 revision 被 run/optimization job/experiment 引用时
// 拒绝删除（ErrEntityReferenced，禁级联破坏）；否则事务内级联删除 revisions 与
// cases 并写变更审计。外键违例兜底翻译为 ErrEntityReferenced。
func (r *PgSuiteRepository) DeleteSuite(
	ctx context.Context, tenantID, suiteID string, audit *auditdomain.ResourceChangeAuditEvent,
) error {
	return r.execTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var referenced bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM eval_runs
					WHERE suite_revision_id IN (SELECT id FROM eval_suite_revisions WHERE suite_id=$1))
			    OR EXISTS(SELECT 1 FROM optimization_jobs
					WHERE suite_revision_id IN (SELECT id FROM eval_suite_revisions WHERE suite_id=$1))
			    OR EXISTS(SELECT 1 FROM evaluation_experiments
					WHERE suite_revision_id IN (SELECT id FROM eval_suite_revisions WHERE suite_id=$1))`,
			suiteID).Scan(&referenced); err != nil {
			return fmt.Errorf("evaluation suite repository: check suite references: %w", err)
		}
		if referenced {
			return domain.ErrEntityReferenced
		}
		tag, err := tx.Exec(ctx, `DELETE FROM eval_suites WHERE id=$1`, suiteID)
		if err != nil {
			return translateEntityReferenced(fmt.Errorf("evaluation suite repository: delete suite: %w", err))
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("evaluation suite repository: delete suite %s: not found", suiteID)
		}
		return insertChangeAudit(ctx, tx, audit)
	})
}
