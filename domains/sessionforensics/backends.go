package sessionforensics

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// 这个文件提供 sessionforensics 自带的 in-memory backend 实现。
// 它们是 compression.SessionCacheBackend 和 compression.SessionCacheDB 的
// 测试用实现，与 tests/session_replay/ 中的同名 mock 完全等价 — 在这里
// 重新实现是为了让 sessionforensics 包不依赖测试目录。
//
// 真实部署应该用：
//   - L2: *invertedcache.RedisBackend（composed by SessionCache.Set caller）
//   - L3: *sql.DB 由 SessionCache ctor 包装

// InMemoryL2Backend 是一个极简的 Redis Hash 模拟。
type InMemoryL2Backend struct {
	mu     sync.Mutex
	data   map[string]map[string]string
	expiry map[string]time.Time
}

func newTestL2Backend() *InMemoryL2Backend {
	return &InMemoryL2Backend{
		data:   make(map[string]map[string]string),
		expiry: make(map[string]time.Time),
	}
}

func (m *InMemoryL2Backend) HSet(_ context.Context, key string, values ...any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[key] == nil {
		m.data[key] = make(map[string]string)
	}
	for i := 0; i+1 < len(values); i += 2 {
		k := toString(values[i])
		v := toString(values[i+1])
		m.data[key][k] = v
	}
	m.expiry[key] = time.Now().Add(30 * time.Minute)
	return nil
}

func (m *InMemoryL2Backend) HGetAll(_ context.Context, key string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if exp, ok := m.expiry[key]; ok && time.Now().After(exp) {
		delete(m.data, key)
		delete(m.expiry, key)
	}
	if _, ok := m.data[key]; !ok {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(m.data[key]))
	for k, v := range m.data[key] {
		out[k] = v
	}
	return out, nil
}
func (m *InMemoryL2Backend) Expire(_ context.Context, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expiry[key] = time.Now().Add(ttl)
	return nil
}
func (m *InMemoryL2Backend) Del(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	delete(m.expiry, key)
	return nil
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case int:
		b, _ := json.Marshal(x)
		return string(b)
	case int64:
		b, _ := json.Marshal(x)
		return string(b)
	case float64:
		b, _ := json.Marshal(x)
		return string(b)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// InMemoryL3DB 模拟 SessionCacheDB（实现 LastOutboundForSession）。
//
// 不暴露 *compression.LastOutboundRow 之外的字段（用户插入预热 row 时才会命中）。
type InMemoryL3DB struct {
	mu    sync.Mutex
	rows  map[string]*compression.LastOutboundRow
	Calls int
	Err   error
}

func newTestL3DB() *InMemoryL3DB {
	return &InMemoryL3DB{rows: make(map[string]*compression.LastOutboundRow)}
}

func (db *InMemoryL3DB) PutRow(tenantID, sessionID string, row *compression.LastOutboundRow) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.rows[tenantID+":"+sessionID] = row
}

func (db *InMemoryL3DB) LastOutboundForSession(_ context.Context, tenantID, sessionID string) (*compression.LastOutboundRow, error) {
	db.Calls++
	if db.Err != nil {
		return nil, db.Err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	r, ok := db.rows[tenantID+":"+sessionID]
	if !ok {
		return nil, nil
	}
	return r, nil
}
