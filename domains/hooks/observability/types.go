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
type Counter struct {
	Name   string
	Value  float64
	Labels map[string]string
}

// Inc 自增 1
func (c *Counter) Inc() { c.Value++ }

// Add 增加 v
func (c *Counter) Add(v float64) { c.Value += v }

// Histogram 直方图
type Histogram struct {
	Name    string
	Buckets []float64
	Counts  []int64 // len = len(Buckets)+1; last is +Inf
	Labels  map[string]string
	Sum     float64
	Count   int64
}

// Observe 记录一个值
func (h *Histogram) Observe(v float64) {
	h.Sum += v
	h.Count++
	for i, bucket := range h.Buckets {
		if v <= bucket {
			h.Counts[i]++
		}
	}
	h.Counts[len(h.Buckets)]++ // +Inf bucket
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
		c := *v
		c.Labels = cloneLabels(v.Labels)
		out[k] = &c
	}
	return out
}

// Histograms 返回所有直方图（深拷贝）
func (r *Registry) Histograms() map[string]*Histogram {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*Histogram, len(r.histograms))
	for k, v := range r.histograms {
		h := *v
		h.Labels = cloneLabels(v.Labels)
		out[k] = &h
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
