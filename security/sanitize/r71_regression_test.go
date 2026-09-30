package sanitize

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// R71 回归：重叠敏感片段的未覆盖尾部曾以明文发往上游。
//
// detector_test.go 早先把这条写成 "sanitizer position-dedup handles it"，
// 但位置去重只在「后一片段完全落在前一片段内」时才成立。当检测器给出的
// 是「前一片段较短、后一片段更长」时，超出部分是静默留在 SanitizedText
// 里的——即上游 LLM 和日志能看到真实数字。
func TestSanitizeInput_OverlappingFragmentsLeaveNoPlaintext(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	tests := []struct {
		name string
		text string
		// 至少应被占位符覆盖的连续明文片段（一旦出现在脱敏结果里即为泄漏）
		mustNotAppear []string
	}{
		{
			// id_card[0,18) 与 phone[0,11) 同起点；phone 在前，尾部 7 位曾泄漏
			name:          "id card whose prefix is a phone number",
			text:          "138001380001234567",
			mustNotAppear: []string{"1234567", "138001380001234567"},
		},
		{
			name:          "id card with Chinese prefix",
			text:          "身份证号11010119900307123X",
			mustNotAppear: []string{"19900307123"},
		},
		{
			name:          "id card followed by more digits",
			text:          "11010119900307123X999",
			mustNotAppear: []string{"19900307123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := s.SanitizeInput(context.Background(), tt.text)
			require.NoError(t, err)
			require.NotEmpty(t, res.SanitizeMap, "expected at least one placeholder")

			for _, leak := range tt.mustNotAppear {
				require.NotContains(t, res.SanitizedText, leak,
					"sensitive bytes leaked into the text sent upstream: %q in %q", leak, res.SanitizedText)
			}
			// 往返必须仍然精确：占位符对应原文的连续片段。
			restored, err := s.RestoreOutput(context.Background(), res.SanitizedText, res.SanitizeMap)
			require.NoError(t, err)
			require.Equal(t, tt.text, restored, "sanitize→restore must be lossless")
		})
	}
}

// 回归的第二半：占位符覆盖的字节必须真的被覆盖，而不是「大部分被覆盖」。
// 这里直接断言 Fragments 两两不重叠且并集等于原文里被检测到的区间。
func TestSanitizeInput_FragmentsAreDisjointAndInBounds(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	text := "联系人13800138000身份证号11010119900307123X邮箱a@b.com"
	res, err := s.SanitizeInput(context.Background(), text)
	require.NoError(t, err)
	require.NotEmpty(t, res.Fragments)

	prevEnd := 0
	for i, f := range res.Fragments {
		require.GreaterOrEqual(t, f.Start, prevEnd,
			"fragment %d overlaps the previous one: %+v", i, f)
		require.Greater(t, f.End, f.Start, "fragment %d is empty: %+v", i, f)
		require.LessOrEqual(t, f.End, len(text), "fragment %d out of bounds: %+v", i, f)
		require.Equal(t, text[f.Start:f.End], f.Value,
			"fragment %d value must match the original slice it claims to cover", i)
		prevEnd = f.End
	}
}

// Detector 是可插拔接口。第三方实现给出越界/负数/空片段时，
// 旧实现直接下标切片会 panic —— 把一次规则误配变成请求 500。
type hostileDetector struct{ frags []SensitiveFragment }

func (h hostileDetector) Detect(context.Context, string) ([]SensitiveFragment, error) {
	return h.frags, nil
}
func (h hostileDetector) Name() string { return "hostile" }

func TestSanitizeInput_OutOfRangeFragmentsDoNotPanic(t *testing.T) {
	tests := []struct {
		name  string
		frags []SensitiveFragment
	}{
		{"end past the text", []SensitiveFragment{{Type: TypePhone, Start: 2, End: 9999}}},
		{"start past the text", []SensitiveFragment{{Type: TypePhone, Start: 500, End: 600}}},
		{"negative start", []SensitiveFragment{{Type: TypePhone, Start: -5, End: 3}}},
		{"both negative", []SensitiveFragment{{Type: TypePhone, Start: -9, End: -2}}},
		{"inverted range", []SensitiveFragment{{Type: TypePhone, Start: 6, End: 2}}},
		{"empty range", []SensitiveFragment{{Type: TypePhone, Start: 4, End: 4}}},
		{"unsorted", []SensitiveFragment{
			{Type: TypeEmail, Start: 10, End: 14},
			{Type: TypePhone, Start: 1, End: 5},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewSanitizer(hostileDetector{frags: tt.frags})
			require.NoError(t, err)
			require.NotPanics(t, func() {
				res, err := s.SanitizeInput(context.Background(), "0123456789abcdefghij")
				require.NoError(t, err)
				for _, f := range res.Fragments {
					require.GreaterOrEqual(t, f.Start, 0)
					require.LessOrEqual(t, f.End, 20)
				}
			})
		})
	}
}

// R71 回归：中间件过去用一把进程级互斥锁把「读取 offset → 正则扫描 →
// Redis 提交」整段串行化，把网关吞吐压到 1/检测耗时。锁必须按会话键控。
func TestSanitizeSessionLocks_SameKeySerializes(t *testing.T) {
	var locks sanitizeSessionLocks
	var inside int32
	var maxInside int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := locks.lock("k")
			n := atomic.AddInt32(&inside, 1)
			for {
				old := atomic.LoadInt32(&maxInside)
				if n <= old || atomic.CompareAndSwapInt32(&maxInside, old, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&inside, -1)
			release()
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), maxInside, "same key must never run two holders at once")
}

func TestSanitizeSessionLocks_DistinctKeysRunConcurrently(t *testing.T) {
	var locks sanitizeSessionLocks
	var inside int32
	var maxInside int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release := locks.lock("k" + string(rune('a'+i)))
			n := atomic.AddInt32(&inside, 1)
			for {
				old := atomic.LoadInt32(&maxInside)
				if n <= old || atomic.CompareAndSwapInt32(&maxInside, old, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&inside, -1)
			release()
		}(i)
	}
	wg.Wait()
	require.Greater(t, maxInside, int32(1),
		"distinct sessions must not serialize against each other (the old process-wide mutex did)")
}

// 键控锁的引用计数必须回收条目，否则「每个会话一把锁」会变成内存泄漏。
func TestSanitizeSessionLocks_ReleasesEntries(t *testing.T) {
	var locks sanitizeSessionLocks
	for i := 0; i < 500; i++ {
		locks.lock("k" + strconv.Itoa(i))()
	}
	locks.mu.Lock()
	n := len(locks.locks)
	locks.mu.Unlock()
	require.Zero(t, n, "session lock table must not retain released keys")
}

// 端到端：带 Redis 与会话 ID 的请求仍必须按会话串行（这是正确性，不是性能）。
// 走真 Lua 提交路径，验证改造没有削弱会话内的占位符唯一性。
func TestSanitizeInputMiddleware_SameSessionStaysUniqueUnderConcurrency(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	require.NoError(t, err)

	const workers = 6
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := `{"model":"m","messages":[{"role":"user","content":"call 1380013800` +
				string(rune('0'+i)) + `"}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("X-Gw-Session-Id", "sess-shared")
			req = req.WithContext(WithAuthenticatedTenant(req.Context(), "tenant-a"))
			rec := httptest.NewRecorder()
			mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(rec, req)
			// 租约争用会 fail-closed 成 503；那是设计取舍，不算失败。
			if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
				t.Errorf("unexpected status %d: %s", rec.Code, rec.Body.String())
			}
		}(i)
	}
	wg.Wait()

	// 存活下来的映射里，占位符不得复用成不同的值。
	fields, err := rdb.HKeys(context.Background(), sanitizeMapKey("tenant-a", "sess-shared")).Result()
	require.NoError(t, err)
	seen := map[string]string{}
	for _, field := range fields {
		val, hErr := rdb.HGet(context.Background(), sanitizeMapKey("tenant-a", "sess-shared"), field).Result()
		require.NoError(t, hErr)
		if prev, ok := seen[field]; ok {
			require.Equal(t, prev, val, "placeholder %s was reused for a different value", field)
		}
		seen[field] = val
	}
}

// concurrencyProbeDetector records how many Detect calls are in flight at once.
// The measurement point is the detector — i.e. INSIDE the region the middleware
// locks — so the assertion is about the middleware's locking decision, not
// about the keyed-mutex primitive in isolation.
type concurrencyProbeDetector struct {
	inner   Detector
	inside  int32
	maxSeen int32
	hold    time.Duration
}

func (p *concurrencyProbeDetector) Detect(ctx context.Context, text string) ([]SensitiveFragment, error) {
	n := atomic.AddInt32(&p.inside, 1)
	for {
		old := atomic.LoadInt32(&p.maxSeen)
		if n <= old || atomic.CompareAndSwapInt32(&p.maxSeen, old, n) {
			break
		}
	}
	time.Sleep(p.hold)
	frags, err := p.inner.Detect(ctx, text)
	atomic.AddInt32(&p.inside, -1)
	return frags, err
}

func (p *concurrencyProbeDetector) Name() string { return "probe" }

func runProbeRequests(t *testing.T, mw *SanitizeInputMiddleware, sessions []string) {
	t.Helper()
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	body := `{"model":"m","messages":[{"role":"user","content":"call 13800138000"}]}`
	var wg sync.WaitGroup
	for _, session := range sessions {
		wg.Add(1)
		go func(session string) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("X-Gw-Session-Id", session)
			req = req.WithContext(WithAuthenticatedTenant(req.Context(), "tenant-a"))
			mw2 := httptest.NewRecorder()
			handler.ServeHTTP(mw2, req)
			if mw2.Code != http.StatusOK && mw2.Code != http.StatusServiceUnavailable {
				t.Errorf("session %s: unexpected status %d: %s", session, mw2.Code, mw2.Body.String())
			}
		}(session)
	}
	wg.Wait()
}

// 端到端反序列化：带 Redis、带会话 ID 的不同会话必须能同时进入检测阶段。
// 曾经的进程级互斥锁把这里压成 1，本测试就是它的守门人。
func TestSanitizeInputMiddleware_DistinctSessionsAreNotSerialized(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	probe := &concurrencyProbeDetector{inner: NewPatternDetector(), hold: 40 * time.Millisecond}
	s, err := NewSanitizer(probe)
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	require.NoError(t, err)

	sessions := make([]string, 6)
	for i := range sessions {
		sessions[i] = "sess-" + strconv.Itoa(i)
	}
	runProbeRequests(t, mw, sessions)

	require.Greater(t, probe.maxSeen, int32(1),
		"distinct sessions were serialized against each other: max concurrent Detect = %d", probe.maxSeen)
}

// 反向断言：同一会话仍然必须串行，否则并发轮次会分配到重复的占位符编号。
func TestSanitizeInputMiddleware_SameSessionIsSerialized(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	probe := &concurrencyProbeDetector{inner: NewPatternDetector(), hold: 40 * time.Millisecond}
	s, err := NewSanitizer(probe)
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	require.NoError(t, err)

	runProbeRequests(t, mw, []string{"same", "same", "same", "same"})

	require.Equal(t, int32(1), probe.maxSeen,
		"same session must not run two sanitizes at once (placeholder index reuse)")
}

// R71 回归：合法 JSON 但不属于 messages / responses / chat-choices 任一信封
// 的响应体，过去会从所有守卫之间漏过去，最终原样透传内部标记
// （{SENSITIVE:phone:1}）给客户端，且已映射的真实值也不还原。
func TestInterceptNonStream_UnrecognizedEnvelopeNeverLeaksMarker(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)

	seedMap(t, rdb, "tenant-a", "sess-unknown", map[string]string{
		"{SENSITIVE:phone:1}": "13800138000",
	})

	bodies := map[string]string{
		"vendor error envelope": `{"type":"error","error":{"message":"upstream rejected {SENSITIVE:phone:1}","code":"400"}}`,
		"choices not an array":  `{"object":"chat.completion","choices":{"message":{"content":"hi {SENSITIVE:phone:1}"}}}`,
		"gemini candidates":     `{"candidates":[{"content":{"parts":[{"text":"call {SENSITIVE:phone:1}"}]}}]}`,
		"bare output_text":      `{"output_text":"call {SENSITIVE:phone:1}","id":"resp_x"}`,
		"forged marker":         `{"output_text":"call {SENSITIVE:phone:99}","id":"resp_x"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			res, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
				ResponseBody: []byte(body),
				SessionID:    "sess-unknown",
				TenantID:     "tenant-a",
			})
			require.NoError(t, err)
			require.NotNil(t, res, "unrecognized envelope must not pass through untouched")
			out := string(res.ModifiedBody)
			require.NotContains(t, out, "{SENSITIVE:",
				"internal placeholder token reached the client: %s", out)
			require.True(t, json.Valid(res.ModifiedBody), "masked body must stay valid JSON: %s", out)
			if name != "forged marker" {
				require.Contains(t, out, "13800138000", "known placeholder should be restored: %s", out)
			} else {
				require.Contains(t, out, "[REDACTED]", "unknown placeholder must be redacted: %s", out)
			}
		})
	}
}

// 没有会话 ID 时非流式过去直接透传，而流式是 fail-closed 的——两翼不一致。
func TestInterceptNonStream_NoSessionDoesNotLeakMarker(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, nil, time.Minute)
	require.NoError(t, err)

	res, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		ResponseBody: []byte(`{"choices":[{"message":{"content":"call {SENSITIVE:phone:1}"}}]}`),
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotContains(t, string(res.ModifiedBody), "{SENSITIVE:")
}

// 注释声称 2^53 以上的 token 数可以精确往返，但该声称只由 native 分支兑现；
// legacy chat 路径曾用普通 Unmarshal，把大整数改写成浮点形式。
func TestInterceptNonStream_LegacyChatPreservesBigIntegers(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	seedMap(t, rdb, "tenant-a", "sess-big", map[string]string{"{SENSITIVE:phone:1}": "13800138000"})

	const big = "123456789012345678901234567890"
	body := `{"choices":[{"message":{"content":"call {SENSITIVE:phone:1}"}}],` +
		`"usage":{"prompt_tokens":9007199254740993,"reference":` + big + `}}`
	res, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		ResponseBody: []byte(body), SessionID: "sess-big", TenantID: "tenant-a",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	out := string(res.ModifiedBody)
	require.Contains(t, out, "9007199254740993", "prompt_tokens lost precision: %s", out)
	require.Contains(t, out, big, "large integer was re-rendered in float form: %s", out)
	require.NotContains(t, out, "e+29")
}

func seedMap(t *testing.T, rdb *redis.Client, tenant, session string, m map[string]string) {
	t.Helper()
	key := sanitizeMapKey(tenant, session)
	fields := make([]string, 0, len(m)*2)
	for k, v := range m {
		fields = append(fields, k, v)
	}
	require.NoError(t, rdb.HSet(context.Background(), key, fields).Err())
}
