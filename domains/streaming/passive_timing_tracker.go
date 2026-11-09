// passive_timing_tracker.go — 被动时序指纹的**逐请求**追踪器与观察者组合器。
//
// passive_timing_tracker.go — the per-request passive timing tracker and
// the text-observer composer.
//
// # 数据从哪来
//
// audit.StreamCapture 的 ObserveText 在每个文本增量追加时被同步调用
// （持 sc.mu）。追踪器在 ObserveText 里取 time.Now() —— 这就是「该增量
// 到达网关」的时刻，增量间隔（ITT）与首个增量到达时刻（TTFT 近似：
// 首 ObserveText − 追踪器创建时刻）都从它推。粒度是「每次 notify」而
// 不是「每个 token」，但批处理形态本身也是模型/量化档位的指纹
// （GenerateConfig 的批量语义不同 ⇒ 到达节奏不同）。
//
// # 热路径纪律（audit.StreamTextObserver 契约）
//
// ObserveText 在 sc.mu 持有期间被调 ⇒ 必须快、必须不重入 capture：
// 只做一次 time.Now() + 有界 slice append + 位标 OR。样本有界
// （passiveMaxSamplesPerRequest）⇒ 超长流不增长内存。
//
// # 组合器
//
// capture 只有一个观察者槽，而完整性追踪器（integrity.StreamTracker）
// 已经占着它 —— 组合器让两者并存：两者都 observe；**中断裁决权归
// integrity**（返回它的 breach 判定），被动追踪器只观察、永不中断流
// （观测器不该有改变流行为的权力，这是采样器与执法器的分界）。
package streaming

import (
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
)

// passiveMaxSamplesPerRequest 是单请求 ITT 采样上限（防超长流把内存吃穿）。
const passiveMaxSamplesPerRequest = 4096

// PassiveTimingTracker 是一个请求的被动时序采样器。
//
// 实现 audit.StreamTextObserver：中断相关方法全部 no-op（永不中断流）。
type PassiveTimingTracker struct {
	mu sync.Mutex

	// createdAt 是追踪器创建时刻（≈ 流进入时刻，TTFT 的起点）。
	createdAt time.Time

	// firstAt / lastAt 是首个/最近一个文本增量到达时刻。
	firstAt time.Time
	lastAt  time.Time

	// itts 是相邻文本增量的到达间隔。
	itts []time.Duration

	// textBytes 是累计文本字节数（吞吐分母的近似：token 数在完成点由
	// usage 提供，字节数只做兜底估算）。
	textBytes int

	// formatFlags 是格式位标（### / **bold** / list / code block）。
	// ★ 增量可能把标记切开（"#" 到了、"##" 还没到）⇒ 只在**流结束**
	// 由消费方对完整文本判 —— 但追踪器不存全文（内存纪律）。
	// ⇒ 位标在完成点由消费方从 capture 的 textContent 取，不在追踪器算。
}

// NewPassiveTimingTracker 创建追踪器。now 可注入（测试用）。
func NewPassiveTimingTracker(now time.Time) *PassiveTimingTracker {
	if now.IsZero() {
		now = time.Now()
	}
	return &PassiveTimingTracker{createdAt: now}
}

// ObserveText 实现 audit.StreamTextObserver（采样；永不中断）。
func (t *PassiveTimingTracker) ObserveText(s string) bool {
	if t == nil || s == "" {
		return false
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstAt.IsZero() {
		t.firstAt = now
	} else if !t.lastAt.IsZero() {
		if d := now.Sub(t.lastAt); d > 0 && len(t.itts) < passiveMaxSamplesPerRequest {
			t.itts = append(t.itts, d)
		}
	}
	t.lastAt = now
	t.textBytes += len(s)
	return false
}

// BreachReason / RepeatedContentHash / Reset —— 观察者契约的 no-op 面：
// 被动采样器不是执法器，永不中断流、无 breach 语义、无重复块检测。
func (t *PassiveTimingTracker) BreachReason() string { return "" }

func (t *PassiveTimingTracker) RepeatedContentHash() (string, int, int, int, bool) {
	return "", 0, 0, 0, false
}

// Reset 清空累积状态（凭据 failover 后复用同一 capture 的场景）。
func (t *PassiveTimingTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.firstAt = time.Time{}
	t.lastAt = time.Time{}
	t.itts = nil
	t.textBytes = 0
	t.createdAt = time.Now()
}

// Compile-time assertion：与 integrity.StreamTracker 同一观察者契约。
var _ audit.StreamTextObserver = (*PassiveTimingTracker)(nil)

// Snapshot 汇出本请求的时序观测（完成点调用一次）。
//
// usageTokens 由完成点提供（capture.SummaryAsMap 的 completion_tokens）；
// 0 ⇒ 吞吐不产出（不按 0 分算的口径纪律）。
func (t *PassiveTimingTracker) Snapshot(usageCompletionTokens int) (ttft time.Duration, itts []time.Duration, throughput float64) {
	if t == nil {
		return 0, nil, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.firstAt.IsZero() && !t.createdAt.IsZero() {
		if d := t.firstAt.Sub(t.createdAt); d > 0 {
			ttft = d
		}
	}
	itts = append([]time.Duration(nil), t.itts...)
	if usageCompletionTokens > 0 && !t.firstAt.IsZero() && t.lastAt.After(t.firstAt) {
		if secs := t.lastAt.Sub(t.firstAt).Seconds(); secs > 0 {
			throughput = float64(usageCompletionTokens) / secs
		}
	}
	return ttft, itts, throughput
}

// composedTextObserver 让多个观察者并存于 capture 的单一槽位。
//
// 中断裁决权归 primary（integrity 执法器）；被动追踪器只观察。
type composedTextObserver struct {
	primary audit.StreamTextObserver
	extra   []audit.StreamTextObserver
}

func (c *composedTextObserver) ObserveText(s string) bool {
	abort := false
	if c.primary != nil {
		abort = c.primary.ObserveText(s)
	}
	for _, o := range c.extra {
		// ★ extra 的返回值不采纳：采样器永不拥有中断权。
		_ = o.ObserveText(s)
	}
	return abort
}

func (c *composedTextObserver) BreachReason() string {
	if c.primary == nil {
		return ""
	}
	return c.primary.BreachReason()
}

func (c *composedTextObserver) RepeatedContentHash() (string, int, int, int, bool) {
	if c.primary == nil {
		return "", 0, 0, 0, false
	}
	return c.primary.RepeatedContentHash()
}

func (c *composedTextObserver) Reset() {
	if c.primary != nil {
		c.primary.Reset()
	}
	for _, o := range c.extra {
		o.Reset()
	}
}

var _ audit.StreamTextObserver = (*composedTextObserver)(nil)

// PassiveTimingFactory 是 handler 侧的窄接口：每请求产出一个独立追踪器
// （integrityObserverFactory 同款形态，domains/streaming 不 import
// 具体实现包）。
type PassiveTimingFactory interface {
	NewPassiveTimingTracker() *PassiveTimingTracker
}

// PassiveFeaturesSink 是完成点的被动特征出口（handler 调一次/请求）。
// 生产实现是 modelcredibility.PassiveCollector.Add 的适配器。
type PassiveFeaturesSink interface {
	Submit(features *modelcredibility.PassiveFeatures)
}

// PassiveTrackerFromCapture 从 capture 的观察者槽取出被动追踪器
// （组合或直挂两种形态）。nil-safe。
func PassiveTrackerFromCapture(capture *audit.StreamCapture) *PassiveTimingTracker {
	if capture == nil {
		return nil
	}
	switch o := capture.TextObserver().(type) {
	case *PassiveTimingTracker:
		return o
	case *composedTextObserver:
		for _, e := range o.extra {
			if tr, ok := e.(*PassiveTimingTracker); ok {
				return tr
			}
		}
	}
	return nil
}

// ExtractPassiveFromTracker 把追踪器时序 + 完成点元数据合成被动特征。
//
// usage 为 0 值的维度安全缺省（不产出 0 吞吐/0 密度——缺失不计 0）。
func ExtractPassiveFromTracker(tracker *PassiveTimingTracker, usageCompletionTokens int, meta modelcredibility.Metadata) *modelcredibility.PassiveFeatures {
	if tracker == nil || meta.ModelName == "" {
		return nil
	}
	ttft, itts, throughput := tracker.Snapshot(usageCompletionTokens)
	if ttft == 0 && len(itts) == 0 {
		// 没有任何文本增量（空流/纯工具调用）⇒ 没有可学的东西。
		return nil
	}
	return &modelcredibility.PassiveFeatures{
		TenantID:         meta.TenantID,
		ModelName:        meta.ModelName,
		CredentialID:     meta.CredentialID,
		RequestID:        meta.RequestID,
		TTFT:             ttft,
		ITTs:             itts,
		Throughput:       throughput,
		CompletionTokens: usageCompletionTokens,
		MeasuredAt:       time.Now(),
	}
}
