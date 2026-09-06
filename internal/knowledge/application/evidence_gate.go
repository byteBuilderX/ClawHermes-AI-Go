package application

import (
	"context"

	"github.com/byteBuilderX/stratum/internal/knowledge/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"go.uber.org/zap"
)

// judgeSufficiencyGate 是生成前证据充分性门（仅 evidence 路径挂载，由
// searchWorkspaceWithEvidence 调用）：相似度阈值回答"像不像"，judge 回答
// "能不能推出结论"。判 INSUFFICIENT 时该 workspace 按无内容处理（Sources
// 置空 + NoAnswer=insufficient_evidence，维持 content=="" ⇒ NoAnswer!=nil
// 不变量），经聚合严重度排序上报。
//
// 术语分层：judge 实现方 fail-closed——错误必须向上传播、绝不静默给默认判定
// （见 domain/port/judge.go 契约）；本 gate 是 fail-open——resolver 解析失败 /
// judge 未装配 / JudgeSufficiency 调用失败（含超时）均原样放行（WARN 留痕 +
// degraded 指标），行为与不配置 judge 时完全一致，绝不误杀检索。model 为空
// （JudgeModel 未配置 = judge 门关闭）与空 sources（no_sources 已是更强信号）
// 直接短路，属正常路径而非降级，不记 degraded。
func (rs *RAGService) judgeSufficiencyGate(ctx context.Context, tenantID, workspace, query, model, instructions string, result *RAGQueryResult) *RAGQueryResult {
	if model == "" || rs.judgeResolver == nil || len(result.Sources) == 0 {
		return result
	}
	judge, err := rs.judgeResolver(ctx, model)
	if err != nil {
		rs.recordJudgeDegraded(model)
		rs.logger.Warn("knowledge.judge.sufficiency_degraded",
			zap.String("tenant_id", tenantID), zap.String("workspace", workspace),
			zap.String("model", model), zap.Error(err))
		return result
	}
	if judge == nil {
		rs.recordJudgeDegraded(model)
		rs.logger.Warn("knowledge.judge.sufficiency_unavailable",
			zap.String("tenant_id", tenantID), zap.String("workspace", workspace),
			zap.String("model", model))
		return result
	}
	verdict, err := judge.JudgeSufficiency(ctx, query, formatSources(result.Sources), instructions)
	if err != nil {
		rs.recordJudgeDegraded(model)
		rs.logger.Warn("knowledge.judge.sufficiency_degraded",
			zap.String("tenant_id", tenantID), zap.String("workspace", workspace),
			zap.String("model", model), zap.Error(err))
		return result
	}
	if verdict != port.SufficiencyInsufficient {
		return result
	}
	if rs.metrics != nil {
		rs.metrics.IncNoAnswer(tenantID, constants.NoAnswerReasonInsufficientEvidence)
	}
	return &RAGQueryResult{
		NoAnswer:       buildNoAnswer(NoAnswerInsufficientEvidence, result.CandidateCount, 0, result.BestScore),
		BestScore:      result.BestScore,
		CandidateCount: result.CandidateCount,
	}
}

// recordJudgeDegraded 记录证据充分性门降级放行（gate 未能产出判定、按 fail-open
// 原样放行）。wiring 层 knowledgeJudge 只对实际 LLM 调用记 ok/error；resolver
// 解析失败 / judge 未装配这类调用前降级由本层以 degraded 补齐（与
// llmReranker 的 rerankBuiltinSemantic 同一双层模式：wiring 记 ok/error、
// 应用层记 degraded）。metrics 为 nil 时跳过。
func (rs *RAGService) recordJudgeDegraded(model string) {
	if rs.metrics != nil {
		rs.metrics.IncKnowledgeJudge(model, "degraded")
	}
}
