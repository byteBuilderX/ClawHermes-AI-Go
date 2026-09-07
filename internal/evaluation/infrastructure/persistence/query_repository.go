package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/storage/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgCenterQueryRepository struct{ pool poolIface }

func NewPgCenterQueryRepository(pool *pgxpool.Pool) *PgCenterQueryRepository {
	return &PgCenterQueryRepository{pool: pool}
}

func (r *PgCenterQueryRepository) tenant(ctx context.Context, tenantID string, fn func(context.Context, pgx.Tx) error) error {
	ctx = postgres.WithTenant(ctx, &postgres.TenantContext{TenantID: tenantID})
	return execTenantTx(ctx, r.pool, tenantID, fn)
}

func (r *PgCenterQueryRepository) Overview(ctx context.Context, tenantID string) (domain.CenterOverview, error) {
	var result domain.CenterOverview
	err := r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			(SELECT COUNT(DISTINCT (resource_kind,resource_id)) FROM resource_revisions),
			(SELECT COUNT(*) FROM eval_suites), (SELECT COUNT(*) FROM eval_runs),
			(SELECT COUNT(*) FROM optimization_candidates), (SELECT COUNT(*) FROM evaluation_experiments)`).Scan(
			&result.Resources, &result.Suites, &result.Runs, &result.Candidates, &result.Experiments)
	})
	return result, wrapCenterQuery("overview", err)
}

func cursorValues(raw string) (*time.Time, *string, error) {
	if raw == "" {
		return nil, nil, nil
	}
	cursor, err := domain.DecodeCenterCursor(raw)
	if err != nil {
		return nil, nil, err
	}
	return &cursor.CreatedAt, &cursor.ID, nil
}

func pageCursor(createdAt time.Time, id string) string {
	return domain.EncodeCenterCursor(createdAt, id)
}

const timelineCursorSeparator = "\x00"

func timelineCursorValues(raw string) (*time.Time, *string, *string, error) {
	createdAt, qualifiedID, err := cursorValues(raw)
	if err != nil || qualifiedID == nil {
		return createdAt, qualifiedID, nil, err
	}
	parts := strings.Split(*qualifiedID, timelineCursorSeparator)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, nil, nil, domain.ErrInvalidCenterQuery
	}
	return createdAt, &parts[0], &parts[1], nil
}

func timelinePageCursor(event domain.TimelineEvent) string {
	return pageCursor(event.CreatedAt, event.ID+timelineCursorSeparator+event.Kind)
}

func (r *PgCenterQueryRepository) ListResources(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.ResourcePage, error) {
	var page domain.ResourcePage
	ct, cid, err := cursorValues(filter.Cursor)
	if err != nil {
		return page, err
	}
	err = r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH latest AS (
			SELECT DISTINCT ON (rr.resource_kind,rr.resource_id) rr.id,rr.resource_kind,rr.resource_id,rr.status,
				rr.safe_summary,rr.created_at,
				COALESCE(d.stable_revision_id,CASE WHEN rr.status='published' THEN rr.id ELSE '' END) stable_revision_id,
				(SELECT er.status FROM eval_runs er WHERE er.resource_kind=rr.resource_kind AND er.resource_id=rr.resource_id ORDER BY er.created_at DESC,er.id DESC LIMIT 1) latest_run_status
			FROM resource_revisions rr LEFT JOIN evaluation_deployments d USING(resource_kind,resource_id)
			WHERE ($1='' OR rr.resource_kind = ANY(string_to_array($1, ','))) AND ($2='' OR rr.resource_id=$2)
			ORDER BY rr.resource_kind,rr.resource_id,rr.created_at DESC,rr.id DESC)
		SELECT id,resource_kind,resource_id,status,safe_summary,created_at,COALESCE(stable_revision_id,''),COALESCE(latest_run_status,'')
		FROM latest WHERE ($3='' OR status=$3) AND ($4::timestamptz IS NULL OR (created_at,id)<($4,$5))
		ORDER BY created_at DESC,id DESC LIMIT $6`,
			filter.ResourceKind, filter.ResourceID, filter.Status, ct, cid, filter.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.ResourceSummary
			var kind string
			var safe []byte
			if err := rows.Scan(&item.ID, &kind, &item.ResourceID, &item.Status, &safe, &item.CreatedAt, &item.StableRevisionID, &item.LatestRunStatus); err != nil {
				return err
			}
			item.ResourceKind = domain.ResourceKind(kind)
			item.SafeSummary = parseSanitizedSafeSummary(safe)
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	trimResources(&page, filter.Limit)
	return page, wrapCenterQuery("list resources", err)
}

func trimResources(p *domain.ResourcePage, limit int) {
	if len(p.Items) > limit {
		last := p.Items[limit-1]
		p.NextCursor = pageCursor(last.CreatedAt, last.ID)
		p.Items = p.Items[:limit]
	}
}

func (r *PgCenterQueryRepository) ListSuites(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.SuitePage, error) {
	var page domain.SuitePage
	ct, cid, e := cursorValues(filter.Cursor)
	if e != nil {
		return page, e
	}
	e = r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// 一次 LEFT JOIN 同时带出 active 与 draft 两版（kind/状态/版本号 + 启用
		// case 数经相关子查询聚合），列表行携带当前链完整演进元信息；filter 语义
		// 沿用旧的"代表性 revision"= COALESCE(active, draft)。
		rows, e := tx.Query(ctx, `SELECT s.id,s.name,s.description,
			COALESCE(ar.resource_kind,dr.resource_kind,''),
			COALESCE(ar.status,dr.status,''),
			s.created_by,s.created_at,
			COALESCE(ar.id,''),COALESCE(dr.id,''),
			COALESCE(ar.version_no,0),COALESCE(dr.version_no,0),
			COALESCE((SELECT count(*)::int FROM eval_cases c WHERE c.suite_revision_id=ar.id AND c.enabled),0),
			COALESCE((SELECT count(*)::int FROM eval_cases c WHERE c.suite_revision_id=dr.id AND c.enabled),0)
			FROM eval_suites s
			LEFT JOIN eval_suite_revisions ar ON ar.id=s.active_revision_id
			LEFT JOIN eval_suite_revisions dr ON dr.id=s.draft_revision_id
			WHERE ($1='' OR COALESCE(ar.resource_kind,dr.resource_kind,'') = ANY(string_to_array($1, ',')))
			  AND ($2='' OR EXISTS (SELECT 1 FROM eval_runs r WHERE (r.suite_revision_id=ar.id OR r.suite_revision_id=dr.id) AND r.resource_id=$2))
			  AND ($3='' OR COALESCE(ar.status,dr.status,'')=$3)
			  AND ($4::timestamptz IS NULL OR (s.created_at,s.id)<($4,$5))
			ORDER BY s.created_at DESC,s.id DESC LIMIT $6`, filter.ResourceKind, filter.ResourceID, filter.Status, ct, cid, filter.Limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var x domain.SuiteSummary
			var kind string
			if e = rows.Scan(&x.ID, &x.Name, &x.Description, &kind, &x.Status, &x.CreatedBy, &x.CreatedAt,
				&x.ActiveRevisionID, &x.DraftRevisionID, &x.ActiveVersionNo, &x.DraftVersionNo,
				&x.ActiveCaseCount, &x.DraftCaseCount); e != nil {
				return e
			}
			x.ResourceKind = domain.ResourceKind(kind)
			page.Items = append(page.Items, x)
		}
		return rows.Err()
	})
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		page.NextCursor = pageCursor(last.CreatedAt, last.ID)
		page.Items = page.Items[:filter.Limit]
	}
	return page, wrapCenterQuery("list suites", e)
}

func (r *PgCenterQueryRepository) ListRuns(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.RunPage, error) {
	var page domain.RunPage
	ct, cid, e := cursorValues(filter.Cursor)
	if e != nil {
		return page, e
	}
	e = r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id,resource_kind,resource_id,revision_id,status,passed,total_cases,passed_cases,created_by,created_at FROM eval_runs WHERE ($1='' OR resource_kind = ANY(string_to_array($1, ','))) AND ($2='' OR resource_id=$2) AND ($3='' OR status=$3) AND ($4::timestamptz IS NULL OR (created_at,id)<($4,$5)) AND ($7='' OR revision_id=$7) ORDER BY created_at DESC,id DESC LIMIT $6`, filter.ResourceKind, filter.ResourceID, filter.Status, ct, cid, filter.Limit+1, filter.RevisionID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var x domain.RunSummary
			var kind string
			if e = rows.Scan(&x.ID, &kind, &x.ResourceID, &x.RevisionID, &x.Status, &x.Passed, &x.TotalCases, &x.PassedCases, &x.CreatedBy, &x.CreatedAt); e != nil {
				return e
			}
			x.ResourceKind = domain.ResourceKind(kind)
			page.Items = append(page.Items, x)
		}
		return rows.Err()
	})
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		page.NextCursor = pageCursor(last.CreatedAt, last.ID)
		page.Items = page.Items[:filter.Limit]
	}
	return page, wrapCenterQuery("list runs", e)
}

func (r *PgCenterQueryRepository) ListCandidates(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.CandidatePage, error) {
	var page domain.CandidatePage
	ct, cid, e := cursorValues(filter.Cursor)
	if e != nil {
		return page, e
	}
	e = r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT c.id,j.resource_kind,j.resource_id,c.revision_id,c.parent_revision_id,c.source,c.status,c.rank,c.state_version,COALESCE(parent.safe_summary,'{}'::jsonb),parent.id IS NOT NULL,COALESCE(candidate.safe_summary,'{}'::jsonb),j.created_by,c.created_at FROM optimization_candidates c JOIN optimization_jobs j ON j.id=c.optimization_job_id LEFT JOIN resource_revisions parent ON parent.resource_kind=j.resource_kind AND parent.resource_id=j.resource_id AND parent.id=c.parent_revision_id LEFT JOIN resource_revisions candidate ON candidate.resource_kind=j.resource_kind AND candidate.resource_id=j.resource_id AND candidate.id=c.revision_id WHERE ($1='' OR j.resource_kind = ANY(string_to_array($1, ','))) AND ($2='' OR j.resource_id=$2) AND ($3='' OR c.status=$3 OR j.status=$3) AND ($4::timestamptz IS NULL OR (c.created_at,c.id)<($4,$5)) ORDER BY c.created_at DESC,c.id DESC LIMIT $6`, filter.ResourceKind, filter.ResourceID, filter.Status, ct, cid, filter.Limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var x domain.CandidateSummary
			var kind string
			var parent, candidate []byte
			var parentExists bool
			if e = rows.Scan(&x.ID, &kind, &x.ResourceID, &x.RevisionID, &x.ParentRevisionID, &x.Source, &x.Status,
				&x.Rank, &x.StateVersion, &parent, &parentExists, &candidate, &x.CreatedBy, &x.CreatedAt); e != nil {
				return e
			}
			x.ResourceKind = domain.ResourceKind(kind)
			x.SafeDiff = buildCandidateSafeDiff(parseSanitizedSafeSummary(parent),
				parseSanitizedSafeSummary(candidate), parentExists)
			page.Items = append(page.Items, x)
		}
		return rows.Err()
	})
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		page.NextCursor = pageCursor(last.CreatedAt, last.ID)
		page.Items = page.Items[:filter.Limit]
	}
	return page, wrapCenterQuery("list candidates", e)
}

func (r *PgCenterQueryRepository) ListExperiments(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.ExperimentPage, error) {
	var page domain.ExperimentPage
	ct, cid, e := cursorValues(filter.Cursor)
	if e != nil {
		return page, e
	}
	e = r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id,resource_kind,resource_id,stable_revision_id,canary_revision_id,status,stage_percent,recommendation,safety_stopped,state_version,policy,decision_snapshot,created_by,created_at FROM evaluation_experiments WHERE ($1='' OR resource_kind = ANY(string_to_array($1, ','))) AND ($2='' OR resource_id=$2) AND ($3='' OR status=$3) AND ($4::timestamptz IS NULL OR (created_at,id)<($4,$5)) ORDER BY created_at DESC,id DESC LIMIT $6`, filter.ResourceKind, filter.ResourceID, filter.Status, ct, cid, filter.Limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var x domain.ExperimentSummary
			var kind string
			var policyJSON, snapshotJSON []byte
			if e = rows.Scan(&x.ID, &kind, &x.ResourceID, &x.StableRevisionID, &x.CanaryRevisionID, &x.Status,
				&x.StagePercent, &x.Recommendation, &x.SafetyStopped, &x.StateVersion, &policyJSON, &snapshotJSON,
				&x.CreatedBy, &x.CreatedAt); e != nil {
				return e
			}
			x.ResourceKind = domain.ResourceKind(kind)
			var policy domain.PromotionPolicy
			if e = json.Unmarshal(policyJSON, &policy); e != nil {
				return fmt.Errorf("decode experiment policy: %w", e)
			}
			var snapshot struct {
				Metrics *domain.StageMetrics `json:"metrics"`
			}
			if e = json.Unmarshal(snapshotJSON, &snapshot); e != nil {
				return fmt.Errorf("decode experiment evidence: %w", e)
			}
			x.PromotionEvidence = domain.BuildPromotionEvidence(policy, snapshot.Metrics,
				domain.Decision(x.Recommendation), x.SafetyStopped)
			page.Items = append(page.Items, x)
		}
		return rows.Err()
	})
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		page.NextCursor = pageCursor(last.CreatedAt, last.ID)
		page.Items = page.Items[:filter.Limit]
	}
	return page, wrapCenterQuery("list experiments", e)
}

func (r *PgCenterQueryRepository) Timeline(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.TimelinePage, error) {
	var page domain.TimelinePage
	ct, cid, ckind, e := timelineCursorValues(filter.Cursor)
	if e != nil {
		return page, e
	}
	e = r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM resource_revisions WHERE resource_kind=$1 AND resource_id=$2)`, filter.ResourceKind, filter.ResourceID).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return port.ErrCenterResourceNotFound
		}
		rows, e := tx.Query(ctx, `WITH events AS (
		SELECT id,'revision' kind,status,'' summary,safe_summary,resource_kind,resource_id,created_at FROM resource_revisions WHERE resource_kind=$1 AND resource_id=$2
		UNION ALL SELECT id,'run',status,CASE WHEN passed THEN 'passed' ELSE 'not passed' END,NULL::jsonb,resource_kind,resource_id,created_at FROM eval_runs WHERE resource_kind=$1 AND resource_id=$2
		UNION ALL SELECT c.id,'candidate',c.status,c.source,NULL::jsonb,j.resource_kind,j.resource_id,c.created_at FROM optimization_candidates c JOIN optimization_jobs j ON j.id=c.optimization_job_id WHERE j.resource_kind=$1 AND j.resource_id=$2
		UNION ALL SELECT id,'experiment',status,recommendation,NULL::jsonb,resource_kind,resource_id,created_at FROM evaluation_experiments WHERE resource_kind=$1 AND resource_id=$2
		UNION ALL SELECT d.id,'decision',d.new_status,d.action,NULL::jsonb,e.resource_kind,e.resource_id,d.created_at FROM experiment_decisions d JOIN evaluation_experiments e ON e.id=d.experiment_id WHERE e.resource_kind=$1 AND e.resource_id=$2)
		SELECT id,kind,status,summary,safe_summary,resource_kind,resource_id,created_at FROM events
		WHERE ($3='' OR status=$3) AND ($4::timestamptz IS NULL OR (created_at,id,kind)<($4,$5,$6))
		ORDER BY created_at DESC,id DESC,kind DESC LIMIT $7`,
			filter.ResourceKind, filter.ResourceID, filter.Status, ct, cid, ckind, filter.Limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var x domain.TimelineEvent
			var kind string
			var safe []byte
			if e = rows.Scan(&x.ID, &x.Kind, &x.Status, &x.Summary, &safe, &kind, &x.ResourceID, &x.CreatedAt); e != nil {
				return e
			}
			if x.Kind == "revision" {
				sanitized, marshalErr := json.Marshal(parseSanitizedSafeSummary(safe))
				if marshalErr != nil {
					x.Summary = "{}"
				} else {
					x.Summary = string(sanitized)
				}
			}
			x.ResourceKind = domain.ResourceKind(kind)
			page.Items = append(page.Items, x)
		}
		return rows.Err()
	})
	if len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		page.NextCursor = timelinePageCursor(last)
		page.Items = page.Items[:filter.Limit]
	}
	return page, wrapCenterQuery("timeline", e)
}

// ListRevisions (0) 返回被测资源在 resource_revisions 中的 eval 版本表（含零引用
// 版本，created_at DESC 游标分页）。资源在 resource_revisions 无任何行（未建档）
// → ErrCenterResourceNotFound，与 Timeline 存在性语义一致。
func (r *PgCenterQueryRepository) ListRevisions(ctx context.Context, tenantID string, filter port.CenterFilter) (domain.RevisionPage, error) {
	var page domain.RevisionPage
	ct, cid, err := cursorValues(filter.Cursor)
	if err != nil {
		return page, err
	}
	err = r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM resource_revisions WHERE resource_kind=$1 AND resource_id=$2)`, filter.ResourceKind, filter.ResourceID).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return port.ErrCenterResourceNotFound
		}
		rows, e := tx.Query(ctx, `SELECT id,resource_kind,resource_id,COALESCE(parent_revision_id,''),source,status,safe_summary,created_by,created_at FROM resource_revisions WHERE resource_kind=$1 AND resource_id=$2 AND ($3='' OR status=$3) AND ($4::timestamptz IS NULL OR (created_at,id)<($4,$5)) ORDER BY created_at DESC,id DESC LIMIT $6`, filter.ResourceKind, filter.ResourceID, filter.Status, ct, cid, filter.Limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var item domain.RevisionSummary
			var kind, parent string
			var safe []byte
			if e := rows.Scan(&item.ID, &kind, &item.ResourceID, &parent, &item.Source, &item.Status, &safe, &item.CreatedBy, &item.CreatedAt); e != nil {
				return e
			}
			if parent != "" {
				item.ParentRevisionID = parent
			}
			item.ResourceKind = domain.ResourceKind(kind)
			item.SafeSummary = parseSanitizedSafeSummary(safe)
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	// service 已归一 limit（默认 20）；此处守卫 limit<=0 直接直通防御（新端点不得
	// 因直调 repo 的零值 limit 触发索引 -1 panic，与 list resources 的 trimResources
	// 同契约、但新代码不加新 panic 面）。
	if filter.Limit > 0 && len(page.Items) > filter.Limit {
		last := page.Items[filter.Limit-1]
		page.NextCursor = pageCursor(last.CreatedAt, last.ID)
		page.Items = page.Items[:filter.Limit]
	}
	return page, wrapCenterQuery("list revisions", err)
}

// RevisionReferences (c) 返回单 eval 版本 R 的引用方账本：deployment 角色投影 +
// subject runs（被评主体即 R 的 run）+ pinned runs（把 R 当绑定资源 pin 的其它
// run，subject 按 run id 去重）+ candidate/baseline 演进引用 + 实验臂引用。版本
// 不属于该资源（resource_revisions 无 (kind,id,R) 行）→ ErrCenterResourceNotFound。
// 各明细组只读陈列、非精确计数，按 created_at 倒序各取一组上限（超限即取最近）。
func (r *PgCenterQueryRepository) RevisionReferences(ctx context.Context, tenantID string, ref domain.ResourceRef) (domain.RevisionReferences, error) {
	var result domain.RevisionReferences
	err := r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if !revisionBelongsToResource(ctx, tx, ref) {
			return port.ErrCenterResourceNotFound
		}
		deployment, e := loadRevisionDeployment(ctx, tx, ref)
		if e != nil {
			return e
		}
		if deployment != nil {
			result.Deployment = deployment
		}
		subject, e := scanRevisionSubjectRuns(ctx, tx, ref)
		if e != nil {
			return e
		}
		result.SubjectRuns = subject
		exclude := make(map[string]struct{}, len(subject))
		for _, run := range subject {
			exclude[run.ID] = struct{}{}
		}
		result.PinnedRuns, e = scanRevisionPinnedRuns(ctx, tx, ref, exclude)
		if e != nil {
			return e
		}
		result.Candidates, e = scanRevisionCandidates(ctx, tx, ref)
		if e != nil {
			return e
		}
		result.Experiments, e = scanRevisionExperiments(ctx, tx, ref)
		return e
	})
	return result, wrapCenterQuery("revision references", err)
}

// RevisionPassRate (d) 返回单 eval 版本 R 的通过率摘要：total/succeeded run 数按
// 版本全部 status 统计；用例 a/b 仅聚合 succeeded run；pass_rate=成功 run 用例通过
// 合计/用例合计，0 次成功或用例合计为 0 → nil（诚实空态）。recent_runs 该版本最近
// EvalReferenceRecentRunsLimit 条（含非成功 run，前端筛 succeeded 绘点）。
func (r *PgCenterQueryRepository) RevisionPassRate(ctx context.Context, tenantID string, ref domain.ResourceRef) (domain.RevisionPassRate, error) {
	var result domain.RevisionPassRate
	err := r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if !revisionBelongsToResource(ctx, tx, ref) {
			return port.ErrCenterResourceNotFound
		}
		var succeededRuns, passedCases, totalCases int
		if e := tx.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status='succeeded')::int,COALESCE(SUM(passed_cases) FILTER (WHERE status='succeeded'),0)::int,COALESCE(SUM(total_cases) FILTER (WHERE status='succeeded'),0)::int FROM eval_runs WHERE resource_kind=$1 AND resource_id=$2 AND revision_id=$3`, ref.Kind, ref.ResourceID, ref.RevisionID).Scan(&succeededRuns, &passedCases, &totalCases); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT COUNT(*)::int FROM eval_runs WHERE resource_kind=$1 AND resource_id=$2 AND revision_id=$3`, ref.Kind, ref.ResourceID, ref.RevisionID).Scan(&result.TotalRuns); e != nil {
			return e
		}
		result.SucceededRuns = succeededRuns
		result.PassedCases = passedCases
		result.TotalCases = totalCases
		if succeededRuns > 0 && totalCases > 0 {
			rate := float64(passedCases) / float64(totalCases)
			result.PassRate = &rate
		}
		recent, e := scanRevisionSubjectRuns(ctx, tx, ref, constants.EvalReferenceRecentRunsLimit)
		if e != nil {
			return e
		}
		result.RecentRuns = recent
		return nil
	})
	return result, wrapCenterQuery("revision pass rate", err)
}

// revisionBelongsToResource 报告 (kind,id,revisionID) 是否存在于 resource_revisions。
func revisionBelongsToResource(ctx context.Context, tx pgx.Tx, ref domain.ResourceRef) bool {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM resource_revisions WHERE resource_kind=$1 AND resource_id=$2 AND id=$3)`, ref.Kind, ref.ResourceID, ref.RevisionID).Scan(&exists)
	return err == nil && exists
}

// loadRevisionDeployment 读取资源 deployment 行并投影所查版本的臂角色：版本正作为
// stable/canary 臂时返回对应投影，否则 nil（诚实空态，版本不是当前线上臂）。
func loadRevisionDeployment(ctx context.Context, tx pgx.Tx, ref domain.ResourceRef) (*domain.RevisionDeployment, error) {
	var stable, canary string
	var canaryPercent int
	err := tx.QueryRow(ctx, `SELECT stable_revision_id,COALESCE(canary_revision_id,''),canary_percent FROM evaluation_deployments WHERE resource_kind=$1 AND resource_id=$2`, ref.Kind, ref.ResourceID).Scan(&stable, &canary, &canaryPercent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch {
	case stable != ref.RevisionID && canary != ref.RevisionID:
		return nil, nil
	case stable == ref.RevisionID && canary == ref.RevisionID:
		return &domain.RevisionDeployment{Role: "both", StableRevisionID: stable, CanaryRevisionID: canary, CanaryPercent: canaryPercent}, nil
	case stable == ref.RevisionID:
		return &domain.RevisionDeployment{Role: "stable", StableRevisionID: stable}, nil
	default:
		return &domain.RevisionDeployment{Role: "canary", CanaryRevisionID: canary, CanaryPercent: canaryPercent}, nil
	}
}

// scanRevisionSubjectRuns 返回被评主体版本即 ref.RevisionID 的 run（created_at 倒序，
// limit<=0 时用 EvalReferenceRunsLimit）。RevisionPassRate 的 recent_runs 复用同一扫描。
func scanRevisionSubjectRuns(ctx context.Context, tx pgx.Tx, ref domain.ResourceRef, limit ...int) ([]domain.RunSummary, error) {
	bounded := constants.EvalReferenceRunsLimit
	if len(limit) > 0 && limit[0] > 0 {
		bounded = limit[0]
	}
	rows, err := tx.Query(ctx, `SELECT id,resource_kind,resource_id,revision_id,status,passed,total_cases,passed_cases,created_by,created_at FROM eval_runs WHERE resource_kind=$1 AND resource_id=$2 AND revision_id=$3 ORDER BY created_at DESC,id DESC LIMIT $4`, ref.Kind, ref.ResourceID, ref.RevisionID, bounded)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.RunSummary{}
	for rows.Next() {
		var item domain.RunSummary
		var kind string
		if err := rows.Scan(&item.ID, &kind, &item.ResourceID, &item.RevisionID, &item.Status, &item.Passed, &item.TotalCases, &item.PassedCases, &item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.ResourceKind = domain.ResourceKind(kind)
		items = append(items, item)
	}
	return items, rows.Err()
}

// scanRevisionPinnedRuns 收窄候选：context_snapshot 文本含 ref.RevisionID 的 run 按
// created_at 倒序取 EvalReferenceRunsLimit 条；Go decode 后仅保留 revisionID 出现在
// 任一 PinnedAssignments map 值中的行（值命中=该版本被作为绑定资源 pin；键命名空间
// 因跨产品侧 id 语义不统一只做值比较），并跳过 subjectRuns 已含的 run id。
func scanRevisionPinnedRuns(ctx context.Context, tx pgx.Tx, ref domain.ResourceRef, exclude map[string]struct{}) ([]domain.RevisionPinnedRun, error) {
	rows, err := tx.Query(ctx, `SELECT id,resource_kind,resource_id,status,passed,total_cases,passed_cases,context_snapshot,created_at FROM eval_runs WHERE context_snapshot::text LIKE '%'||$1||'%' ORDER BY created_at DESC,id DESC LIMIT $2`, ref.RevisionID, constants.EvalReferenceRunsLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.RevisionPinnedRun{}
	for rows.Next() {
		var item domain.RevisionPinnedRun
		var kind string
		var snapshot []byte
		if err := rows.Scan(&item.RunID, &kind, &item.ResourceID, &item.Status, &item.Passed, &item.TotalCases, &item.PassedCases, &snapshot, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.ResourceKind = domain.ResourceKind(kind)
		if _, dup := exclude[item.RunID]; dup {
			continue
		}
		snap, decodeErr := decodeContextSnapshot(snapshot)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if snap == nil || !pinnedAssignmentsContain(snap.PinnedAssignments, ref.RevisionID) {
			continue
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// pinnedAssignmentsContain 报告 revisionID 是否为任一 PinnedAssignments map 的绑定
// 版本值（SkillAgentRevision/SkillRevisions/MCPRevisions/KnowledgeRevisions）。
func pinnedAssignmentsContain(pinned domain.PinnedAssignments, revisionID string) bool {
	return pinnedValueContains(pinned.SkillAgentRevision, revisionID) ||
		pinnedValueContains(pinned.SkillRevisions, revisionID) ||
		pinnedValueContains(pinned.MCPRevisions, revisionID) ||
		pinnedValueContains(pinned.KnowledgeRevisions, revisionID)
}

func pinnedValueContains(entries map[string]string, revisionID string) bool {
	for _, value := range entries {
		if value == revisionID {
			return true
		}
	}
	return false
}

// scanRevisionCandidates 返回把 ref.RevisionID 作为候选（revision_id）或基线
// （parent_revision_id）的优化候选行；两者同值时优先记 candidate。
func scanRevisionCandidates(ctx context.Context, tx pgx.Tx, ref domain.ResourceRef) ([]domain.RevisionCandidateRef, error) {
	rows, err := tx.Query(ctx, `SELECT c.id,c.revision_id,c.parent_revision_id,c.source,c.status,c.rank,c.created_at FROM optimization_candidates c JOIN optimization_jobs j ON j.id=c.optimization_job_id WHERE j.resource_kind=$1 AND j.resource_id=$2 AND (c.revision_id=$3 OR c.parent_revision_id=$3) ORDER BY c.created_at DESC,c.id DESC LIMIT $4`, ref.Kind, ref.ResourceID, ref.RevisionID, constants.EvalReferenceCandidatesLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.RevisionCandidateRef{}
	for rows.Next() {
		var item domain.RevisionCandidateRef
		if err := rows.Scan(&item.ID, &item.RevisionID, &item.ParentRevisionID, &item.Source, &item.Status, &item.Rank, &item.CreatedAt); err != nil {
			return nil, err
		}
		if item.RevisionID == ref.RevisionID {
			item.Role = "candidate"
		} else {
			item.Role = "baseline"
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// scanRevisionExperiments 返回把 ref.RevisionID 作为 stable/canary 臂的实验行；两臂
// 同值时 role=both。
func scanRevisionExperiments(ctx context.Context, tx pgx.Tx, ref domain.ResourceRef) ([]domain.RevisionExperimentRef, error) {
	rows, err := tx.Query(ctx, `SELECT id,stable_revision_id,canary_revision_id,status,stage_percent,recommendation,created_at FROM evaluation_experiments WHERE resource_kind=$1 AND resource_id=$2 AND (stable_revision_id=$3 OR canary_revision_id=$3) ORDER BY created_at DESC,id DESC LIMIT $4`, ref.Kind, ref.ResourceID, ref.RevisionID, constants.EvalReferenceExperimentsLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.RevisionExperimentRef{}
	for rows.Next() {
		var item domain.RevisionExperimentRef
		if err := rows.Scan(&item.ID, &item.StableRevisionID, &item.CanaryRevisionID, &item.Status, &item.StagePercent, &item.Recommendation, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.Role = revisionExperimentRole(ref.RevisionID, item.StableRevisionID, item.CanaryRevisionID)
		items = append(items, item)
	}
	return items, rows.Err()
}

func revisionExperimentRole(revisionID, stable, canary string) string {
	switch {
	case stable == revisionID && canary == revisionID:
		return "both"
	case stable == revisionID:
		return "stable"
	default:
		return "canary"
	}
}

func wrapCenterQuery(operation string, err error) error {
	if err == nil {
		return nil
	}
	if err == port.ErrCenterResourceNotFound {
		return err
	}
	return fmt.Errorf("evaluation center repository: %s: %w", operation, err)
}

// MonitorResources 返回窗口内按观测样本数降序的资源行四区摘要（spec §4.2 端点 1）。
func (r *PgCenterQueryRepository) MonitorResources(ctx context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorResourcesPage, error) {
	var page domain.MonitorResourcesPage
	err := r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		items, index, err := scanMonitorResourceRows(ctx, tx, filter)
		if err != nil {
			return err
		}
		page.Items = items
		if len(page.Items) == 0 {
			return nil // 窗口无观测 → 不展开维度
		}
		return expandMonitorResourceDims(ctx, tx, filter, page.Items, index)
	})
	for i := range page.Items {
		if page.Items[i].Quality == nil {
			page.Items[i].Quality = []domain.QualityDim{}
		}
	}
	return page, wrapCenterQuery("monitor resources", err)
}

// monitorResourceKey 组装 (kind, id) 行键。
func monitorResourceKey(kind, id string) string {
	return kind + "\x00" + id
}

// scanMonitorResourceRows 端点1 Q1：窗口内资源行聚合 + LEFT JOIN LATERAL 最近 succeeded run。
func scanMonitorResourceRows(ctx context.Context, tx pgx.Tx, filter port.MonitorFilter) ([]domain.MonitorResourceSummary, map[string]int, error) {
	rows, err := tx.Query(ctx, `
WITH top AS (
	SELECT o.resource_kind, o.resource_id,
		count(*)::int AS sample_count,
		coalesce(sum(jsonb_array_length(o.signals->'rule')),0)::int AS rule_hits,
		count(*) FILTER (WHERE o.signals->'behavior'->>'retry' = 'true')::int AS retry_count,
		count(*) FILTER (WHERE o.signals->'behavior'->>'escalation' = 'true')::int AS escalation_count,
		count(*) FILTER (WHERE o.signals->'behavior'->>'abandonment' = 'true')::int AS abandonment_count,
		count(*) FILTER (WHERE o.verdict = 'pass')::int AS verdict_pass,
		count(*) FILTER (WHERE o.verdict = 'flag')::int AS verdict_flag,
		count(*) FILTER (WHERE o.verdict = 'block')::int AS verdict_block,
		coalesce(sum((o.cost_perf->>'tokens')::bigint),0) AS total_tokens,
		coalesce(sum((o.cost_perf->>'cost_usd')::double precision),0) AS total_cost_usd,
		avg((o.cost_perf->>'latency_ms')::double precision) AS avg_latency_ms,
		percentile_cont(0.95) WITHIN GROUP (ORDER BY (o.cost_perf->>'latency_ms')::double precision) AS p95_latency_ms,
		p.run_id, p.process_pass_rate, p.run_created_at
	FROM eval_observations o
	LEFT JOIN LATERAL (
		SELECT r.id AS run_id, (r.metrics->>'process_pass_rate')::double precision AS process_pass_rate,
			r.created_at AS run_created_at
		FROM eval_runs r
		WHERE r.resource_kind = o.resource_kind AND r.resource_id = o.resource_id
			AND r.status = 'succeeded' AND r.created_at >= $3 AND r.created_at <= $4
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT 1
	) p ON true
	WHERE ($1 = '' OR o.resource_kind = $1) AND ($2 = '' OR o.resource_id = $2)
		AND o.created_at >= $3 AND o.created_at <= $4
	GROUP BY o.resource_kind, o.resource_id, p.run_id, p.process_pass_rate, p.run_created_at)
SELECT resource_kind, resource_id, sample_count, rule_hits, retry_count, escalation_count, abandonment_count,
	verdict_pass, verdict_flag, verdict_block, total_tokens, total_cost_usd, avg_latency_ms, p95_latency_ms,
	run_id, process_pass_rate, run_created_at
FROM top
ORDER BY sample_count DESC, resource_id ASC
LIMIT $5`,
		filter.ResourceKind, filter.ResourceID, *filter.From, *filter.To, filter.Limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]domain.MonitorResourceSummary, 0, 8)
	index := make(map[string]int, 8)
	for rows.Next() {
		var (
			s               domain.MonitorResourceSummary
			kind            string
			ruleHits, retry int
			escalation      int
			abandonment     int
			vPass, vFlag    int
			vBlock          int
			totalTokens     int64
			totalCostUSD    float64
			avgLatency      *float64
			p95Latency      *float64
			runID           *string
			processRate     *float64
			runCreatedAt    *time.Time
		)
		if err := rows.Scan(&kind, &s.ResourceID, &s.SampleCount, &ruleHits, &retry, &escalation,
			&abandonment, &vPass, &vFlag, &vBlock, &totalTokens, &totalCostUSD, &avgLatency, &p95Latency,
			&runID, &processRate, &runCreatedAt); err != nil {
			return nil, nil, err
		}
		s.ResourceKind = domain.ResourceKind(kind)
		s.Behavior = domain.BehaviorStats{RuleHits: ruleHits, RetryCount: retry,
			EscalationCount: escalation, AbandonmentCount: abandonment,
			Verdict: domain.VerdictDistribution{Pass: vPass, Flag: vFlag, Block: vBlock}}
		s.Cost = domain.CostStats{TotalTokens: totalTokens, TotalCostUSD: totalCostUSD,
			AvgLatencyMS: avgLatency, P95LatencyMS: p95Latency}
		if runID != nil && processRate != nil && runCreatedAt != nil {
			utc := runCreatedAt.UTC()
			s.Process = &domain.ProcessBaseline{ProcessPassRate: *processRate, RunID: *runID,
				RunCreatedAt: utc}
		}
		index[monitorResourceKey(kind, s.ResourceID)] = len(items)
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return items, index, nil
}

// expandMonitorResourceDims 端点1 Q2：按 top 资源展开 judge 维度
// （与 run 侧 by_dimension「未出现维度不返回」一致）。
func expandMonitorResourceDims(ctx context.Context, tx pgx.Tx, filter port.MonitorFilter, items []domain.MonitorResourceSummary, index map[string]int) error {
	jrows, err := tx.Query(ctx, `
WITH top AS (
	SELECT o.resource_kind, o.resource_id
	FROM eval_observations o
	WHERE ($1 = '' OR o.resource_kind = $1) AND ($2 = '' OR o.resource_id = $2)
		AND o.created_at >= $3 AND o.created_at <= $4
	GROUP BY o.resource_kind, o.resource_id
	ORDER BY count(*) DESC, o.resource_id ASC
	LIMIT $5)
SELECT o.resource_kind, o.resource_id, j.value->>'dimension' AS dimension,
	avg((j.value->>'score')::double precision) AS avg_score,
	avg((j.value->>'confidence')::double precision) AS avg_confidence,
	count(*)::int AS samples
FROM eval_observations o, jsonb_array_elements(o.signals->'judge') j
WHERE (o.resource_kind, o.resource_id) IN (SELECT resource_kind, resource_id FROM top)
GROUP BY o.resource_kind, o.resource_id, j.value->>'dimension'
ORDER BY o.resource_kind, o.resource_id, j.value->>'dimension'`,
		filter.ResourceKind, filter.ResourceID, *filter.From, *filter.To, filter.Limit)
	if err != nil {
		return err
	}
	defer jrows.Close()
	for jrows.Next() {
		var kind, id, dimension string
		var avgScore, avgConf float64
		var samples int
		if err := jrows.Scan(&kind, &id, &dimension, &avgScore, &avgConf, &samples); err != nil {
			return err
		}
		pos, ok := index[monitorResourceKey(kind, id)]
		if !ok {
			continue
		}
		items[pos].Quality = append(items[pos].Quality, domain.QualityDim{
			Dimension: dimension, PassRate: avgScore, AvgScore: avgScore, AvgConfidence: avgConf, Samples: samples})
	}
	return jrows.Err()
}

// MonitorTrend 返回单资源窗口内按日桶聚合的四区趋势 + succeeded run 过程基线点
// （spec §4.2 端点 2）。桶为 UTC 日；桶内无观测则不出点（不返回 sample_count=0 假桶）。
// 扫描出的 timestamptz 统一转 UTC（spec 示例 bucket_at/run_created_at 为 Z 后缀）。
func (r *PgCenterQueryRepository) MonitorTrend(ctx context.Context, tenantID string, filter port.MonitorFilter) (domain.MonitorTrendSeries, error) {
	var result domain.MonitorTrendSeries
	err := r.tenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		series, bucketIndex, err := scanMonitorTrendBuckets(ctx, tx, filter)
		if err != nil {
			return err
		}
		result.Series = series
		if err := expandMonitorTrendDims(ctx, tx, filter, result.Series, bucketIndex); err != nil {
			return err
		}
		runs, err := scanMonitorTrendRuns(ctx, tx, filter)
		if err != nil {
			return err
		}
		result.Runs = runs
		// 按桶升序稳定排序（SQL group by 无顺序保证）。
		sort.Slice(result.Series, func(i, j int) bool { return result.Series[i].BucketAt.Before(result.Series[j].BucketAt) })
		return nil
	})
	result.ResourceKind = domain.ResourceKind(filter.ResourceKind)
	result.ResourceID = filter.ResourceID
	return result, wrapCenterQuery("monitor trend", err)
}

// scanMonitorTrendBuckets 端点2 Q1：按 UTC 日桶聚合四区观测统计。
func scanMonitorTrendBuckets(ctx context.Context, tx pgx.Tx, filter port.MonitorFilter) ([]domain.MonitorTrendPoint, map[time.Time]int, error) {
	rows, err := tx.Query(ctx, `
SELECT (date_trunc('day', o.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS bucket_at,
	count(*)::int AS sample_count,
	coalesce(sum(jsonb_array_length(o.signals->'rule')),0)::int AS rule_hits,
	count(*) FILTER (WHERE o.signals->'behavior'->>'retry' = 'true')::int AS retry_count,
	count(*) FILTER (WHERE o.signals->'behavior'->>'escalation' = 'true')::int AS escalation_count,
	count(*) FILTER (WHERE o.signals->'behavior'->>'abandonment' = 'true')::int AS abandonment_count,
	count(*) FILTER (WHERE o.verdict = 'pass')::int AS verdict_pass,
	count(*) FILTER (WHERE o.verdict = 'flag')::int AS verdict_flag,
	count(*) FILTER (WHERE o.verdict = 'block')::int AS verdict_block,
	coalesce(sum((o.cost_perf->>'tokens')::bigint),0) AS total_tokens,
	coalesce(sum((o.cost_perf->>'cost_usd')::double precision),0) AS total_cost_usd,
	avg((o.cost_perf->>'latency_ms')::double precision) AS avg_latency_ms,
	percentile_cont(0.95) WITHIN GROUP (ORDER BY (o.cost_perf->>'latency_ms')::double precision) AS p95_latency_ms
FROM eval_observations o
WHERE o.resource_kind = $1 AND o.resource_id = $2
	AND o.created_at >= $3 AND o.created_at <= $4
GROUP BY bucket_at`,
		filter.ResourceKind, filter.ResourceID, *filter.From, *filter.To)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	points := make([]domain.MonitorTrendPoint, 0, 16)
	index := make(map[time.Time]int, 16)
	for rows.Next() {
		var (
			bucket                                   time.Time
			ruleHits, retry, escalation, abandonment int
			vPass, vFlag, vBlock, sampleCount        int
			totalTokens                              int64
			totalCostUSD                             float64
			avgLatency                               *float64
			p95Latency                               *float64
		)
		if err := rows.Scan(&bucket, &sampleCount, &ruleHits, &retry, &escalation, &abandonment,
			&vPass, &vFlag, &vBlock, &totalTokens, &totalCostUSD, &avgLatency, &p95Latency); err != nil {
			return nil, nil, err
		}
		bucket = bucket.UTC()
		point := domain.MonitorTrendPoint{BucketAt: bucket, SampleCount: sampleCount,
			Quality: []domain.QualityDim{},
			Behavior: domain.BehaviorStats{RuleHits: ruleHits, RetryCount: retry,
				EscalationCount: escalation, AbandonmentCount: abandonment,
				Verdict: domain.VerdictDistribution{Pass: vPass, Flag: vFlag, Block: vBlock}},
			Cost: domain.CostStats{TotalTokens: totalTokens, TotalCostUSD: totalCostUSD,
				AvgLatencyMS: avgLatency, P95LatencyMS: p95Latency}}
		index[bucket] = len(points)
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return points, index, nil
}

// expandMonitorTrendDims 端点2 Q2：judge 维度按桶展开。
func expandMonitorTrendDims(ctx context.Context, tx pgx.Tx, filter port.MonitorFilter, series []domain.MonitorTrendPoint, bucketIndex map[time.Time]int) error {
	jrows, err := tx.Query(ctx, `
WITH obs AS (
	SELECT o.id, o.created_at, o.signals FROM eval_observations o
	WHERE o.resource_kind = $1 AND o.resource_id = $2 AND o.created_at >= $3 AND o.created_at <= $4)
SELECT (date_trunc('day', o.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS bucket_at,
	j.value->>'dimension' AS dimension,
	avg((j.value->>'score')::double precision) AS avg_score,
	avg((j.value->>'confidence')::double precision) AS avg_confidence,
	count(*)::int AS samples
FROM obs o, jsonb_array_elements(o.signals->'judge') j
GROUP BY bucket_at, j.value->>'dimension'`,
		filter.ResourceKind, filter.ResourceID, *filter.From, *filter.To)
	if err != nil {
		return err
	}
	defer jrows.Close()
	for jrows.Next() {
		var bucket time.Time
		var dimension string
		var avgScore, avgConf float64
		var samples int
		if err := jrows.Scan(&bucket, &dimension, &avgScore, &avgConf, &samples); err != nil {
			return err
		}
		bucket = bucket.UTC()
		pos, ok := bucketIndex[bucket]
		if !ok {
			continue
		}
		series[pos].Quality = append(series[pos].Quality, domain.QualityDim{
			Dimension: dimension, PassRate: avgScore, AvgScore: avgScore, AvgConfidence: avgConf, Samples: samples})
	}
	return jrows.Err()
}

// scanMonitorTrendRuns 端点2 Q3：窗口内该资源全部 succeeded run 过程点（升序）。
// metrics 无 process_pass_rate 的 run 跳过；始终返回非 nil 切片保证 `runs: []`。
func scanMonitorTrendRuns(ctx context.Context, tx pgx.Tx, filter port.MonitorFilter) ([]domain.RunProcessPoint, error) {
	rrows, err := tx.Query(ctx, `
SELECT r.id, (r.metrics->>'process_pass_rate')::double precision, r.created_at
FROM eval_runs r
WHERE r.resource_kind = $1 AND r.resource_id = $2 AND r.status = 'succeeded'
	AND r.created_at >= $3 AND r.created_at <= $4
ORDER BY r.created_at ASC, r.id ASC`,
		filter.ResourceKind, filter.ResourceID, *filter.From, *filter.To)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	runs := []domain.RunProcessPoint{}
	for rrows.Next() {
		var (
			runID string
			rate  *float64
			runAt time.Time
		)
		if err := rrows.Scan(&runID, &rate, &runAt); err != nil {
			return nil, err
		}
		if rate == nil {
			continue // 理论可达：该 run 未做过程断言（metrics 无该键），如实跳过
		}
		runs = append(runs, domain.RunProcessPoint{RunID: runID, ProcessPassRate: *rate, RunCreatedAt: runAt.UTC()})
	}
	if err := rrows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}
