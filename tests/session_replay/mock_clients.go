// Package session_replay - mock_clients.go
//
// 为回放生产请求构造可控的依赖注入。
//
// 类型说明：
//
//   - MockSessionCacheBackend       — 满足 SessionCacheBackend 接口，仿 Redis Hash 接口。
//     用于在没有真实 Redis 的情况下注入 SessionCache 的 L2 tier。
//   - MockSessionCacheDB            — 满足 SessionCacheDB 接口，读写 L3 fallback。
//   - MockMemoraClient              — 满足 MemoraClient 接口的轻量 stub，缺省返空。
//   - MockProviderClient            — 满足 ProviderClient 接口，可配置返回候选列表。
//   - ReplayLLMResponder            — 接收完整 outbound body 作为输入，
//     按真实生产中会话的"下一轮"内容回放上游响应。这里不依赖真实 LLM，而是回放
//     导出文件中已记录的 response_body（包含 commit/content/tool_calls 等）。
//
// 所有的 mock 都设计为确定性（无随机、即时响应、无外部副作用）以便压缩/缓存
// 逻辑可以稳定地回归测试。
package session_replay

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/memory"
)

// ── SessionCache L2 backend ────────────────────────────────────────────────────

// MockSessionCacheBackend 一个可重置的 Redis Hash 模拟实现。
type MockSessionCacheBackend struct {
	mu     sync.Mutex
	data   map[string]map[string]string // hashKey → {field: value}
	expiry map[string]time.Time         // hashKey → 过期时间
	// LatencyMs simulates network RTT for GetOrLoad L2 path.
	LatencyMs int
}

func NewMockSessionCacheBackend() *MockSessionCacheBackend {
	return &MockSessionCacheBackend{
		data:      make(map[string]map[string]string),
		expiry:    make(map[string]time.Time),
		LatencyMs: 0,
	}
}

func (m *MockSessionCacheBackend) HSet(ctx context.Context, key string, values ...any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[key] == nil {
		m.data[key] = make(map[string]string)
	}
	for i := 0; i < len(values); i += 2 {
		k := toString(values[i])
		v := toString(values[i+1])
		m.data[key][k] = v
	}
	m.expiry[key] = time.Now().Add(30 * time.Minute)
	return nil
}

func (m *MockSessionCacheBackend) HGetAll(ctx context.Context, key string) (map[string]string, error) {
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

func (m *MockSessionCacheBackend) Expire(ctx context.Context, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expiry[key] = time.Now().Add(ttl)
	return nil
}

func (m *MockSessionCacheBackend) Del(ctx context.Context, key string) error {
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

// ── SessionCache L3 DB ─────────────────────────────────────────────────────────

// MockSessionCacheDB 直接实现 compression.SessionCacheDB。
type MockSessionCacheDB struct {
	mu   sync.Mutex
	rows map[string]*compression.LastOutboundRow // key = "tenantID:gwSessionID"
	// 返回 ErrDBDown 让 getOrLoad 走 miss 路径（用于测试 L2/L3 失效降级）。
	ErrDBDown error
	// Calls 记录被调用的次数（用于断言 L3 已读）
	Calls int
}

func NewMockSessionCacheDB() *MockSessionCacheDB {
	return &MockSessionCacheDB{
		rows: make(map[string]*compression.LastOutboundRow),
	}
}

func (db *MockSessionCacheDB) PutRow(tenantID, sessionID string, row *compression.LastOutboundRow) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.rows[tenantID+":"+sessionID] = row
}

func (db *MockSessionCacheDB) LastOutboundForSession(_ context.Context, tenantID, sessionID string) (*compression.LastOutboundRow, error) {
	db.Calls++
	if db.ErrDBDown != nil {
		return nil, db.ErrDBDown
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	r, ok := db.rows[tenantID+":"+sessionID]
	if !ok {
		return nil, nil
	}
	return r, nil
}

// ── Memora / Provider (LLM summary deps) ───────────────────────────────────────

// MockMemoraClient 提供可控的 SmartSearch 结果（默认返空，配合 compressor 测试）。
type MockMemoraClient struct {
	SearchResults []memory.Memory
	SearchErr     error
	CallCount     int
}

func (m *MockMemoraClient) SmartSearch(_ context.Context, _, _ string, _ int) ([]memory.Memory, error) {
	m.CallCount++
	if m.SearchErr != nil {
		return nil, m.SearchErr
	}
	return m.SearchResults, nil
}

// MockProviderClient 可控制候选模型列表（默认空 → tryLLMSummary 跳过）。
type MockProviderClient struct {
	Candidates []compression.ProviderCandidate
	EnabledVal bool
	GetErr     error
}

func (m *MockProviderClient) Enabled() bool { return m.EnabledVal }

func (m *MockProviderClient) GetCandidates(_ context.Context, _, _ string) ([]compression.ProviderCandidate, error) {
	if m.GetErr != nil {
		return nil, m.GetErr
	}
	out := make([]compression.ProviderCandidate, 0, len(m.Candidates))
	for _, c := range m.Candidates {
		if c.Available {
			out = append(out, c)
		}
	}
	return out, nil
}

// ── ReplayLLMResponder ─────────────────────────────────────────────────────────

// ReplayLLMResponder 根据请求体（messages 数量 + turn 序号）回放已存的
// response_body。专为离线 replay 设计 — 不调真实 LLM。
type ReplayLLMResponder struct {
	mu      sync.Mutex
	turnIdx int
	Next    func(turnIdx int, requestBody []byte) (responseBody []byte, ok bool)
}

func NewReplayLLMResponder() *ReplayLLMResponder {
	return &ReplayLLMResponder{}
}

// SequentialReplay 按 turn 列表轮询顺序返回 response_body
func NewSequentialReplay(turns []SessionTurn) *ReplayLLMResponder {
	r := &ReplayLLMResponder{}
	r.Next = func(idx int, _ []byte) ([]byte, bool) {
		if idx >= len(turns) {
			return nil, false
		}
		t := turns[idx]
		if len(t.ResponseBody) == 0 {
			return nil, false
		}
		return t.ResponseBody, true
	}
	return r
}

func (r *ReplayLLMResponder) Respond(reqBody []byte) ([]byte, error) {
	r.mu.Lock()
	idx := r.turnIdx
	r.turnIdx++
	r.mu.Unlock()
	body, ok := r.Next(idx, reqBody)
	if !ok {
		return nil, ErrNoReplayResponse
	}
	return body, nil
}

// ErrNoReplayResponse 当 responder.Next 返回 false 时使用
var ErrNoReplayResponse = errors.New("replay responder: no response configured")
