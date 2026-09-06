package port

import (
	"context"
	"encoding/json"
	"time"
)

// PlatformValue is one stored platform-layer parameter value with audit
// metadata (platform_settings row).
type PlatformValue struct {
	Key       string
	Value     json.RawMessage
	UpdatedBy string
	UpdatedAt time.Time
}

// PlatformVersion is one immutable snapshot version of a platform config
// group. draft is the only editable status; published snapshots are
// read-only; archived is the auto-trimmed tail. BaseVersion records the
// production version in effect at Publish time (rollback then branches from
// a version that actually took effect). JSON tags drive the /admin version
// history response (snake_case, matching ParameterDefinition).
type PlatformVersion struct {
	ID         int64  `json:"id"`
	GroupKey   string `json:"group_key"`
	VersionSeq int    `json:"version_seq"`
	Status     string `json:"status"` // draft | published | archived
	// IsCurrent marks the version the production label points at. Deriving it
	// server-side avoids the frontend re-deriving "current" by string-comparing
	// snapshots, which is false under multiple groups (PlatformValues is a
	// cross-group flat map; snapshots are per-group).
	IsCurrent   bool                       `json:"is_current"`
	Snapshot    map[string]json.RawMessage `json:"snapshot"`
	BaseVersion *int64                     `json:"base_version_id,omitempty"`
	Message     string                     `json:"message"`
	CreatedBy   string                     `json:"created_by"`
	// CreatedByName 是 created_by 的可读名（display_name > github_login > 原文），
	// ListVersions 时 LEFT JOIN public.users 现算；system/未知 uuid 无命中则回退原文。
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
	// EvalState 是平台门禁对该版本的评测结论（spec §4.1.1：unknown|sentinel_failed|
	// sentinel_passed|anomaly_flag|anomaly_block|rollback_recommended|rollback_executed）。
	// 044 迁移已建列，P2 只接读路径；写路径 UpdateEvalState 已存在。JSON tag 无
	// omitempty：DB 列 NOT NULL，读回恒有值（未过门禁的历史行 = 'unknown'）。
	EvalState string `json:"eval_state"`
}

// PlatformStore persists platform-scope parameter values in the public
// platform_settings table. All SQL uses schema-qualified names (startup-path
// rule); this store is public-scope by nature and never routes through
// execTenant.
type PlatformStore interface {
	// GetValue returns the stored value for a key, or (false, nil) when the
	// key is absent (absent == unset == definition default applies).
	GetValue(ctx context.Context, key string) (json.RawMessage, bool, error)
	// SetValue upserts one key's value (merge semantics live in the service;
	// the store only ever touches the given key).
	SetValue(ctx context.Context, key string, value json.RawMessage, updatedBy string) error
	// GetAll returns every stored platform value keyed by registry key.
	GetAll(ctx context.Context) ([]PlatformValue, error)

	// GetSnapshot returns the current effective snapshot for a group: the
	// version the production label points at, decoded to key → JSONB value.
	// A missing label (no version ever published) is NOT an error: it means
	// every key is unset and definition defaults apply. Only DB failures
	// surface as errors (fail-closed: resolvers must not swallow them).
	GetSnapshot(ctx context.Context, groupKey string) (map[string]json.RawMessage, error)
	// CreateDraft allocates the next version_seq for the group and stores a
	// draft snapshot. Version_seq allocation is serialized per group (FOR
	// UPDATE) inside one transaction; a lost race maps to ErrConcurrentPublish.
	CreateDraft(ctx context.Context, groupKey string, snapshot map[string]json.RawMessage, message, createdBy string) (PlatformVersion, error)
	// Publish promotes a draft to published and moves the production/latest
	// labels onto it, recording base_version_id = the production version in
	// effect. One transaction. Trims the oldest published versions beyond
	// MaxPlatformConfigVersionsPerGroup.
	Publish(ctx context.Context, groupKey string, versionID int64, actor string) error
	// Rollback moves the production/latest labels onto a historical published
	// version. No new version is produced. One transaction.
	Rollback(ctx context.Context, groupKey string, targetVersionID int64, actor string) error
	// ListVersions returns the full version history for a group (newest seq
	// first), including each version's immutable snapshot — the version history
	// view and the diff against base_version_id both read from here.
	ListVersions(ctx context.Context, groupKey string) ([]PlatformVersion, error)

	// GetVersion returns one historical published version by group+version_seq
	// (the gate writes eval_state onto a version the observation anchored to).
	// Returns domain.ErrVersionNotFound when the version does not exist.
	GetVersion(ctx context.Context, groupKey string, versionSeq int64) (PlatformVersion, error)
	// UpdateEvalState records the gate's evaluation state on a version
	// (e.g. "rollback_recommended"). Returns domain.ErrVersionNotFound when the
	// version does not exist. eval_state_updated_at/by are stamped server-side.
	UpdateEvalState(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error
}
