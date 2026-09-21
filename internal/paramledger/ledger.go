// Package paramledger: 请求参数协商账本（2026-09-22）。
//
// 背景：网关在出站方向会把客户端参数适配到供应商边界内——
// paramguard 按模型能力就近降档（reasoning_effort x-high→high、
// temperature 上限截断）、别名归一（x-high→xhigh）、方言剔除
// （Grok reasoning 模型的 stop/penalty）；reqprobe 在上游 4xx 后剔除
// 参数或回退请求形态。这些调整让请求能完成，但客户端对"自己的参数
// 被改了"一无所知——Responses API 的响应对象会回显 reasoning.effort，
// 客户端看到的将是降级后的值。
//
// 本包做两件事：
//
//  1. Record：按 request_id 追加"参数调整"记录（Field/Original/Sent/
//     Action/Reason），去重合并，TTL 15 分钟（覆盖请求+响应生命周期，
//     足够流式收尾）。
//  2. Restore：响应回程时把被调整的字段还原为客户端原始值。首版覆盖
//     唯一的真实回显点——OpenAI Responses 的 reasoning.effort（非流式
//     响应体 + 流式 response.* 生命周期帧）。chat / anthropic 形态的
//     响应不回显这些请求参数，天然无需还原。
//
// 存储模式（与部署形态对齐）：
//   - 进程内存为主存（读写都在请求实例内完成，流式逐帧还原零网络开销）；
//   - Full 模式额外异步镜像到 Redis（llmgw:paramledger:{request_id}，
//     同 TTL），供跨进程审计/观测——读路径永不走 Redis，镜像失败静默。
//
// 安全红线：Record/Restore 均为非阻塞快速路径；绝不因账本问题让请求
// 失败（restore 出错时原样返回 data）。
package paramledger

import (
	"bytes"
	"sync"
	"time"
)

// Action 是参数调整的类别。
type Action string

const (
	// ActionClamp：值被就近降/升档（x-high→high）。
	ActionClamp Action = "clamp"
	// ActionNormalize：值被别名归一（x-high→xhigh，语义不变）。
	ActionNormalize Action = "normalize"
	// ActionStrip：参数被整体剔除（Original 记参数名，Sent 为空）。
	ActionStrip Action = "strip"
	// ActionModeSwitch：请求形态被切换（responses→chat）。
	ActionModeSwitch Action = "mode_switch"
)

// Adjustment 是一条参数调整记录。
type Adjustment struct {
	// Field 是规范字段名。扁平写法 reasoning_effort；嵌套写法用点号
	// reasoning.effort。回显还原按"两种写法都匹配"处理。
	Field string `json:"field"`
	// Original 是客户端发送的原始值（strip 时为参数名占位）。
	Original string `json:"original"`
	// Sent 是实际出站的值（strip 时为空）。
	Sent   string `json:"sent"`
	Action Action `json:"action"`
	// Reason 是触发来源（paramguard clamp / reqprobe strip / …）。
	Reason string `json:"reason"`
}

// Entry 是一个请求的全部参数调整。
type Entry struct {
	RequestID   string       `json:"request_id"`
	Adjustments []Adjustment `json:"adjustments"`
}

// entryTTL 覆盖请求发出到响应写回的完整生命周期（含流式收尾）。
const entryTTL = 15 * time.Minute

// maxEntries 是进程内存上限（按 request_id 计，FIFO 淘汰）。
const maxEntries = 20000

// mirror 是可选的异步镜像函数（Full 模式下指向 Redis 写入）。
type mirrorFn func(requestID string, entry Entry)

// Ledger 是账本本体。零值可用（纯内存）；SetMirror 注入 Redis 镜像。
type Ledger struct {
	mu      sync.RWMutex
	entries map[string]*entryState
	order   *fifoList
	mirror  mirrorFn
}

type entryState struct {
	entry    Entry
	expireAt time.Time
}

// New 创建账本。mirror 非 nil 时每次 Record 后异步镜像（fire-and-forget）。
func New(mirror mirrorFn) *Ledger {
	return &Ledger{
		entries: map[string]*entryState{},
		order:   newFIFO(),
		mirror:  mirror,
	}
}

// Record 追加参数调整（按 Field+Action+Original+Sent 去重）。多次
// finalize / 多次 attempt 重复报告同一条调整不会膨胀。
func (l *Ledger) Record(requestID string, adjs ...Adjustment) {
	if l == nil || requestID == "" || len(adjs) == 0 {
		return
	}
	now := time.Now()
	l.mu.Lock()
	st, ok := l.entries[requestID]
	if !ok {
		st = &entryState{entry: Entry{RequestID: requestID}}
		l.entries[requestID] = st
		l.order.push(requestID)
		for l.order.len() > maxEntries {
			if evicted := l.order.pop(); evicted != "" {
				delete(l.entries, evicted)
			}
		}
	}
	changed := false
	for _, adj := range adjs {
		if adj.Field == "" {
			continue
		}
		dup := false
		for _, ex := range st.entry.Adjustments {
			if ex.Field == adj.Field && ex.Action == adj.Action &&
				ex.Original == adj.Original && ex.Sent == adj.Sent {
				dup = true
				break
			}
		}
		if !dup {
			st.entry.Adjustments = append(st.entry.Adjustments, adj)
			changed = true
		}
	}
	st.expireAt = now.Add(entryTTL)
	entry := st.entry
	mirror := l.mirror
	l.mu.Unlock()

	if changed && mirror != nil {
		go mirror(requestID, entry)
	}
}

// Lookup 返回请求的调整账目（过期/不存在返回 nil）。
func (l *Ledger) Lookup(requestID string) *Entry {
	if l == nil || requestID == "" {
		return nil
	}
	now := time.Now()
	l.mu.RLock()
	st, ok := l.entries[requestID]
	var out *Entry
	if ok && st.expireAt.After(now) {
		cp := st.entry
		out = &cp
	}
	l.mu.RUnlock()
	return out
}

// Len 返回活跃账目数（测试用）。
func (l *Ledger) Len() int {
	if l == nil {
		return 0
	}
	now := time.Now()
	l.mu.RLock()
	defer l.mu.RUnlock()
	n := 0
	for _, st := range l.entries {
		if st.expireAt.After(now) {
			n++
		}
	}
	return n
}

// ─── 回显还原 ───────────────────────────────────────────────────────────────

// RestoreResponsesEffort 把 Responses 形态数据（非流式响应体 JSON 或单帧
// SSE data JSON）里回显的 reasoning.effort 从出站值还原为客户端原始值。
// 仅当帧内的值与某条调整的 Sent 精确相等时才替换——避免误伤模型输出里
// 恰好同名的文本。无匹配调整或数据不含该字段时原样返回。
func (l *Ledger) RestoreResponsesEffort(data []byte, requestID string) []byte {
	entry := l.Lookup(requestID)
	if entry == nil || len(data) == 0 || !bytes.Contains(data, []byte(`"effort"`)) {
		return data
	}
	out := data
	for _, adj := range entry.Adjustments {
		if adj.Original == "" || adj.Sent == "" || adj.Sent == adj.Original {
			continue
		}
		switch adj.Field {
		case "reasoning_effort", "reasoning.effort", "reasoning":
			// Responses 回显形如 "reasoning":{"effort":"high"}。
			old := `"effort":"` + adj.Sent + `"`
			new := `"effort":"` + adj.Original + `"`
			if bytes.Contains(out, []byte(old)) {
				out = bytes.ReplaceAll(out, []byte(old), []byte(new))
			}
		}
	}
	return out
}

// HasAdjustments 便捷判断（日志用）。
func (e *Entry) HasAdjustments() bool {
	return e != nil && len(e.Adjustments) > 0
}

// ─── 极简 FIFO（淘汰顺序） ─────────────────────────────────────────────────

type fifoList struct {
	mu   sync.Mutex
	head *fifoNode
	tail *fifoNode
	n    int
}

type fifoNode struct {
	id   string
	next *fifoNode
}

func newFIFO() *fifoList { return &fifoList{} }

func (f *fifoList) push(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	node := &fifoNode{id: id}
	if f.tail == nil {
		f.head, f.tail = node, node
	} else {
		f.tail.next = node
		f.tail = node
	}
	f.n++
}

func (f *fifoList) pop() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.head == nil {
		return ""
	}
	id := f.head.id
	f.head = f.head.next
	if f.head == nil {
		f.tail = nil
	}
	f.n--
	return id
}

func (f *fifoList) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}
