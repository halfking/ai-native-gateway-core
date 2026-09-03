// Package v2: RawCacheV2 是 L0 原始缓存（与 session_turns/session_bodies 写入同步）
// 仅存储本轮 delta；不做 LCS、不存完整 outbound body。
//
// KEEP: L0 原始缓存预留 — 当前无 package 外消费者，仅在写入 pipeline
// 「写前快速读取」加速链路就绪后接入（docs/design/2026-08-09 三层缓存）。
// 勿按死代码清理。 @acc review 2026-11-09
package v2

import (
	"container/list"
	"context"
	"encoding/json"
	"sync"
	"time"
)

// RawEntry 表示 L0 缓存中一个会话在本轮的原始增量数据。
// 注意：仅存储当前轮的 request/response delta，绝不存储完整 outbound body；
// 持久化失败重放由 L1 (CompressionMetaCache) 接管。
type RawEntry struct {
	TurnNo        int
	RequestDelta  []Message
	ResponseDelta []Message
	SubmitMode    string
	Attachments   []AttachmentRef
	UpdatedAt     time.Time
}

type rawEntry struct {
	key   string
	entry *RawEntry
}

func cloneRawEntry(entry *RawEntry) *RawEntry {
	if entry == nil {
		return nil
	}
	clone := *entry
	clone.RequestDelta = cloneMessages(entry.RequestDelta)
	clone.ResponseDelta = cloneMessages(entry.ResponseDelta)
	clone.Attachments = append([]AttachmentRef(nil), entry.Attachments...)
	for i := range clone.RequestDelta {
		clone.RequestDelta[i].ToolCalls = cloneRawMapSlice(entry.RequestDelta[i].ToolCalls)
		clone.RequestDelta[i].ContentRaw = append(json.RawMessage(nil), entry.RequestDelta[i].ContentRaw...)
		clone.RequestDelta[i].RawContent = cloneRawValue(entry.RequestDelta[i].RawContent)
	}
	for i := range clone.ResponseDelta {
		clone.ResponseDelta[i].ToolCalls = cloneRawMapSlice(entry.ResponseDelta[i].ToolCalls)
		clone.ResponseDelta[i].ContentRaw = append(json.RawMessage(nil), entry.ResponseDelta[i].ContentRaw...)
		clone.ResponseDelta[i].RawContent = cloneRawValue(entry.ResponseDelta[i].RawContent)
	}
	return &clone
}

func cloneMessages(messages []Message) []Message {
	return append([]Message(nil), messages...)
}

func cloneRawValue(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var clone interface{}
	if err := json.Unmarshal(raw, &clone); err != nil {
		return nil
	}
	return clone
}

func cloneRawMapSlice(src []map[string]interface{}) []map[string]interface{} {
	if src == nil {
		return nil
	}
	out := make([]map[string]interface{}, len(src))
	for i, item := range src {
		raw, err := json.Marshal(item)
		if err != nil {
			continue
		}
		_ = json.Unmarshal(raw, &out[i])
	}
	return out
}

// RawCacheV2 是 L0 原始缓存：
//   - LRU，容量默认 1024（可由 NewRawCacheV2 自定义）
//   - 键为 "tenantID|sessionID"，跨租户隔离
//   - 线程安全（sync.Mutex）
//   - 用于在写入 pipeline 中做「写前快速读取」加速
type RawCacheV2 struct {
	capacity int
	mu       sync.Mutex
	ll       *list.List               // front = most recent
	index    map[string]*list.Element // "tenantID|sessionID" → ll element
}

// NewRawCacheV2 创建一个 L0 原始缓存。capacity <= 0 时使用默认值 1024。
func NewRawCacheV2(capacity int) *RawCacheV2 {
	if capacity <= 0 {
		capacity = 1024
	}
	return &RawCacheV2{
		capacity: capacity,
		ll:       list.New(),
		index:    make(map[string]*list.Element, capacity),
	}
}

// rawCacheKey 生成 L0 缓存键。
// 使用 "|" 分隔（与 cache_v2.go 中的 cacheKey 用 ":" 分隔区分），避免包级命名冲突。
func rawCacheKey(tenant, session string) string { return tenant + "|" + session }

// Put 将 (tenant, session) 对应的原始增量写入 L0 缓存；
// 若 key 已存在则覆盖 entry 并移动到 MRU 位置。
// 容量满时淘汰最久未访问的 entry（LRU）。
func (c *RawCacheV2) Put(_ context.Context, tenant, session string, e *RawEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := rawCacheKey(tenant, session)
	if el, ok := c.index[k]; ok {
		c.ll.MoveToFront(el)
		el.Value.(*rawEntry).entry = cloneRawEntry(e)
		return
	}
	el := c.ll.PushFront(&rawEntry{key: k, entry: cloneRawEntry(e)})
	c.index[k] = el
	if c.ll.Len() > c.capacity {
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.index, oldest.Value.(*rawEntry).key)
		}
	}
}

// Get 读取 (tenant, session) 对应的原始增量；命中时将其提升到 MRU 位置。
// miss 时返回 (nil, false)。
func (c *RawCacheV2) Get(_ context.Context, tenant, session string) (*RawEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[rawCacheKey(tenant, session)]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return cloneRawEntry(el.Value.(*rawEntry).entry), true
}

// Invalidate 从 L0 缓存中移除 (tenant, session) 对应条目。
// 用于会话显式重置 / 写入失败需要强制重读上游的场景。
func (c *RawCacheV2) Invalidate(_ context.Context, tenant, session string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := rawCacheKey(tenant, session)
	el, ok := c.index[k]
	if !ok {
		return
	}
	c.ll.Remove(el)
	delete(c.index, k)
}

// Close 释放缓存；当前实现无后台资源，返回 nil。
func (c *RawCacheV2) Close() error { return nil }
