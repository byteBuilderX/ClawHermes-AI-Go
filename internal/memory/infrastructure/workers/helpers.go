package workers

import (
	"context"
	"time"

	memport "github.com/byteBuilderX/stratum/internal/memory/domain/port"
	"github.com/byteBuilderX/stratum/internal/memory/infrastructure/modelconfig"
	"go.uber.org/zap"
)

// logModelConfigError 记录一次记忆模型参数配置失败并计数（stage 标识消费组件）。
// err 非 *modelconfig.Err 时为空操作（不吞普通错误、不伪造计数）；logger 可为
// nil（LLMHistorySummarizer 未注入 logger 时仅计数）。
func logModelConfigError(logger *zap.Logger, stage string, err error) {
	ce, ok := modelconfig.AsConfigError(err)
	if !ok {
		return
	}
	modelconfig.IncError(ce.Key, stage, ce.State)
	if logger != nil {
		logger.Error("memory.modelconfig.config_error",
			zap.String("param", ce.Key),
			zap.String("config_state", string(ce.State)),
			zap.Error(err))
	}
}

// resolvePlatformString resolves a platform string param through r, returning
// def when r is nil, the key is unset, or resolution fails. Shared by the
// history summarizer and superseder cross-agent workers.
func resolvePlatformString(ctx context.Context, r memport.PlatformParamResolver, key, def string) string {
	if r == nil {
		return def
	}
	v, ok, err := r.ResolvePlatform(ctx, key)
	if err != nil || !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

// resolvePlatformFloat resolves a platform float param through r, returning
// def when r is nil, the key is unset (0), or resolution fails.
func resolvePlatformFloat(ctx context.Context, r memport.PlatformParamResolver, key string, def float32) float32 {
	if r == nil {
		return def
	}
	v, ok, err := r.ResolvePlatform(ctx, key)
	if err != nil || !ok {
		return def
	}
	if f, ok := v.(float64); ok {
		return float32(f)
	}
	return def
}

// SleepCtx sleeps for duration d, returning early if ctx is cancelled or stopCh is closed.
// Returns true if the full duration elapsed, false if cancelled early.
func SleepCtx(ctx context.Context, stopCh <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-stopCh:
		return false
	case <-t.C:
		return true
	}
}

// runWithRestart executes fn in a supervisor loop: recovers from panics and restarts
// with exponential backoff, exits cleanly on ctx cancel or stopCh close.
func runWithRestart(ctx context.Context, stopCh chan struct{}, logger *zap.Logger, name string, fn func(context.Context)) {
	const (
		baseBackoff       = 100 * time.Millisecond
		maxBackoff        = 30 * time.Second
		fastExitThreshold = 5
		fastExitWindow    = 5 * time.Second
	)
	backoff := baseBackoff
	fastExits := 0
	for {
		start := time.Now()
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error(name+".panic", zap.Any("panic", r), zap.Stack("stack"))
					incWorkerPanics(name)
				}
			}()
			fn(ctx)
		}()
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		default:
		}
		runtime := time.Since(start)
		switch {
		case runtime > time.Minute:
			backoff = baseBackoff
			fastExits = 0
		case runtime < fastExitWindow:
			fastExits++
			if fastExits >= fastExitThreshold {
				backoff = maxBackoff
				fastExits = 0
			}
		default:
			fastExits = 0
		}
		logger.Warn(name+".restarting", zap.Duration("backoff", backoff))
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}
