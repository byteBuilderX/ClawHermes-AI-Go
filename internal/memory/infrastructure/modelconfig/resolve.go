package modelconfig

import (
	"context"
	"strings"
)

// PlatformResolver 解析平台级记忆模型参数。memport.PlatformParamResolver
// 结构上满足本接口；保留本地窄接口使本包不 import 兄弟 context，且便于单测。
type PlatformResolver interface {
	// ResolvePlatform 返回 key 的有效值；present=false 表示未设置（走定义默认），
	// err 表示瞬时解析失败。
	ResolvePlatform(ctx context.Context, key string) (any, bool, error)
}

// ResolveChatModel 严格解析一个 chat 模型平台参数，禁止空值回落 llmgateway 默认：
//
//   - r 为 nil（未装配参数服务）→ *Err{StateMissing}
//   - ResolvePlatform 返回错误（读时 ValidateFn 目录拒绝、DB 故障）→
//     *Err{StateUnavailable, Cause}
//   - 值缺失/非 string/纯空白 → *Err{StateMissing}
//   - 其余 → 返回非空模型名
//
// 本方法不做目录查询：运行期每消息不得新增 DB 负载。disabled 模型由两条已覆盖
// 的路径处理——带 ValidateFn 的 key 读时即 unavailable，extraction/reflection 由
// 探针按 enabled 名单兜底。
func ResolveChatModel(ctx context.Context, r PlatformResolver, key string) (string, error) {
	if r == nil {
		return "", &Err{Key: key, State: StateMissing}
	}
	v, ok, err := r.ResolvePlatform(ctx, key)
	if err != nil {
		return "", &Err{Key: key, State: StateUnavailable, Cause: err}
	}
	if !ok || v == nil {
		return "", &Err{Key: key, State: StateMissing}
	}
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", &Err{Key: key, State: StateMissing}
	}
	return s, nil
}

// CheckModelConfig 是 ResolveChatModel 的便捷包装，供周期 worker 预检
// （supersede/history RunOnce 顶部）复用同一判定口径。
func CheckModelConfig(ctx context.Context, r PlatformResolver, key string) error {
	_, err := ResolveChatModel(ctx, r, key)
	return err
}
