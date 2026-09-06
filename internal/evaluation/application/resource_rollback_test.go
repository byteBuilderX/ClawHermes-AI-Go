package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
)

// stubProductBackend 记录调用；err 模拟失败。
type stubProductBackend struct {
	candidates []port.RollbackCandidate
	listErr    error
	rollErr    error
	listCalls  int
	rollCalls  []string // "resourceID->candidateID(actor)"
}

func (s *stubProductBackend) ListCandidates(_ context.Context, _, resourceID string) ([]port.RollbackCandidate, error) {
	s.listCalls++
	return s.candidates, s.listErr
}

func (s *stubProductBackend) RollbackProduct(_ context.Context, _, resourceID, candidateID, actorID string) error {
	s.rollCalls = append(s.rollCalls, resourceID+"->"+candidateID+"("+actorID+")")
	return s.rollErr
}

// stubCanaryBackend 记录判定与清除调用。
type stubCanaryBackend struct {
	dep        domain.Deployment
	found      bool
	resolveErr error
	clearErr   error
	resolves   int
	cleared    []string // "experimentID(actor)"
}

func (s *stubCanaryBackend) ResolveDeployment(_ context.Context, _ string, _ domain.ResourceKind, _ string) (domain.Deployment, bool, error) {
	s.resolves++
	return s.dep, s.found, s.resolveErr
}

func (s *stubCanaryBackend) ClearCanary(_ context.Context, _, experimentID, actorID, _ string) error {
	s.cleared = append(s.cleared, experimentID+"("+actorID+")")
	return s.clearErr
}

func resourceTarget(kind, resourceID, revisionID string) domain.GateTarget {
	return domain.GateTarget{Scope: domain.ScopeResource, Kind: kind, ResourceID: resourceID, RevisionID: revisionID}
}

func TestPreviousGoodVersion(t *testing.T) {
	candidates := []port.RollbackCandidate{
		{ID: "v1", RevisionNo: 1, IsCurrent: false, Rollbackable: true},
		{ID: "v3", RevisionNo: 3, IsCurrent: true, Rollbackable: false},
		{ID: "v2", RevisionNo: 2, IsCurrent: false, Rollbackable: true},
	}
	got, ok := previousGoodVersion(candidates)
	if !ok || got.ID != "v2" {
		t.Fatalf("previousGoodVersion = (%+v, %v), want v2", got, ok)
	}
	if _, ok := previousGoodVersion([]port.RollbackCandidate{{ID: "c", RevisionNo: 1, IsCurrent: true, Rollbackable: false}}); ok {
		t.Fatal("previousGoodVersion should be false when no deprecated non-current candidate")
	}
}

func TestIsCanaryBadState(t *testing.T) {
	dep := domain.Deployment{ExperimentID: "exp-1", StableRevisionID: "rev-s", CanaryRevisionID: "rev-c", CanaryPercent: 10}
	if !isCanaryBadState(dep, true, "rev-c") {
		t.Fatal("observed canary should be judged canary-bad")
	}
	if isCanaryBadState(dep, true, "rev-s") {
		t.Fatal("observed stable revision must NOT be treated as canary-bad")
	}
	if isCanaryBadState(dep, false, "rev-c") {
		t.Fatal("missing deployment must not be treated as canary-bad")
	}
	if isCanaryBadState(domain.Deployment{ExperimentID: "exp-1"}, true, "rev-c") {
		t.Fatal("empty canary revision must not be treated as canary-bad")
	}
}

func TestResourceRollbackExecutorDispatch(t *testing.T) {
	ctx := context.Background()
	t.Run("mcp kind returns ErrRollbackUnsupported", func(t *testing.T) {
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{},
		})
		err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindMCP), "m1", "r1"), "gate", "", "")
		if !errors.Is(err, port.ErrRollbackUnsupported) {
			t.Fatalf("err = %v, want ErrRollbackUnsupported", err)
		}
	})
	t.Run("platform scope returns ErrRollbackUnsupported", func(t *testing.T) {
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{})
		err := ex.Rollback(ctx, "t1", domain.GateTarget{Scope: domain.ScopePlatform, GroupKey: "agent", VersionSeq: 1}, "gate", "", "")
		if !errors.Is(err, port.ErrRollbackUnsupported) {
			t.Fatalf("err = %v, want ErrRollbackUnsupported", err)
		}
	})
	t.Run("canary bad clears canary and skips product rollback", func(t *testing.T) {
		prod := &stubProductBackend{candidates: []port.RollbackCandidate{{ID: "v2", RevisionNo: 2, Rollbackable: true}}}
		can := &stubCanaryBackend{dep: domain.Deployment{ExperimentID: "exp-1", CanaryRevisionID: "rev-c"}, found: true}
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{Logger: nil,
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindAgent: prod}, Canary: can})
		err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindAgent), "a1", "rev-c"), "gate", "u-admin", "ap-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(can.cleared) != 1 || can.cleared[0] != "exp-1(u-admin)" {
			t.Fatalf("cleared = %#v, want exp-1(u-admin)", can.cleared)
		}
		if prod.listCalls != 0 || len(prod.rollCalls) != 0 {
			t.Fatalf("product backend must not be called on canary path, list=%d roll=%#v", prod.listCalls, prod.rollCalls)
		}
	})
	t.Run("product path rolls back to previous good with decidedBy actor", func(t *testing.T) {
		prod := &stubProductBackend{candidates: []port.RollbackCandidate{
			{ID: "v3", RevisionNo: 3, IsCurrent: true},
			{ID: "v2", RevisionNo: 2, Rollbackable: true},
			{ID: "v1", RevisionNo: 1, Rollbackable: true},
		}}
		can := &stubCanaryBackend{dep: domain.Deployment{CanaryRevisionID: "rev-c"}, found: true} // observed != canary
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindAgent: prod}, Canary: can})
		if err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindAgent), "a1", "rev-s"), "gate", "u-admin", ""); err != nil {
			t.Fatal(err)
		}
		if len(prod.rollCalls) != 1 || prod.rollCalls[0] != "a1->v2(u-admin)" {
			t.Fatalf("roll calls = %#v, want a1->v2(u-admin)", prod.rollCalls)
		}
	})
	t.Run("auto path falls back actor when decidedBy empty", func(t *testing.T) {
		prod := &stubProductBackend{candidates: []port.RollbackCandidate{{ID: "v2", RevisionNo: 2, Rollbackable: true}}}
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindAgent: prod}})
		if err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindAgent), "a1", "rev-s"), "gate", "", ""); err != nil {
			t.Fatal(err)
		}
		if len(prod.rollCalls) != 1 || prod.rollCalls[0] != "a1->v2(gate)" {
			t.Fatalf("roll calls = %#v, want a1->v2(gate)", prod.rollCalls)
		}
	})
	t.Run("no rollback candidate returns wrapped errNoRollbackCandidate", func(t *testing.T) {
		prod := &stubProductBackend{candidates: []port.RollbackCandidate{{ID: "v3", RevisionNo: 3, IsCurrent: true}}}
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindSkill: prod}})
		err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindSkill), "s1", "v3"), "gate", "", "")
		if err == nil || !strings.Contains(err.Error(), errNoRollbackCandidate.Error()) {
			t.Fatalf("err = %v, want errNoRollbackCandidate", err)
		}
	})
	t.Run("list failure propagates", func(t *testing.T) {
		boom := errors.New("boom")
		prod := &stubProductBackend{listErr: boom}
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindKnowledge: prod}})
		err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindKnowledge), "k1", "v2"), "gate", "", "")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	})
	t.Run("canary resolve failure propagates before product path", func(t *testing.T) {
		boom := errors.New("resolve boom")
		prod := &stubProductBackend{candidates: []port.RollbackCandidate{{ID: "v2", RevisionNo: 2, Rollbackable: true}}}
		can := &stubCanaryBackend{resolveErr: boom}
		ex := NewResourceRollbackExecutor(ResourceRollbackExecutorDeps{
			Products: map[domain.ResourceKind]port.ProductRollbackBackend{domain.ResourceKindAgent: prod}, Canary: can})
		err := ex.Rollback(ctx, "t1", resourceTarget(string(domain.ResourceKindAgent), "a1", "rev-s"), "gate", "", "")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	})
}
