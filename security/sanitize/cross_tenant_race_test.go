// Package sanitize - cross_tenant_race_test.go
//
// T11-P0: 跨租户隔离 race 测试。必须在 `go test -race` 下干净通过。
//
// 覆盖：
//   - 同一 sessionID 不同 tenant 并发请求：每个 tenant 只看到自己的占位符映射表；
//   - 同一 (tenant, session) N 个 goroutine 并发请求：所有 placeholder index 唯一且连续；
//   - allocateOffsets 原子性：并发 N 次分配，每类的 index 累加精确等于 N×M；
//   - Restore 拦截器跨租户不会读到对方的 map。
package sanitize

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// raceMiddleware 把 sanitize 中间件 + miniredis 拼起来，每个测试自带独立 Redis 实例。
type raceMiddleware struct {
	mw      *SanitizeInputMiddleware
	rdb     *redis.Client
	cleanup func()
}

func newRaceMiddleware(t *testing.T) *raceMiddleware {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*60*1000) // 30 min
	require.NoError(t, err)
	return &raceMiddleware{
		mw:  mw,
		rdb: rdb,
		cleanup: func() {
			_ = rdb.Close()
			mr.Close()
		},
	}
}

// fireRequest 构造一个 chat 请求并穿过中间件。
func (rm *raceMiddleware) fireRequest(t *testing.T, tenantID, sessionID, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("X-Gw-Session-Id", sessionID)
	}
	if tenantID != "" {
		req.Header.Set("X-Gw-Tenant-Id", tenantID)
	}
	var captured string
	handler := rm.mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		captured = buf.String()
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return captured
}

// TestSanitizeMiddleware_CrossTenantIsolation_Concurrent 验证两个 tenant 共享同一
// sessionID 并发请求时，每个 tenant 的占位符映射表互不可见。
//
// 失败模式（修复前）：两个 tenant 共用 session:{sid}:sanitize key，A 的 placeholder
// 出现在 B 的 map 里 → B 还原时拿到 A 的真实 PII，跨租户泄漏。
// 修复后：key 是 session:{tenantHash_A}:{sid}:sanitize vs session:{tenantHash_B}:{sid}:sanitize，
// 互不影响。
func TestSanitizeMiddleware_CrossTenantIsolation_Concurrent(t *testing.T) {
	rm := newRaceMiddleware(t)
	defer rm.cleanup()

	const sessionID = "shared-session"
	tenantA := "tenant-A"
	tenantB := "tenant-B"
	hashA := HashTenant(tenantA)
	hashB := HashTenant(tenantB)

	const goroutines = 16
	const rounds = 5

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"A %d-%d 13800138000"}]}`, idx, r)
				rm.fireRequest(t, tenantA, sessionID, body)
			}
		}(i)
		go func(idx int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"B %d-%d a@b.com"}]}`, idx, r)
				rm.fireRequest(t, tenantB, sessionID, body)
			}
		}(i)
	}
	wg.Wait()

	// === 断言 A 的 map 只含 A 的 PII ===
	mapA, err := rm.rdb.HGetAll(context.Background(), SanitizeRedisKey(hashA, sessionID)).Result()
	require.NoError(t, err)
	require.NotEmpty(t, mapA, "tenant A map should not be empty")
	for placeholder, plaintext := range mapA {
		require.Contains(t, placeholder, "{SENSITIVE:phone:", "tenant A only emits phone placeholders, got %s", placeholder)
		require.Contains(t, plaintext, "13800138000", "tenant A plaintext should be its own PII, got %q (placeholder=%s)", plaintext, placeholder)
	}

	// === 断言 B 的 map 只含 B 的 PII ===
	mapB, err := rm.rdb.HGetAll(context.Background(), SanitizeRedisKey(hashB, sessionID)).Result()
	require.NoError(t, err)
	require.NotEmpty(t, mapB, "tenant B map should not be empty")
	for placeholder, plaintext := range mapB {
		require.Contains(t, placeholder, "{SENSITIVE:email:", "tenant B only emits email placeholders, got %s", placeholder)
		require.Contains(t, plaintext, "a@b.com", "tenant B plaintext should be its own PII, got %q (placeholder=%s)", plaintext, placeholder)
	}

	// === 关键断言：A 和 B 的 map 大小必须各自反映自己 goroutine × rounds 的产出 ===
	require.Equal(t, goroutines*rounds, len(mapA), "A map size = goroutines*rounds")
	require.Equal(t, goroutines*rounds, len(mapB), "B map size = goroutines*rounds")
}

// TestSanitizeMiddleware_AllocateOffsets_NoIndexLeak 验证同一 (tenant, session) 100
// 个 goroutine 并发请求，每类的 placeholder index 都唯一、不重复、不漏号。
//
// 失败模式（修复前）：read-modify-write race 导致两个 goroutine 都读到 phone=5，
// 都生成 phone:6 占位符 → 同一 phone:6 出现两次，sanitizeMap 互相覆盖，
// LLM 看到的占位符与最终还原时的 map 不一致。
// 修复后：Lua/HINCRBY 单脚本原子分配，phone offset 从 0 累加到 N（= goroutines 次数）。
func TestSanitizeMiddleware_AllocateOffsets_NoIndexLeak(t *testing.T) {
	rm := newRaceMiddleware(t)
	defer rm.cleanup()

	const tenantID = "tenant-iso"
	const sessionID = "iso-sess"
	const goroutines = 50
	const rounds = 2

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				body := `{"model":"m","messages":[{"role":"user","content":"phone 13800138000"}]}`
				rm.fireRequest(t, tenantID, sessionID, body)
			}
		}()
	}
	wg.Wait()

	// 占位符总数 = goroutines × rounds
	wantTotal := goroutines * rounds
	mapVals, err := rm.rdb.HGetAll(context.Background(), SanitizeRedisKey(HashTenant(tenantID), sessionID)).Result()
	require.NoError(t, err)
	require.Equal(t, wantTotal, len(mapVals), "总占位数应等于并发请求数")

	// 收集所有 index，断言 1..wantTotal 各出现一次（顺序不保证）
	seen := make(map[int]bool, wantTotal)
	for ph := range mapVals {
		p, ok := ParsePlaceholder(ph)
		require.True(t, ok, "invalid placeholder %q", ph)
		require.Equal(t, TypePhone, p.Type, "should only contain phone type")
		require.False(t, seen[p.Index], "duplicate index %d (placeholder %q)", p.Index, ph)
		require.GreaterOrEqual(t, p.Index, 1, "index must be >= 1")
		require.LessOrEqual(t, p.Index, wantTotal, "index must be <= wantTotal")
		seen[p.Index] = true
	}
	for i := 1; i <= wantTotal; i++ {
		require.True(t, seen[i], "missing index %d", i)
	}

	// offset key 的累计 phone 数必须等于 wantTotal
	offsets, err := rm.rdb.HGetAll(context.Background(), SanitizeOffsetRedisKey(HashTenant(tenantID), sessionID)).Result()
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d", wantTotal), offsets["phone"], "phone offset 累加必须精确")
}

// TestSanitizeMiddleware_AllocateOffsets_MultiType 验证多类型并发：每个 goroutine
// 的请求同时产生 phone 和 email，Lua 必须为两类独立原子预占。
func TestSanitizeMiddleware_AllocateOffsets_MultiType(t *testing.T) {
	rm := newRaceMiddleware(t)
	defer rm.cleanup()

	const tenantID = "tenant-multi"
	const sessionID = "multi-sess"
	const goroutines = 30

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"model":"m","messages":[{"role":"user","content":"phone 13800138000 email a@b.com"}]}`
			rm.fireRequest(t, tenantID, sessionID, body)
		}()
	}
	wg.Wait()

	offsets, err := rm.rdb.HGetAll(context.Background(), SanitizeOffsetRedisKey(HashTenant(tenantID), sessionID)).Result()
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d", goroutines), offsets["phone"], "phone offset")
	require.Equal(t, fmt.Sprintf("%d", goroutines), offsets["email"], "email offset")
}

// TestSanitizeRestoreInterceptor_CrossTenant_DoesNotLeakMap 验证 restore 拦截器
// 在 tenantHash 不同时不会读到对方的 map。
//
// 失败模式（修复前）：loadMap 只用 sessionID 读 key，跨租户泄漏 map → tenant B
// 还原时拿到 tenant A 的真实 PII。
// 修复后：loadMap 用 tenantHash + sessionID 读 key。
func TestSanitizeRestoreInterceptor_CrossTenant_DoesNotLeakMap(t *testing.T) {
	rm := newRaceMiddleware(t)
	defer rm.cleanup()

	// 预先把 tenant A 的 map 写入 Redis
	const tenantA = "tenant-A"
	const tenantB = "tenant-B"
	const sessionID = "shared-sid"

	ctx := context.Background()
	require.NoError(t, rm.rdb.HSet(ctx, SanitizeRedisKey(HashTenant(tenantA), sessionID),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	// 用 tenant B 的 interceptor 试图还原 — 必须返回 nil（无 map）
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rm.rdb, 30*60*1000)
	require.NoError(t, err)

	respBody := []byte(`{"choices":[{"message":{"role":"assistant","content":"您的手机号{SENSITIVE:phone:1}"}}]}`)
	result, err := it.InterceptNonStream(ctx, &response.InterceptRequest{
		SessionID:    sessionID,
		TenantID:     tenantB, // ← 关键：tenant B 看不到 tenant A 的 map
		ResponseBody: respBody,
	})
	require.NoError(t, err)
	require.Nil(t, result, "tenant B restore must not see tenant A's map")
}

// TestSanitizeMiddleware_NilTenantHash_FallsBackToUnknown 并发验证：缺 tenant header
// 的请求都落到 _unknown sentinel 桶（共用一个 hash），但不同 (sid) 仍隔离。
func TestSanitizeMiddleware_NilTenantHash_FallsBackToUnknown(t *testing.T) {
	rm := newRaceMiddleware(t)
	defer rm.cleanup()

	const sessionID = "unknown-sid"
	const goroutines = 8

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"model":"m","messages":[{"role":"user","content":"phone 13800138000"}]}`
			// 不设 X-Gw-Tenant-Id → 落到 sentinel
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Gw-Session-Id", sessionID)
			handler := rm.mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()

	sentinelHash := HashTenant("_unknown")
	mapVals, err := rm.rdb.HGetAll(context.Background(), SanitizeRedisKey(sentinelHash, sessionID)).Result()
	require.NoError(t, err)
	require.Equal(t, goroutines, len(mapVals), "无 tenant 请求都进 sentinel bucket")
}
