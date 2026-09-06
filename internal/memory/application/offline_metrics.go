package application

import (
	"math"
	"sort"
	"strings"

	"github.com/byteBuilderX/stratum/internal/memory/domain/port"
)

// 本文件实现 memory 离线管道评测（spec §6.4）的确定性度量纯函数。检索类度量
// 与 knowledge/application/metrics.go 同构（同一 doc-set/rank-window 语义：
// 空 relevant / 空 retrieved 返回 0 不除零；k 被 clamp 到 retrieved 长度；
// retrieved 先按首现去重，避免 chunk 级多占 rank 使 recall/nDCG 超过 1）。

// offlineRecallAtK 是 top-k 内命中的期望记忆比例。
func offlineRecallAtK(retrieved, relevant []string, k int) float64 {
	retrieved = offlineDedupePreservingOrder(retrieved)
	if len(relevant) == 0 || k <= 0 {
		return 0
	}
	if k > len(retrieved) {
		k = len(retrieved)
	}
	rel := offlineIDSet(relevant)
	hits := 0
	for _, id := range retrieved[:k] {
		if _, ok := rel[id]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(relevant))
}

// offlinePrecisionAtK 是 top-k 结果中期望记忆的比例。
func offlinePrecisionAtK(retrieved, relevant []string, k int) float64 {
	retrieved = offlineDedupePreservingOrder(retrieved)
	if k <= 0 {
		return 0
	}
	if k > len(retrieved) {
		k = len(retrieved)
	}
	if k == 0 {
		return 0
	}
	rel := offlineIDSet(relevant)
	hits := 0
	for _, id := range retrieved[:k] {
		if _, ok := rel[id]; ok {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// offlineMRR 是首个命中期望记忆的排名的倒数；无命中返回 0。
func offlineMRR(retrieved, relevant []string) float64 {
	retrieved = offlineDedupePreservingOrder(retrieved)
	rel := offlineIDSet(relevant)
	for i, id := range retrieved {
		if _, ok := rel[id]; ok {
			return 1 / float64(i+1)
		}
	}
	return 0
}

// offlineNDCGAtK 按二值增益在理想排序上归一化 DCG@k。
func offlineNDCGAtK(retrieved, relevant []string, k int) float64 {
	retrieved = offlineDedupePreservingOrder(retrieved)
	if k <= 0 {
		return 0
	}
	if k > len(retrieved) {
		k = len(retrieved)
	}
	if k == 0 {
		return 0
	}
	rel := offlineIDSet(relevant)
	dcg := 0.0
	for i, id := range retrieved[:k] {
		if _, ok := rel[id]; ok {
			dcg += 1 / math.Log2(float64(i+2))
		}
	}
	ideal := len(relevant)
	if ideal > k {
		ideal = k
	}
	if ideal == 0 {
		return 0
	}
	idcg := 0.0
	for i := range ideal {
		idcg += 1 / math.Log2(float64(i+2))
	}
	return dcg / idcg
}

// offlineDedupePreservingOrder 去重并保留各 ID 首现位置。
func offlineDedupePreservingOrder(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func offlineIDSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// 提取阶段度量（§6.4）：实体名集合的 recall/precision 与事实内容覆盖（包含断言）。

// cleanEntityNames trim、去空、按首现去重并排序（镜像 domain canonical 路径的
// 实体名归一：只 trim，不改变大小写）。
func cleanEntityNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// extractedEntityUnion 汇总一批提取事实的实体名为排序去重集合。
func extractedEntityUnion(facts []*port.ExtractedFact) []string {
	seen := make(map[string]struct{})
	for _, fact := range facts {
		if fact == nil {
			continue
		}
		for _, name := range fact.Entities {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// expectedSetRecall 是期望集合中出现在实际集合的比例（len(expected)==0 → 0）。
func expectedSetRecall(actual, expected []string) float64 {
	if len(expected) == 0 {
		return 0
	}
	actualSet := offlineIDSet(actual)
	hits := 0
	for _, want := range expected {
		if _, ok := actualSet[want]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(expected))
}

// expectedSetPrecision 是实际集合中属于期望集合的比例（len(actual)==0 → 0）。
func expectedSetPrecision(actual, expected []string) float64 {
	if len(actual) == 0 {
		return 0
	}
	expectedSet := offlineIDSet(expected)
	hits := 0
	for _, have := range actual {
		if _, ok := expectedSet[have]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(actual))
}

// normalizeArtifactText 小写并折叠空白，使 LLM 提取内容的大小写/排版差异不使
// 确定性包含断言脆弱。
func normalizeArtifactText(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// cleanExpectedFacts trim、去空并按规范化文本去重；保留去重后的首个原始文本。
func cleanExpectedFacts(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := normalizeArtifactText(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

// factContentCovered 报告期望事实内容是否作为规范化子串覆盖到至少一条实际事实。
func factContentCovered(facts []ExtractedArtifactFact, want string) bool {
	target := normalizeArtifactText(want)
	if target == "" {
		return true
	}
	for _, fact := range facts {
		if strings.Contains(normalizeArtifactText(fact.Content), target) {
			return true
		}
	}
	return false
}

// factCoverageRecall 是期望事实内容被实际事实覆盖的比例（len(expected)==0 → 0）。
func factCoverageRecall(facts []ExtractedArtifactFact, expected []string) float64 {
	if len(expected) == 0 {
		return 0
	}
	covered := 0
	for _, want := range expected {
		if factContentCovered(facts, want) {
			covered++
		}
	}
	return float64(covered) / float64(len(expected))
}
