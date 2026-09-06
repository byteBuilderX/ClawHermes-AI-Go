package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/byteBuilderX/stratum/internal/memory/domain"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
)

const testReflectionPrompt = "你是轨迹反思助手。输入工具调用骨架 JSON。过滤临时查询与试错噪声。" +
	"输出 JSON 数组，每项含 content/importance/fact_type/evidence{execution_id}。现有事实：{existing_facts}"

func newConfiguredReflector(llm *extractorLLMStub, extra map[string]any) *TrajectoryReflector {
	vals := map[string]any{
		"memory.reflection_prompt": testReflectionPrompt,
		"memory.reflection_model":  "test-model",
	}
	for k, v := range extra {
		vals[k] = v
	}
	r := NewTrajectoryReflector(llm)
	r.SetTenantID("t1")
	r.SetParamResolver(keyedResolverStub{vals: vals})
	return r
}

func TestTrajectoryReflector_FailsClosedWithoutPrompt(t *testing.T) {
	llm := &extractorLLMStub{content: `[]`}
	r := NewTrajectoryReflector(llm)
	sk := domain.TrajectorySkeleton{ExecutionID: "e1", Steps: []domain.TrajectoryStep{{ToolName: "search", Status: domain.TrajectoryStepStatusSuccess}}}
	if _, err := r.Reflect(context.Background(), "t1", sk, ""); err == nil {
		t.Fatal("missing memory.reflection_prompt must fail closed")
	}
}

func TestTrajectoryReflector_ParsesEntriesAndUsesPlatformModel(t *testing.T) {
	llm := &extractorLLMStub{content: `[{"content":"失败后应先核对参数","importance":0.8,"fact_type":"skill","evidence":{"execution_id":"e1"}}]`}
	r := newConfiguredReflector(llm, map[string]any{"memory.reflection_model": "qwen-turbo"})
	sk := domain.TrajectorySkeleton{
		ExecutionID: "e1",
		TaskGoal:    "goal",
		Steps:       []domain.TrajectoryStep{{ToolName: "search", Status: domain.TrajectoryStepStatusError, ErrorFingerprint: "boom"}},
	}
	entries, err := r.Reflect(context.Background(), "t1", sk, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Evidence.ExecutionID != "e1" || entries[0].FactType != "skill" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	if llm.model != "qwen-turbo" {
		t.Fatalf("model=%q, want qwen-turbo", llm.model)
	}
	if !strings.Contains(llm.prompt, "过滤临时查询") {
		t.Fatalf("reflection prompt not used: %q", llm.prompt)
	}
	if !strings.Contains(llm.user, "Task goal: goal") {
		t.Fatalf("task goal not in user message: %q", llm.user)
	}
}

func TestTrajectoryReflector_AllInvalidTriggersRetryError(t *testing.T) {
	llm := &extractorLLMStub{content: `[{"content":"","importance":0.9,"fact_type":"other"}]`}
	r := newConfiguredReflector(llm, nil)
	sk := domain.TrajectorySkeleton{ExecutionID: "e1", Steps: []domain.TrajectoryStep{{ToolName: "search", Status: domain.TrajectoryStepStatusSuccess}}}
	if _, err := r.Reflect(context.Background(), "t1", sk, ""); err == nil {
		t.Fatal("all-invalid entries must produce retry error")
	}
}

// TestTrajectoryReflector_FailsClosedWithoutModel 验证 memory.reflection_model
// 缺失即 fail-closed：Reflect 返回 *modelconfig.Err(missing)，不发起 LLM 调用。
func TestTrajectoryReflector_FailsClosedWithoutModel(t *testing.T) {
	llm := &extractorLLMStub{content: `[]`}
	r := NewTrajectoryReflector(llm)
	r.SetTenantID("t1")
	r.SetParamResolver(keyedResolverStub{vals: map[string]any{
		"memory.reflection_prompt": testReflectionPrompt,
	}})
	sk := domain.TrajectorySkeleton{ExecutionID: "e1", Steps: []domain.TrajectoryStep{{ToolName: "search", Status: domain.TrajectoryStepStatusSuccess}}}
	_, err := r.Reflect(context.Background(), "t1", sk, "")
	if err == nil {
		t.Fatal("missing memory.reflection_model must fail closed")
	}
	ce, ok := modelconfig.AsConfigError(err)
	if !ok {
		t.Fatalf("expected *modelconfig.Err, got %v", err)
	}
	if ce.State != modelconfig.StateMissing || ce.Key != modelconfig.KeyReflectionModel {
		t.Fatalf("state/key = %s/%s, want missing/%s", ce.State, ce.Key, modelconfig.KeyReflectionModel)
	}
	if llm.model != "" {
		t.Fatalf("no LLM call must be issued on config failure, got model %q", llm.model)
	}
}
