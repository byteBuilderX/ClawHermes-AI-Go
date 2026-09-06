package application

import (
	"context"
	"testing"
	"time"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
)

// verifyRunRepoStub 只实现 PlatformSeq 锚点查询：嵌入 port.RunRepository 满足接口，
// runner 只触达该方法；其余方法调用会因 nil 嵌入而 panic（测试不触达）。
type verifyRunRepoStub struct {
	port.RunRepository
	bySeq map[int64]*domain.EvalRun
}

func (s *verifyRunRepoStub) FindLatestCompletedRunForPlatformSeq(
	_ context.Context, _, _ string, seq int64,
) (*domain.EvalRun, error) {
	return s.bySeq[seq], nil // nil map 读取合法：缺失 seq → nil（无信号）
}

// verifyJobRepoStub 双语义：runner 用例走 Claim 返回 claimed；enqueue 用例走
// EnqueuePlatformVerify 返回 enqueueOK（true=新插入/false=幂等冲突）。
type verifyJobRepoStub struct {
	claimed    *domain.PlatformVerifyJob
	enqueueOK  bool
	enqueueErr error
}

func (s *verifyJobRepoStub) EnqueuePlatformVerify(
	_ context.Context, _ string, _ domain.PlatformVerifyPayload, _, _ string,
) (bool, error) {
	return s.enqueueOK, s.enqueueErr
}

func (s *verifyJobRepoStub) ClaimPlatformVerify(
	_ context.Context, _, _ string, _ time.Duration,
) (*domain.PlatformVerifyJob, error) {
	return s.claimed, nil
}

// testVerifyRunner 组默认 deps：claim 指定 job、runs 按 seq 供锚点、Compare 固定
// regressed 结论。返回 runner + metrics spy。
func testVerifyRunner(job *domain.PlatformVerifyJob, runs map[int64]*domain.EvalRun, regressed bool) (*MultiTenantVerifyRunner, *gateMetricsSpy) {
	spy := &gateMetricsSpy{}
	deps := MultiTenantVerifyDeps{
		Metrics: spy,
		Repo:    &verifyJobRepoStub{claimed: job},
		Runs:    &verifyRunRepoStub{bySeq: runs},
		Compare: compareStub(regressed, nil),
	}
	return NewMultiTenantVerifyRunner(deps), spy
}

func TestMultiTenantVerifyRunner_RecoveredWhenRollbackRestores(t *testing.T) {
	// 回滚后（to_seq=3，好版本）run 不劣于回滚前（from_seq=2，坏版本）→ recovered。
	runner, spy := testVerifyRunner(
		&domain.PlatformVerifyJob{ID: "pv-1", Payload: domain.PlatformVerifyPayload{GroupKey: "evaluation", FromSeq: 2, ToSeq: 3}},
		map[int64]*domain.EvalRun{2: {ID: "run-bad-v2"}, 3: {ID: "run-good-v3"}},
		false, // Compare(坏, 好).Regressed=false → 已恢复
	)
	worked, err := runner.RunOnce(context.Background(), "tenant-1", "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("RunOnce err = %v, want nil", err)
	}
	if !worked {
		t.Fatal("worked = false, want true (job claimed & processed)")
	}
	wantActions(t, spy, domain.LayerL3MultiTenantVerify+":"+domain.ActionRecovered)
}

func TestMultiTenantVerifyRunner_NotRecoveredWhenStillRegressed(t *testing.T) {
	// 回滚后（好版本）仍劣于回滚前（坏版本）→ not_recovered（R31 未恢复计数）。
	runner, spy := testVerifyRunner(
		&domain.PlatformVerifyJob{ID: "pv-1", Payload: domain.PlatformVerifyPayload{GroupKey: "evaluation", FromSeq: 2, ToSeq: 3}},
		map[int64]*domain.EvalRun{2: {ID: "run-bad-v2"}, 3: {ID: "run-bad-v3"}},
		true,
	)
	worked, err := runner.RunOnce(context.Background(), "tenant-1", "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("RunOnce err = %v, want nil", err)
	}
	if !worked {
		t.Fatal("worked = false, want true (job claimed & processed)")
	}
	wantActions(t, spy, domain.LayerL3MultiTenantVerify+":"+domain.ActionNotRecovered)
}

func TestMultiTenantVerifyRunner_SkipWhenNoSignal(t *testing.T) {
	// 锚点 run 缺失（如回滚后租户尚未重跑）→ 无信号：不产生 recovered/not_recovered 计数。
	runner, spy := testVerifyRunner(
		&domain.PlatformVerifyJob{ID: "pv-1", Payload: domain.PlatformVerifyPayload{GroupKey: "evaluation", FromSeq: 2, ToSeq: 3}},
		map[int64]*domain.EvalRun{2: {ID: "run-bad-v2"}}, // to_seq=3 无 completed run
		false,
	)
	worked, err := runner.RunOnce(context.Background(), "tenant-1", "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("RunOnce err = %v, want nil", err)
	}
	if !worked {
		t.Fatal("worked = false, want true (job consumed, skip 也是已处理)")
	}
	wantActions(t, spy)
}

func TestEnqueueMultiTenantVerify_DedupesByIdempotencyKey(t *testing.T) {
	// 幂等键冲突（第二次 enqueue inserted=false）→ 不重复 +queued。
	spy := &gateMetricsSpy{}
	repo := &verifyJobRepoStub{enqueueOK: true}
	p := domain.PlatformVerifyPayload{GroupKey: "evaluation", FromSeq: 1, ToSeq: 2}

	if err := EnqueueMultiTenantVerify(context.Background(), repo, "tenant-1", p, "actor", spy); err != nil {
		t.Fatalf("first enqueue err = %v, want nil", err)
	}
	wantActions(t, spy, domain.LayerL3MultiTenantVerify+":"+domain.ActionQueued)

	repo.enqueueOK = false // 已存在 → inserted=false
	if err := EnqueueMultiTenantVerify(context.Background(), repo, "tenant-1", p, "actor", spy); err != nil {
		t.Fatalf("second enqueue err = %v, want nil", err)
	}
	wantActions(t, spy, domain.LayerL3MultiTenantVerify+":"+domain.ActionQueued) // 仍只记一次
}
