package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// 本文件把 memory 异步离线管道变成可评测对象（spec §6.4，卡 B：§6.4 离线链路
// 评测）：每个阶段作为「输入→产物」的变换，评测 runner 直接同步触发管道阶段
// （管道直调①，成本最低）并对变换产物做确定性断言，不依赖在线对话。实现模式
// 镜像 knowledge/application/retrieval_evaluator.go：评估器只依赖阶段端口
// （LLM 提取器 / 检索召回端口），真实实现由 wiring seam 注入，单测 mock 端口。
// 摘要 judge/回放②/链路③ 属设计内预留（backlog），不在本文件实现。

// OfflineStage 标识可离线评测的记忆管道阶段。
type OfflineStage string

const (
	// OfflineStageExtract 提取阶段：会话文本 → 期望实体/事实（§6.4 提取行）。
	OfflineStageExtract OfflineStage = "extract"
	// OfflineStageRetrieve 检索阶段：查询 → 期望命中记忆（§6.4 检索行，与
	// knowledge 检索评测同构：recall@k / mrr）。
	OfflineStageRetrieve OfflineStage = "retrieve"
)

func (s OfflineStage) validate() error {
	switch s {
	case OfflineStageExtract, OfflineStageRetrieve:
		return nil
	case "":
		return errors.New("offline memory evaluation: stage required")
	default:
		return fmt.Errorf("offline memory evaluation: unsupported stage %q", s)
	}
}

// offlineEvalDefaultTopK 是检索评测 case 未显式给 TopK（0/负）时的窗口默认值。
// 数值与生产召回兜底常量 constants.MemoryRecallTopK（=5）保持一致：评测默认窗
// 口即生产默认召回窗口。若生产侧调整兜底值，须同步评估此处，防止静默漂移。
// 评测集作者仍应显式给 TopK，此处仅兜底避免 case 因缺省被拒。
const offlineEvalDefaultTopK = 5

// normalizeRetrievalTopK 把检索评测窗口归一化到
// [constants.MemoryRecallMinTopK, constants.MemoryRecallMaxTopK]（镜像生产召回
// clamp）：0/负回退默认，超上界截断。防止未来 recaller adapter 镜像 2*topK
// 候选拉取时随评测集无界放大。
func normalizeRetrievalTopK(requested int) int {
	if requested <= 0 {
		requested = offlineEvalDefaultTopK
	}
	if requested > constants.MemoryRecallMaxTopK {
		requested = constants.MemoryRecallMaxTopK
	}
	return requested
}

// --- 提取阶段 ---

// ExtractedArtifactFact 是提取阶段产物中单条事实的可序列化快照。
type ExtractedArtifactFact struct {
	Content  string   `json:"content"`
	Entities []string `json:"entities"`
}

// ExtractionCase 是一条提取阶段离线评测 case（§6.4）：会话文本 → 期望提取的
// 实体/事实。ExpectedEntities / ExpectedFacts 至少一个非空（fail-closed：两维
// 皆空视为未配置断言，validate 拒绝，与检索侧 RetrievalCase.validate 拒绝空期
// 望对齐）；单维为空时该维不参与 Passed 判定（只保留对应指标 0，供面板参考）。
type ExtractionCase struct {
	// Session 是会话文本（管道与在线路径同构：role + ": " + content 逐条渲染）。
	Session          []port.MessageDTO
	UserID           string
	AgentID          string
	ExpectedEntities []string
	ExpectedFacts    []string
}

func (c ExtractionCase) validate() error {
	if strings.TrimSpace(c.UserID) == "" {
		return errors.New("offline extraction case: user ID required")
	}
	if len(c.Session) == 0 || strings.TrimSpace(renderSessionText(c.Session)) == "" {
		return errors.New("offline extraction case: session text required")
	}
	if len(c.ExpectedEntities) == 0 && len(c.ExpectedFacts) == 0 {
		return errors.New("offline extraction case: at least one of expected entities or facts required")
	}
	return nil
}

// ExtractionEvaluation 是提取阶段产物断言结果：Passed 为确定性包含断言（期望
// 实体全部出现且期望事实内容全部被覆盖），指标带 recall/precision 供分级。
type ExtractionEvaluation struct {
	Passed            bool                    `json:"passed"`
	Message           string                  `json:"message,omitempty"`
	EntityRecall      float64                 `json:"entity_recall,omitempty"`
	EntityPrecision   float64                 `json:"entity_precision,omitempty"`
	FactRecall        float64                 `json:"fact_recall,omitempty"`
	ExtractedEntities []string                `json:"extracted_entities,omitempty"`
	Facts             []ExtractedArtifactFact `json:"facts,omitempty"`
}

// ExtractionEvaluator 直接调用提取阶段端口（管道直调①）并对产物做确定性断言。
type ExtractionEvaluator struct {
	extractor port.LLMExtractor
}

// NewExtractionEvaluator 构造提取阶段评估器；extractor 由 wiring 注入真实
// LLM 提取器（pipeline.LLMExtractor），单测注入 fake。
func NewExtractionEvaluator(extractor port.LLMExtractor) *ExtractionEvaluator {
	return &ExtractionEvaluator{extractor: extractor}
}

// Evaluate 喂入会话文本 → 同步触发提取阶段 → 对产物做确定性断言。
func (e *ExtractionEvaluator) Evaluate(ctx context.Context, tc ExtractionCase) (ExtractionEvaluation, error) {
	if e == nil || e.extractor == nil {
		return ExtractionEvaluation{}, errors.New("offline extraction evaluator unavailable")
	}
	if err := tc.validate(); err != nil {
		return ExtractionEvaluation{}, err
	}
	facts, err := e.extractor.ExtractFacts(ctx, tc.UserID, tc.AgentID, renderSessionText(tc.Session))
	if err != nil {
		return ExtractionEvaluation{}, fmt.Errorf("offline extraction: run extraction stage: %w", err)
	}
	artifacts := buildExtractionArtifacts(facts)
	expectedEntities := cleanEntityNames(tc.ExpectedEntities)
	expectedFacts := cleanExpectedFacts(tc.ExpectedFacts)
	actualEntities := extractedEntityUnion(facts)
	evaluation := ExtractionEvaluation{
		EntityRecall:      expectedSetRecall(actualEntities, expectedEntities),
		EntityPrecision:   expectedSetPrecision(actualEntities, expectedEntities),
		FactRecall:        factCoverageRecall(artifacts, expectedFacts),
		ExtractedEntities: actualEntities,
		Facts:             artifacts,
	}
	evaluation.Passed = extractionPassed(expectedEntities, expectedFacts, evaluation)
	if !evaluation.Passed {
		evaluation.Message = extractionFailureMessage(expectedEntities, expectedFacts, evaluation)
	}
	return evaluation, nil
}

// buildExtractionArtifacts 把提取阶段返回的事实转成可序列化快照（跳过 nil）。
func buildExtractionArtifacts(facts []*port.ExtractedFact) []ExtractedArtifactFact {
	artifacts := make([]ExtractedArtifactFact, 0, len(facts))
	for _, fact := range facts {
		if fact == nil {
			continue
		}
		artifacts = append(artifacts, ExtractedArtifactFact{
			Content:  fact.Content,
			Entities: cleanEntityNames(fact.Entities),
		})
	}
	return artifacts
}

// extractionPassed 是提取阶段确定性包含断言：期望实体/事实维度为空则跳过该维度。
func extractionPassed(expectedEntities, expectedFacts []string, evaluation ExtractionEvaluation) bool {
	entityOK := len(expectedEntities) == 0 || evaluation.EntityRecall == 1
	if !entityOK {
		return false
	}
	return len(expectedFacts) == 0 || evaluation.FactRecall == 1
}

// extractionFailureMessage 汇总未命中的期望实体与未被覆盖的期望事实（确定性）。
func extractionFailureMessage(expectedEntities, expectedFacts []string, evaluation ExtractionEvaluation) string {
	var parts []string
	actualSet := offlineIDSet(evaluation.ExtractedEntities)
	var missing []string
	for _, want := range expectedEntities {
		if _, ok := actualSet[want]; !ok {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		parts = append(parts, "missing entities ["+strings.Join(missing, ", ")+"]")
	}
	var uncovered []string
	for _, want := range expectedFacts {
		if !factContentCovered(evaluation.Facts, want) {
			uncovered = append(uncovered, want)
		}
	}
	if len(uncovered) > 0 {
		parts = append(parts, "uncovered facts ["+strings.Join(uncovered, ", ")+"]")
	}
	return "offline extraction assertion: " + strings.Join(parts, "; ")
}

// renderSessionText 与会话处理路径（application/extraction.go ExtractFacts）
// 逐条渲染消息为 "role: content\n"。
func renderSessionText(session []port.MessageDTO) string {
	var text strings.Builder
	for _, message := range session {
		text.WriteString(message.Role)
		text.WriteString(": ")
		text.WriteString(message.Content)
		text.WriteByte('\n')
	}
	return text.String()
}

// --- 检索阶段 ---

// MemoryHit 是检索阶段产物中的一条命中记忆（按阶段相关性排序）。
type MemoryHit struct {
	ID      string `json:"id"`
	Content string `json:"content,omitempty"`
}

// RecallEvaluationRequest 描述一次检索阶段直调所需的输入。
type RecallEvaluationRequest struct {
	Query   string
	UserID  string
	AgentID string
	TopK    int
}

// RecallEvaluationResult 是检索阶段返回的有序产物列表。
type RecallEvaluationResult struct {
	Hits []MemoryHit
}

// OfflineRecaller 是离线评测可直调的检索阶段端口。wiring 在真实评测环境用
// MemoryService 的召回路径（RecallMemory 或只读变体）实现；单测注入 fake。
type OfflineRecaller interface {
	Recall(ctx context.Context, tenantID string, req RecallEvaluationRequest) (RecallEvaluationResult, error)
}

// RetrievalCase 是一条检索阶段离线评测 case（§6.4）：查询 → 期望命中记忆。
type RetrievalCase struct {
	Query   string
	UserID  string
	AgentID string
	// TopK 是评测窗口（recall@k 的 k）。运行时经 normalizeRetrievalTopK 归一化到
	// [MemoryRecallMinTopK, MemoryRecallMaxTopK]：0/负回退默认，超上限截断。
	TopK              int
	ExpectedMemoryIDs []string
}

func (c RetrievalCase) validate() error {
	if strings.TrimSpace(c.Query) == "" {
		return errors.New("offline retrieval case: query required")
	}
	if strings.TrimSpace(c.UserID) == "" {
		return errors.New("offline retrieval case: user ID required")
	}
	if len(c.ExpectedMemoryIDs) == 0 {
		return errors.New("offline retrieval case: expected memory IDs required")
	}
	return nil
}

// RetrievalEvaluation 是检索阶段产物断言结果：recall@k / precision@k / mrr /
// ndcg@k 与 knowledge 检索评测同构；Passed = 全部期望记忆落在 top-k 窗口。
type RetrievalEvaluation struct {
	Passed       bool     `json:"passed"`
	Message      string   `json:"message,omitempty"`
	RetrievedIDs []string `json:"retrieved_ids,omitempty"`
	RecallAtK    float64  `json:"recall_at_k,omitempty"`
	PrecisionAtK float64  `json:"precision_at_k,omitempty"`
	MRR          float64  `json:"mrr,omitempty"`
	NDCGAtK      float64  `json:"ndcg_at_k,omitempty"`
}

// RetrievalEvaluator 直接调用检索阶段端口并对产物做确定性断言。
type RetrievalEvaluator struct {
	recaller OfflineRecaller
}

// NewRetrievalEvaluator 构造检索阶段评估器。
func NewRetrievalEvaluator(recaller OfflineRecaller) *RetrievalEvaluator {
	return &RetrievalEvaluator{recaller: recaller}
}

// Evaluate 喂入查询 → 同步触发检索阶段 → 对 top-k 命中做确定性断言。
func (e *RetrievalEvaluator) Evaluate(
	ctx context.Context, tenantID string, tc RetrievalCase,
) (RetrievalEvaluation, error) {
	if e == nil || e.recaller == nil {
		return RetrievalEvaluation{}, errors.New("offline retrieval evaluator unavailable")
	}
	if err := tc.validate(); err != nil {
		return RetrievalEvaluation{}, err
	}
	if strings.TrimSpace(tenantID) == "" {
		return RetrievalEvaluation{}, errors.New("offline retrieval case: tenant ID required")
	}
	topK := normalizeRetrievalTopK(tc.TopK)
	result, err := e.recaller.Recall(ctx, tenantID, RecallEvaluationRequest{
		Query: tc.Query, UserID: tc.UserID, AgentID: tc.AgentID, TopK: topK,
	})
	if err != nil {
		return RetrievalEvaluation{}, fmt.Errorf("offline retrieval: run retrieval stage: %w", err)
	}
	retrievedIDs := collectHitIDs(result.Hits, topK)
	relevant := cleanEntityNames(tc.ExpectedMemoryIDs)
	evaluation := RetrievalEvaluation{
		RetrievedIDs: retrievedIDs,
		RecallAtK:    offlineRecallAtK(retrievedIDs, relevant, topK),
		PrecisionAtK: offlinePrecisionAtK(retrievedIDs, relevant, topK),
		MRR:          offlineMRR(retrievedIDs, relevant),
		NDCGAtK:      offlineNDCGAtK(retrievedIDs, relevant, topK),
	}
	// RecallAtK==1 ⟺ 全部期望记忆都在 top-k 窗口内（确定性包含断言）。
	evaluation.Passed = evaluation.RecallAtK == 1
	if !evaluation.Passed {
		evaluation.Message = retrievalFailureMessage(relevant, retrievedIDs)
	}
	return evaluation, nil
}

// collectHitIDs 收集非空命中 ID、截断到 top-k 窗口并保序去重。
func collectHitIDs(hits []MemoryHit, topK int) []string {
	rawIDs := make([]string, 0, len(hits))
	for _, hit := range hits {
		if strings.TrimSpace(hit.ID) != "" {
			rawIDs = append(rawIDs, hit.ID)
		}
	}
	if len(rawIDs) > topK {
		rawIDs = rawIDs[:topK]
	}
	return offlineDedupePreservingOrder(rawIDs)
}

// retrievalFailureMessage 汇总未命中 top-k 窗口的期望记忆 ID。
func retrievalFailureMessage(relevant, retrievedIDs []string) string {
	actualSet := offlineIDSet(retrievedIDs)
	var missing []string
	for _, want := range relevant {
		if _, ok := actualSet[want]; !ok {
			missing = append(missing, want)
		}
	}
	return "offline retrieval assertion: missing memories [" + strings.Join(missing, ", ") + "]"
}

// --- 管道直调 runner（按阶段分发） ---

// OfflinePipelineCase 是一条离线管道评测 case：按 Stage 只设置 Extract 或
// Retrieve 之一。
type OfflinePipelineCase struct {
	ID       string
	Name     string
	Stage    OfflineStage
	Extract  *ExtractionCase
	Retrieve *RetrievalCase
}

// OfflineCaseResult 是一次直调评测的结果，结构与评测中心的 case result 对齐
// （Passed/Message + 阶段产物），便于后续接入 EvalSuite/Revision 时映射。
type OfflineCaseResult struct {
	CaseID     string                `json:"case_id,omitempty"`
	Stage      OfflineStage          `json:"stage"`
	Passed     bool                  `json:"passed"`
	Message    string                `json:"message,omitempty"`
	Extraction *ExtractionEvaluation `json:"extraction,omitempty"`
	Retrieval  *RetrievalEvaluation  `json:"retrieval,omitempty"`
}

// OfflineEvaluator 是 §6.4 管道直调①的 runner：喂 case → 直接触发对应管道阶段
// → 对产物断言。
type OfflineEvaluator struct {
	extract   *ExtractionEvaluator
	retrieval *RetrievalEvaluator
}

// NewOfflineEvaluator 构造管道直调 runner；两个阶段端口都可独立注入。
func NewOfflineEvaluator(extractor port.LLMExtractor, recaller OfflineRecaller) *OfflineEvaluator {
	return &OfflineEvaluator{
		extract:   NewExtractionEvaluator(extractor),
		retrieval: NewRetrievalEvaluator(recaller),
	}
}

// EvaluateCase 按 case.Stage 分发到对应阶段评估器。
func (e *OfflineEvaluator) EvaluateCase(
	ctx context.Context, tenantID string, c OfflinePipelineCase,
) (OfflineCaseResult, error) {
	if e == nil || e.extract == nil || e.retrieval == nil {
		return OfflineCaseResult{}, errors.New("offline pipeline evaluator unavailable")
	}
	if err := c.Stage.validate(); err != nil {
		return OfflineCaseResult{}, err
	}
	switch c.Stage {
	case OfflineStageExtract:
		return e.evaluateExtractionCase(ctx, c)
	case OfflineStageRetrieve:
		return e.evaluateRetrievalCase(ctx, tenantID, c)
	default:
		return OfflineCaseResult{}, fmt.Errorf("offline pipeline evaluator: unsupported stage %q", c.Stage)
	}
}

func (e *OfflineEvaluator) evaluateExtractionCase(
	ctx context.Context, c OfflinePipelineCase,
) (OfflineCaseResult, error) {
	if c.Extract == nil {
		return OfflineCaseResult{}, errors.New("offline pipeline case: extract input missing")
	}
	evaluation, err := e.extract.Evaluate(ctx, *c.Extract)
	if err != nil {
		return OfflineCaseResult{}, err
	}
	return OfflineCaseResult{
		CaseID: c.ID, Stage: c.Stage, Passed: evaluation.Passed, Message: evaluation.Message,
		Extraction: &evaluation,
	}, nil
}

func (e *OfflineEvaluator) evaluateRetrievalCase(
	ctx context.Context, tenantID string, c OfflinePipelineCase,
) (OfflineCaseResult, error) {
	if c.Retrieve == nil {
		return OfflineCaseResult{}, errors.New("offline pipeline case: retrieve input missing")
	}
	evaluation, err := e.retrieval.Evaluate(ctx, tenantID, *c.Retrieve)
	if err != nil {
		return OfflineCaseResult{}, err
	}
	return OfflineCaseResult{
		CaseID: c.ID, Stage: c.Stage, Passed: evaluation.Passed, Message: evaluation.Message,
		Retrieval: &evaluation,
	}, nil
}
