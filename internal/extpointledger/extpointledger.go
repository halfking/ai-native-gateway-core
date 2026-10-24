// Package extpointledger —— **扩展点台账的运行期部分**（P2-2）。
//
// # 这道改造补的是哪块缺口
//
// 计划书 §6-P2-2 要求一份「扩展点台账」：
//
//	「按扩展点登记『调用次数 / p99 / 失败数 / 超时数 / 最近错误』，admin 可查」
//
// ★ 而本仓的**静态那一半**已经有了：`internal/extpoints` 的裁决表
// （每个机制一行：ID / 中文名 / 代码位置 / 裁决 / **是否在生产** / 所挂 phase）。
// ⇒ ⇒ **缺的是运行期那一半**（调用次数 / p99 / 失败数 / 超时数 / 最近错误）。
//
// ⇒ 本包提供那**一半**，并且刻意与裁决表**共用同一套机制 ID**
// ⇒ ⇒ admin 可以把两半**合并**成一行一机制的完整台账，
// 而且 ★ **两半可以互相印证**：
// 裁决表说「这个机制不在生产路径上」，那么台账的**调用次数必须恒为 0**
// —— 这是**两份独立来源**的对拍（裁决表来自源码扫描，台账来自运行期计数）。
//
// # 为什么用「每机制一个进程内台账」而不是 DB
//
// 台账是**观测面**，不是**账本**：
//
//	① 它要能在**进程崩溃后消失**（那才是它没有撒谎的证据）；
//	② 写 DB 会在请求热路径上引入 IO（⇒ 观测面拖慢数据面 ⇒ 观测面被关掉）。
//
// ⇒ ★ 口径：**进程内、只读快照、可随时丢**。
//
// # 内存有界
//
// 每个机制只保留**最近 DefaultSampleCapacity 个**延迟样本（环形缓冲）⇒
// p99 是「最近窗口」的 p99，不是全量 p99 ⇒ ★ 这个口径必须写在读端上（见 `Entry.P99Window`）。
package extpointledger

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// Outcome 是一次扩展点调用的结局。
type Outcome string

const (
	// OutcomeOK：调用成功。
	OutcomeOK Outcome = "ok"
	// OutcomeFailed：调用返回错误。
	OutcomeFailed Outcome = "failed"
	// OutcomeTimeout：调用超时。
	OutcomeTimeout Outcome = "timeout"
	// OutcomeDropped：事件/结果被**丢弃**（例如观察者队列满）。
	OutcomeDropped Outcome = "dropped"
	// OutcomeRejected：调用方**主动拒绝**了请求（不是出错，是"这道闸门按设计拦下来了"）。
	//
	// ★ 为什么需要它（2026-10-24 补）：HTTP 中间件链的「失败」有两种**完全不同**的形态——
	//
	//	① **拒绝**（4xx）：Auth / Origin / SecurityHeaders 按设计把请求拦下来了
	//	   ⇒ 这是扩展点**在正常工作**，不是它坏了；
	//	② **出错**（5xx）：panic 兜底、下游处理器报错
	//	   ⇒ 这才是扩展点/下游**真的失败**。
	//
	// ⇒ 若把两者都算成 `failed` ⇒ ★ **「失败数」会变成"拒绝率"** ⇒
	//   运维看到主链失败数飙升，第一反应是"网关坏了"，而真相可能只是
	//   **有人在狂刷坏 key** ⇒ ⇒ 口径失真。
	// ⇒ ⇒ 所以单列一个结局，**不与错误混同**。
	OutcomeRejected Outcome = "rejected"
)

// DefaultSampleCapacity 是每个机制保留的延迟样本数（环形缓冲容量）。
//
// ★ 有界 ⇒ 内存有界（6 个机制 ⇒ 6×256 个 time.Duration ⇒ 约 12 KiB）。
const DefaultSampleCapacity = 256

// Entry 是台账的一行。
type Entry struct {
	// MechanismID 是 `internal/extpoints` 的机制 ID。
	MechanismID string `json:"mechanism_id"`
	// Calls 是调用总次数（所有结局都算）。
	Calls uint64 `json:"calls"`
	// Failures 是失败次数。
	Failures uint64 `json:"failures"`
	// Timeouts 是超时次数。
	Timeouts uint64 `json:"timeouts"`
	// Dropped 是丢弃次数。
	Dropped uint64 `json:"dropped"`
	// Rejected 是**被拒绝**的次数（扩展点按设计拦下请求；不是出错）。
	Rejected uint64 `json:"rejected"`
	// P99Millis 是**最近窗口**的 p99 延迟（毫秒，整数向下取整）。
	P99Millis uint64 `json:"p99_ms"`
	// P99Window 是 p99 样本窗口的**容量**（读端必须标出口径）。
	P99Window int `json:"p99_window"`
	// LastError 是最近一次错误的文本（无错误则空）。
	LastError string `json:"last_error,omitempty"`
	// LastErrorAt 是最近一次错误的时间（无错误则零值）。
	LastErrorAt time.Time `json:"last_error_at,omitempty"`
}

// entryState 是一个机制的运行期状态。
type entryState struct {
	calls    uint64
	failures uint64
	timeouts uint64
	dropped  uint64
	rejected uint64

	lastError   string
	lastErrorAt time.Time

	// samples 是延迟样本的环形缓冲（长度 = 容量，满则覆盖最旧的）。
	samples []time.Duration
	next    int
	count   int
}

// Ledger 是扩展点台账（并发安全、内存有界）。
type Ledger struct {
	mu      sync.Mutex
	entries map[string]*entryState
	cap     int
}

// New 造一个台账（默认样本容量）。
func New() *Ledger {
	return &Ledger{entries: map[string]*entryState{}, cap: DefaultSampleCapacity}
}

// NewWithCapacity 造一个台账并指定样本容量（≤0 ⇒ 默认）。
func NewWithCapacity(capacity int) *Ledger {
	if capacity <= 0 {
		capacity = DefaultSampleCapacity
	}
	return &Ledger{entries: map[string]*entryState{}, cap: capacity}
}

// defaultLedger 是进程级默认台账（观测面用；像 prometheus 那样是全局的）。
var defaultLedger = New()

// Default 返回进程级默认台账。
func Default() *Ledger { return defaultLedger }

// Record 记录一次扩展点调用。
//
// ★ 语义：
//
//	① `calls` **总是**加一（所有结局都算「调用过」）；
//	② `OutcomeFailed` ⇒ `failures++`；`OutcomeTimeout` ⇒ `timeouts++`；
//	   `OutcomeDropped` ⇒ `dropped++`；`OutcomeRejected` ⇒ `rejected++`；
//	③ `err != nil` ⇒ 记下**最近错误**（文本 + 时间）；
//	④ 延迟样本进环形缓冲（满了覆盖最旧的 ⇒ 内存有界）。
//
// ★ 时间戳用 `time.Now()` ⇒ 单测里可注入时钟的诉求由 `RecordAt` 满足。
func (l *Ledger) Record(mechanismID string, outcome Outcome, latency time.Duration, err error) {
	l.recordAt(mechanismID, outcome, latency, err, time.Now())
}

// RecordAt 与 `Record` 相同，但显式给出时间（供单测与回放使用）。
func (l *Ledger) RecordAt(mechanismID string, outcome Outcome, latency time.Duration, err error, at time.Time) {
	l.recordAt(mechanismID, outcome, latency, err, at)
}

func (l *Ledger) recordAt(mechanismID string, outcome Outcome, latency time.Duration, err error, at time.Time) {
	if l == nil || mechanismID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[mechanismID]
	if !ok {
		e = &entryState{samples: make([]time.Duration, l.cap)}
		l.entries[mechanismID] = e
	}
	e.calls++
	switch outcome {
	case OutcomeFailed:
		e.failures++
	case OutcomeTimeout:
		e.timeouts++
	case OutcomeDropped:
		e.dropped++
	case OutcomeRejected:
		e.rejected++
	}
	if err != nil {
		e.lastError = err.Error()
		e.lastErrorAt = at
	}
	// 环形缓冲：写满后覆盖最旧的那个。
	if len(e.samples) == 0 {
		return
	}
	e.samples[e.next] = latency
	e.next = (e.next + 1) % len(e.samples)
	if e.count < len(e.samples) {
		e.count++
	}
}

// Snapshot 返回台账快照（按机制 ID 排序）。
//
// ★ 没被记录过的机制**不在**快照里（调用次数 0 与「不存在」是两件事）。
func (l *Ledger) Snapshot() []Entry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, 0, len(l.entries))
	for id, e := range l.entries {
		out = append(out, e.entry(id, l.cap))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MechanismID < out[j].MechanismID })
	return out
}

// Entry 返回某个机制的台账行（第二个返回值报告它是否被记录过）。
func (l *Ledger) Entry(mechanismID string) (Entry, bool) {
	if l == nil {
		return Entry{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[mechanismID]
	if !ok {
		return Entry{}, false
	}
	return e.entry(mechanismID, l.cap), true
}

// entry 把内部状态折成一行（调用方持锁）。
func (e *entryState) entry(id string, capacity int) Entry {
	out := Entry{
		MechanismID: id,
		Calls:       e.calls,
		Failures:    e.failures,
		Timeouts:    e.timeouts,
		Dropped:     e.dropped,
		Rejected:    e.rejected,
		P99Window:   capacity,
		LastError:   e.lastError,
		LastErrorAt: e.lastErrorAt,
	}
	if e.count > 0 {
		buf := make([]time.Duration, e.count)
		copy(buf, e.samples[:e.count])
		sort.Slice(buf, func(i, j int) bool { return buf[i] < buf[j] })
		// p99：取第 ceil(0.99*n)-1 个（0 下标）。
		idx := (99*e.count + 99) / 100
		if idx < 1 {
			idx = 1
		}
		if idx > e.count {
			idx = e.count
		}
		out.P99Millis = uint64(buf[idx-1].Milliseconds())
	}
	return out
}

// ErrUnknownMechanism 表示机制 ID 不在允许集合里。
var ErrUnknownMechanism = errors.New("extpointledger: 未知的机制 ID")

// KnownMechanism 报告 mechanismID 是否在允许集合里。
//
// ★ 允许集合由调用方注入（通常是 `internal/extpoints` 的机制 ID 列表）
// ⇒ **台账只认裁决表里的机制** ⇒ 拼错的 ID 不会凭空长出一行台账。
func KnownMechanism(known map[string]bool, mechanismID string) bool {
	if mechanismID == "" {
		return false
	}
	if known == nil {
		return true // ★ 未注入允许集合时不拦截（由调用方选择严格度）。
	}
	return known[mechanismID]
}
