package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	llmdomain "github.com/byteBuilderX/stratum/internal/llmgateway/domain"
	"github.com/byteBuilderX/stratum/internal/memory/domain"
	memport "github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
	"github.com/byteBuilderX/stratum/pkg/constants"
)

// TrajectoryReflector 把压缩后的工具轨迹骨架提炼为结构化记忆候选。
// 提示词与模型均为平台级参数（memory.reflection_prompt / memory.reflection_model），
// 不与 agent 绑定；未配置提示词时 fail-closed，无内置兜底模板。
type TrajectoryReflector struct {
	client   LLMClient
	resolver memport.PlatformParamResolver
	tenantID string
	logger   *zap.Logger
}

func NewTrajectoryReflector(client LLMClient) *TrajectoryReflector {
	return &TrajectoryReflector{client: client}
}

// SetParamResolver wires the platform parameter resolver. nil keeps the
// fail-closed prompt path (which errors when the prompt is unset).
func (r *TrajectoryReflector) SetParamResolver(p memport.PlatformParamResolver) { r.resolver = p }

// SetTenantID sets the tenant identity used for parameter resolution.
func (r *TrajectoryReflector) SetTenantID(t string) { r.tenantID = t }

// WithLogger injects a logger for structured degraded summaries. nil safe.
func (r *TrajectoryReflector) WithLogger(l *zap.Logger) *TrajectoryReflector {
	r.logger = l
	return r
}

// reflectionPrompt 渲染反思 system prompt：memory.reflection_prompt（平台级）
// 承载完整提示词，代码只替换 {existing_facts}；未配置/空 → 错误（fail-closed）。
func (r *TrajectoryReflector) reflectionPrompt(ctx context.Context) (string, error) {
	if r.resolver == nil {
		return "", fmt.Errorf("memory reflection: memory.reflection_prompt not configured (fail-closed)")
	}
	v, _, err := r.resolver.ResolvePlatform(ctx, "memory.reflection_prompt")
	if err != nil {
		return "", fmt.Errorf("memory reflection: resolve prompt: %w", err)
	}
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("memory reflection: memory.reflection_prompt not configured (fail-closed)")
	}
	// {existing_facts} 当前传入空摘要；重复/冲突消解由共享 supersede 链承担。
	return strings.ReplaceAll(s, "{existing_facts}", ""), nil
}

// Reflect 调用反思模型：system 为平台级提示词，user 为骨架 JSON + 任务目标。
// 输出契约由 parseReflectionEntries + ExtractedFact.Validate 强制。
func (r *TrajectoryReflector) Reflect(
	ctx context.Context,
	tenantID string,
	skeleton domain.TrajectorySkeleton,
	existing string,
) ([]*memport.ReflectionEntry, error) {
	system, err := r.reflectionPrompt(ctx)
	if err != nil {
		return nil, err
	}
	skeletonRaw, err := json.Marshal(skeleton)
	if err != nil {
		return nil, fmt.Errorf("memory reflection: marshal skeleton: %w", err)
	}
	userMsg := string(skeletonRaw)
	if skeleton.TaskGoal != "" {
		userMsg += "\n\nTask goal: " + skeleton.TaskGoal
	}
	// 反思模型为必需平台参数（fail-closed）：未显式配置或解析失败即返回
	// *modelconfig.Err，禁止空模型静默回落 llmgateway 默认。
	model, err := modelconfig.ResolveChatModel(ctx, r.resolver, modelconfig.KeyReflectionModel)
	if err != nil {
		logConfigError(r.logger, modelconfig.KeyReflectionModel, "reflection", err)
		return nil, err
	}
	req := llmdomain.NewExtractRequest(model, system, userMsg, 0, constants.MemoryReflectionLLMMaxTokens)
	return reflectStructured(ctx, r.client, req, r.logger)
}

// reflectStructured 走 CompleteStructured 带错重试管线，部分成功语义与
// extractFactsStructured 一致：≥1 条通过立即返回通过子集；0 条通过触发
// 带错重试，耗尽返回 typed error（worker 保留 DLQ 语义）。
func reflectStructured(
	ctx context.Context,
	client llmdomain.Completer,
	req *llmdomain.CompletionRequest,
	logger *zap.Logger,
) ([]*memport.ReflectionEntry, error) {
	var valid []*memport.ReflectionEntry
	_, err := CompleteStructured(ctx, client, req, parseReflectionEntries,
		func(entries []*memport.ReflectionEntry) error {
			if len(entries) == 0 {
				valid = nil
				return nil
			}
			valid = entries[:0]
			allInvalid := true
			for _, e := range entries {
				if e == nil || e.ToExtractedFact().Validate() != nil {
					continue
				}
				valid = append(valid, e)
				allInvalid = false
			}
			if allInvalid {
				return &memport.ValidationError{
					Location: "reflection", FieldName: "entries",
					Reason: "no reflection entry passed validation",
				}
			}
			return nil
		}, logger, "reflection")
	if err != nil {
		return nil, err
	}
	return valid, nil
}

// parseReflectionEntries 从 LLM 原始输出剥离非 JSON 前缀并解析条目数组；
// Token 截断时按最后完整对象恢复（recoverTruncatedArray）。
func parseReflectionEntries(raw string) ([]*memport.ReflectionEntry, error) {
	start := strings.Index(raw, "[")
	if start == -1 {
		return nil, fmt.Errorf("parse reflection entries: no JSON array in response")
	}
	body := raw[start:]
	var entries []*memport.ReflectionEntry
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&entries); err != nil {
		if recovered := recoverTruncatedArray(body); recovered != "" {
			var recoveredEntries []*memport.ReflectionEntry
			if err2 := json.Unmarshal([]byte(recovered), &recoveredEntries); err2 == nil {
				return recoveredEntries, nil
			}
		}
		return nil, fmt.Errorf("parse reflection entries: %w", err)
	}
	return entries, nil
}
