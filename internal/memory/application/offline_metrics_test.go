package application

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 本文件直测 offline_metrics.go 的检索度量纯函数边界，锁定与
// knowledge/application/metrics.go mirror 的语义一致（review M-3）：空 relevant、
// 空 retrieved、dup relevant、k>len 等边界不经 evaluator 间接覆盖，防止未来任一
// 侧漂移。期望值与 knowledge mirror 逐行同构（空集合返回 0 不除零；k clamp 到
// retrieved 长度；relevant 按 set 判命中但分母/IDCG 用 len(relevant)）。

func TestOfflineRecallAtKBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		retrieved []string
		relevant  []string
		k         int
		want      float64
	}{
		{name: "empty relevant returns zero", retrieved: []string{"a"}, k: 5, want: 0},
		{name: "empty retrieved returns zero", relevant: []string{"a"}, k: 5, want: 0},
		{name: "k zero returns zero", retrieved: []string{"a"}, relevant: []string{"a"}, k: 0, want: 0},
		{
			name:      "k clamps to retrieved length",
			retrieved: []string{"a", "b", "c"}, relevant: []string{"a", "b"}, k: 10, want: 1,
		},
		{
			name:      "partial hits within k",
			retrieved: []string{"a", "b", "c"}, relevant: []string{"a", "c", "d"}, k: 2,
			want: 1.0 / 3.0,
		},
		{
			name:      "duplicate relevant counts denominator only",
			retrieved: []string{"a"}, relevant: []string{"a", "a"}, k: 5, want: 0.5,
		},
		{
			name:      "duplicate retrieved deduped first occurrence",
			retrieved: []string{"a", "a"}, relevant: []string{"a"}, k: 5, want: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := offlineRecallAtK(tc.retrieved, tc.relevant, tc.k)
			require.InDelta(t, tc.want, got, 1e-9,
				"RecallAtK(%v,%v,%d)=%v", tc.retrieved, tc.relevant, tc.k, got)
		})
	}
}

func TestOfflinePrecisionAtKBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		retrieved []string
		relevant  []string
		k         int
		want      float64
	}{
		{name: "empty retrieved returns zero", relevant: []string{"a"}, k: 5, want: 0},
		{name: "k zero returns zero", retrieved: []string{"a"}, relevant: []string{"a"}, k: 0, want: 0},
		{
			name:      "k clamps to retrieved length",
			retrieved: []string{"a", "b"}, relevant: []string{"a"}, k: 10, want: 0.5,
		},
		{name: "no relevant hits returns zero", retrieved: []string{"x", "y"}, relevant: []string{"a"}, k: 5, want: 0},
		{
			name:      "duplicate retrieved deduped",
			retrieved: []string{"a", "a", "b"}, relevant: []string{"a"}, k: 5, want: 0.5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := offlinePrecisionAtK(tc.retrieved, tc.relevant, tc.k)
			require.InDelta(t, tc.want, got, 1e-9,
				"PrecisionAtK(%v,%v,%d)=%v", tc.retrieved, tc.relevant, tc.k, got)
		})
	}
}

func TestOfflineMRRBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		retrieved []string
		relevant  []string
		want      float64
	}{
		{name: "empty retrieved returns zero", relevant: []string{"a"}, want: 0},
		{name: "empty relevant returns zero", retrieved: []string{"a"}, want: 0},
		{name: "first relevant at rank one", retrieved: []string{"a", "b"}, relevant: []string{"a"}, want: 1},
		{name: "first relevant at rank two", retrieved: []string{"x", "a"}, relevant: []string{"a"}, want: 0.5},
		{
			name:      "duplicate retrieved keeps first occurrence rank",
			retrieved: []string{"x", "x", "a"}, relevant: []string{"a"}, want: 0.5,
		},
		{name: "no relevant returns zero", retrieved: []string{"x", "y"}, relevant: []string{"a"}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := offlineMRR(tc.retrieved, tc.relevant)
			require.InDelta(t, tc.want, got, 1e-9, "MRR(%v,%v)=%v", tc.retrieved, tc.relevant, got)
		})
	}
}

func TestOfflineNDCGAtKBoundaries(t *testing.T) {
	// idcg=1+1/log2(3)≈1.6309；rank1 命中即 dcg=1 → 归一 ≈0.6131。
	const partialNDCG = 1.0 / (1.0 + 1.0/1.5849625007211563)
	cases := []struct {
		name      string
		retrieved []string
		relevant  []string
		k         int
		want      float64
	}{
		{name: "empty retrieved returns zero", relevant: []string{"a"}, k: 5, want: 0},
		{name: "empty relevant returns zero", retrieved: []string{"a"}, k: 5, want: 0},
		{name: "k zero returns zero", retrieved: []string{"a"}, relevant: []string{"a"}, k: 0, want: 0},
		{
			name:      "all relevant in window equals one",
			retrieved: []string{"a", "b", "c"}, relevant: []string{"a", "b"}, k: 10, want: 1,
		},
		{
			name:      "partial relevant penalizes below ideal",
			retrieved: []string{"a", "b", "c"}, relevant: []string{"a", "c"}, k: 2,
			want: partialNDCG,
		},
		{
			name:      "duplicate relevant mirrors knowledge ideal cap",
			retrieved: []string{"a", "b"}, relevant: []string{"a", "a"}, k: 5,
			want: partialNDCG,
		},
		{
			name:      "duplicate retrieved deduped reaches one",
			retrieved: []string{"a", "a", "b"}, relevant: []string{"a", "b"}, k: 5, want: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := offlineNDCGAtK(tc.retrieved, tc.relevant, tc.k)
			require.InDelta(t, tc.want, got, 1e-9,
				"NDCGAtK(%v,%v,%d)=%v", tc.retrieved, tc.relevant, tc.k, got)
		})
	}
}
