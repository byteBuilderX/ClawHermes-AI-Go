package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	knowledgeport "github.com/byteBuilderX/stratum/internal/knowledge/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"go.uber.org/zap"
)

type stubSufficiencyJudge struct {
	verdict          knowledgeport.SufficiencyVerdict
	err              error
	lastInstructions string
	lastEvidence     string
}

func (s *stubSufficiencyJudge) JudgeSufficiency(_ context.Context, _, evidence, instructions string) (knowledgeport.SufficiencyVerdict, error) {
	s.lastEvidence = evidence
	s.lastInstructions = instructions
	return s.verdict, s.err
}

// judgeMetrics records judge/no-answer metric calls alongside NoopMetrics.
type judgeMetrics struct {
	observability.NoopMetrics
	judge    []string // model:status
	noAnswer []string // tenantID:reason
}

func (m *judgeMetrics) IncKnowledgeJudge(model, status string) {
	m.judge = append(m.judge, model+":"+status)
}

func (m *judgeMetrics) IncNoAnswer(tenantID, reason string) {
	m.noAnswer = append(m.noAnswer, tenantID+":"+reason)
}

func gateResult() *RAGQueryResult {
	return &RAGQueryResult{
		Sources:        []Source{{DocumentID: "d1", Content: "c1", Score: 0.8}},
		BestScore:      0.8,
		CandidateCount: 3,
	}
}

func TestJudgeSufficiencyGate(t *testing.T) {
	judge := func(j knowledgeport.SufficiencyJudge) *RAGService {
		rs := NewRAGService(nil, nil, zap.NewNop())
		rs.SetSufficiencyJudgeResolver(func(_ context.Context, _ string) (knowledgeport.SufficiencyJudge, error) { return j, nil })
		return rs
	}

	cases := []struct {
		name       string
		rs         *RAGService
		result     *RAGQueryResult
		wantAnswer bool
		wantReason NoAnswerReason
	}{
		{
			name:       "nil judge 未装配 fail-open 放行",
			rs:         NewRAGService(nil, nil, zap.NewNop()),
			wantAnswer: true,
		},
		{
			name:       "sufficient 放行",
			rs:         judge(&stubSufficiencyJudge{verdict: knowledgeport.SufficiencySufficient}),
			wantAnswer: true,
		},
		{
			name:       "insufficient 升级为 insufficient_evidence 且清空 sources",
			rs:         judge(&stubSufficiencyJudge{verdict: knowledgeport.SufficiencyInsufficient}),
			wantAnswer: false,
			wantReason: NoAnswerInsufficientEvidence,
		},
		{
			name:       "judge 失败降级 fail-open 放行",
			rs:         judge(&stubSufficiencyJudge{err: errors.New("timeout")}),
			wantAnswer: true,
		},
		{
			name: "空 sources 不判（no_sources 已是更强信号）",
			rs:   judge(&stubSufficiencyJudge{verdict: knowledgeport.SufficiencyInsufficient}),
			result: &RAGQueryResult{
				NoAnswer:       buildNoAnswer(NoAnswerNoSources, 0, 0, 0),
				BestScore:      0,
				CandidateCount: 0,
			},
			wantAnswer: false,
			wantReason: NoAnswerNoSources,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.result == nil {
				tc.result = gateResult()
			}
			got := tc.rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "qwen-turbo", "测试指令", tc.result)
			if tc.wantAnswer {
				if got.NoAnswer != nil || len(got.Sources) == 0 {
					t.Errorf("expected pass-through (sources kept), got NoAnswer=%v sources=%d", got.NoAnswer, len(got.Sources))
				}
				return
			}
			if len(got.Sources) != 0 {
				t.Errorf("expected sources cleared, got %d", len(got.Sources))
			}
			if got.NoAnswer == nil {
				t.Fatal("expected NoAnswer signal")
			}
			if got.NoAnswer.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.NoAnswer.Reason, tc.wantReason)
			}
			if formatSources(got.Sources) == "" && got.NoAnswer == nil {
				t.Error("invariant broken: empty content without NoAnswer")
			}
		})
	}
}

func TestJudgeSufficiencyGatePreservesStats(t *testing.T) {
	rs := NewRAGService(nil, nil, zap.NewNop())
	rs.SetSufficiencyJudgeResolver(func(_ context.Context, _ string) (knowledgeport.SufficiencyJudge, error) {
		return &stubSufficiencyJudge{verdict: knowledgeport.SufficiencyInsufficient}, nil
	})
	got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "qwen-turbo", "", gateResult())
	if got.BestScore != 0.8 || got.CandidateCount != 3 {
		t.Errorf("stats lost: BestScore=%v CandidateCount=%d, want 0.8/3", got.BestScore, got.CandidateCount)
	}
	if got.NoAnswer.BestScore != 0.8 || got.NoAnswer.RetrievedCount != 3 {
		t.Errorf("NoAnswer stats wrong: BestScore=%v RetrievedCount=%d", got.NoAnswer.BestScore, got.NoAnswer.RetrievedCount)
	}
}

// TestJudgeSufficiencyGateEvidenceIsEnrichedFormat 锁定 judge 与回答模型同格式:
// gate 把 formatSources(result.Sources) 作 evidence 传给 judge —— leaf+parent 追加、
// 同 section 多命中 parent 只带一次(与喂 LLM 的回答文本一致)。
func TestJudgeSufficiencyGateEvidenceIsEnrichedFormat(t *testing.T) {
	stub := &stubSufficiencyJudge{verdict: knowledgeport.SufficiencySufficient}
	rs := NewRAGService(nil, nil, zap.NewNop())
	rs.SetSufficiencyJudgeResolver(func(_ context.Context, _ string) (knowledgeport.SufficiencyJudge, error) {
		return stub, nil
	})
	result := &RAGQueryResult{
		Sources: []Source{
			// 同一 section 的两个 leaf 命中 → parent 只应出现一次
			{DocumentID: "d1", Content: "leaf-1a", ParentContent: "parent-section-a", Score: 0.8},
			{DocumentID: "d1", Content: "leaf-1b", ParentContent: "parent-section-a", Score: 0.7},
			{DocumentID: "d2", Content: "leaf-2", ParentContent: "parent-section-b", Score: 0.6},
		},
		BestScore:      0.8,
		CandidateCount: 3,
	}
	got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "qwen-turbo", "", result)
	if got.NoAnswer != nil || len(got.Sources) != 3 {
		t.Fatalf("sufficient verdict must pass through, got NoAnswer=%v sources=%d", got.NoAnswer, len(got.Sources))
	}
	ev := stub.lastEvidence
	for _, want := range []string{"leaf-1a", "leaf-1b", "leaf-2", "parent-section-a", "parent-section-b"} {
		if !strings.Contains(ev, want) {
			t.Errorf("judge evidence missing %q:\n%s", want, ev)
		}
	}
	if gotN := strings.Count(ev, "parent-section-a"); gotN != 1 {
		t.Errorf("same-section parent must appear exactly once in judge evidence, got %d occurrence(s):\n%s", gotN, ev)
	}
}

func TestJudgeSufficiencyGateModelAndResolverPaths(t *testing.T) {
	rs := NewRAGService(nil, nil, zap.NewNop())
	rs.SetSufficiencyJudgeResolver(func(_ context.Context, model string) (knowledgeport.SufficiencyJudge, error) {
		if model != "qwen-turbo" {
			return nil, errors.New("model not in chat catalogue")
		}
		return &stubSufficiencyJudge{verdict: knowledgeport.SufficiencyInsufficient}, nil
	})

	t.Run("空 model 短路放行（judge 门关闭）", func(t *testing.T) {
		got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "", "", gateResult())
		if len(got.Sources) == 0 || got.NoAnswer != nil {
			t.Fatalf("empty model must pass through, got NoAnswer=%v", got.NoAnswer)
		}
	})
	t.Run("resolver 失败 fail-open 放行", func(t *testing.T) {
		got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "qwen-max", "", gateResult())
		if len(got.Sources) == 0 || got.NoAnswer != nil {
			t.Fatalf("resolver failure must pass through, got NoAnswer=%v", got.NoAnswer)
		}
	})
	t.Run("insufficient 升级 insufficient_evidence", func(t *testing.T) {
		got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "qwen-turbo", "", gateResult())
		if len(got.Sources) != 0 || got.NoAnswer == nil || got.NoAnswer.Reason != NoAnswerInsufficientEvidence {
			t.Fatalf("want insufficient_evidence, got sources=%d NoAnswer=%+v", len(got.Sources), got.NoAnswer)
		}
	})
}

// TestJudgeSufficiencyGateRecordsDegraded 断言 degraded 指标只在真实降级路径
// （resolver 失败 / judge 未装配 / judge 调用失败）记录；gate 正常关闭（空
// model）与正常判定不记 degraded（wiring 层才记 ok/error，本层不重复）。
func TestJudgeSufficiencyGateRecordsDegraded(t *testing.T) {
	gateSvc := func(resolve SufficiencyJudgeResolver) (*RAGService, *judgeMetrics) {
		metrics := &judgeMetrics{}
		rs := NewRAGService(nil, nil, zap.NewNop())
		rs.SetMetrics(metrics)
		rs.SetSufficiencyJudgeResolver(resolve)
		return rs, metrics
	}
	healthy := func(_ context.Context, _ string) (knowledgeport.SufficiencyJudge, error) {
		return &stubSufficiencyJudge{verdict: knowledgeport.SufficiencySufficient}, nil
	}
	wantDegraded := []string{"qwen-turbo:degraded"}

	t.Run("resolver 解析失败记 degraded", func(t *testing.T) {
		rs, m := gateSvc(func(_ context.Context, _ string) (knowledgeport.SufficiencyJudge, error) {
			return nil, errors.New("model not in chat catalogue")
		})
		got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "qwen-turbo", "", gateResult())
		if got.NoAnswer != nil || len(got.Sources) == 0 {
			t.Fatalf("resolver failure must pass through, got NoAnswer=%v", got.NoAnswer)
		}
		if !slices.Equal(m.judge, wantDegraded) {
			t.Errorf("judge metric = %v, want %v", m.judge, wantDegraded)
		}
	})

	t.Run("resolver 返回 nil judge 记 degraded", func(t *testing.T) {
		rs, m := gateSvc(func(_ context.Context, _ string) (knowledgeport.SufficiencyJudge, error) {
			return nil, nil
		})
		got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "qwen-turbo", "", gateResult())
		if got.NoAnswer != nil || len(got.Sources) == 0 {
			t.Fatalf("nil judge must pass through, got NoAnswer=%v", got.NoAnswer)
		}
		if !slices.Equal(m.judge, wantDegraded) {
			t.Errorf("judge metric = %v, want %v", m.judge, wantDegraded)
		}
	})

	t.Run("judge 调用失败记 degraded", func(t *testing.T) {
		rs, m := gateSvc(func(_ context.Context, _ string) (knowledgeport.SufficiencyJudge, error) {
			return &stubSufficiencyJudge{err: errors.New("timeout")}, nil
		})
		got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "qwen-turbo", "", gateResult())
		if got.NoAnswer != nil || len(got.Sources) == 0 {
			t.Fatalf("judge failure must pass through, got NoAnswer=%v", got.NoAnswer)
		}
		if !slices.Equal(m.judge, wantDegraded) {
			t.Errorf("judge metric = %v, want %v", m.judge, wantDegraded)
		}
	})

	t.Run("sufficient 判定不记 degraded", func(t *testing.T) {
		rs, m := gateSvc(healthy)
		got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "qwen-turbo", "", gateResult())
		if got.NoAnswer != nil || len(got.Sources) == 0 {
			t.Fatalf("sufficient verdict must pass through, got NoAnswer=%v", got.NoAnswer)
		}
		if len(m.judge) != 0 {
			t.Errorf("no degraded expected on healthy path, got %v", m.judge)
		}
	})

	t.Run("空 model 短路不记 degraded", func(t *testing.T) {
		rs, m := gateSvc(healthy)
		got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "", "", gateResult())
		if got.NoAnswer != nil || len(got.Sources) == 0 {
			t.Fatalf("empty model must pass through, got NoAnswer=%v", got.NoAnswer)
		}
		if len(m.judge) != 0 {
			t.Errorf("no degraded expected when judge gate off, got %v", m.judge)
		}
	})

	t.Run("insufficient 判定记 noAnswer 不记 degraded", func(t *testing.T) {
		rs, m := gateSvc(func(_ context.Context, _ string) (knowledgeport.SufficiencyJudge, error) {
			return &stubSufficiencyJudge{verdict: knowledgeport.SufficiencyInsufficient}, nil
		})
		got := rs.judgeSufficiencyGate(context.Background(), "tenant-1", "kb", "q", "qwen-turbo", "", gateResult())
		if got.NoAnswer == nil || got.NoAnswer.Reason != NoAnswerInsufficientEvidence {
			t.Fatalf("want insufficient_evidence, got NoAnswer=%+v", got.NoAnswer)
		}
		if len(m.judge) != 0 {
			t.Errorf("no degraded expected on a real verdict, got %v", m.judge)
		}
		if want := []string{"tenant-1:" + constants.NoAnswerReasonInsufficientEvidence}; !slices.Equal(m.noAnswer, want) {
			t.Errorf("noAnswer metric = %v, want %v", m.noAnswer, want)
		}
	})
}
