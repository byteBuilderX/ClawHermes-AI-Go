package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// stubGateStore 内存版 GateStore（QueryWindow 预置证据；AppendAction 收台账）。
// queryErr/appendErr 分开注入：QueryWindow 失败与台账写失败是不同 fail-open 路径，
// 共用同一 err 无法独立断言。
type stubGateStore struct {
	evidence  domain.GateEvidence
	actions   []domain.GateActionRecord
	queryErr  error // QueryWindow 失败注入
	appendErr error // AppendAction 失败注入
}

func (s *stubGateStore) AppendAction(_ context.Context, _ string, rec domain.GateActionRecord) error {
	if s.appendErr != nil {
		return s.appendErr
	}
	s.actions = append(s.actions, rec)
	return nil
}

func (s *stubGateStore) QueryWindow(context.Context, string, domain.GateTarget, time.Time) (domain.GateEvidence, error) {
	return s.evidence, s.queryErr
}

// stubGatePolicy 固定返回策略；err 非 nil 模拟解析失败。
type stubGatePolicy struct {
	policy domain.GatePolicy
	err    error
}

func (s *stubGatePolicy) Resolve(context.Context, domain.GateTarget) (domain.GatePolicy, error) {
	return s.policy, s.err
}

// stubPlatformOps 记录平台 eval_state 写回；calls 计尝试次数（失败路径也计数，
// 便于断言写回确实被尝试过），states 只收成功写入的 state。
type stubPlatformOps struct {
	states []string
	calls  int
	err    error
}

func (s *stubPlatformOps) UpdateEvalState(_ context.Context, _ string, _ int64, state, _ string) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	s.states = append(s.states, state)
	return nil
}

// stubApprovals 记录人工审批请求；approvalID 为预置返回 id，err 模拟请求失败
// （请求先记录、err 再决定返回值，便于断言失败路径确实到达 requestApproval）。
type stubApprovals struct {
	requests   []domain.GateActionRecord
	approvalID string
	err        error
}

func (s *stubApprovals) RequestRollbackApproval(_ context.Context, _ string, rec domain.GateActionRecord) (string, error) {
	s.requests = append(s.requests, rec)
	if s.err != nil {
		return "", s.err
	}
	return s.approvalID, nil
}

// stubResourceRollback 记录资源自动回滚执行；err 模拟执行失败。
type stubResourceRollback struct {
	rollbacks []domain.GateTarget
	err       error
}

func (s *stubResourceRollback) Rollback(_ context.Context, _ string, target domain.GateTarget, _, _, _ string) error {
	s.rollbacks = append(s.rollbacks, target)
	if s.err != nil {
		return s.err
	}
	return nil
}

// gateObs 构造一条平台源观测（组=agent，seq=2）。
func gateObs() domain.EvalObservation {
	return domain.EvalObservation{
		Resource: domain.ObservationResourceRef{Kind: "agent", ResourceID: "agent-1"},
		Param: domain.ParamVersion{
			Source:   domain.ParamSourcePlatform,
			Platform: domain.PlatformParamVersion{GroupKey: "agent", VersionSeq: 2},
		},
	}
}

// gateResourceObs 构造一条资源源观测（kind=skill、resourceID=s1、ref=rev-9 已执行
// revision、version=canary-v1 变体标签）。Ref 与 Version 分离：台账 RevisionID 落 Ref
// （与生产 buildObservation 的填充一致），绝不落变体标签。
func gateResourceObs() domain.EvalObservation {
	return domain.EvalObservation{
		Resource: domain.ObservationResourceRef{Kind: "skill", ResourceID: "s1"},
		Param: domain.ParamVersion{
			Source:   domain.ParamSourceResource,
			Resource: domain.ResourceParamVersion{Ref: "rev-9", Version: "canary-v1"},
		},
	}
}

func fixedNow() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }

func newTestGate(deps GateServiceDeps, start time.Time) *GateService {
	s := NewGateService(deps)
	s.now = func() time.Time { return start }
	return s
}

func TestHandleObservationRoutesRollbackManualToPlatformWriteback(t *testing.T) {
	ctx := context.Background()
	store := &stubGateStore{evidence: domain.GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin}}
	platform := &stubPlatformOps{}
	svc := newTestGate(GateServiceDeps{
		Cfg:      func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
		Repo:     store,
		Policy:   &stubGatePolicy{policy: domain.GatePolicy{Scope: domain.ScopePlatform, RollbackSupported: true}},
		Platform: platform,
	}, fixedNow())

	if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatalf("HandleObservation error = %v", err)
	}
	if len(platform.states) != 1 || platform.states[0] != "rollback_recommended" {
		t.Fatalf("platform writeback = %v, want [rollback_recommended]", platform.states)
	}
	if len(store.actions) != 1 {
		t.Fatalf("ledger actions = %d, want 1", len(store.actions))
	}
	rec := store.actions[0]
	if rec.Decision != domain.GateRollbackManual || rec.Action != "rollback_recommended" {
		t.Fatalf("ledger decision/action = %q/%q, want rollback_manual/rollback_recommended", rec.Decision, rec.Action)
	}
	if rec.Target.Scope != domain.ScopePlatform || rec.Target.GroupKey != "agent" || rec.Target.VersionSeq != 2 {
		t.Fatalf("ledger target = %+v, want platform agent seq 2", rec.Target)
	}
}

func TestHandleObservationDisabledOrCleanSkips(t *testing.T) {
	ctx := context.Background()
	disabled := newTestGate(GateServiceDeps{
		Cfg:    func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: false} },
		Repo:   &stubGateStore{},
		Policy: &stubGatePolicy{},
	}, fixedNow())
	if err := disabled.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatalf("disabled error = %v", err)
	}

	clean := &stubGateStore{} // evidence 全零 → none
	svc := newTestGate(GateServiceDeps{
		Cfg:    func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
		Repo:   clean,
		Policy: &stubGatePolicy{policy: domain.GatePolicy{Scope: domain.ScopePlatform, RollbackSupported: true}},
	}, fixedNow())
	if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatalf("clean error = %v", err)
	}
	if len(clean.actions) != 0 {
		t.Fatalf("clean window must not append ledger, got %d", len(clean.actions))
	}
}

func TestHandleObservationGateNilRepoDoesNotPanic(t *testing.T) {
	// fail-open：Repo nil（未装配证据源）→ HandleObservation 跳过评估，不 panic。
	ctx := context.Background()
	svc := newTestGate(GateServiceDeps{
		Cfg:    func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
		Repo:   nil,
		Policy: &stubGatePolicy{policy: domain.GatePolicy{Scope: domain.ScopeResource, RollbackSupported: true}},
	}, fixedNow())
	if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatalf("error = %v", err)
	}
}

func TestHandleObservationCooldownSuppressesRapidRepeats(t *testing.T) {
	ctx := context.Background()
	store := &stubGateStore{evidence: domain.GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin}}
	platform := &stubPlatformOps{}
	svc := newTestGate(GateServiceDeps{
		Cfg:      func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
		Repo:     store,
		Policy:   &stubGatePolicy{policy: domain.GatePolicy{Scope: domain.ScopePlatform, RollbackSupported: true}},
		Platform: platform,
	}, fixedNow())
	if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatal(err)
	}
	// 同 target 仍在冷却期内 → 跳过。
	if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
		t.Fatal(err)
	}
	if len(store.actions) != 1 {
		t.Fatalf("cooldown not applied, ledger actions = %d, want 1", len(store.actions))
	}
}

func TestGateTargetForObservation(t *testing.T) {
	cases := []struct {
		name   string
		obs    domain.EvalObservation
		wantOK bool
		want   domain.GateTarget
	}{
		{
			name: "platform anchor only maps to platform group target",
			obs: domain.EvalObservation{
				Resource: domain.ObservationResourceRef{Kind: "agent", ResourceID: "a1"},
				Param: domain.ParamVersion{Source: domain.ParamSourceUnknown,
					Platform: domain.PlatformParamVersion{GroupKey: "agent", VersionSeq: 3}},
			},
			wantOK: true,
			want:   domain.GateTarget{Scope: domain.ScopePlatform, GroupKey: "agent", VersionSeq: 3},
		},
		{
			// 非变体产品资源观测（执行 revision 在 Ref、Version 空）→ 资源目标，
			// RevisionID 必须落 Ref（修复前该 case 漏判为非锚点/误判平台）。
			name: "resource ref without variant anchors resource target",
			obs: domain.EvalObservation{
				Resource: domain.ObservationResourceRef{Kind: "skill", ResourceID: "s1"},
				Param: domain.ParamVersion{Source: domain.ParamSourceResource,
					Resource: domain.ResourceParamVersion{Ref: "rev-9"}},
			},
			wantOK: true,
			want:   domain.GateTarget{Scope: domain.ScopeResource, Kind: "skill", ResourceID: "s1", RevisionID: "rev-9"},
		},
		{
			// 变体观测携带 Ref 与 Version（canary-v1）→ RevisionID 落 Ref，绝不落变体标签。
			name: "resource revision is the executed ref not the variant label",
			obs: domain.EvalObservation{
				Resource: domain.ObservationResourceRef{Kind: "skill", ResourceID: "s1"},
				Param: domain.ParamVersion{Source: domain.ParamSourceResource,
					Resource: domain.ResourceParamVersion{Ref: "rev-9", Version: "canary-v1"}},
			},
			wantOK: true,
			want:   domain.GateTarget{Scope: domain.ScopeResource, Kind: "skill", ResourceID: "s1", RevisionID: "rev-9"},
		},
		{
			// 只有变体标签、无已执行 revision（Ref 空）→ 非锚点（修复前被误判资源并
			// 把变体标签当 RevisionID 落库）。
			name: "resource variant without executed ref is not anchored",
			obs: domain.EvalObservation{
				Resource: domain.ObservationResourceRef{Kind: "skill", ResourceID: "s1"},
				Param: domain.ParamVersion{Source: domain.ParamSourceResource,
					Resource: domain.ResourceParamVersion{Version: "canary-v1"}},
			},
			wantOK: false,
		},
		{
			// 双锚点（平台+资源 Ref，Source both）→ 资源优先（回滚被测资源以恢复行为）。
			name: "both platform and resource ref anchors resource target",
			obs: domain.EvalObservation{
				Resource: domain.ObservationResourceRef{Kind: "agent", ResourceID: "a1"},
				Param: domain.ParamVersion{Source: domain.ParamSourceBoth,
					Platform: domain.PlatformParamVersion{GroupKey: "agent", VersionSeq: 3},
					Resource: domain.ResourceParamVersion{Ref: "rev-9", Version: "canary-v1"}},
			},
			wantOK: true,
			want:   domain.GateTarget{Scope: domain.ScopeResource, Kind: "agent", ResourceID: "a1", RevisionID: "rev-9"},
		},
		{
			name: "platform group without published seq is not anchored",
			obs: domain.EvalObservation{
				Resource: domain.ObservationResourceRef{Kind: "agent", ResourceID: "a1"},
				Param: domain.ParamVersion{
					Platform: domain.PlatformParamVersion{GroupKey: "agent", VersionSeq: 0}},
			},
			wantOK: false,
		},
		{
			name: "observation without any anchor is not anchored",
			obs: domain.EvalObservation{
				Resource: domain.ObservationResourceRef{Kind: "agent", ResourceID: "a1"},
				Param:    domain.ParamVersion{},
			},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgt, ok := domain.GateTargetForObservation(tc.obs)
			if ok != tc.wantOK {
				t.Fatalf("mapping ok = %v, want %v (target=%+v)", ok, tc.wantOK, tgt)
			}
			if tc.wantOK && tgt != tc.want {
				t.Fatalf("mapping = %+v, want %+v", tgt, tc.want)
			}
		})
	}
}

func TestActionLabel(t *testing.T) {
	if got := actionLabel(domain.GateRollbackManual); got != "rollback_recommended" {
		t.Fatalf("manual label = %q", got)
	}
	if got := actionLabel(domain.GateRollbackAuto); got != "rollback_recommended" {
		t.Fatalf("auto label = %q", got)
	}
	if got := actionLabel(domain.GateL2Escalate); got != "escalate" {
		t.Fatalf("escalate label = %q", got)
	}
	if got := actionLabel(domain.GateNone); got != "" {
		t.Fatalf("none label = %q, want empty", got)
	}
}

func TestHandleObservationRoutesResourceScopeRollbackActions(t *testing.T) {
	// I1：资源 scope 锚点决策必须落到 rollback_auto / rollback_manual 对应执行路径
	// （execAutoRollback / requestApproval / applyResourceRollback 分发）。
	ctx := context.Background()
	blocked := domain.GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin}
	resourceTarget := domain.GateTarget{
		Scope: domain.ScopeResource, Kind: "skill", ResourceID: "s1", RevisionID: "rev-9",
	}
	policy := func(auto bool) domain.GatePolicy {
		return domain.GatePolicy{Scope: domain.ScopeResource, RollbackSupported: true, AutoRollbackAllowed: auto}
	}
	boom := errors.New("boom")

	tests := []struct {
		name           string
		auto           bool // true → auto 策略（rollback_auto）；false → manual 策略（rollback_manual）
		approvals      *stubApprovals
		rollback       *stubResourceRollback
		wantDecision   domain.GateAction
		wantRollbacks  int
		wantApprovals  int
		wantApprovalID string
	}{
		{
			name:          "auto calls resource executor and records rollback_auto ledger",
			auto:          true,
			rollback:      &stubResourceRollback{},
			wantDecision:  domain.GateRollbackAuto,
			wantRollbacks: 1,
		},
		{
			name:          "auto keeps ledger when resource executor unassembled",
			auto:          true,
			wantDecision:  domain.GateRollbackAuto,
			wantRollbacks: 0,
		},
		{
			name:          "auto executor failure keeps ledger (fail-open)",
			auto:          true,
			rollback:      &stubResourceRollback{err: boom},
			wantDecision:  domain.GateRollbackAuto,
			wantRollbacks: 1,
		},
		{
			name:           "manual requests approval and backfills ledger approval id",
			auto:           false,
			approvals:      &stubApprovals{approvalID: "ap-1"},
			wantDecision:   domain.GateRollbackManual,
			wantApprovals:  1,
			wantApprovalID: "ap-1",
		},
		{
			name:          "manual keeps ledger when approval requester unassembled",
			auto:          false,
			wantDecision:  domain.GateRollbackManual,
			wantApprovals: 0,
		},
		{
			name:          "manual approval failure keeps ledger (fail-open)",
			auto:          false,
			approvals:     &stubApprovals{err: boom},
			wantDecision:  domain.GateRollbackManual,
			wantApprovals: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubGateStore{evidence: blocked}
			// 注意：未装配（nil→skip）须把依赖接口字段保持真 nil，不能赋 typed nil
			// 具体指针（那样接口非 nil、nil 守卫失效）。故按 case 条件填充。
			deps := GateServiceDeps{
				Cfg:    func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
				Repo:   store,
				Policy: &stubGatePolicy{policy: policy(tc.auto)},
			}
			if tc.approvals != nil {
				deps.Approvals = tc.approvals
			}
			if tc.rollback != nil {
				deps.ResourceRollback = tc.rollback
			}
			svc := newTestGate(deps, fixedNow())

			if err := svc.HandleObservation(ctx, "t1", gateResourceObs()); err != nil {
				t.Fatalf("HandleObservation error = %v", err)
			}
			if len(store.actions) != 1 {
				t.Fatalf("ledger actions = %d, want 1", len(store.actions))
			}
			rec := store.actions[0]
			if rec.Decision != tc.wantDecision || rec.Action != "rollback_recommended" {
				t.Fatalf("ledger decision/action = %q/%q, want %q/rollback_recommended",
					rec.Decision, rec.Action, tc.wantDecision)
			}
			if rec.Target != resourceTarget {
				t.Fatalf("ledger target = %+v, want %+v", rec.Target, resourceTarget)
			}
			if rec.ApprovalID != tc.wantApprovalID {
				t.Fatalf("ledger approval id = %q, want %q", rec.ApprovalID, tc.wantApprovalID)
			}

			gotRollbacks := 0
			if tc.rollback != nil {
				gotRollbacks = len(tc.rollback.rollbacks)
			}
			if gotRollbacks != tc.wantRollbacks {
				t.Fatalf("execAutoRollback calls = %d, want %d", gotRollbacks, tc.wantRollbacks)
			}
			if gotRollbacks > 0 && tc.rollback.rollbacks[0] != resourceTarget {
				t.Fatalf("rollback target = %+v, want %+v", tc.rollback.rollbacks[0], resourceTarget)
			}

			gotApprovals := 0
			if tc.approvals != nil {
				gotApprovals = len(tc.approvals.requests)
			}
			if gotApprovals != tc.wantApprovals {
				t.Fatalf("requestApproval calls = %d, want %d", gotApprovals, tc.wantApprovals)
			}
			if gotApprovals > 0 {
				req := tc.approvals.requests[0]
				if req.Decision != domain.GateRollbackManual {
					t.Fatalf("approval request decision = %q, want rollback_manual", req.Decision)
				}
				if req.Target != resourceTarget {
					t.Fatalf("approval request target = %+v, want %+v", req.Target, resourceTarget)
				}
			}
		})
	}
}

// rollbackGateService 装配一台会用平台 manual 决策的门禁服务：调用方提供回滚
// 证据的 store 与平台 manual 策略，nil 依赖字段保持真 nil（fail-open 分支可测）。
// 不能把 typed nil 具体指针赋给接口字段（接口非 nil、nil 守卫失效），故按 nil 条件填充。
func rollbackGateService(cfg func(context.Context) domain.GateConfig, store *stubGateStore,
	policy *stubGatePolicy, platform *stubPlatformOps,
) *GateService {
	deps := GateServiceDeps{
		Cfg:  cfg,
		Repo: store,
	}
	if policy != nil {
		deps.Policy = policy
	}
	if platform != nil {
		deps.Platform = platform
	}
	return newTestGate(deps, fixedNow())
}

func TestHandleObservationFailOpenOnDependencyErrors(t *testing.T) {
	// T6 fail-open：依赖失败只日志，HandleObservation 恒返回 nil 不阻断观测主流程。
	ctx := context.Background()
	boom := errors.New("boom")
	rollbackEvidence := domain.GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin}
	platformManual := &stubGatePolicy{
		policy: domain.GatePolicy{Scope: domain.ScopePlatform, RollbackSupported: true},
	}
	enabled := func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} }

	t.Run("query window error returns nil and records no ledger", func(t *testing.T) {
		store := &stubGateStore{evidence: rollbackEvidence, queryErr: boom}
		svc := rollbackGateService(enabled, store, platformManual, nil)
		if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
			t.Fatalf("query error must not surface, got %v", err)
		}
		if len(store.actions) != 0 {
			t.Fatalf("query error must not record ledger, got %d", len(store.actions))
		}
	})

	t.Run("policy resolve error returns nil and records no ledger", func(t *testing.T) {
		store := &stubGateStore{evidence: rollbackEvidence}
		svc := rollbackGateService(enabled, store, &stubGatePolicy{err: boom}, nil)
		if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
			t.Fatalf("policy resolve error must not surface, got %v", err)
		}
		if len(store.actions) != 0 {
			t.Fatalf("policy resolve error must not record ledger, got %d", len(store.actions))
		}
	})

	t.Run("append action error returns nil and records no ledger", func(t *testing.T) {
		store := &stubGateStore{evidence: rollbackEvidence, appendErr: boom}
		svc := rollbackGateService(enabled, store, platformManual, nil)
		if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
			t.Fatalf("append error must not surface, got %v", err)
		}
		if len(store.actions) != 0 {
			t.Fatalf("append error must not record ledger, got %d", len(store.actions))
		}
	})

	t.Run("platform write-back error still records ledger", func(t *testing.T) {
		// 写回失败只丢平台 eval_state 副作用；台账是决策事实源，仍须落 rollback_manual。
		store := &stubGateStore{evidence: rollbackEvidence}
		platform := &stubPlatformOps{err: boom}
		svc := rollbackGateService(enabled, store, platformManual, platform)
		if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
			t.Fatalf("write-back error must not surface, got %v", err)
		}
		if len(store.actions) != 1 || store.actions[0].Decision != domain.GateRollbackManual {
			t.Fatalf("write-back error must keep rollback_manual ledger, got %+v", store.actions)
		}
		if platform.calls != 1 {
			t.Fatalf("write-back attempts = %d, want 1", platform.calls)
		}
		if len(platform.states) != 0 {
			t.Fatalf("failed write-back must not record state, got %v", platform.states)
		}
	})
}

func TestHandleObservationNilDepsSkipGate(t *testing.T) {
	// T6 剩余 nil 依赖分支：Cfg/Policy nil → 整段跳过不评估；Platform nil 只跳过
	// 平台写回，rollback_manual 决策仍落台账（fail-open）。
	ctx := context.Background()
	rollbackEvidence := domain.GateEvidence{RuleBlockCount: constants.GateRuleBlockRollbackMin}
	platformManual := &stubGatePolicy{
		policy: domain.GatePolicy{Scope: domain.ScopePlatform, RollbackSupported: true},
	}

	t.Run("nil Cfg skips evaluation and records no ledger", func(t *testing.T) {
		store := &stubGateStore{evidence: rollbackEvidence}
		svc := rollbackGateService(nil, store, platformManual, nil)
		if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
			t.Fatalf("nil Cfg must not error, got %v", err)
		}
		if len(store.actions) != 0 {
			t.Fatalf("nil Cfg must not record ledger, got %d", len(store.actions))
		}
	})

	t.Run("nil Policy skips evaluation and records no ledger", func(t *testing.T) {
		store := &stubGateStore{evidence: rollbackEvidence}
		svc := rollbackGateService(
			func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
			store, nil, nil)
		if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
			t.Fatalf("nil Policy must not error, got %v", err)
		}
		if len(store.actions) != 0 {
			t.Fatalf("nil Policy must not record ledger, got %d", len(store.actions))
		}
	})

	t.Run("nil Platform keeps rollback_manual ledger and skips write-back", func(t *testing.T) {
		store := &stubGateStore{evidence: rollbackEvidence}
		svc := rollbackGateService(
			func(context.Context) domain.GateConfig { return domain.GateConfig{Enabled: true} },
			store, platformManual, nil)
		if err := svc.HandleObservation(ctx, "t1", gateObs()); err != nil {
			t.Fatalf("nil Platform must not error, got %v", err)
		}
		if len(store.actions) != 1 || store.actions[0].Decision != domain.GateRollbackManual {
			t.Fatalf("nil Platform must keep rollback_manual ledger, got %+v", store.actions)
		}
	})
}
