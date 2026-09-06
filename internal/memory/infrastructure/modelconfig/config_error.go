package modelconfig

import (
	"errors"
	"fmt"
)

// 7 个记忆模型平台参数 key 的唯一内存侧常量源。参数注册表
// （internal/parameters/domain/registry.go）内联字符串；本块收敛 memory 消费层
// 与探针/wiring 的引用，避免同一 key 多处魔数漂移。
const (
	KeyExtractionModel     = "memory.extraction_model"
	KeyReflectionModel     = "memory.reflection_model"
	KeyEnrichModel         = "memory.enrich_model"
	KeySummaryModel        = "memory.summary_model"
	KeyEmbeddingModel      = "memory.embedding_model"
	KeyHistorySummaryModel = "memory.history_summary_model"
	KeySupersedeModel      = "memory.supersede_model"
)

// State 是模型参数配置状态的语义枚举，作为指标 state label 与告警判定口径。
type State string

const (
	// StateOK 表示参数已显式配置且命中 enabled 模型目录。
	StateOK State = "ok"
	// StateMissing 表示参数未配置（空/缺失），运行期将 fail-closed。
	StateMissing State = "missing"
	// StateUnavailable 表示参数解析失败（读时目录拒绝、DB 故障），无法判定。
	StateUnavailable State = "unavailable"
	// StateDisabled 表示参数配置了模型，但该模型不在 enabled 目录。
	StateDisabled State = "disabled"
)

// Err 是记忆模型参数不满足强制化语义时的哨兵错误。调用方用 AsConfigError 分支：
// 例如 enrich 主链把 StateMissing/StateUnavailable 当「配置错 → 即时 DLQ」，
// 与普通 LLM 错误（重试）区分。
type Err struct {
	// Key 是出问题的平台参数 key（如 memory.enrich_model）。
	Key string
	// State 描述失败形态：missing（空/缺失）或 unavailable（解析失败）。
	State State
	// Cause 仅在解析失败（unavailable）时携带根因。
	Cause error
}

// Error 实现 error 接口。
func (e *Err) Error() string {
	return fmt.Sprintf("memory model config: %s state=%s", e.Key, e.State)
}

// Unwrap 暴露根因，供 errors.Is/As 穿透。
func (e *Err) Unwrap() error {
	return e.Cause
}

// AsConfigError 报告 err 是否为 *modelconfig.Err 并解出该哨兵。
func AsConfigError(err error) (*Err, bool) {
	var ce *Err
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}
