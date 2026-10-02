// responses_durable_verdict_test.go — 持久结论解析（2026-10-02）。
//
// 判据钉在两处，两处都不是源码形状：
//
//  1. resolveDurableResponsesVerdict 的**契约**：读错误 ⇒ 回落 SQL 持久结论，
//     不是回落默认值。
//  2. 端到端：真实 miniredis 里放一个**解码失败**的 node-state 载荷（第三轮
//     审计里那个 bug 的形状），断言 executor 仍按 SQL 结论路由，且把这次降级
//     抬到 warn —— 它此前是 debug，而"静默失效"正是第三轮那次的全部症状。
package executors

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
)

func TestResolveDurableResponsesVerdict(t *testing.T) {
	cases := []struct {
		name         string
		read         durableVerdict
		sqlSupported bool
		sqlKnown     bool
		wantSupport  bool
		wantKnown    bool
		wantDegrade  bool
	}{
		{
			name:         "read error falls back to the SQL conclusion, not the default",
			read:         durableVerdict{ReadErr: errors.New("unmarshal node state: boom")},
			sqlSupported: true, sqlKnown: true,
			wantSupport: true, wantKnown: true, wantDegrade: true,
		},
		{
			name:         "read error with a negative SQL conclusion is still a conclusion",
			read:         durableVerdict{ReadErr: errors.New("redis down")},
			sqlSupported: false, sqlKnown: true,
			wantSupport: false, wantKnown: true, wantDegrade: true,
		},
		{
			name:         "read error with no SQL row is honestly unknown",
			read:         durableVerdict{ReadErr: errors.New("redis down")},
			sqlSupported: false, sqlKnown: false,
			wantSupport: false, wantKnown: false, wantDegrade: true,
		},
		{
			name:         "a known Redis verdict wins over SQL",
			read:         durableVerdict{Supported: true, Known: true},
			sqlSupported: false, sqlKnown: true,
			wantSupport: true, wantKnown: true, wantDegrade: false,
		},
		{
			name:         "a negative Redis verdict wins over a positive SQL row",
			read:         durableVerdict{Supported: false, Known: true},
			sqlSupported: true, sqlKnown: true,
			wantSupport: false, wantKnown: true, wantDegrade: false,
		},
		{
			name:         "no Redis verdict (evicted / TTL elapsed) falls back to SQL",
			read:         durableVerdict{Supported: false, Known: false},
			sqlSupported: true, sqlKnown: true,
			wantSupport: true, wantKnown: true, wantDegrade: false,
		},
		{
			name:         "nothing anywhere is unknown, not false",
			read:         durableVerdict{},
			sqlSupported: false, sqlKnown: false,
			wantSupport: false, wantKnown: false, wantDegrade: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			supported, known, degraded := resolveDurableResponsesVerdict(
				tc.read, tc.sqlSupported, tc.sqlKnown)
			if supported != tc.wantSupport || known != tc.wantKnown || degraded != tc.wantDegrade {
				t.Fatalf("resolveDurableResponsesVerdict() = (%v,%v,%v), want (%v,%v,%v)",
					supported, known, degraded, tc.wantSupport, tc.wantKnown, tc.wantDegrade)
			}
		})
	}
}

// newDegradedReadExecutor 造一个「Redis 读一定失败」的执行器：往 node-state
// 键里塞一个解码不过去的载荷。这不是人为构造的怪形状——第三轮审计里
// vapEUR 生产的 slide_window 就是一个解码不过去的载荷，那一次能力位
// 两个方向同时静默失效整整一轮。
func newDegradedReadExecutor(t *testing.T) *Executor {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// slide_window 是字符串而不是数组：UnmarshalJSON 的 {} → [] 归一只覆盖
	// 空对象形状，这里是真正畸形，必须读出错。
	if err := client.Set(t.Context(), "llmgw:cred_fp_node:126:gpt-5.6-terra",
		`{"credential_id":126,"model":"gpt-5.6-terra","slide_window":"not-an-array"}`, 0).Err(); err != nil {
		t.Fatalf("seed corrupt node state: %v", err)
	}

	mgr := credentialfpslot.New(credentialfpslot.Config{Enabled: true, DefaultLimit: 5}, client)
	e := newOverloadTestExecutor()
	e.FpSlots = mgr
	return e
}

// 端到端：Redis 读失败时，路由仍按 SQL 持久结论走，并记录一次 warn 级降级。
//
// 变异验证：
//   - 把 warn 改回 debug        → 本用例红（降级信号消失）；
//   - 把回落 SQL 改成回落默认值 → 本用例在 sql_known=true 的分支上红。
func TestExecuteOpenAI_DegradedCapabilityReadUsesSQLConclusion(t *testing.T) {
	rec := &legRecorder{}
	upstream := rec.server(t)
	e := newDegradedReadExecutor(t)

	// 先自检判据：确认 Redis 读确实报错，否则下面全部是空转。
	if _, _, err := e.FpSlots.GetSupportsResponses(t.Context(), 126, "gpt-5.6-terra", nil); err == nil {
		t.Fatal("判据自检失败：预置的畸形载荷没有让 GetSupportsResponses 报错，本用例测不到降级路径")
	}

	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cand := bridgeCandidate(upstream.URL, "gpt-5.6-terra")
	// SQL 持久结论：这一行有回填记录且为正向。
	cand.SupportsNativeResponses = true
	cand.SupportsNativeResponsesKnown = true

	_, err := e.executeOpenAI(&ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		W:                  httptest.NewRecorder(),
		BodyBytes:          []byte(`{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hi"}]}`),
		ResponsesBodyBytes: []byte(`{"model":"gpt-5.6-terra","input":"hi"}`),
		ClientProtocol:     "openai-responses",
		ClientModel:        "gpt-5.6-terra",
		ClientID:           identity.ClientIdentity{IdentityHash: "cap-degraded"},
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}

	// SQL 结论为正向 ⇒ 仍走 responses 腿（读错误没有把它降级成 chat）。
	if got := rec.last(); !strings.HasSuffix(got, "/responses") {
		t.Fatalf("upstream leg = %q, want /v1/responses: the SQL durable conclusion must survive a Redis read error", got)
	}
	// 降级必须被人看见。此前读错误只写 debug，那正是静默失效的成因。
	logged := logBuf.String()
	if !strings.Contains(logged, "level=WARN") ||
		!strings.Contains(logged, "using the SQL durable conclusion") {
		t.Fatalf("a failed durable read must log at WARN so the degradation is visible; got:\n%s", logged)
	}
	if !strings.Contains(logged, "sql_known=true") {
		t.Fatalf("the degraded log must say which SQL conclusion was used; got:\n%s", logged)
	}
}
