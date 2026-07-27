// Package observability 实现可观测性领域 (Hook)。
// 阶段: PreRouting (trace) / PostResponse (metrics)
package observability

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// Span 表示一个追踪 span
type Span struct {
	TraceID   string
	SpanID    string
	ParentID  string
	Name      string
	StartTime time.Time
	EndTime   time.Time
	Tags      map[string]string
	Logs      []SpanLog
}

// SpanLog span 日志
type SpanLog struct {
	Timestamp time.Time
	Message   string
	Fields    map[string]any
}

// Duration 返回 span 持续时间
func (s *Span) Duration() time.Duration {
	if s.EndTime.IsZero() {
		return time.Since(s.StartTime)
	}
	return s.EndTime.Sub(s.StartTime)
}

// Tracer tracer 接口
type Tracer interface {
	StartSpan(name string, parent *Span) *Span
	FinishSpan(span *Span)
}

// InMemoryTracer 内存 tracer（测试用）
type InMemoryTracer struct {
	mu    sync.Mutex
	spans []*Span
}

// NewInMemoryTracer 创建内存 tracer
func NewInMemoryTracer() *InMemoryTracer {
	return &InMemoryTracer{spans: make([]*Span, 0)}
}

// StartSpan 启动一个 span
func (t *InMemoryTracer) StartSpan(name string, parent *Span) *Span {
	span := &Span{
		Name:      name,
		StartTime: time.Now(),
		Tags:      make(map[string]string),
		Logs:      make([]SpanLog, 0),
	}
	if parent != nil {
		span.ParentID = parent.SpanID
		span.TraceID = parent.TraceID
	} else {
		span.TraceID = generateID()
	}
	span.SpanID = generateID()
	return span
}

// FinishSpan 结束 span
func (t *InMemoryTracer) FinishSpan(span *Span) {
	span.EndTime = time.Now()
	t.mu.Lock()
	t.spans = append(t.spans, span)
	t.mu.Unlock()
}

// Spans 返回所有 span（副本）
func (t *InMemoryTracer) Spans() []*Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*Span, len(t.spans))
	copy(out, t.spans)
	return out
}

// Reset 清空所有 span
func (t *InMemoryTracer) Reset() {
	t.mu.Lock()
	t.spans = make([]*Span, 0)
	t.mu.Unlock()
}

// NoopTracer 空 tracer（生产用零开销 fallback）
type NoopTracer struct{}

// NewNoopTracer 创建 noop tracer
func NewNoopTracer() *NoopTracer { return &NoopTracer{} }

// StartSpan 启动 span
func (n *NoopTracer) StartSpan(name string, parent *Span) *Span {
	span := &Span{Name: name, StartTime: time.Now(), Tags: make(map[string]string)}
	if parent != nil {
		span.ParentID = parent.SpanID
		span.TraceID = parent.TraceID
	} else {
		span.TraceID = generateID()
	}
	span.SpanID = generateID()
	return span
}

// FinishSpan 结束 span
func (n *NoopTracer) FinishSpan(span *Span) {
	span.EndTime = time.Now()
}

// Counter 计数器
//
// 2026-07-27 concurrency fix: Inc/Add 之前直接对 Value 做读改写，而
// MetricsHook.Execute 会在每个请求 goroutine 上调用它们（Registry.mu 只
// 保护 map，不保护 metric 内部字段）→ 数据竞争 + 丢计数。
// 现在用 per-metric mu 保护 Value；Value 仍然是导出字段，因为
// cmd/gateway-v2 的 /metrics 渲染读取快照里的 Value（快照由
// Registry.Counters() 持锁逐字段拷贝产生，不拷贝 mu）。
type Counter struct {
	mu     sync.Mutex
	Name   string
	Value  float64
	Labels map[string]string
}

// Inc 自增 1
func (c *Counter) Inc() {
	c.mu.Lock()
	c.Value++
	c.mu.Unlock()
}

// Add 增加 v
func (c *Counter) Add(v float64) {
	c.mu.Lock()
	c.Value += v
	c.mu.Unlock()
}

// Get 持锁读取当前值
func (c *Counter) Get() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Value
}

// Histogram 直方图
//
// 2026-07-27 concurrency fix: Observe 之前无锁修改 Sum/Count/Counts，
// 与 Counter 同一条请求路径 → 同样的竞争。per-metric mu 保护三者。
type Histogram struct {
	mu      sync.Mutex
	Name    string
	Buckets []float64
	Counts  []int64 // len = len(Buckets)+1; last is +Inf
	Labels  map[string]string
	Sum     float64
	Count   int64
}

// Observe 记录一个值
func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Sum += v
	h.Count++
	for i, bucket := range h.Buckets {
		if v <= bucket {
			h.Counts[i]++
		}
	}
	h.Counts[len(h.Buckets)]++ // +Inf bucket
}

// Snapshot 返回持锁拷贝（Counts 深拷贝），供导出路径安全读取
func (h *Histogram) Snapshot() *Histogram {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

// snapshotLocked 调用方必须已持有 h.mu
func (h *Histogram) snapshotLocked() *Histogram {
	counts := make([]int64, len(h.Counts))
	copy(counts, h.Counts)
	return &Histogram{
		Name:    h.Name,
		Buckets: h.Buckets, // 创建后只读
		Counts:  counts,
		Labels:  cloneLabels(h.Labels),
		Sum:     h.Sum,
		Count:   h.Count,
	}
}

// Registry 指标注册表
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	histograms map[string]*Histogram
}

// NewRegistry 创建注册表
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		histograms: make(map[string]*Histogram),
	}
}

// Counter 获取或创建计数器
func (r *Registry) Counter(name string, labels map[string]string) *Counter {
	key := name + labelKey(labels)
	r.mu.RLock()
	if c, ok := r.counters[key]; ok {
		r.mu.RUnlock()
		return c
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	// 双重检查
	if c, ok := r.counters[key]; ok {
		return c
	}
	c := &Counter{Name: name, Labels: cloneLabels(labels)}
	r.counters[key] = c
	return c
}

// Histogram 获取或创建直方图
func (r *Registry) Histogram(name string, buckets []float64, labels map[string]string) *Histogram {
	key := name + labelKey(labels)
	r.mu.RLock()
	if h, ok := r.histograms[key]; ok {
		r.mu.RUnlock()
		return h
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.histograms[key]; ok {
		return h
	}
	h := &Histogram{
		Name:    name,
		Buckets: buckets,
		Counts:  make([]int64, len(buckets)+1),
		Labels:  cloneLabels(labels),
	}
	r.histograms[key] = h
	return h
}

// Counters 返回所有计数器（深拷贝）
func (r *Registry) Counters() map[string]*Counter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*Counter, len(r.counters))
	for k, v := range r.counters {
		// 2026-07-27 concurrency fix: 逐字段拷贝而不是 `c := *v`
		// —— 后者会连带复制 Counter.mu（vet copylocks）且读 Value 无锁。
		v.mu.Lock()
		out[k] = &Counter{Name: v.Name, Value: v.Value, Labels: cloneLabels(v.Labels)}
		v.mu.Unlock()
	}
	return out
}

// Histograms 返回所有直方图（深拷贝）
func (r *Registry) Histograms() map[string]*Histogram {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*Histogram, len(r.histograms))
	for k, v := range r.histograms {
		// 2026-07-27 concurrency fix: 同上 —— 持 metric 锁做逐字段快照，
		// 并深拷贝 Counts（原来共享底层数组，导出时仍会与 Observe 竞争）。
		out[k] = v.Snapshot()
	}
	return out
}

// Reset 清空所有指标
func (r *Registry) Reset() {
	r.mu.Lock()
	r.counters = make(map[string]*Counter)
	r.histograms = make(map[string]*Histogram)
	r.mu.Unlock()
}

func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := ""
	for _, k := range keys {
		v := labels[k]
		s += k + "=" + v + ","
	}
	return s
}

func cloneLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
