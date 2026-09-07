package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/internal/agent/domain"
	"github.com/byteBuilderX/stratum/internal/agent/domain/port"
	"github.com/byteBuilderX/stratum/pkg/constants"
	"github.com/byteBuilderX/stratum/pkg/observability"
	"github.com/byteBuilderX/stratum/pkg/tokenutil"
)

// BuildReActGraph constructs and compiles the ReAct agent graph.
func BuildReActGraph(capGW port.CapabilityGateway, ledger TokenRecorder, logger *zap.Logger) (*CompiledGraph[ReActState], error) {
	g := New[ReActState]()
	g.AddNode(nodeLLM, makeLLMNode(capGW, ledger, logger))
	g.AddNode(nodeTool, makeToolNode(capGW, logger))
	g.AddConditionalEdge(nodeLLM, func(s ReActState) []string {
		if s.TerminatedBy != "" {
			return []string{END}
		}
		if len(s.Messages) == 0 {
			return []string{END}
		}
		last := s.Messages[len(s.Messages)-1]
		if last.Role == "assistant" && len(last.ToolCalls) > 0 {
			return []string{nodeTool}
		}
		return []string{END}
	})
	// tool 的条件边优先于静态边：存在已排程 plan 波次时进入槽位，否则回到
	// LLM 节点（多后继形式，行为不变）。
	g.AddConditionalEdge(nodeTool, makeToolNext)
	// plan 槽位预注册（plan-0..plan-MaxPlanSteps-1），波次经静态边汇合到
	// finalize 节点，最终回到 LLM。
	for i := 0; i < constants.MaxPlanSteps; i++ {
		g.AddNode(fmt.Sprintf("plan-%d", i), makePlanSlotNode(i))
		g.AddEdges(fmt.Sprintf("plan-%d", i), nodePlanFinalize)
	}
	g.AddNode(nodePlanFinalize, makePlanFinalizeNode())
	g.AddEdges(nodePlanFinalize, nodeLLM)
	g.SetEntryPoint(nodeLLM)
	return g.Compile()
}

func makeLLMNode(capGW port.CapabilityGateway, ledger TokenRecorder, logger *zap.Logger) NodeFunc[ReActState] {
	return func(ctx context.Context, s ReActState) (ReActState, error) {
		// 已业务终止（如成本预算超限）：不再发起 LLM 调用，直接交给条件边
		// END 收尾。否则 plan-finalize 等遗留路由会把终止态重新带回 LLM
		// 节点，预算硬上限会被多一轮调用击穿（Spec 第 3 节）。
		if s.TerminatedBy != "" {
			return s, nil
		}
		// 工具审批续跑（C2b）：跳过轮直接返回，不调 routeLLM、不 Steps++、不记账。
		// 条件边依据末尾消息路由——合成的 assistant+tool_calls 必然走 nodeTool。
		// 消费后立即清零，保证每轮只跳一次。
		if s.SkipNextLLM {
			s.SkipNextLLM = false
			return s, nil
		}
		// 无进展停滞判定（无状态派生，见 no_progress.go）：连续同指纹完成回合达
		// 终止阈值 → 业务终止收尾（不发起本轮 LLM、不执行工具）；达 nudge 阈值 →
		// 本轮请求注入换路提示给模型一次转机。已终止 / 强制收尾步已在判定内让位。
		verdict, runLen := noProgressDetail(s, constants.AgentNoProgressNudgeThreshold,
			constants.AgentNoProgressTerminateThreshold)
		if verdict == noProgressTerminate {
			s.TerminatedBy = NoProgressTerminated
			s.Output = noProgressTerminationOutput(runLen)
			logger.Warn("react llm: no-progress termination",
				zap.Int("consecutive_rounds", runLen))
			return s, nil
		}
		start := time.Now()

		tools, messages, _ := prepareLLMRequest(ctx, &s)
		if verdict == noProgressNudge {
			// 换路提示只进本轮请求（s.Messages 不落合成 user 轮，不会当用户气泡
			// 持久化）；尾部 user 角色与 BuildContextMessages「任务指令在尾部 user」
			// 一致，对 OpenAI 兼容网关合法。tools 保持可用，模型可换工具继续推进。
			messages = append(messages, port.LLMMessage{
				Role:    "user",
				Content: noProgressNudgeInstruction(runLen),
			})
		}
		ctx, llmSpan := startLLMTrace(ctx, &s, messages, tools, start)
		defer llmSpan.End()

		// Always stream: tool-decision turns typically produce empty content so no tokens
		// reach the client; final-answer turns stream the output to the frontend as required.
		resp, err := routeLLM(ctx, s, messages, tools, capGW)
		latencyMs := time.Since(start).Milliseconds()
		if err != nil {
			llmSpan.SetAttributes(attribute.String("opik.metadata.stratum.status", domain.ToolTraceStatusError))
			llmSpan.RecordError(err)
			llmSpan.SetStatus(codes.Error, "llm call failed")
			logLLMError(logger, s, latencyMs, err)
			return s, fmt.Errorf("react llm node: %w", err)
		}
		s.Steps++
		total, cost := recordLLMUsage(ctx, &s, resp, ledger, llmSpan, latencyMs)
		s.TotalTokens += total
		s.TotalCostUSD += cost
		logLLMSuccess(logger, s, latencyMs, len(resp.ToolCalls) > 0)
		appendLLMResponse(&s, resp, cost, latencyMs, start)
		// 成本预算检查点：每次 LLM 调用后按 Ledger 累计（Spec 第 3 节）。
		// 超限标记业务终止（非错误），已产出部分由 collectGraphResult 保留。
		if budgetExceeded(s.TotalTokens, s.MaxTokensPerExecution) {
			s.TerminatedBy = CostBudgetTerminated
		}
		return s, nil
	}
}

// prepareLLMRequest 计算本步实际分发的 tools/messages：最终步骤剥离工具、
// 活动技能指令注入、上下文预算裁剪与循环内压缩，并更新 LastEstimatedTokens
// 作为用量反馈循环的基线。返回 (tools, messages, toolTokens)。
func prepareLLMRequest(ctx context.Context, s *ReActState) ([]port.ToolDefinition, []port.LLMMessage, int) {
	tools := effectiveTools(s.AvailableTools)
	if s.PlanToolsDisabled {
		tools = withoutPlanTools(tools)
	}
	messages := messagesWithActiveSkills(s.Messages, s.Actives)
	protectedUsers := 1
	if s.MaxLLMSteps > 0 && s.Steps >= s.MaxLLMSteps-1 {
		tools = nil
		protectedUsers = 2
		instruction := constants.AgentFinalAnswerInstruction
		// 降级执行（含子节点经共享 map 传播的止损）：只基于已确认事实回答，
		// 禁止声称完成了未验证的操作。
		if s.Degraded || len(s.StopLossTools) > 0 {
			instruction = constants.AgentDegradedFinalAnswerInstruction
		}
		// system 级注入：指令进入头部 anchor 区（压缩永不逐出），要求模型基于
		// 已知分析/工具结果总结已做到的事，并明确告知用户已达最大迭代次数。
		messages = insertSystemBlockAfterFirstSystem(messages, []port.LLMMessage{{
			Role:    "system",
			Content: instruction,
		}})
		// 引用约束收尾强化：主注入常驻 base context，最终步骤再追加同规则，
		// 自然结束（无工具调用直接出答案）也受约束。
		if s.EnforceClaimCitations {
			messages = insertSystemBlockAfterFirstSystem(messages, []port.LLMMessage{{
				Role:    "system",
				Content: constants.AgentCitationReferenceInstruction,
			}})
		}
	}
	// In-loop compaction: bound the complete request, including any final-step
	// instruction, without mutating s.Messages (trace/history stay complete).
	// Tunable overrides resolve here: 0 means auto-derive from the window.
	recentGroups, safetyRatio, correction := loopPolicy(*s)
	// 预算账本：工具定义走 ToolsCap 独立配额，history 压缩走 HistoryCap；
	// 二者互不挤占（Spec 第 2 节根因修复）。Budget 零值 = 未初始化，
	// 工具裁剪与压缩自动禁用（与旧 0 预算语义一致）。
	// 两阶段截断（Spec D1）：阶段一先用当前 messages 估算工具 allowance，
	// 阶段二按 allowance 构建 stratum_skill 描述并置于工具面首位——描述恒
	// fit 门槛，该工具在 fitToolList 贪心打包中永不被整工具丢弃（杜绝"零
	// skill 可激活"的静默失败）。最终步 tools==nil（禁止调用工具）时跳过。
	// 所有 agent 一视同仁参与裁剪；8 个内置运维工具为 protected 语义（见
	// fitToolList），预算受限时优先保底保留，不因裁剪静默缺失角色能力。
	allowance := toolAllowanceFor(messages, s.Budget.ToolsCap, protectedUsers, correction, safetyRatio)
	if tools != nil {
		if skillTool := buildSkillTool(s.SkillCatalog, s.Actives, allowance); skillTool != nil {
			tools = append([]port.ToolDefinition{*skillTool}, tools...)
		}
	}
	tools = fitToolsToContextBudget(tools, messages, s.Budget.ToolsCap, protectedUsers, correction, safetyRatio)
	toolTokens := 0
	if encodedTools, err := json.Marshal(tools); err == nil {
		toolTokens = tokenutil.EstimateText(string(encodedTools))
	}
	messages = compactLoopMessagesWithPolicy(ctx, messages, s.Budget, toolTokens, recentGroups, protectedUsers, correction, safetyRatio, s.HistoryCompactor, s)
	// Baseline for the usage-feedback loop: the estimate of what is actually
	// dispatched this step (post-compaction messages + tools), so the ratio
	// stays on a consistent basis across steps.
	s.LastEstimatedTokens = tokenutil.EstimateMessages(toEstimate(messages)) + toolTokens
	return tools, messages, toolTokens
}

// startLLMTrace 构建 LLM 请求的 span 与 TraceEventLLMRequest 事件；返回带 span
// 上下文的 ctx（routeLLM 依赖其链路传播）。span 的 End 由调用方 defer。
func startLLMTrace(ctx context.Context, s *ReActState, messages []port.LLMMessage, tools []port.ToolDefinition, start time.Time) (context.Context, oteltrace.Span) {
	tracer := otel.Tracer("stratum/agent")
	inputPayload := observability.SafeTracePayload(map[string]any{"messages": messages, "tools": tools}, constants.AgentToolTraceMaxRawJSONBytes)
	llmAttributes := []attribute.KeyValue{
		attribute.String("llm.model", s.Model),
		attribute.String("gen_ai.request.model", s.Model),
		attribute.Int("react.step", s.Steps+1),
		attribute.Int("stratum.llm.step", s.Steps+1),
		attribute.String("stratum.input.sha256", inputPayload.SHA256),
		attribute.Bool("stratum.input.truncated", inputPayload.Truncated),
		attribute.String("opik.metadata.stratum.tenant_id", s.TenantID),
		attribute.String("opik.metadata.stratum.trace_id", s.TraceID),
		attribute.String("opik.metadata.stratum.provider_type", domain.ProviderTypeLLM),
		attribute.String("opik.metadata.stratum.provider_id", s.Model),
		attribute.String("opik.metadata.stratum.status", domain.ToolTraceStatusSuccess),
	}
	// Effective request parameters: only non-zero (set) values are recorded so
	// "unset" is not misrepresented as 0.
	if s.Temperature != 0 {
		llmAttributes = append(llmAttributes, attribute.Float64("gen_ai.request.temperature", float64(s.Temperature)))
	}
	if s.MaxTokens > 0 {
		llmAttributes = append(llmAttributes, attribute.Int("gen_ai.request.max_tokens", s.MaxTokens))
	}
	// Prompt version fingerprints tie the LLM request to the prompt revision and
	// effective config that produced it. system_prompt keeps the legacy
	// (key, version) pair so existing dashboards stay valid; config is emitted
	// under its own attribute because a single (key, version) pair cannot carry
	// both. Written deterministically (no map iteration) so repeated reads of a
	// span never observe a random winner.
	if version, ok := s.PromptVersions["system_prompt"]; ok && version != "" {
		llmAttributes = append(llmAttributes,
			attribute.String("stratum.prompt.key", "system_prompt"),
			attribute.String("stratum.prompt.version", version),
		)
	}
	if version, ok := s.PromptVersions["config"]; ok && version != "" {
		llmAttributes = append(llmAttributes, attribute.String("stratum.prompt.config", version))
	}
	llmAttributes = append(llmAttributes, tracePayloadAttributes(
		ctx, s.TracePayloadStore, s.TenantID, s.TraceID, "llm-input",
		map[string]any{"messages": messages, "tools": tools},
	)...)
	ctx, llmSpan := tracer.Start(ctx, "react.llm",
		oteltrace.WithAttributes(llmAttributes...),
	)
	s.TraceEvents = append(s.TraceEvents, domain.AgentTraceEvent{
		TraceID:         s.TraceID,
		ConversationID:  s.ConversationID,
		RunType:         domain.RunTypeAgent,
		ObservationType: domain.ObservationTypeLLM,
		EventType:       domain.TraceEventLLMRequest,
		StepIndex:       s.Steps + 1,
		SpanName:        "react.llm",
		Status:          domain.ToolTraceStatusSuccess,
		ProviderType:    domain.ProviderTypeLLM,
		ProviderID:      s.Model,
		NodeID:          nodeLLM,
		NodeType:        domain.ObservationTypeLLM,
		Input: map[string]any{
			"model":    s.Model,
			"messages": messages,
			"tools":    tools,
		},
		Model:     s.Model,
		StartedAt: start,
		EndedAt:   start,
	})
	return ctx, llmSpan
}

// recordLLMUsage 记录本次 LLM 调用的模型解析、token 记账与 span 输出属性，
// 返回 (total tokens, cost USD)，供调用方累加。
func recordLLMUsage(ctx context.Context, s *ReActState, resp port.CapabilityResponse, ledger TokenRecorder, llmSpan oteltrace.Span, latencyMs int64) (int, float64) {
	total, cost := recordModelResolution(ctx, s, resp, ledger)
	s.TokenCorrection = updateTokenCorrection(s.TokenCorrection, s.LastEstimatedTokens, resp.Usage.Prompt)
	llmSpan.SetAttributes(
		attribute.Int("llm.prompt_tokens", resp.Usage.Prompt),
		attribute.Int("llm.completion_tokens", resp.Usage.Completion),
		attribute.Int("gen_ai.usage.input_tokens", resp.Usage.Prompt),
		attribute.Int("gen_ai.usage.output_tokens", resp.Usage.Completion),
		attribute.Float64("stratum.cost_usd", cost),
		attribute.Bool("llm.has_tool_calls", len(resp.ToolCalls) > 0),
		attribute.Int64("opik.metadata.stratum.latency_ms", latencyMs),
		attribute.Int64("opik.metadata.stratum.total_tokens", int64(resp.Usage.Total)),
		attribute.Float64("opik.metadata.stratum.cost_usd", cost),
	)
	outputPayload := observability.SafeTracePayload(map[string]any{"content": resp.Content, "tool_calls": resp.ToolCalls}, constants.AgentToolTraceMaxRawJSONBytes)
	outputAttributes := []attribute.KeyValue{
		attribute.String("stratum.output.sha256", outputPayload.SHA256),
		attribute.Bool("stratum.output.truncated", outputPayload.Truncated),
	}
	outputAttributes = append(outputAttributes, tracePayloadAttributes(
		ctx, s.TracePayloadStore, s.TenantID, s.TraceID, "llm-output",
		map[string]any{"content": resp.Content, "tool_calls": resp.ToolCalls},
	)...)
	llmSpan.SetAttributes(outputAttributes...)
	return total, cost
}

// logLLMError 记录 LLM 调用失败日志；上下文取消按可预期事件记 Info。
func logLLMError(logger *zap.Logger, s ReActState, latencyMs int64, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		logger.Info("react.llm",
			zap.String("trace_id", s.TraceID),
			zap.String("tenant_id", s.TenantID),
			zap.String("conversation_id", s.ConversationID),
			zap.String("model", s.Model),
			zap.Int("step", s.Steps+1),
			zap.Int64("latency_ms", latencyMs),
			zap.String("error", "context canceled"),
		)
		return
	}
	logger.Error("react.llm",
		zap.String("trace_id", s.TraceID),
		zap.String("tenant_id", s.TenantID),
		zap.String("conversation_id", s.ConversationID),
		zap.String("model", s.Model),
		zap.Int("step", s.Steps+1),
		zap.Int64("latency_ms", latencyMs),
		zap.Error(err),
	)
}

// logLLMSuccess 记录 LLM 调用成功的用量与延迟日志。
func logLLMSuccess(logger *zap.Logger, s ReActState, latencyMs int64, hasToolCalls bool) {
	logger.Info("react.llm",
		zap.String("trace_id", s.TraceID),
		zap.String("tenant_id", s.TenantID),
		zap.String("conversation_id", s.ConversationID),
		zap.String("model", s.Model),
		zap.Int("step", s.Steps),
		zap.Int("total_tokens", s.TotalTokens),
		zap.Float64("cost_usd", s.TotalCostUSD),
		zap.Int64("latency_ms", latencyMs),
		zap.Bool("has_tool_calls", hasToolCalls),
	)
}

// appendLLMResponse 将 LLM 响应追加到消息历史并记录 LLMResponse trace 事件。
func appendLLMResponse(s *ReActState, resp port.CapabilityResponse, cost float64, latencyMs int64, start time.Time) {
	if len(resp.ToolCalls) == 0 {
		s.Output = resp.Content
		s.Messages = append(s.Messages, port.LLMMessage{
			Role:    "assistant",
			Content: resp.Content,
		})
		s.TraceEvents = append(s.TraceEvents, domain.AgentTraceEvent{
			TraceID:          s.TraceID,
			ConversationID:   s.ConversationID,
			RunType:          domain.RunTypeAgent,
			ObservationType:  domain.ObservationTypeLLM,
			EventType:        domain.TraceEventLLMResponse,
			StepIndex:        s.Steps,
			SpanName:         "react.llm",
			Status:           domain.ToolTraceStatusSuccess,
			Output:           map[string]any{"content": resp.Content},
			Summary:          truncateRunes(resp.Content, 500),
			Model:            s.Model,
			ProviderType:     domain.ProviderTypeLLM,
			ProviderID:       s.Model,
			NodeID:           nodeLLM,
			NodeType:         domain.ObservationTypeLLM,
			PromptTokens:     resp.Usage.Prompt,
			CompletionTokens: resp.Usage.Completion,
			TotalTokens:      resp.Usage.Total,
			CostUSD:          cost,
			LatencyMs:        latencyMs,
			StartedAt:        start,
			EndedAt:          start.Add(time.Duration(latencyMs) * time.Millisecond),
		})
		return
	}
	s.Messages = append(s.Messages, port.LLMMessage{
		Role:      "assistant",
		ToolCalls: resp.ToolCalls,
	})
	s.TraceEvents = append(s.TraceEvents, domain.AgentTraceEvent{
		TraceID:          s.TraceID,
		ConversationID:   s.ConversationID,
		RunType:          domain.RunTypeAgent,
		ObservationType:  domain.ObservationTypeLLM,
		EventType:        domain.TraceEventLLMResponse,
		StepIndex:        s.Steps,
		SpanName:         "react.llm",
		Status:           domain.ToolTraceStatusSuccess,
		Output:           map[string]any{"tool_calls": resp.ToolCalls},
		Summary:          fmt.Sprintf("model requested %d tool call(s)", len(resp.ToolCalls)),
		Model:            s.Model,
		ProviderType:     domain.ProviderTypeLLM,
		ProviderID:       s.Model,
		NodeID:           nodeLLM,
		NodeType:         domain.ObservationTypeLLM,
		PromptTokens:     resp.Usage.Prompt,
		CompletionTokens: resp.Usage.Completion,
		TotalTokens:      resp.Usage.Total,
		CostUSD:          cost,
		LatencyMs:        latencyMs,
		StartedAt:        start,
		EndedAt:          start.Add(time.Duration(latencyMs) * time.Millisecond),
	})
}

// recordModelResolution 回写模型解析结果（fallback 降级后与配置模型不同）
// 并按实际解析模型记账（价格不同），返回累计 token 与成本。
func recordModelResolution(ctx context.Context, s *ReActState, resp port.CapabilityResponse, ledger TokenRecorder) (int, float64) {
	s.ModelResolved = resp.ModelResolved
	s.ModelRoutedVia = resp.ModelRoutedVia
	ledgerModel := s.Model
	if resp.ModelResolved != "" {
		ledgerModel = resp.ModelResolved
	}
	return ledger.Record(ctx, ledgerModel, resp.Usage)
}

// loopPolicy resolves the in-loop compaction tunables from the run state:
// 0 means auto-derive (recent groups from the window size), a zero correction
// is treated as 1 (no correction), and the safety ratio is locked to the
// platform default (product spec: not user-configurable — the value would
// otherwise double as the assembly-side reserve ratio with opposite sign).
func loopPolicy(s ReActState) (recentGroups int, safetyRatio, correction float64) {
	recentGroups = s.CompactionRecentGroups
	if recentGroups == 0 {
		recentGroups = constants.DynamicRecentGroups(s.MaxContextTokens)
	}
	safetyRatio = constants.LoopCompactionSafetyRatio
	correction = s.TokenCorrection
	if correction <= 0 {
		correction = 1
	}
	return recentGroups, safetyRatio, correction
}

// updateTokenCorrection folds the previous step's estimated-vs-actual prompt
// token ratio into the EMA correction factor, clamped to [TokenCorrectionMin,
// TokenCorrectionMax]. Without a baseline (first step) or a reported prompt
// count the correction is left unchanged. Under-estimation (ratio > 1) raises
// the correction, lowering the next compaction threshold so compaction starts
// earlier.
func updateTokenCorrection(correction float64, estimatedTokens, actualPrompt int) float64 {
	if correction <= 0 {
		// 零值 state（绕过 buildReActInitState 的调用方）按 1.0 处理，
		// 与 compactLoopMessagesWithPolicy 的 correction≤0→1 同语义，
		// 否则 0.9×0 会把 EMA 塌到 clamp 下限，低估反而变成更晚压缩。
		correction = 1
	}
	if estimatedTokens <= 0 || actualPrompt <= 0 {
		return correction
	}
	ratio := float64(actualPrompt) / float64(estimatedTokens)
	smoothed := constants.TokenCorrectionAlpha*ratio + (1-constants.TokenCorrectionAlpha)*correction
	return min(max(smoothed, constants.TokenCorrectionMin), constants.TokenCorrectionMax)
}

// routeLLM streams one LLM call with retry. Extracted from makeLLMNode so the
// request construction and retry closure stay within the code-quality line and
// complexity budgets of the node function.
func routeLLM(ctx context.Context, s ReActState, messages []port.LLMMessage, tools []port.ToolDefinition, capGW port.CapabilityGateway) (port.CapabilityResponse, error) {
	// 流式 token 一旦推给前端，本次 attempt 再失败（截断/断连）就不能重试：
	// RetryFn 重试会再次全量推流 → 前端重复/错乱内容。emitted 标志把这类失败
	// 标记为 permanent，只允许「首 token 尚未输出」的失败重试。llmgateway 层
	// 对 outputStarted 已有同样的 fail-fast 语义，此处是图级独立防线。
	var emitted atomic.Bool
	stream := s.OnToken
	if stream != nil {
		stream = func(tok string) {
			emitted.Store(true)
			s.OnToken(tok)
		}
	}
	return RetryFn(ctx, DefaultRetry, func() (port.CapabilityResponse, error) {
		resp, err := capGW.Route(ctx, port.CapabilityRequest{
			TraceID:     s.TraceID,
			TenantID:    s.TenantID,
			Type:        port.CapLLM,
			TokenStream: stream,
			LLM: &port.LLMCapRequest{
				Model:           s.Model,
				Messages:        messages,
				Tools:           tools,
				Temperature:     s.Temperature,
				ReasoningEffort: s.ReasoningEffort,
				MaxTokens:       s.MaxTokens,
			},
		})
		if err != nil && emitted.Load() {
			// 已输出 token 的失败永不重试（含截断），原错误透传。
			return resp, markStreamReplayPermanent(err)
		}
		return resp, err
	})
}

// contextLengthMarker 是 llmgateway ErrContextLengthExceeded 的跨包探测协议
// （与 capability 包本地副本同模式）：graph 不 import llmgateway，经方法
// 探测鸭子类型识别 context_length 错误。
type contextLengthMarker interface{ ContextLengthExceeded() bool }

// IsContextLengthExceeded 报告错误链（含 %w 包装）是否携带上下文超限标记，
// 供 executeReAct 判定最终请求是否触发最小请求降级（Spec D4）。
func IsContextLengthExceeded(err error) bool {
	var m contextLengthMarker
	return errors.As(err, &m)
}

// minimalRetryBudgetFloor 是降级最小请求的字节预算下界：窗口被
// system+task 占满时仍保留至少一条最近历史消息，避免无上下文的裸请求。
const minimalRetryBudgetFloor = 1

// BuildMinimalRetryMessages 构造最终请求 400 context_length_exceeded 后的
// 降级最小请求（Spec D4）：system + 纯截断历史（成对剔除工具交换）+ task。
// 非流式、单次调用；再次失败即终止，不换模型不退避。只删 tool 消息会让
// 模型看到"调用了工具但没有结果"，破坏消息配对，也留下无内容的 assistant
// 消息——故成对剔除 assistant tool_calls 与其 tool 结果。len() 字节数是
// token 的保守上界（CJK 每字符 3 字节），字符数下界保证最小请求必然小于
// 原请求。
func BuildMinimalRetryMessages(systemPrompt, task string, messages []port.LLMMessage, window int) []port.LLMMessage {
	out := make([]port.LLMMessage, 0, len(messages)+2)
	out = append(out, port.LLMMessage{Role: "system", Content: systemPrompt})
	budget := window - len(systemPrompt) - len(task) - constants.MinimalRetryReserveBytes
	if budget <= 0 {
		budget = minimalRetryBudgetFloor
	}
	// 保留最近消息，成对剔除工具交换（assistant tool_calls 与其 tool 结果）。
	for i := len(messages) - 1; i >= 0 && budget > 0; i-- {
		msg := messages[i]
		if msg.Role == "tool" {
			continue
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			continue
		}
		budget -= len(msg.Content)
		if budget < 0 {
			break
		}
		out = append(out, msg)
	}
	// 反转恢复时间顺序；历史自带 system 消息未被预算保留时补回前置
	// 占位——预算在扫描前已为 system 预留字节，无条件剔除即浪费保留额，
	// 最小请求必须满足 D4 语义（system + task + 压缩后历史）。
	out = out[1:]
	slices.Reverse(out)
	if !hasSystemRole(out) {
		out = append([]port.LLMMessage{{Role: "system", Content: systemPrompt}}, out...)
	}
	return append(out, port.LLMMessage{Role: "user", Content: task})
}

// hasSystemRole 报告消息序列是否已含 system 角色消息（降级请求的
// D4 语义守卫：system 缺失时由调用方补回占位）。
func hasSystemRole(messages []port.LLMMessage) bool {
	for _, m := range messages {
		if m.Role == "system" {
			return true
		}
	}
	return false
}

func fitToolsToContextBudget(tools []port.ToolDefinition, messages []port.LLMMessage, budget, protectedUsers int, correction, safetyRatio float64) []port.ToolDefinition {
	if budget <= 0 || len(tools) == 0 {
		return tools
	}
	return fitToolList(tools, toolAllowanceFor(messages, budget, protectedUsers, correction, safetyRatio))
}

// toolAllowanceFor 估算本步工具可用的 token allowance（Spec D1 阶段一）：压缩
// 阈值减去受保护消息（anchor 头 + 最近用户轮次）的估算占用，余量即工具配额。
// budget <= 0 返回 0，配合 fitToolsToContextBudget 的早退分支表示预算未初始化。
func toolAllowanceFor(messages []port.LLMMessage, budget, protectedUsers int, correction, safetyRatio float64) int {
	if budget <= 0 {
		return 0
	}
	threshold := compactionThreshold(budget, 0, correction, safetyRatio)
	groups := groupMessages(messages)
	protectedMessages := flatten(groups[:anchorCount(groups)])
	protectedMessages = append(protectedMessages, protectedUserMessages(groups, protectedUsers)...)
	return max(threshold-tokenutil.EstimateMessages(toEstimate(protectedMessages)), 0)
}

// protectedUserMessages collects the most recent protected user turns (the
// active task and, when configured, earlier task history) so tools never crowd
// out the messages that must survive compaction.
func protectedUserMessages(groups []msgGroup, protectedUsers int) []port.LLMMessage {
	var out []port.LLMMessage
	usersKept := 0
	for i := len(groups) - 1; i >= 0 && usersKept < protectedUsers; i-- {
		if groups[i].role0 == "user" {
			out = append(out, groups[i].msgs...)
			usersKept++
		}
	}
	return out
}

// isProtectedTool 判定 8 个内置运维工具（等化后对所有 agent 通用）。这些
// 工具是角色权限建模的固定组成，裁剪会导致能力静默缺失，故在 fitToolList
// 贪心打包中保底优先保留。
func isProtectedTool(name string) bool {
	switch name {
	case domain.SystemAssistantToolSearchOfficialDocs,
		domain.SystemAssistantToolDiagnoseTenant,
		domain.SystemAssistantToolProposeResourceChange,
		domain.SystemAssistantToolApplyResourceChange,
		domain.SystemAssistantToolListModels,
		domain.SystemAssistantToolUpdateSystemModel,
		domain.SystemAssistantToolListAgents,
		domain.SystemAssistantToolListMCPServers:
		return true
	default:
		return false
	}
}

// fitToolList packs tool schemas within the token allowance. 预算充足时保持
// 声明顺序全量返回；不足时按优先级贪心：激活技能与授权能力工具（skill、
// MCP、知识、记忆）优先打包，plan 工作流工具最后裁——技能激活是用户显式
// 功能开关（TestAgentService_ExecuteSkillScenarioActivatesMultipleSkills），
// plan 是内置辅助工作流，预算受限时先牺牲后者。
func fitToolList(tools []port.ToolDefinition, allowance int) []port.ToolDefinition {
	encoded, err := json.Marshal(tools)
	if err == nil && tokenutil.EstimateText(string(encoded)) <= allowance {
		return tools
	}
	ordered := append([]port.ToolDefinition(nil), tools...)
	slices.SortStableFunc(ordered, func(a, b port.ToolDefinition) int {
		// 8 个内置运维工具 protected 保底最前：先于 skill/MCP/知识/记忆贪心，
		// 预算受限时优先保留（D17），静默缺失即角色能力缺失。
		if ap, bp := isProtectedTool(a.Name), isProtectedTool(b.Name); ap != bp {
			if ap {
				return -1
			}
			return 1
		}
		switch {
		case isReservedPlanTool(a.Name) == isReservedPlanTool(b.Name):
			return 0
		case isReservedPlanTool(a.Name):
			return 1
		default:
			return -1
		}
	})
	fitted := make([]port.ToolDefinition, 0, len(ordered))
	for _, tool := range ordered {
		candidate := make([]port.ToolDefinition, len(fitted), len(fitted)+1)
		copy(candidate, fitted)
		candidate = append(candidate, tool)
		encoded, err = json.Marshal(candidate)
		if err == nil && tokenutil.EstimateText(string(encoded)) <= allowance {
			fitted = candidate
		}
	}
	return fitted
}
