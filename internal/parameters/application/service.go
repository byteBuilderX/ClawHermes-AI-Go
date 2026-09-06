package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/domain/port"
)

// Service is the parameters application facade consumed by handlers and by
// sibling contexts through the wiring ACL. It enforces per-key validation and
// merge-write semantics. Platform values are global settings for
// platform-scope keys only; resource-scope keys are configured at the
// resource layer and rejected from platform writes.
type Service struct {
	registry *domain.ParametersRegistry
	store    port.PlatformStore
	resolver *Resolver
}

func NewService(registry *domain.ParametersRegistry, store port.PlatformStore) *Service {
	return &Service{
		registry: registry,
		store:    store,
		resolver: NewResolver(registry, store),
	}
}

// Registry exposes the code-level definitions to consumers (schema-driven
// rendering, evaluation search space).
func (s *Service) Registry() *domain.ParametersRegistry { return s.registry }

// Resolver returns the two-level fallback resolver for execution paths.
func (s *Service) Resolver() *Resolver { return s.resolver }

// Schema returns all parameter definitions for schema-driven rendering.
func (s *Service) Schema() []domain.ParameterDefinition { return s.registry.Schema() }

// ValidateResourceValues validates resource-scope declared sampling values
// against registry definitions, mapping bare JSONB keys (temperature,
// max_tokens, ...) through EvaluationKeys. Resource keys that deliberately
// carry no EvaluationKeys alias (e.g. agent.compaction_temperature — kept out
// of the evaluation search space) fall back to a registry-key short-name match.
// Unknown keys and out-of-bounds values return an error. Callers skip 0=unset
// values before invoking — an explicit zero is indistinguishable from an
// absent key.
func (s *Service) ValidateResourceValues(declared map[string]any) error {
	for bareKey, value := range declared {
		key, ok := s.registry.KeyForEvaluation(bareKey)
		if !ok {
			key, ok = s.registry.KeyByShortName(bareKey)
		}
		if !ok {
			return fmt.Errorf("unknown parameter %s", bareKey)
		}
		def, ok := s.registry.Get(key)
		if !ok {
			return fmt.Errorf("parameter %s not registered", bareKey)
		}
		if err := def.Validate(value); err != nil {
			return err
		}
	}
	return nil
}

// PlatformValues returns the current effective platform-layer values for
// platform-scope keys: the production snapshot value when present, otherwise
// the definition default. Each group snapshot is read once per call (cache
// shared across keys of the same group). Resource-scope keys are never
// returned — resources own their required configuration and platform settings
// hold no resource defaults. Absent numeric-0 defaults are omitted so the
// frontend sees "unset". A DB failure propagates (fail-closed: the management
// surface must not show a half-merged view).
func (s *Service) PlatformValues(ctx context.Context) (map[string]any, error) {
	cache := make(map[string]map[string]json.RawMessage)
	out := make(map[string]any, len(s.registry.Schema()))
	for _, def := range s.registry.Schema() {
		if def.Scope != domain.ScopePlatform {
			continue
		}
		snapshot, err := s.groupSnapshot(ctx, def.GroupKey, cache)
		if err != nil {
			return nil, fmt.Errorf("parameters service: list platform values: %w", err)
		}
		if raw, ok := snapshot[def.Key]; ok {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, fmt.Errorf("parameters service: decode %s: %w", def.Key, err)
			}
			out[def.Key] = value
			continue
		}
		if !isUnset(def.Default) {
			out[def.Key] = def.Default
		}
	}
	return out, nil
}

// SetPlatformValues applies merge semantics through the versioned pipeline:
// only keys present in input are written (a legacy client PUT can never wipe
// stored values it does not know). Every key must be platform-scope and pass
// its definition's validation; resource-scope keys are rejected fail-closed.
// For each affected group one draft is derived from the current production
// snapshot, then published — the legacy single-step PUT now produces a version
// record (audit/trace attribution). updatedBy is the actor on the version rows.
func (s *Service) SetPlatformValues(
	ctx context.Context,
	values map[string]any,
	updatedBy string,
) error {
	if len(values) == 0 {
		return nil
	}
	if updatedBy == "" {
		updatedBy = "api"
	}
	byGroup := make(map[string]map[string]json.RawMessage)
	for key, rawValue := range values {
		groupKey, encoded, err := s.normalizePlatformKey(key, rawValue)
		if err != nil {
			return err
		}
		if byGroup[groupKey] == nil {
			byGroup[groupKey] = make(map[string]json.RawMessage)
		}
		byGroup[groupKey][key] = encoded
	}
	for groupKey, changes := range byGroup {
		if err := s.applyGroupChanges(ctx, groupKey, changes, "update via /admin/parameters", updatedBy); err != nil {
			return err
		}
	}
	return nil
}

// CreateDraft validates platform-scope values for one group, merges them over
// the current production snapshot and stores a draft. The draft is the only
// editable state; publishing promotes it. Sensitive values are masked before
// the snapshot is persisted (platform_config_versions is not a credential
// store).
func (s *Service) CreateDraft(
	ctx context.Context,
	groupKey string,
	values map[string]any,
	message, actor string,
) (port.PlatformVersion, error) {
	if actor == "" {
		actor = "api"
	}
	changes := make(map[string]json.RawMessage, len(values))
	for key, rawValue := range values {
		gk, encoded, err := s.normalizePlatformKey(key, rawValue)
		if err != nil {
			return port.PlatformVersion{}, err
		}
		if gk != groupKey {
			return port.PlatformVersion{}, &domain.ErrInvalidParameter{
				Key: key,
				Err: fmt.Errorf("parameter %s belongs to group %q, not %q", key, gk, groupKey),
			}
		}
		changes[key] = encoded
	}
	changes = s.sanitize(changes)
	snapshot, err := s.store.GetSnapshot(ctx, groupKey)
	if err != nil {
		return port.PlatformVersion{}, fmt.Errorf("parameters service: snapshot %s: %w", groupKey, err)
	}
	if snapshot == nil {
		snapshot = make(map[string]json.RawMessage)
	}
	for key, encoded := range changes {
		snapshot[key] = encoded
	}
	return s.store.CreateDraft(ctx, groupKey, snapshot, message, actor)
}

// Publish promotes a draft of the group to published (moves production/latest
// labels, records base_version_id, trims to the retention cap). One store
// transaction.
func (s *Service) Publish(ctx context.Context, groupKey string, versionID int64, actor string) error {
	if actor == "" {
		actor = "api"
	}
	return s.store.Publish(ctx, groupKey, versionID, actor)
}

// Rollback moves the production/latest labels onto a historical published
// version. No new version is produced — rollback never mutates snapshots.
func (s *Service) Rollback(ctx context.Context, groupKey string, versionID int64, actor string) error {
	if actor == "" {
		actor = "api"
	}
	return s.store.Rollback(ctx, groupKey, versionID, actor)
}

// Versions returns the full version history for a group (newest first) for the
// versioned configuration view. Each row carries its immutable snapshot so the
// UI can diff against base_version_id.
func (s *Service) Versions(ctx context.Context, groupKey string) ([]port.PlatformVersion, error) {
	return s.store.ListVersions(ctx, groupKey)
}

// GetVersion 转发：按 group+version_seq 读历史版本元数据（门禁/对照链路用）。
func (s *Service) GetVersion(ctx context.Context, groupKey string, versionSeq int64) (port.PlatformVersion, error) {
	return s.store.GetVersion(ctx, groupKey, versionSeq)
}

// UpdateEvalState 转发：给平台版本写门禁状态（actor 空默认 "api"，与 Publish/Rollback 一致）。
func (s *Service) UpdateEvalState(ctx context.Context, groupKey string, versionSeq int64, state, actor string) error {
	if actor == "" {
		actor = "api"
	}
	return s.store.UpdateEvalState(ctx, groupKey, versionSeq, state, actor)
}

// groupSnapshot returns the production snapshot for a group, caching reads per
// call so keys of the same group share one GetSnapshot.
func (s *Service) groupSnapshot(
	ctx context.Context,
	groupKey string,
	cache map[string]map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	if snapshot, ok := cache[groupKey]; ok {
		return snapshot, nil
	}
	snapshot, err := s.store.GetSnapshot(ctx, groupKey)
	if err != nil {
		return nil, err
	}
	cache[groupKey] = snapshot
	return snapshot, nil
}

// normalizePlatformKey validates and encodes one platform-scope input value,
// returning its group key. Unknown and resource-scope keys are rejected
// fail-closed with ErrInvalidParameter.
func (s *Service) normalizePlatformKey(key string, rawValue any) (string, json.RawMessage, error) {
	def, ok := s.registry.Get(key)
	if !ok {
		return "", nil, &domain.ErrInvalidParameter{Key: key, Err: fmt.Errorf("unknown parameter %s", key)}
	}
	if def.Scope != domain.ScopePlatform {
		return "", nil, &domain.ErrInvalidParameter{
			Key: key,
			Err: fmt.Errorf(
				"parameter %s is %s-scope: resource parameters are configured at the resource layer, not as platform defaults",
				key, def.Scope,
			),
		}
	}
	value, err := def.Normalize(rawValue)
	if err != nil {
		return "", nil, &domain.ErrInvalidParameter{Key: key, Err: err}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", nil, fmt.Errorf("parameters service: encode %s: %w", key, err)
	}
	return def.GroupKey, encoded, nil
}

// applyGroupChanges merges normalized changes into a new draft of the group's
// current production snapshot and publishes it in one versioned step. Only the
// input keys are touched — everything else is preserved (merge semantics).
func (s *Service) applyGroupChanges(
	ctx context.Context,
	groupKey string,
	changes map[string]json.RawMessage,
	message, actor string,
) error {
	changes = s.sanitize(changes)
	snapshot, err := s.store.GetSnapshot(ctx, groupKey)
	if err != nil {
		return fmt.Errorf("parameters service: snapshot %s: %w", groupKey, err)
	}
	if snapshot == nil {
		snapshot = make(map[string]json.RawMessage)
	}
	for key, encoded := range changes {
		snapshot[key] = encoded
	}
	draft, err := s.store.CreateDraft(ctx, groupKey, snapshot, message, actor)
	if err != nil {
		return fmt.Errorf("parameters service: create draft %s: %w", groupKey, err)
	}
	if err := s.store.Publish(ctx, groupKey, draft.ID, actor); err != nil {
		return fmt.Errorf("parameters service: publish %s: %w", groupKey, err)
	}
	return nil
}

// sanitize masks Sensitive parameter values with a SHA-256 fingerprint before
// a snapshot is persisted: platform_config_versions is not a credential store.
// An already-fingerprinted value (sha256: prefix) passes through untouched so
// re-saving a masked draft is idempotent. No current platform parameter is
// Sensitive (#420 guards the judge prompt); the hook enforces the invariant
// for future additions.
func (s *Service) sanitize(changes map[string]json.RawMessage) map[string]json.RawMessage {
	for key, raw := range changes {
		def, ok := s.registry.Get(key)
		if !ok || !def.Sensitive {
			continue
		}
		// 幂等守卫：已指纹化的值（"sha256:" 前缀，8 字节）原样透传。raw[:7] 只
		// 切 7 字节恒不等，幂等路径会变成死代码，故必须取 [:8] 且 len >= 8。
		if len(raw) >= 8 && string(raw[:8]) == `"sha256:` {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%v", value)))
		changes[key] = json.RawMessage(fmt.Sprintf(`"sha256:%x"`, sum[:]))
	}
	return changes
}
