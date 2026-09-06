package application

import (
	"math"
	"testing"

	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// dimScores 把每维 avg_score 构造成 run.Metrics["by_dimension"] 的真实形态
// （metrics.go aggregateByDimension：{avg_score, pass_rate, samples}）。
func dimScores(m map[string]float64) map[string]any {
	out := make(map[string]any, len(m))
	for name, score := range m {
		out[name] = map[string]any{"avg_score": score, "pass_rate": 1.0, "samples": 1}
	}
	return map[string]any{"by_dimension": out}
}

func runWithMetrics(m map[string]float64) *domain.EvalRun {
	return &domain.EvalRun{ID: "r", Metrics: dimScores(m)}
}

func TestCompareRunRegression(t *testing.T) {
	cases := []struct {
		name       string
		baseline   *domain.EvalRun
		current    *domain.EvalRun
		wantReg    bool
		wantDeltas map[string]float64
	}{
		{
			name:       "degradation below threshold flags regression",
			baseline:   runWithMetrics(map[string]float64{"faithfulness": 0.9, "relevance": 0.8}),
			current:    runWithMetrics(map[string]float64{"faithfulness": 0.84, "relevance": 0.8}),
			wantReg:    true,
			wantDeltas: map[string]float64{"faithfulness": -0.06, "relevance": 0.0},
		},
		{
			// 向量取 0.95→0.90：真实十进制差恰为 -0.05（boundary），且 float64 相减舍入到
			// -0.049999999999999933（>= 阈值），保证“边界不判劣”的分类符合十进制语义。
			// 反例 0.9→0.85 相减得 -0.050000000000000044（< 阈值）会把纯浮点噪声误判为劣化。
			name:       "delta at threshold boundary is not regression",
			baseline:   runWithMetrics(map[string]float64{"faithfulness": 0.95}),
			current:    runWithMetrics(map[string]float64{"faithfulness": 0.90}),
			wantReg:    false,
			wantDeltas: map[string]float64{"faithfulness": constants.RunRegressionDeltaThreshold},
		},
		{
			name:       "improvement and flat deltas are not regression",
			baseline:   runWithMetrics(map[string]float64{"faithfulness": 0.8}),
			current:    runWithMetrics(map[string]float64{"faithfulness": 0.95}),
			wantReg:    false,
			wantDeltas: map[string]float64{"faithfulness": 0.15},
		},
		{
			name:       "dimension missing in baseline is skipped",
			baseline:   runWithMetrics(map[string]float64{"faithfulness": 0.9}),
			current:    runWithMetrics(map[string]float64{"faithfulness": 0.8, "relevance": 0.1}),
			wantReg:    true,
			wantDeltas: map[string]float64{"faithfulness": -0.1},
		},
		{
			name:       "nil run yields empty comparison",
			baseline:   nil,
			current:    runWithMetrics(map[string]float64{"faithfulness": 0.8}),
			wantReg:    false,
			wantDeltas: map[string]float64{},
		},
		{
			name:       "missing by_dimension node yields empty comparison",
			baseline:   &domain.EvalRun{ID: "a"},
			current:    &domain.EvalRun{ID: "b", Metrics: map[string]any{"pass_rate": 0.5}},
			wantReg:    false,
			wantDeltas: map[string]float64{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CompareRunRegression(tc.baseline, tc.current)
			if got.Regressed != tc.wantReg {
				t.Fatalf("Regressed = %v, want %v", got.Regressed, tc.wantReg)
			}
			if len(got.DimensionDeltas) != len(tc.wantDeltas) {
				t.Fatalf("DimensionDeltas = %v, want %v", got.DimensionDeltas, tc.wantDeltas)
			}
			for dim, want := range tc.wantDeltas {
				gotDelta, ok := got.DimensionDeltas[dim]
				if !ok {
					t.Fatalf("DimensionDeltas missing dim %q in %v", dim, got.DimensionDeltas)
				}
				if math.Abs(gotDelta-want) > 1e-9 {
					t.Fatalf("delta[%q] = %v, want %v", dim, gotDelta, want)
				}
			}
		})
	}
}
