package milvus

import (
	"testing"

	"github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
)

// TestDefaultMetricTypePinned 把包级默认 metric 契约 pin 到 COSINE：
// defaultMetricType 是建索引（补索引 + 新集合）与 search 传参的唯一来源，
// 一旦误改会让 index metric 与 search metric 同步漂移且不报错。
func TestDefaultMetricTypePinned(t *testing.T) {
	if defaultMetricType != entity.COSINE {
		t.Fatalf("defaultMetricType = %q, want %q", defaultMetricType, entity.COSINE)
	}
}

// TestNormalizeScore 锁定 metric → 0-1 归一化相似度的映射（仓库对外契约：越大越相关）。
// COSINE 走 (s+1)/2（Milvus v2.4.15 实测返回原始余弦相似度，同向 1 / 正交 0 / 反向 -1）；
// L2 与未知 metric 回退 1/(1+d) 保序兼容 legacy l2ToSim；结果恒 clamp 到 [0,1] 防御越界。
func TestNormalizeScore(t *testing.T) {
	tests := []struct {
		name   string
		metric entity.MetricType
		raw    float32
		want   float32
	}{
		// COSINE：[-1,1] 单调折叠到 [0,1]，正交中间值 0.5。
		{name: "cosine identical", metric: entity.COSINE, raw: 1, want: 1},
		{name: "cosine orthogonal", metric: entity.COSINE, raw: 0, want: 0.5},
		{name: "cosine opposite", metric: entity.COSINE, raw: -1, want: 0},
		{name: "cosine typical", metric: entity.COSINE, raw: 0.5, want: 0.75},
		// clamp：SDK/服务端极端的越界 raw 也不得破坏 0-1 契约。
		{name: "cosine overflow clamps to 1", metric: entity.COSINE, raw: 3, want: 1},
		{name: "cosine underflow clamps to 0", metric: entity.COSINE, raw: -3, want: 0},
		// L2：距离（小=近）翻成相似度 1/(1+d)，单调不反转排序。
		{name: "l2 zero distance", metric: entity.L2, raw: 0, want: 1},
		{name: "l2 distance one", metric: entity.L2, raw: 1, want: 0.5},
		{name: "l2 distance three", metric: entity.L2, raw: 3, want: 0.25},
		// 未知 metric 走 default 回退，语义同 L2（绝不放大或反转顺序）。
		{name: "unknown metric falls back to l2 mapping", metric: entity.MetricType("IP"), raw: 1, want: 0.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeScore(tc.metric, tc.raw); got != tc.want {
				t.Fatalf("normalizeScore(%q, %v) = %v, want %v", tc.metric, tc.raw, got, tc.want)
			}
		})
	}
}

// TestSearchRowsToResultsNormalizesCosineAndSkipsBlankRows 用构造的 SDK
// client.SearchResult（Scores + 多输出列）验证：COSINE 原始分被归一化为 0-1
// 相似度，id/content 为空的行被跳过，source_document/chunk_index 照常映射。
func TestSearchRowsToResultsNormalizesCosineAndSkipsBlankRows(t *testing.T) {
	sr := client.SearchResult{
		ResultCount: 3,
		Fields: client.ResultSet{
			entity.NewColumnVarChar("id", []string{"r1", "", "r3"}), // 中间行空 id → 跳过
			entity.NewColumnVarChar("content", []string{"c1", "c2", "c3"}),
			entity.NewColumnVarChar("source_document", []string{"s1", "s2", "s3"}),
			entity.NewColumnInt64("chunk_index", []int64{1, 2, 3}),
		},
		// COSINE 原始余弦相似度：r1 同向(1)、r2 空行分是正交(0)、r3 反向(-1)。
		Scores: []float32{1, 0, -1},
	}
	got := searchRowsToResults(sr, entity.COSINE)
	want := []SearchResult{
		{ID: "r1", Content: "c1", SourceDocument: "s1", ChunkIndex: 1, Score: 1}, // (1+1)/2
		{ID: "r3", Content: "c3", SourceDocument: "s3", ChunkIndex: 3, Score: 0}, // (-1+1)/2
	}
	assertSearchRows(t, got, want)
}

// TestSearchRowsToResultsHonorsL2MetricFallback 验证 metric 透传到 normalizeScore
// 而非硬编码 COSINE，且缺少可选输出列（source_document/chunk_index）不 panic。
func TestSearchRowsToResultsHonorsL2MetricFallback(t *testing.T) {
	sr := client.SearchResult{
		ResultCount: 1,
		Fields: client.ResultSet{
			entity.NewColumnVarChar("id", []string{"r1"}),
			entity.NewColumnVarChar("content", []string{"c1"}),
		},
		Scores: []float32{3}, // L2 距离 3
	}
	got := searchRowsToResults(sr, entity.L2)
	want := []SearchResult{{ID: "r1", Content: "c1", Score: 0.25}} // 1/(1+3)
	assertSearchRows(t, got, want)
}

// TestSearchRowsToResultsDefaultsZeroScoreWhenScoresAbsent 覆盖 Scores 与 ResultCount
// 不同步的防御分支：缺该行 score 时回落 0 而不是越界读 slice。
func TestSearchRowsToResultsDefaultsZeroScoreWhenScoresAbsent(t *testing.T) {
	sr := client.SearchResult{
		ResultCount: 1,
		Fields: client.ResultSet{
			entity.NewColumnVarChar("id", []string{"r1"}),
			entity.NewColumnVarChar("content", []string{"c1"}),
		},
		// 无 Scores：Milvus 空命中批的形态，score 恒为默认 0。
	}
	got := searchRowsToResults(sr, entity.COSINE)
	want := []SearchResult{{ID: "r1", Content: "c1"}}
	assertSearchRows(t, got, want)
}

func assertSearchRows(t *testing.T, got, want []SearchResult) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %d rows (%+v)", got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
