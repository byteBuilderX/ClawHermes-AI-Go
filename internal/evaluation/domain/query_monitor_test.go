package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestMonitorQualityDimJSONPinsWireShape 守护质量维 wire 字段名与 spec §4.2 一致。
func TestMonitorQualityDimJSONPinsWireShape(t *testing.T) {
	conf := 0.87
	dim := QualityDim{Dimension: "faithfulness", PassRate: 0.92, AvgScore: 0.92, AvgConfidence: conf, Samples: 128}
	got, err := json.Marshal(dim)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"dimension":"faithfulness","pass_rate":0.92,"avg_score":0.92,"avg_confidence":0.87,"samples":128}`
	if string(got) != want {
		t.Fatalf("marshal quality dim = %s, want %s", got, want)
	}
}

// TestMonitorCostStatsNullLatencyIsJSONNull 空态诚实：无延迟样本时 avg/p95 序列化为
// null 而非 0（spec §3.1 禁止以 0 伪装无数据）。
func TestMonitorCostStatsNullLatencyIsJSONNull(t *testing.T) {
	got, err := json.Marshal(CostStats{TotalTokens: 154000, TotalCostUSD: 0.42})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"avg_latency_ms":null`) || !strings.Contains(string(got), `"p95_latency_ms":null`) {
		t.Fatalf("marshal cost stats = %s, want null latency fields", got)
	}
}

// TestMonitorProcessNilSerializesNull process 为 nil（窗口无 succeeded run）时 wire 是
// null 而非缺省 0（spec §4.2 process 可为 null）。
func TestMonitorProcessNilSerializesNull(t *testing.T) {
	summary := MonitorResourceSummary{ResourceKind: ResourceKindSkill, ResourceID: "skill-a", Process: nil}
	got, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"process":null`) {
		t.Fatalf("marshal summary = %s, want process null", got)
	}
}

// TestMonitorTrendSeriesRoundTrip 端点 2 响应整体 round-trip（runs 空数组保真）。
func TestMonitorTrendSeriesRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	series := MonitorTrendSeries{
		ResourceKind: ResourceKindSkill,
		ResourceID:   "skill-a",
		Series: []MonitorTrendPoint{{
			BucketAt: at, SampleCount: 20,
			Behavior: BehaviorStats{RuleHits: 2, Verdict: VerdictDistribution{Pass: 19, Flag: 1}},
		}},
		Runs: []RunProcessPoint{},
	}
	data, err := json.Marshal(series)
	if err != nil {
		t.Fatal(err)
	}
	var back MonitorTrendSeries
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"runs":[]`) || len(back.Runs) != 0 {
		t.Fatalf("round trip lost empty runs: %s", data)
	}
	if back.Series[0].BucketAt != at {
		t.Fatalf("bucket_at round trip = %v, want %v", back.Series[0].BucketAt, at)
	}
}
