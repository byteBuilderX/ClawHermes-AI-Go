package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	auditdomain "github.com/byteBuilderX/stratum/internal/audit/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// platformPool keeps the repository decoupled from *pgxpool.Pool for tests.
// Begin is only exercised by the transactional versioning methods; the legacy
// per-key methods stay single-query.
type platformPool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// PlatformRepository stores platform-scope parameter values in the public
// platform_settings table and the versioned platform config snapshots.
// Public-scope by nature: direct pool access with schema-qualified names
// (startup-path rule in migration-tenant.md), no execTenant routing.
type PlatformRepository struct {
	pool platformPool
}

func NewPlatformRepository(pool *pgxpool.Pool) *PlatformRepository {
	return &PlatformRepository{pool: pool}
}

func (r *PlatformRepository) GetValue(
	ctx context.Context,
	key string,
) (json.RawMessage, bool, error) {
	var raw []byte
	err := r.pool.QueryRow(ctx,
		`SELECT value FROM public.platform_settings WHERE key = $1`, key,
	).Scan(&raw)
	switch err {
	case nil:
		return json.RawMessage(raw), true, nil
	case pgx.ErrNoRows:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("platform repository: get %s: %w", key, err)
	}
}

func (r *PlatformRepository) SetValue(
	ctx context.Context,
	key string,
	value json.RawMessage,
	updatedBy string,
) error {
	if err := r.pool.QueryRow(ctx,
		`INSERT INTO public.platform_settings (key, value, updated_by)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = NOW()
		 RETURNING key`, key, string(value), updatedBy,
	).Scan(new(string)); err != nil {
		return fmt.Errorf("platform repository: set %s: %w", key, err)
	}
	return nil
}

func (r *PlatformRepository) GetAll(ctx context.Context) ([]port.PlatformValue, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT key, value, updated_by, updated_at FROM public.platform_settings`)
	if err != nil {
		return nil, fmt.Errorf("platform repository: list: %w", err)
	}
	defer rows.Close()

	var out []port.PlatformValue
	for rows.Next() {
		var (
			v    port.PlatformValue
			raw  []byte
			upAt time.Time
		)
		if err := rows.Scan(&v.Key, &raw, &v.UpdatedBy, &upAt); err != nil {
			return nil, fmt.Errorf("platform repository: scan: %w", err)
		}
		v.Value = json.RawMessage(raw)
		v.UpdatedAt = upAt
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform repository: rows iteration: %w", err)
	}
	return out, nil
}

// GetSnapshot returns the version the production label points at for a group,
// decoded to key → JSONB value. A missing label (no version published yet) is
// unset, not an error: definition defaults apply. DB failures propagate so
// resolvers can fail closed instead of silently degrading.
func (r *PlatformRepository) GetSnapshot(
	ctx context.Context,
	groupKey string,
) (map[string]json.RawMessage, error) {
	var raw []byte
	err := r.pool.QueryRow(ctx,
		`SELECT v.snapshot
		 FROM public.platform_config_versions v
		 JOIN public.platform_config_labels l ON l.version_id = v.id
		 WHERE l.group_key = $1 AND l.label = 'production'`,
		groupKey,
	).Scan(&raw)
	switch err {
	case nil:
	case pgx.ErrNoRows:
		return map[string]json.RawMessage{}, nil
	default:
		return nil, fmt.Errorf("platform repository: get snapshot %s: %w", groupKey, err)
	}
	snapshot := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("platform repository: get snapshot %s decode: %w", groupKey, err)
	}
	return snapshot, nil
}

// CreateDraft allocates the next version_seq under a per-group FOR UPDATE lock
// and stores a draft snapshot, all in one transaction. A lost race on the
// unique (group_key, version_seq) constraint maps to ErrConcurrentPublish.
func (r *PlatformRepository) CreateDraft(
	ctx context.Context,
	groupKey string,
	snapshot map[string]json.RawMessage,
	message, createdBy string,
) (port.PlatformVersion, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return port.PlatformVersion{}, fmt.Errorf("platform repository: create draft %s: begin: %w", groupKey, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockGroup(ctx, tx, groupKey); err != nil {
		return port.PlatformVersion{}, err
	}
	var nextSeq int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version_seq), 0) + 1
		 FROM public.platform_config_versions WHERE group_key = $1`,
		groupKey,
	).Scan(&nextSeq); err != nil {
		return port.PlatformVersion{}, fmt.Errorf("platform repository: create draft %s: next seq: %w", groupKey, err)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return port.PlatformVersion{}, fmt.Errorf("platform repository: create draft %s: encode: %w", groupKey, err)
	}

	var (
		id        int64
		createdAt time.Time
	)
	if err := tx.QueryRow(ctx,
		`INSERT INTO public.platform_config_versions
		   (group_key, version_seq, status, snapshot, message, created_by)
		 VALUES ($1, $2, 'draft', $3, $4, $5)
		 RETURNING id, created_at`,
		groupKey, nextSeq, string(encoded), message, createdBy,
	).Scan(&id, &createdAt); err != nil {
		if isUniqueViolation(err) {
			return port.PlatformVersion{}, fmt.Errorf("platform repository: create draft %s: %w", groupKey, domain.ErrConcurrentPublish)
		}
		return port.PlatformVersion{}, fmt.Errorf("platform repository: create draft %s: %w", groupKey, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return port.PlatformVersion{}, fmt.Errorf("platform repository: create draft %s: commit: %w", groupKey, err)
	}
	return port.PlatformVersion{
		ID:         id,
		GroupKey:   groupKey,
		VersionSeq: nextSeq,
		Status:     "draft",
		Snapshot:   snapshot,
		Message:    message,
		CreatedBy:  createdBy,
		CreatedAt:  createdAt,
	}, nil
}

// Publish promotes a draft to published, records base_version_id = the
// production version currently in effect, and moves the production/latest
// labels onto it — one transaction. Published versions beyond the retention
// cap are auto-archived (oldest first).
func (r *PlatformRepository) Publish(
	ctx context.Context,
	groupKey string,
	versionID int64,
	actor string,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("platform repository: publish %s: begin: %w", groupKey, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockGroup(ctx, tx, groupKey); err != nil {
		return err
	}

	// 目标版本快照 = After 投影（审计）；base_version = 当前 production 所指版本
	// （无则 NULL，Before 归一化为 '{}'）。draft 快照写入后不可变，一次读足。
	snapshot, base, before, err := loadVersionPair(ctx, tx, groupKey, versionID, "draft", domain.ErrVersionNotDraft)
	if err != nil {
		return fmt.Errorf("platform repository: publish %s: %w", groupKey, err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.platform_config_versions
		 SET status = 'published', base_version_id = $2
		 WHERE id = $1`, versionID, base); err != nil {
		return fmt.Errorf("platform repository: publish %s: promote: %w", groupKey, err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.platform_config_labels
		 SET version_id = $1, updated_by = $2, updated_at = NOW()
		 WHERE group_key = $3 AND label IN ('production', 'latest')`,
		versionID, actor, groupKey); err != nil {
		return fmt.Errorf("platform repository: publish %s: move labels: %w", groupKey, err)
	}

	if err := trimToRetentionLimit(ctx, tx, groupKey); err != nil {
		return fmt.Errorf("platform repository: publish %s: trim: %w", groupKey, err)
	}

	// 审计行与 label 挪动同一事务：Before = 发布前 production 快照，After = 新
	// 发布版本快照（快照写入前已脱敏，投影不携带凭据）。
	if err := insertPlatformConfigAuditTx(ctx, tx, &auditdomain.ResourceChangeAuditEvent{
		ResourceKind: auditdomain.ResourceKindPlatformConfig,
		ResourceID:   groupKey,
		Operation:    auditdomain.ChangeOpPublish,
		ActorID:      actor,
		Before:       json.RawMessage(before),
		After:        json.RawMessage(snapshot),
	}); err != nil {
		return fmt.Errorf("platform repository: publish %s: audit: %w", groupKey, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("platform repository: publish %s: commit: %w", groupKey, err)
	}
	return nil
}

// Rollback moves the production/latest labels onto a historical published
// version. No new version is produced — rollback never mutates snapshots.
func (r *PlatformRepository) Rollback(
	ctx context.Context,
	groupKey string,
	targetVersionID int64,
	actor string,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("platform repository: rollback %s: begin: %w", groupKey, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockGroup(ctx, tx, groupKey); err != nil {
		return err
	}

	// 目标版本快照 = After 投影（审计）；当前 production 快照 = Before。
	snapshot, _, before, err := loadVersionPair(ctx, tx, groupKey, targetVersionID, "published", domain.ErrVersionNotPublished)
	if err != nil {
		return fmt.Errorf("platform repository: rollback %s: %w", groupKey, err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE public.platform_config_labels
		 SET version_id = $1, updated_by = $2, updated_at = NOW()
		 WHERE group_key = $3 AND label IN ('production', 'latest')`,
		targetVersionID, actor, groupKey); err != nil {
		return fmt.Errorf("platform repository: rollback %s: move labels: %w", groupKey, err)
	}

	// 审计行：Before = 回滚前 production 快照，After = 目标版本快照。回滚不发
	// 新版本，审计行是「谁在何时把 production 指回哪个版本」的唯一操作者记录。
	if err := insertPlatformConfigAuditTx(ctx, tx, &auditdomain.ResourceChangeAuditEvent{
		ResourceKind: auditdomain.ResourceKindPlatformConfig,
		ResourceID:   groupKey,
		Operation:    auditdomain.ChangeOpRollback,
		ActorID:      actor,
		Before:       json.RawMessage(before),
		After:        json.RawMessage(snapshot),
	}); err != nil {
		return fmt.Errorf("platform repository: rollback %s: audit: %w", groupKey, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("platform repository: rollback %s: commit: %w", groupKey, err)
	}
	return nil
}

// loadVersionPair reads a version's immutable snapshot (validated against the
// required status) plus the current production snapshot — the Before/After
// projection pair Publish and Rollback both need for the audit row, in one
// helper so each op carries a single load + status-check branch. A missing
// version maps to ErrVersionNotFound; a status mismatch maps to the caller's
// invalidStatusErr. loadProduction normalizes a missing production label to
// '{}', so callers never branch on an unset Before.
func loadVersionPair(
	ctx context.Context,
	tx pgx.Tx,
	groupKey string,
	versionID int64,
	requireStatus string,
	invalidStatusErr error,
) (snapshot []byte, base *int64, before []byte, err error) {
	var status string
	err = tx.QueryRow(ctx,
		`SELECT status, snapshot FROM public.platform_config_versions WHERE id = $1 AND group_key = $2`,
		versionID, groupKey,
	).Scan(&status, &snapshot)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil, nil, domain.ErrVersionNotFound
	case err != nil:
		return nil, nil, nil, err
	}
	if status != requireStatus {
		return nil, nil, nil, invalidStatusErr
	}
	base, before, err = loadProduction(ctx, tx, groupKey)
	if err != nil {
		return nil, nil, nil, err
	}
	return snapshot, base, before, nil
}

// loadProduction reads the version the production label points at inside the
// caller's transaction: the base_version_id to record on publish and the
// Before projection for the audit row. A missing production label (nothing
// published yet) is unset, normalized to an empty snapshot so callers never
// branch on it.
func loadProduction(
	ctx context.Context,
	tx pgx.Tx,
	groupKey string,
) (base *int64, snapshot []byte, err error) {
	err = tx.QueryRow(ctx,
		`SELECT v.id, v.snapshot
		 FROM public.platform_config_labels l
		 JOIN public.platform_config_versions v ON v.id = l.version_id
		 WHERE l.group_key = $1 AND l.label = 'production'`,
		groupKey,
	).Scan(&base, &snapshot)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, []byte(`{}`), nil
	case err != nil:
		return nil, nil, err
	default:
		return base, snapshot, nil
	}
}

// insertPlatformConfigAuditTx writes a platform-config publish/rollback audit
// row in the same public transaction as the label move, so the compliance
// evidence and the state transition are one atomic unit. Snapshots are already
// de-sensitized at write time, so the projections carry no credentials. The
// actor tenant is nil — platform config is a global-admin, public-scope op.
func insertPlatformConfigAuditTx(
	ctx context.Context,
	tx pgx.Tx,
	ev *auditdomain.ResourceChangeAuditEvent,
) error {
	if ev == nil {
		return nil
	}
	ev = ev.Normalized()
	eventID := ev.EventID
	if eventID == "" {
		eventID = uuid.Must(uuid.NewV7()).String()
	}
	_, err := tx.Exec(ctx, auditdomain.PlatformChangeAuditInsertSQL,
		eventID, ev.ResourceKind, ev.ResourceID, ev.Operation, ev.ActorID, nil,
		ev.ActorType, ev.Source, ev.ProposalID, ev.Before, ev.After)
	if err != nil {
		return fmt.Errorf("insert platform config audit: %w", err)
	}
	return nil
}

// ListVersions returns the full version history for a group, newest seq first,
// each row with its immutable snapshot (the version history view diffs against
// base_version_id). Unknown groups return an empty slice — the caller maps an
// empty result to "no versions yet", not an error.
func (r *PlatformRepository) ListVersions(
	ctx context.Context,
	groupKey string,
) ([]port.PlatformVersion, error) {
	// created_by_name 由 LEFT JOIN public.users 现算（display_name > github_login >
	// 原文），与 iam actor_name_resolver 同语义；system/未知 uuid 无命中则回退原文。
	rows, err := r.pool.Query(ctx,
		`SELECT v.id, v.group_key, v.version_seq, v.status, v.eval_state, v.snapshot, v.base_version_id,
		        v.message, v.created_by, v.created_at,
		        (prod.version_id IS NOT NULL) AS is_current,
		        COALESCE(u.display_name, u.github_login, v.created_by) AS created_by_name
		 FROM public.platform_config_versions v
		 LEFT JOIN public.users u ON u.id::text = v.created_by
		 LEFT JOIN public.platform_config_labels prod
		   ON prod.group_key = v.group_key AND prod.label = 'production' AND prod.version_id = v.id
		 WHERE v.group_key = $1
		 ORDER BY v.version_seq DESC`,
		groupKey,
	)
	if err != nil {
		return nil, fmt.Errorf("platform repository: list versions %s: %w", groupKey, err)
	}
	defer rows.Close()

	var out []port.PlatformVersion
	for rows.Next() {
		var (
			v         port.PlatformVersion
			snapshot  []byte
			base      *int64
			createdAt time.Time
		)
		if err := rows.Scan(&v.ID, &v.GroupKey, &v.VersionSeq, &v.Status, &v.EvalState,
			&snapshot, &base, &v.Message, &v.CreatedBy, &createdAt, &v.IsCurrent, &v.CreatedByName); err != nil {
			return nil, fmt.Errorf("platform repository: scan version: %w", err)
		}
		if err := json.Unmarshal(snapshot, &v.Snapshot); err != nil {
			return nil, fmt.Errorf("platform repository: decode version %d snapshot: %w", v.ID, err)
		}
		v.BaseVersion = base
		v.CreatedAt = createdAt
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform repository: list versions %s: rows iteration: %w", groupKey, err)
	}
	return out, nil
}

// GetVersion 读一个历史版本的元数据（门禁写 eval_state 前校验存在性 / 取 seq 用）。
// 按 group_key + version_seq 寻址；命中 0 行 → ErrVersionNotFound。
func (r *PlatformRepository) GetVersion(
	ctx context.Context,
	groupKey string,
	versionSeq int64,
) (port.PlatformVersion, error) {
	const q = `SELECT id, group_key, version_seq, status, eval_state, snapshot
		FROM public.platform_config_versions WHERE group_key = $1 AND version_seq = $2`
	var (
		v        port.PlatformVersion
		snapshot []byte
	)
	if err := r.pool.QueryRow(ctx, q, groupKey, versionSeq).
		Scan(&v.ID, &v.GroupKey, &v.VersionSeq, &v.Status, &v.EvalState, &snapshot); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return port.PlatformVersion{}, domain.ErrVersionNotFound
		}
		return port.PlatformVersion{}, fmt.Errorf("get platform version %s seq %d: %w", groupKey, versionSeq, err)
	}
	if len(snapshot) > 0 {
		if err := json.Unmarshal(snapshot, &v.Snapshot); err != nil {
			return port.PlatformVersion{}, fmt.Errorf(
				"get platform version %s seq %d: decode snapshot: %w", groupKey, versionSeq, err)
		}
	}
	return v, nil
}

// UpdateEvalState 写门禁状态（分层门禁 P1）：命中 0 行说明版本不存在 → ErrVersionNotFound。
// 注入的 pool 无 Exec，沿用 SetValue 的 QueryRow+RETURNING 单语句写模式；eval_state 三列
// （eval_state/eval_state_updated_at/eval_state_updated_by）由 044 迁移提供。
func (r *PlatformRepository) UpdateEvalState(
	ctx context.Context,
	groupKey string,
	versionSeq int64,
	state, actor string,
) error {
	err := r.pool.QueryRow(ctx,
		`UPDATE public.platform_config_versions
		 SET eval_state = $3, eval_state_updated_at = NOW(), eval_state_updated_by = $4
		 WHERE group_key = $1 AND version_seq = $2
		 RETURNING group_key`,
		groupKey, versionSeq, state, actor,
	).Scan(new(string))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrVersionNotFound
	}
	if err != nil {
		return fmt.Errorf("update platform version %s seq %d eval_state: %w", groupKey, versionSeq, err)
	}
	return nil
}

// lockGroup serializes all per-group version/label operations on one FOR
// UPDATE row so MAX(version_seq)+1 and label moves never race.
func lockGroup(ctx context.Context, tx pgx.Tx, groupKey string) error {
	if err := tx.QueryRow(ctx,
		`SELECT group_key FROM public.platform_config_groups WHERE group_key = $1 FOR UPDATE`,
		groupKey,
	).Scan(new(string)); errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("platform repository: lock group %s: %w", groupKey, domain.ErrGroupNotFound)
	} else if err != nil {
		return fmt.Errorf("platform repository: lock group %s: %w", groupKey, err)
	}
	return nil
}

// trimToRetentionLimit archives the oldest published versions once the
// group's published count exceeds MaxPlatformConfigVersionsPerGroup. The cap
// applies to active published versions only: archived rows are the append-only
// history (the audit view), so counting them here would make the excess keep
// growing and eventually archive every published version including the one the
// production label points at. The version just published is the newest seq, so
// it is never the trim victim.
func trimToRetentionLimit(ctx context.Context, tx pgx.Tx, groupKey string) error {
	var total int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM public.platform_config_versions
		 WHERE group_key = $1 AND status = 'published'`,
		groupKey,
	).Scan(&total); err != nil {
		return fmt.Errorf("count published: %w", err)
	}
	over := total - constants.MaxPlatformConfigVersionsPerGroup
	if over <= 0 {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE public.platform_config_versions SET status = 'archived'
		 WHERE id IN (
			SELECT id FROM public.platform_config_versions
			WHERE group_key = $1 AND status = 'published'
			ORDER BY version_seq ASC
			LIMIT $2
		 )`, groupKey, over); err != nil {
		return fmt.Errorf("archive oldest: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a PostgreSQL 23505 violation
// (e.g. the (group_key, version_seq) race).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
