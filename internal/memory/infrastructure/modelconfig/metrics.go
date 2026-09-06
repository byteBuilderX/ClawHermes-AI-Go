// Package modelconfig 提供记忆模型参数强制化的共享判定与观测：
//
//  1. ResolveChatModel / CheckModelConfig —— 运行期 fail-closed，禁止空值回落到
//     llmgateway 默认模型；
//  2. Probe —— 与流量无关的周期完备性探针，按平台参数值 × enabled 模型目录逐
//     必需参数上报 ok/missing/disabled；
//  3. ConfigErrorsTotal / ConfigHealth —— 两条防线各自的指标，供告警规则消费。
//
// 本包被 pipeline / workers / wiring 单向 import，不新增依赖环；不 import 任何
// 兄弟 context（llmgateway / parameters）的实现或领域类型，只通过窄接口取数。
package modelconfig

import (
	"github.com/prometheus/client_golang/prometheus"
)

// 运行期 fail-closed 计数。labels: param(平台参数 key)、stage(消费组件名)、
// state(missing|unavailable|disabled)。state 不含 ok —— 正常路径不计数。
var ConfigErrorsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "memory_model_config_errors_total",
		Help: "Total memory steps that failed closed because a required model parameter was missing or unresolvable.",
	},
	[]string{"param", "stage", "state"},
)

// 周期探针完备性状态。labels: param(平台参数 key)、state(ok|missing|disabled)。
// GaugeVec 需显式 WithLabelValues().Set 才产生序列，故每 tick 全 state Set。
var ConfigHealth = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "memory_model_config_health",
		Help: "Completeness of a required memory model parameter: 1 when the param is in the labeled state.",
	},
	[]string{"param", "state"},
)

// RegisterMetrics 向 reg 注册本包指标。调用方容忍 AlreadyRegisteredError：
// pipeline 与 workers 两个装配点都会调用一次（进程级同一 registerer），重复注册
// 不得 panic。Collector 在未注册时也能计数/设值，仅不对外暴露。
func RegisterMetrics(reg prometheus.Registerer) {
	if reg == nil {
		return
	}
	_ = reg.Register(ConfigErrorsTotal)
	_ = reg.Register(ConfigHealth)
}

// IncError 记录一次运行期配置失败（param 平台参数 key，stage 消费组件，state
// 见 config_error.go 的 State 枚举）。
func IncError(param, stage string, state State) {
	ConfigErrorsTotal.WithLabelValues(param, stage, string(state)).Inc()
}

// SetHealth 把 param 的 state 系列置 on/off；state 仅限 ok|missing|disabled。
func SetHealth(param string, state State, on bool) {
	v := 0.0
	if on {
		v = 1
	}
	ConfigHealth.WithLabelValues(param, string(state)).Set(v)
}

// SetParamHealth 把 param 三个可观测 state（ok/missing/disabled）整体刷新：
// 命中者置 1，其余置 0，保证每个 tick 三系列都存在且单调不残留。
func SetParamHealth(param string, active State) {
	for _, s := range []State{StateOK, StateMissing, StateDisabled} {
		SetHealth(param, s, s == active)
	}
}
