package application

import (
	"github.com/byteBuilderX/stratum/internal/evaluation/domain"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// CompareRunRegression 比较同 suite 两个 run（baseline vs current）的 run 级回归
// （spec §3.2-② / §4.3.6）：对两 run 各自 metrics.by_dimension 的每维 avg_score 求
// current − baseline 的 delta，任一 delta 跌破 RunRegressionDeltaThreshold 即判劣化
// （Regressed=true）。纯函数 + 硬编码阈值，供确认 run 对照与发布哨兵 verdict 复用
// （Task 5/T13 emit，T8 不 emit）。
// 仅两 run 都出现的维度参与（基版缺某维度 = 该维度无从对比，跳过，避免误判）；delta
// 恒为 current − baseline，负值 = 劣化。BaselineSeq/ConfirmedSeq 由调用方（Task 5/T13
// 哨兵）按 run 实际平台版本锚点填充，本函数不解析 metrics.version（保持纯函数不依赖
// JSON 取数；seq 零值由调用方覆盖）。返回永不为 nil（空比较亦为 non-nil 结构）。
func CompareRunRegression(baseline, current *domain.EvalRun) *domain.RunComparison {
	comp := &domain.RunComparison{DimensionDeltas: map[string]float64{}}
	if baseline == nil || current == nil {
		return comp
	}
	base := dimensionAvgScores(baseline.Metrics["by_dimension"])
	cur := dimensionAvgScores(current.Metrics["by_dimension"])
	for dim, curScore := range cur {
		baseScore, ok := base[dim]
		if !ok {
			continue
		}
		delta := curScore - baseScore
		comp.DimensionDeltas[dim] = delta
		if delta < constants.RunRegressionDeltaThreshold {
			comp.Regressed = true
		}
	}
	return comp
}

// dimensionAvgScores 从 run metrics 的 by_dimension 节点提取每维 avg_score（结构见
// metrics.go aggregateByDimension 输出 {avg_score, pass_rate, samples}）。非预期类型或
// 缺 avg_score 的维度跳过（数据缺失不参与对比）。JSONB round-trip 后数字为 float64。
func dimensionAvgScores(v any) map[string]float64 {
	byDim, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]float64, len(byDim))
	for name, entry := range byDim {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if score, ok := m["avg_score"].(float64); ok {
			out[name] = score
		}
	}
	return out
}
