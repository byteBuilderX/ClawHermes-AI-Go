package application

import (
	"context"
	"errors"
	"testing"

	"github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/stretchr/testify/require"
)

// fakeExtractor is a controllable port.LLMExtractor for the extraction stage.
type fakeExtractor struct {
	facts   []*port.ExtractedFact
	err     error
	gotText string
	gotUser string
	gotAgID string
}

func (f *fakeExtractor) ExtractFacts(
	ctx context.Context, userID, agentID, message string,
) ([]*port.ExtractedFact, error) {
	f.gotUser, f.gotAgID, f.gotText = userID, agentID, message
	if f.err != nil {
		return nil, f.err
	}
	return f.facts, nil
}

// fakeRecaller is a controllable OfflineRecaller for the retrieval stage.
type fakeRecaller struct {
	result RecallEvaluationResult
	err    error
	got    RecallEvaluationRequest
	gotTen string
}

func (f *fakeRecaller) Recall(
	ctx context.Context, tenantID string, req RecallEvaluationRequest,
) (RecallEvaluationResult, error) {
	f.gotTen, f.got = tenantID, req
	if f.err != nil {
		return RecallEvaluationResult{}, f.err
	}
	return f.result, nil
}

func session(role, content string) port.MessageDTO {
	return port.MessageDTO{Role: role, Content: content}
}

func offlineFact(content string, entities ...string) *port.ExtractedFact {
	return &port.ExtractedFact{Content: content, Importance: 0.8, FactType: "state", Entities: entities}
}

func TestExtractionEvaluatorRunsStageAndPassesContainment(t *testing.T) {
	fake := &fakeExtractor{facts: []*port.ExtractedFact{
		offlineFact("User prefers Go for backend services", " Go ", "Go"),
		offlineFact("User is based in Shanghai", "Shanghai"),
	}}
	evaluator := NewExtractionEvaluator(fake)
	tc := ExtractionCase{
		Session: []port.MessageDTO{
			session("user", "I prefer Go for backend services."),
			session("user", "I am based in Shanghai."),
		},
		UserID:           "user-1",
		AgentID:          "agent-1",
		ExpectedEntities: []string{"Go", "Shanghai"},
		ExpectedFacts:    []string{"prefers Go"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), tc)
	require.NoError(t, err)
	require.True(t, evaluation.Passed)
	require.Equal(t, "user: I prefer Go for backend services.\nuser: I am based in Shanghai.\n", fake.gotText)
	require.Equal(t, "user-1", fake.gotUser)
	require.Equal(t, "agent-1", fake.gotAgID)
	require.Equal(t, []string{"Go", "Shanghai"}, evaluation.ExtractedEntities)
	require.Equal(t, 1.0, evaluation.EntityRecall)
	require.Equal(t, 1.0, evaluation.EntityPrecision)
	require.Equal(t, 1.0, evaluation.FactRecall)
}

func TestExtractionEvaluatorReportsMissingEntity(t *testing.T) {
	fake := &fakeExtractor{facts: []*port.ExtractedFact{
		offlineFact("User is based in Shanghai", "Shanghai"),
	}}
	evaluator := NewExtractionEvaluator(fake)
	tc := ExtractionCase{
		Session:          []port.MessageDTO{session("user", "I am based in Shanghai.")},
		UserID:           "user-1",
		ExpectedEntities: []string{"Go", "Shanghai"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), tc)
	require.NoError(t, err)
	require.False(t, evaluation.Passed)
	require.InEpsilon(t, 0.5, evaluation.EntityRecall, 0.0001)
	require.Equal(t, 1.0, evaluation.EntityPrecision)
	require.Contains(t, evaluation.Message, "Go")
	require.NotContains(t, evaluation.Message, "Shanghai")
}

func TestExtractionEvaluatorPrecisionPenalizesOverExtraction(t *testing.T) {
	fake := &fakeExtractor{facts: []*port.ExtractedFact{
		offlineFact("User uses Go", "Go", "Rust"),
	}}
	evaluator := NewExtractionEvaluator(fake)
	tc := ExtractionCase{
		Session:          []port.MessageDTO{session("user", "I use Go.")},
		UserID:           "user-1",
		ExpectedEntities: []string{"Go"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), tc)
	require.NoError(t, err)
	// 包含断言只要求期望实体都在；多提的 Rust 不导致失败，但 precision 记分。
	require.True(t, evaluation.Passed)
	require.Equal(t, 1.0, evaluation.EntityRecall)
	require.InEpsilon(t, 0.5, evaluation.EntityPrecision, 0.0001)
}

func TestExtractionEvaluatorUncoveredFactFails(t *testing.T) {
	fake := &fakeExtractor{facts: []*port.ExtractedFact{
		offlineFact("User prefers Go", "Go"),
	}}
	evaluator := NewExtractionEvaluator(fake)
	tc := ExtractionCase{
		Session:       []port.MessageDTO{session("user", "I prefer Go.")},
		UserID:        "user-1",
		ExpectedFacts: []string{"prefers Rust"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), tc)
	require.NoError(t, err)
	require.False(t, evaluation.Passed)
	require.Equal(t, 0.0, evaluation.FactRecall)
	require.Contains(t, evaluation.Message, "prefers Rust")
}

func TestExtractionEvaluatorFactContainmentIsCaseInsensitive(t *testing.T) {
	fake := &fakeExtractor{facts: []*port.ExtractedFact{
		offlineFact("USER  PREFERS   Go", "Go"),
	}}
	evaluator := NewExtractionEvaluator(fake)
	tc := ExtractionCase{
		Session:       []port.MessageDTO{session("user", "I prefer Go.")},
		UserID:        "user-1",
		ExpectedFacts: []string{"user prefers go"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), tc)
	require.NoError(t, err)
	require.True(t, evaluation.Passed)
	require.Equal(t, 1.0, evaluation.FactRecall)
}

func TestExtractionEvaluatorRejectsCaseWithNoExpectations(t *testing.T) {
	// fail-closed：与检索侧 RetrievalCase.validate 拒绝空期望对齐——两维皆空 =
	// 未配置断言，静默绿灯会掩盖「忘填期望」的评测集错误。
	fake := &fakeExtractor{facts: []*port.ExtractedFact{offlineFact("User prefers Go", "Go")}}
	evaluator := NewExtractionEvaluator(fake)
	tc := ExtractionCase{
		Session: []port.MessageDTO{session("user", "I prefer Go.")},
		UserID:  "user-1",
	}

	_, err := evaluator.Evaluate(context.Background(), tc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected entities or facts")
}

func TestExtractionEvaluatorSingleDimensionEmptyOnlyJudgesOther(t *testing.T) {
	// 单维空仍可评测：事实维度未配置，Passed 只由期望实体决定（fail-closed 只挡
	// 两维皆空，不挡单维评测）；未配置维度指标保留 0 供面板参考。
	fake := &fakeExtractor{facts: []*port.ExtractedFact{offlineFact("User prefers Go", "Go")}}
	evaluator := NewExtractionEvaluator(fake)
	tc := ExtractionCase{
		Session:          []port.MessageDTO{session("user", "I prefer Go.")},
		UserID:           "user-1",
		ExpectedEntities: []string{"Go"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), tc)
	require.NoError(t, err)
	require.True(t, evaluation.Passed)
	require.Equal(t, 1.0, evaluation.EntityRecall)
	require.Zero(t, evaluation.FactRecall)
}

func TestExtractionEvaluatorFailClosedWithoutExtractor(t *testing.T) {
	evaluator := NewExtractionEvaluator(nil)
	_, err := evaluator.Evaluate(context.Background(), ExtractionCase{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unavailable")
}

func TestExtractionEvaluatorValidation(t *testing.T) {
	fake := &fakeExtractor{facts: []*port.ExtractedFact{}}
	evaluator := NewExtractionEvaluator(fake)

	t.Run("empty session", func(t *testing.T) {
		_, err := evaluator.Evaluate(context.Background(), ExtractionCase{UserID: "user-1"})
		require.Error(t, err)
	})
	t.Run("missing user", func(t *testing.T) {
		_, err := evaluator.Evaluate(context.Background(), ExtractionCase{Session: []port.MessageDTO{session("user", "hi")}})
		require.Error(t, err)
	})
	t.Run("extractor error wrapped", func(t *testing.T) {
		failing := &fakeExtractor{err: errors.New("llm boom")}
		_, err := NewExtractionEvaluator(failing).Evaluate(context.Background(), ExtractionCase{
			Session: []port.MessageDTO{session("user", "hi")}, UserID: "user-1",
			ExpectedEntities: []string{"Go"},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "llm boom")
		require.Contains(t, err.Error(), "extraction stage")
	})
}

func TestRetrievalEvaluatorPassesWhenAllExpectedRetrieved(t *testing.T) {
	fake := &fakeRecaller{result: RecallEvaluationResult{Hits: []MemoryHit{
		{ID: "fact-a", Content: "likes Go"},
		{ID: "fact-b", Content: "lives in Shanghai"},
		{ID: "fact-c", Content: "works on platform"},
	}}}
	evaluator := NewRetrievalEvaluator(fake)
	tc := RetrievalCase{
		Query:             "where does the user live",
		UserID:            "user-1",
		TopK:              3,
		ExpectedMemoryIDs: []string{"fact-b", "fact-a"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), "tenant-1", tc)
	require.NoError(t, err)
	require.True(t, evaluation.Passed)
	require.Equal(t, []string{"fact-a", "fact-b", "fact-c"}, evaluation.RetrievedIDs)
	require.Equal(t, 1.0, evaluation.RecallAtK)
	require.InEpsilon(t, 2.0/3.0, evaluation.PrecisionAtK, 0.0001)
	require.Equal(t, 1.0, evaluation.MRR)
	require.InEpsilon(t, 1.0, evaluation.NDCGAtK, 0.0001)
	require.Equal(t, "tenant-1", fake.gotTen)
	require.Equal(t, "where does the user live", fake.got.Query)
	require.Equal(t, 3, fake.got.TopK)
	require.Equal(t, "user-1", fake.got.UserID)
}

func TestRetrievalEvaluatorMissingExpectedFails(t *testing.T) {
	fake := &fakeRecaller{result: RecallEvaluationResult{Hits: []MemoryHit{
		{ID: "fact-a"}, {ID: "fact-c"},
	}}}
	evaluator := NewRetrievalEvaluator(fake)
	tc := RetrievalCase{
		Query: "x", UserID: "user-1", TopK: 3,
		ExpectedMemoryIDs: []string{"fact-a", "fact-b"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), "tenant-1", tc)
	require.NoError(t, err)
	require.False(t, evaluation.Passed)
	require.InEpsilon(t, 0.5, evaluation.RecallAtK, 0.0001)
	// 命中 1 / 窗口 k=2（只有 2 条 retrieved）→ precision@k = 1/2。
	require.InEpsilon(t, 0.5, evaluation.PrecisionAtK, 0.0001)
	require.Equal(t, 1.0, evaluation.MRR)
	require.Contains(t, evaluation.Message, "fact-b")
}

func TestRetrievalEvaluatorMRRFirstRelevantAtSecondRank(t *testing.T) {
	fake := &fakeRecaller{result: RecallEvaluationResult{Hits: []MemoryHit{
		{ID: "fact-x"}, {ID: "fact-a"},
	}}}
	evaluator := NewRetrievalEvaluator(fake)
	tc := RetrievalCase{
		Query: "x", UserID: "user-1", TopK: 2,
		ExpectedMemoryIDs: []string{"fact-a"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), "tenant-1", tc)
	require.NoError(t, err)
	require.True(t, evaluation.Passed)
	require.InEpsilon(t, 0.5, evaluation.MRR, 0.0001)
	require.Equal(t, 1.0, evaluation.RecallAtK)
	require.InEpsilon(t, 0.5, evaluation.PrecisionAtK, 0.0001)
}

func TestRetrievalEvaluatorTruncatesToTopKWindow(t *testing.T) {
	// 阶段返回超出窗口的候选；评测 runner 只对 top-k 窗口断言（recall@k 定义）。
	fake := &fakeRecaller{result: RecallEvaluationResult{Hits: []MemoryHit{
		{ID: "fact-a"}, {ID: "fact-b"}, {ID: "fact-c"}, {ID: "fact-d"},
	}}}
	evaluator := NewRetrievalEvaluator(fake)
	tc := RetrievalCase{
		Query: "x", UserID: "user-1", TopK: 2,
		ExpectedMemoryIDs: []string{"fact-a"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), "tenant-1", tc)
	require.NoError(t, err)
	require.True(t, evaluation.Passed)
	require.Equal(t, []string{"fact-a", "fact-b"}, evaluation.RetrievedIDs)
	require.Equal(t, 1.0, evaluation.RecallAtK)
	require.InEpsilon(t, 0.5, evaluation.PrecisionAtK, 0.0001)
	require.Equal(t, 2, fake.got.TopK)
}

func TestRetrievalEvaluatorClampsTopKWindow(t *testing.T) {
	// M-1：评测窗口归一化到 [MemoryRecallMinTopK, MemoryRecallMaxTopK]（镜像生产
	// 召回 clamp），防止未来 recaller adapter 按 2*topK 拉候选时随评测集无界放大。
	t.Run("zero falls back to default", func(t *testing.T) {
		fake := &fakeRecaller{result: RecallEvaluationResult{}}
		evaluator := NewRetrievalEvaluator(fake)
		_, err := evaluator.Evaluate(context.Background(), "tenant-1", RetrievalCase{
			Query: "x", UserID: "u", ExpectedMemoryIDs: []string{"a"},
		})
		require.NoError(t, err)
		require.Equal(t, offlineEvalDefaultTopK, fake.got.TopK)
	})
	t.Run("exceeds max clamps to MemoryRecallMaxTopK", func(t *testing.T) {
		fake := &fakeRecaller{result: RecallEvaluationResult{}}
		evaluator := NewRetrievalEvaluator(fake)
		_, err := evaluator.Evaluate(context.Background(), "tenant-1", RetrievalCase{
			Query: "x", UserID: "u", TopK: constants.MemoryRecallMaxTopK + 100,
			ExpectedMemoryIDs: []string{"a"},
		})
		require.NoError(t, err)
		require.Equal(t, constants.MemoryRecallMaxTopK, fake.got.TopK)
	})
	t.Run("within range passes through", func(t *testing.T) {
		fake := &fakeRecaller{result: RecallEvaluationResult{}}
		evaluator := NewRetrievalEvaluator(fake)
		_, err := evaluator.Evaluate(context.Background(), "tenant-1", RetrievalCase{
			Query: "x", UserID: "u", TopK: 3, ExpectedMemoryIDs: []string{"a"},
		})
		require.NoError(t, err)
		require.Equal(t, 3, fake.got.TopK)
	})
}

func TestRetrievalEvaluatorDeduplicatesDuplicateHits(t *testing.T) {
	fake := &fakeRecaller{result: RecallEvaluationResult{Hits: []MemoryHit{
		{ID: "fact-a"}, {ID: "fact-a"}, {ID: "fact-b"},
	}}}
	evaluator := NewRetrievalEvaluator(fake)
	tc := RetrievalCase{
		Query: "x", UserID: "user-1", TopK: 3,
		ExpectedMemoryIDs: []string{"fact-a", "fact-b"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), "tenant-1", tc)
	require.NoError(t, err)
	require.True(t, evaluation.Passed)
	require.Equal(t, []string{"fact-a", "fact-b"}, evaluation.RetrievedIDs)
	require.Equal(t, 1.0, evaluation.RecallAtK)
	require.Equal(t, 1.0, evaluation.PrecisionAtK)
	require.Equal(t, 1.0, evaluation.MRR)
}

func TestRetrievalEvaluatorNoHits(t *testing.T) {
	fake := &fakeRecaller{result: RecallEvaluationResult{}}
	evaluator := NewRetrievalEvaluator(fake)
	tc := RetrievalCase{
		Query: "x", UserID: "user-1", TopK: 5,
		ExpectedMemoryIDs: []string{"fact-a"},
	}

	evaluation, err := evaluator.Evaluate(context.Background(), "tenant-1", tc)
	require.NoError(t, err)
	require.False(t, evaluation.Passed)
	require.Empty(t, evaluation.RetrievedIDs)
	require.Equal(t, 0.0, evaluation.RecallAtK)
	require.Equal(t, 0.0, evaluation.MRR)
}

func TestRetrievalEvaluatorFailClosed(t *testing.T) {
	t.Run("nil recaller", func(t *testing.T) {
		_, err := NewRetrievalEvaluator(nil).Evaluate(context.Background(), "tenant-1", RetrievalCase{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "unavailable")
	})
	t.Run("empty tenant", func(t *testing.T) {
		_, err := NewRetrievalEvaluator(&fakeRecaller{}).Evaluate(context.Background(), "", RetrievalCase{Query: "x"})
		require.Error(t, err)
	})
	t.Run("recaller error wrapped", func(t *testing.T) {
		failing := &fakeRecaller{err: errors.New("milvus down")}
		_, err := NewRetrievalEvaluator(failing).Evaluate(context.Background(), "tenant-1", RetrievalCase{
			Query: "x", UserID: "user-1", TopK: 3, ExpectedMemoryIDs: []string{"fact-a"},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "milvus down")
	})
}

func TestRetrievalEvaluatorValidation(t *testing.T) {
	fake := &fakeRecaller{}
	evaluator := NewRetrievalEvaluator(fake)

	t.Run("empty query", func(t *testing.T) {
		_, err := evaluator.Evaluate(context.Background(), "tenant-1",
			RetrievalCase{UserID: "u", TopK: 3, ExpectedMemoryIDs: []string{"a"}})
		require.Error(t, err)
	})
	t.Run("missing user", func(t *testing.T) {
		_, err := evaluator.Evaluate(context.Background(), "tenant-1",
			RetrievalCase{Query: "x", TopK: 3, ExpectedMemoryIDs: []string{"a"}})
		require.Error(t, err)
	})
	t.Run("empty expectations", func(t *testing.T) {
		_, err := evaluator.Evaluate(context.Background(), "tenant-1", RetrievalCase{Query: "x", UserID: "u", TopK: 3})
		require.Error(t, err)
	})
}

func TestOfflineEvaluatorEvaluateCaseRoutesByStage(t *testing.T) {
	extractor := &fakeExtractor{facts: []*port.ExtractedFact{offlineFact("User prefers Go", "Go")}}
	recaller := &fakeRecaller{result: RecallEvaluationResult{Hits: []MemoryHit{{ID: "fact-a"}}}}
	evaluator := NewOfflineEvaluator(extractor, recaller)
	ctx := context.Background()

	t.Run("extract stage", func(t *testing.T) {
		result, err := evaluator.EvaluateCase(ctx, "tenant-1", OfflinePipelineCase{
			ID: "case-1", Name: "extract Go", Stage: OfflineStageExtract,
			Extract: &ExtractionCase{
				Session: []port.MessageDTO{session("user", "I prefer Go.")}, UserID: "user-1",
				ExpectedEntities: []string{"Go"},
			},
		})
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Equal(t, OfflineStageExtract, result.Stage)
		require.NotNil(t, result.Extraction)
		require.Nil(t, result.Retrieval)
	})

	t.Run("retrieve stage", func(t *testing.T) {
		result, err := evaluator.EvaluateCase(ctx, "tenant-1", OfflinePipelineCase{
			ID: "case-2", Name: "retrieve fact-a", Stage: OfflineStageRetrieve,
			Retrieve: &RetrievalCase{Query: "x", UserID: "user-1", TopK: 3, ExpectedMemoryIDs: []string{"fact-a"}},
		})
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Equal(t, OfflineStageRetrieve, result.Stage)
		require.NotNil(t, result.Retrieval)
		require.Nil(t, result.Extraction)
	})
}

func TestOfflineEvaluatorEvaluateCaseValidation(t *testing.T) {
	evaluator := NewOfflineEvaluator(&fakeExtractor{}, &fakeRecaller{})
	ctx := context.Background()

	t.Run("unsupported stage", func(t *testing.T) {
		_, err := evaluator.EvaluateCase(ctx, "tenant-1", OfflinePipelineCase{Stage: "summary"})
		require.Error(t, err)
	})
	t.Run("missing extract input", func(t *testing.T) {
		_, err := evaluator.EvaluateCase(ctx, "tenant-1", OfflinePipelineCase{Stage: OfflineStageExtract})
		require.Error(t, err)
	})
	t.Run("missing retrieve input", func(t *testing.T) {
		_, err := evaluator.EvaluateCase(ctx, "tenant-1", OfflinePipelineCase{Stage: OfflineStageRetrieve})
		require.Error(t, err)
	})
}
