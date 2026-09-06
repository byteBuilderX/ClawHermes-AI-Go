package wiring

import (
	"context"
	"encoding/json"
	"testing"

	evaldomain "github.com/byteBuilderX/stratum/internal/evaluation/domain"
	evalport "github.com/byteBuilderX/stratum/internal/evaluation/domain/port"
	parametersapp "github.com/byteBuilderX/stratum/internal/parameters/application"
	parametersdomain "github.com/byteBuilderX/stratum/internal/parameters/domain"
	"github.com/byteBuilderX/stratum/internal/parameters/domain/port"
)

// 复用同包 fakePlatformStore（embedding_model_test.go）：ListVersions 按组返回
// 种子版本历史，其余方法空/零值——本测试只用 Versions。

func jsonRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestCaptureGroupOverridePicksHistoricalVersion(t *testing.T) {
	svc := parametersapp.NewService(parametersdomain.NewParametersRegistry(),
		&fakePlatformStore{versions: map[string][]port.PlatformVersion{
			evaldomain.GroupAgent: {
				{GroupKey: evaldomain.GroupAgent, VersionSeq: 1, IsCurrent: false,
					Snapshot: map[string]json.RawMessage{"agent.temperature": jsonRaw(0.2)}},
				{GroupKey: evaldomain.GroupAgent, VersionSeq: 2, IsCurrent: true,
					Snapshot: map[string]json.RawMessage{"agent.temperature": jsonRaw(0.9)}},
			},
		}})
	c := snapshotCapturer{params: svc}
	ctx := context.Background()

	// 有 override → 精确命中历史 seq 1。
	seq := int64(1)
	got, err := c.captureGroup(ctx, evaldomain.GroupAgent, &seq)
	if err != nil {
		t.Fatal(err)
	}
	if got.VersionSeq != 1 {
		t.Fatalf("override capture seq = %d, want 1", got.VersionSeq)
	}
	if v, _ := got.Values["agent.temperature"].(float64); v != 0.2 {
		t.Fatalf("override captured value = %v, want 0.2", got.Values["agent.temperature"])
	}

	// override miss（seq 999 已归档修剪）→ 回退 IsCurrent seq 2，不回错误。
	miss := int64(999)
	got, err = c.captureGroup(ctx, evaldomain.GroupAgent, &miss)
	if err != nil {
		t.Fatalf("override miss must fall back, got error: %v", err)
	}
	if got.VersionSeq != 2 {
		t.Fatalf("override miss fallback seq = %d, want 2", got.VersionSeq)
	}

	// 无 override → 现 IsCurrent 语义。
	got, err = c.captureGroup(ctx, evaldomain.GroupAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.VersionSeq != 2 {
		t.Fatalf("current capture seq = %d, want 2", got.VersionSeq)
	}
}

func TestCaptureOverrideFlowsThroughInput(t *testing.T) {
	in := evalport.CaptureInput{PlatformSeqOverrides: map[string]int64{evaldomain.GroupEvaluation: 7}}
	if got := overrideSeq(in, evaldomain.GroupEvaluation); got == nil || *got != 7 {
		t.Fatalf("overrideSeq(evaluation) = %v, want 7", got)
	}
	if got := overrideSeq(in, evaldomain.GroupAgent); got != nil {
		t.Fatalf("overrideSeq(agent) = %v, want nil", got)
	}
}
