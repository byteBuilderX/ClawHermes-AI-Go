package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"go.uber.org/zap"
)

// stubPlatformResolver 返回固定 key→value 映射；缺失 key 返回 present=false，
// 模拟平台存储未配置该 key。
type stubPlatformResolver struct {
	values map[string]any
}

func (s stubPlatformResolver) ResolvePlatform(_ context.Context, key string) (any, bool, error) {
	v, ok := s.values[key]
	return v, ok, nil
}

// TestEnricherResolveDefaultsWithConfiguredModel 验证解析期默认：模型已显式
// 配置（必需平台参数）时，温度/阈值仍回落 pkg/constants 默认；模型本身不再
// 回落 llmgateway 目录默认（原缺陷根因的禁止项）。
func TestEnricherResolveDefaultsWithConfiguredModel(t *testing.T) {
	w := NewEnricherWorker(nil, nil, nil, zap.NewNop(), Config{})
	w.paramResolver = stubPlatformResolver{values: map[string]any{
		"memory.enrich_model":  "qwen-max",
		"memory.summary_model": "qwen-max",
	}}
	ctx := context.Background()

	enrich, err := w.resolveEnrichSettings(ctx)
	if err != nil {
		t.Fatalf("enrich resolve with configured model: %v", err)
	}
	if enrich.model != "qwen-max" {
		t.Fatalf("enrich platform model = %q, want qwen-max", enrich.model)
	}
	if enrich.temperature != constants.MemoryEnrichLLMTemperature {
		t.Fatalf("enrich default temperature = %v, want %v", enrich.temperature, constants.MemoryEnrichLLMTemperature)
	}

	summary, err := w.resolveSummarySettings(ctx)
	if err != nil {
		t.Fatalf("summary resolve with configured model: %v", err)
	}
	if summary.model != "qwen-max" {
		t.Fatalf("summary platform model = %q, want qwen-max", summary.model)
	}
	if summary.temperature != constants.TaskSummarizeTemperature {
		t.Fatalf("summary default temperature = %v, want %v", summary.temperature, constants.TaskSummarizeTemperature)
	}
	if summary.threshold != constants.EnricherSummaryTokenThreshold {
		t.Fatalf("summary default threshold = %d, want %d", summary.threshold, constants.EnricherSummaryTokenThreshold)
	}
}

// TestEnricherResolveModelMissingFailClosed 验证模型缺失 fail-closed：resolver
// 为 nil 或 key 未配置时，enrich/summary 模型解析必须返回 *modelconfig.Err
// （state=missing），禁止空模型回落 llmgateway 目录默认。
func TestEnricherResolveModelMissingFailClosed(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		key     string
		resolve func(w *EnricherWorker) error
	}{
		{
			name: "enrich key unset",
			key:  modelconfig.KeyEnrichModel,
			resolve: func(w *EnricherWorker) error {
				_, err := w.resolveEnrichSettings(ctx)
				return err
			},
		},
		{
			name: "summary nil resolver",
			key:  modelconfig.KeySummaryModel,
			resolve: func(w *EnricherWorker) error {
				_, err := w.resolveSummarySettings(ctx)
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := NewEnricherWorker(nil, nil, nil, zap.NewNop(), Config{})
			if tc.name == "enrich key unset" {
				w.paramResolver = stubPlatformResolver{values: map[string]any{}}
			}
			err := tc.resolve(w)
			if err == nil {
				t.Fatal("expected missing-model error, got nil")
			}
			ce, ok := modelconfig.AsConfigError(err)
			if !ok {
				t.Fatalf("expected *modelconfig.Err, got %v", err)
			}
			if ce.State != modelconfig.StateMissing {
				t.Fatalf("state = %s, want %s", ce.State, modelconfig.StateMissing)
			}
			if ce.Key != tc.key {
				t.Fatalf("key = %s, want %s", ce.Key, tc.key)
			}
		})
	}
}

// TestEnricherResolvePlatformValues 验证平台解析值生效：resolver 返回的平台
// 模型/温度/阈值覆盖常量默认（运行态热改的解析期断言）。
func TestEnricherResolvePlatformValues(t *testing.T) {
	w := NewEnricherWorker(nil, nil, nil, zap.NewNop(), Config{})
	w.paramResolver = stubPlatformResolver{values: map[string]any{
		"memory.enrich_model":            "glm-4.5-air",
		"memory.enrich_temperature":      float64(0.3),
		"memory.summary_model":           "qwen-max",
		"memory.summary_temperature":     float64(0.5),
		"memory.summary_token_threshold": int64(2500),
	}}
	ctx := context.Background()

	enrich, err := w.resolveEnrichSettings(ctx)
	if err != nil {
		t.Fatalf("enrich platform resolve: %v", err)
	}
	if enrich.model != "glm-4.5-air" {
		t.Fatalf("enrich platform model = %q, want glm-4.5-air", enrich.model)
	}
	if enrich.temperature != 0.3 {
		t.Fatalf("enrich platform temperature = %v, want 0.3", enrich.temperature)
	}

	summary, err := w.resolveSummarySettings(ctx)
	if err != nil {
		t.Fatalf("summary platform resolve: %v", err)
	}
	if summary.model != "qwen-max" {
		t.Fatalf("summary platform model = %q, want qwen-max", summary.model)
	}
	if summary.temperature != 0.5 {
		t.Fatalf("summary platform temperature = %v, want 0.5", summary.temperature)
	}
	if summary.threshold != 2500 {
		t.Fatalf("summary platform threshold = %d, want 2500", summary.threshold)
	}
}

// TestNewSummaryLLMRequestRoundsPlatformTemperature 回归 PR #441 漏网覆盖点：
// 会话摘要请求的平台温度必须经 PlatformTemperaturePtr 舍入 2 位小数，
// float64(float32(0.2)) 直转会变成 0.20000000298023224 触发智谱 400；
// 平台温度 0 保持 unset（nil，走网关默认）。
func TestNewSummaryLLMRequestRoundsPlatformTemperature(t *testing.T) {
	req := newSummaryLLMRequest(summarySettings{model: "qwen-max", temperature: 0.2}, "prompt")
	if req.Temperature == nil || *req.Temperature != 0.2 {
		t.Fatalf("platform summary temperature = %v, want 0.2 (2 位小数)", req.Temperature)
	}
	zero := newSummaryLLMRequest(summarySettings{model: "qwen-max", temperature: 0}, "prompt")
	if zero.Temperature != nil {
		t.Fatalf("zero platform temperature must keep unset (nil), got %v", zero.Temperature)
	}
}

// enrichFakeMsg 构造可终止的假 JetStream 消息（第 1 次投递，远低于 maxDeliver）。
func enrichFakeMsg() *fakeJetStreamMsg {
	return &fakeJetStreamMsg{
		subject: "memory.enriched.tenant-a",
		metadata: &jetstream.MsgMetadata{
			Stream: "MEMORY_ENRICHED", Consumer: "enrich-worker", NumDelivered: 1,
			Sequence: jetstream.SequencePair{Stream: 1},
		},
	}
}

// TestEnricherDisposeConfigErrorImmediateDLQ 验证模型配置错走即时 DLQ：消息被
// TermWithReason 终止（termCount=1）并发布到 DLQ，绝不 Nak 重试（配置错重试
// 不会自愈，且不得消耗重试预算）。
func TestEnricherDisposeConfigErrorImmediateDLQ(t *testing.T) {
	pub := &fakeDLQPublisher{}
	w := NewEnricherWorker(nil, pub, nil, zap.NewNop(), Config{MaxDeliver: 5, EnrichAckWait: time.Second})
	msg := enrichFakeMsg()
	ev := &MemoryEnrichedEvent{MemoryRawEvent: MemoryRawEvent{TenantID: "tenant-a", MessageID: "message-a"}}
	w.disposeEnrichError(context.Background(), msg, func() {}, ev, "trace-1",
		&modelconfig.Err{Key: modelconfig.KeyEnrichModel, State: modelconfig.StateMissing})

	if pub.count != 1 {
		t.Fatalf("DLQ publish count = %d, want 1 (immediate DLQ)", pub.count)
	}
	if msg.termCount != 1 {
		t.Fatalf("termCount = %d, want 1", msg.termCount)
	}
	if msg.nakCount != 0 {
		t.Fatalf("nakCount = %d, want 0 (config error must not retry)", msg.nakCount)
	}
}

// TestEnricherDisposeLLMErrorRetries 验证非配置错仍走重试：消息 NakWithDelay
// 返回队列，不发布 DLQ、不终止。
func TestEnricherDisposeLLMErrorRetries(t *testing.T) {
	pub := &fakeDLQPublisher{}
	w := NewEnricherWorker(nil, pub, nil, zap.NewNop(), Config{MaxDeliver: 5, EnrichAckWait: time.Second})
	msg := enrichFakeMsg()
	ev := &MemoryEnrichedEvent{MemoryRawEvent: MemoryRawEvent{TenantID: "tenant-a", MessageID: "message-a"}}
	w.disposeEnrichError(context.Background(), msg, func() {}, ev, "trace-1", errors.New("llm timeout"))

	if pub.count != 0 {
		t.Fatalf("DLQ publish count = %d, want 0 (plain LLM error retries)", pub.count)
	}
	if msg.termCount != 0 {
		t.Fatalf("termCount = %d, want 0", msg.termCount)
	}
	if msg.nakCount != 1 {
		t.Fatalf("nakCount = %d, want 1 (retry via NakWithDelay)", msg.nakCount)
	}
}
