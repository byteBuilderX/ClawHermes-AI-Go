package modelconfig

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/byteBuilderX/stratum/pkg/constants"
)

// PlatformValuesReader 返回当前平台层生效值（原始值，不触发读时 Validate）。
// internal/parameters/application.Service.PlatformValues 满足本接口。
type PlatformValuesReader interface {
	PlatformValues(ctx context.Context) (map[string]any, error)
}

// EnabledCatalog 返回当前 enabled 模型名名单，按能力拆分。llmgateway 的
// ModelRegistry.ListChatModelsByTenant / ListEmbeddingModelsByTenant 经 wiring
// 薄适配后满足本接口；名单语义 = 运行期可解析模型（model.Enabled ∧ provider
// Enabled ∧ 能力匹配）。
type EnabledCatalog interface {
	ChatEnabled(ctx context.Context) ([]string, error)
	EmbedEnabled(ctx context.Context) ([]string, error)
}

// Kind 表示必需参数属于哪类能力，决定用哪份 enabled 名单判定。
type Kind string

const (
	KindChat  Kind = "chat"
	KindEmbed Kind = "embed"
)

// Requirement 描述一个必需记忆模型参数：平台 key + 能力类别。
type Requirement struct {
	Key  string
	Kind Kind
}

// Probe 是记忆模型参数完备性探针：与流量无关，周期比对平台参数值与 enabled
// 目录，逐必需参数上报 ok/missing/disabled。平台级（全局参数 × 全局目录），
// 无租户维度。首拍立即执行一轮，之后按 interval 周期执行。
type Probe struct {
	values   PlatformValuesReader
	catalog  EnabledCatalog
	reqs     []Requirement
	interval time.Duration
	logger   *zap.Logger

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewProbe 构造探针。reqs 为空或 values/catalog 为 nil 时 CheckOnce 为空转
// （探针由装配方仅在必需集非空时挂载，此处兜底不 panic）。
func NewProbe(values PlatformValuesReader, catalog EnabledCatalog, reqs []Requirement, logger *zap.Logger) *Probe {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Probe{
		values:   values,
		catalog:  catalog,
		reqs:     reqs,
		interval: constants.MemoryModelConfigProbeInterval,
		logger:   logger,
		stopCh:   make(chan struct{}),
	}
}

// WithInterval 覆盖探针周期（测试注入短周期）。
func (p *Probe) WithInterval(d time.Duration) *Probe {
	p.interval = d
	return p
}

// Start 启动后台探针循环；首拍立即 CheckOnce，之后按 interval 周期执行。
func (p *Probe) Start(ctx context.Context) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.run(ctx)
	}()
}

// Stop 关闭探针循环并等待 goroutine 退出。
func (p *Probe) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	p.wg.Wait()
}

func (p *Probe) run(ctx context.Context) {
	p.CheckOnce(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.CheckOnce(ctx)
		}
	}
}

// CheckOnce 对全部必需参数执行一轮完备性检查并刷新指标。Start 的首轮与周期
// 循环复用此方法；导出供测试单轮驱动确定性断言。
func (p *Probe) CheckOnce(ctx context.Context) {
	if len(p.reqs) == 0 || p.values == nil || p.catalog == nil {
		return
	}
	raw, err := p.values.PlatformValues(ctx)
	if err != nil {
		p.logger.Error("memory.modelconfig.probe.platform_values_failed",
			zap.Error(err))
		return
	}
	chat, err := p.catalog.ChatEnabled(ctx)
	if err != nil {
		p.logger.Error("memory.modelconfig.probe.chat_catalog_failed",
			zap.Error(err))
		return
	}
	embed, err := p.catalog.EmbedEnabled(ctx)
	if err != nil {
		p.logger.Error("memory.modelconfig.probe.embed_catalog_failed",
			zap.Error(err))
		return
	}
	chatSet := toSet(chat)
	embedSet := toSet(embed)
	for _, req := range p.reqs {
		state := classify(req, raw, chatSet, embedSet)
		SetParamHealth(req.Key, state)
	}
}

// classify 判定单个必需参数状态：空/缺失 → missing；非空但不在对应 enabled
// 名单 → disabled；命中 → ok。
func classify(req Requirement, raw map[string]any, chatSet, embedSet map[string]struct{}) State {
	s, ok := raw[req.Key].(string)
	if !ok {
		return StateMissing
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return StateMissing
	}
	if contains(toSetSelector(req, chatSet, embedSet), s) {
		return StateOK
	}
	return StateDisabled
}

// toSetSelector 按能力返回对应 enabled 名单。
func toSetSelector(req Requirement, chatSet, embedSet map[string]struct{}) map[string]struct{} {
	if req.Kind == KindEmbed {
		return embedSet
	}
	return chatSet
}

func toSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

func contains(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}
