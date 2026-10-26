//go:build integration

package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	gwtrace "github.com/kaixuan/llm-gateway-go/internal/trace"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
)

// ─────────────────────────────────────────────────────────────────────────────
// 这两个用例驱动**真实 handler 入口**（ChatHandler.ServeHTTP），真实 trace
// 记录器（internal/trace.RedisRecorder + miniredis），并把终态 flush 进**真实
// 本地 PG17** 的唯一隔离 schema。没有任何内部函数被直接调用来绕过数据流：
// 被测的判据在 handler 自己的 deferred 收尾里，只能经由一次真实 HTTP 请求到达。
//
// 隔离口径沿用本仓既有的 trace_retirement_test.go 约定：
//   - 必须显式提供 loopback DSN（TRACE_RETIREMENT_TEST_DSN）
//   - 本机 54318 的共享库需再显式授权 TRACE_RETIREMENT_LOCAL_SCHEMA=1
//   - 每次建**唯一 schema**，cleanup 只 DROP 该 schema CASCADE，绝不碰 public
//   - 要求 server_version_num 落在 PG17
// ─────────────────────────────────────────────────────────────────────────────

func retireStreamingPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TRACE_RETIREMENT_TEST_DSN")
	if dsn == "" {
		t.Skip("TRACE_RETIREMENT_TEST_DSN not supplied")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid isolated PG configuration")
	}
	localSchema := os.Getenv("TRACE_RETIREMENT_LOCAL_SCHEMA") == "1" && cfg.ConnConfig.Port == 54318
	if (cfg.ConnConfig.Host != "127.0.0.1" && cfg.ConnConfig.Host != "localhost") || (!strings.HasPrefix(cfg.ConnConfig.Database, "trace_retirement") && !localSchema) {
		t.Fatal("requires loopback trace_retirement database or explicitly authorized local 54318 schema isolation")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	var version int
	if err := admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version/10000 != 17 {
		t.Fatalf("requires PG17: version=%d err=%v", version, err)
	}
	schema := fmt.Sprintf("retire_stream_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"session_turns_hot", "session_turns", "_request_logs_hot"} {
		if _, err := pool.Exec(ctx, "CREATE TABLE "+table+" (request_id text, ts timestamptz DEFAULT now(), request_status text, trace_events jsonb)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE request_stage_events (
 tenant_id text, request_id text, seq int, stage text, module text, event_timestamp timestamptz,
 duration_ms int, status text, error_message text, http_status int, response_body text,
 failure_hint text, details jsonb, snapshot jsonb, UNIQUE(request_id, seq))`); err != nil {
		t.Fatal(err)
	}
	return pool
}

// retireNoCandidateResolver 是一个最小 providerResolver：模型「已登记」但当前
// 没有任何可路由节点 —— 也就是生产里 no_candidate（503）那一支。
// known=false 则走 invalid_model（400）那一支。两者都是 failAndMark 出口，
// 也正是缺陷A 的现场：SetError 从不被调用，ErrCode 恒空。
type retireNoCandidateResolver struct{ known bool }

func (r retireNoCandidateResolver) Enabled() bool { return true }
func (r retireNoCandidateResolver) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, nil, nil
}

func (r retireNoCandidateResolver) GetCandidatesByModality(context.Context, string, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, nil, nil
}

func (r retireNoCandidateResolver) ModelKnown(context.Context, string) bool { return r.known }

// retirePassVerifier 让鉴权通过（缺陷B 的入口）。
type retirePassVerifier struct{}

func (retirePassVerifier) Enabled() bool { return true }
func (retirePassVerifier) Verify(context.Context, string) (*authentication.KeyInfo, error) {
	return &authentication.KeyInfo{ID: 7, TenantID: "default"}, nil
}
func (retirePassVerifier) VerifyByID(context.Context, int) (*authentication.KeyInfo, error) {
	return &authentication.KeyInfo{ID: 7, TenantID: "default"}, nil
}
func (retirePassVerifier) CheckBudget(context.Context, int) error { return nil }
func (retirePassVerifier) LookupKeyMeta(context.Context, string) (*authentication.KeyLookupMeta, error) {
	return nil, nil
}

func retireHandler(rec gwtrace.Recorder, hook func(*telemetry.RequestLogEntry), resolver providerResolver) *ChatHandler {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(retirePassVerifier{})
	h.executor = &executors.Executor{}
	h.provider = resolver
	h.SetTraceRecorder(rec)
	if hook != nil {
		h.SetRequestLogHook(hook)
	}
	return h
}

const retireChatBody = `{"model":"gpt-4o","stream":false,"messages":[{"role":"user","content":"hi"}]}`

// ── 缺陷A ────────────────────────────────────────────────────────────────────
//
// 失败请求的 trace 终态曾恒为 success。机制：handler 的 deferred 收尾用
// `logCtx.ErrCode == ""` 当作「本次成功」的判据，而 ErrCode 只有 SetError 会写；
// 路由阶段的 failAndMark（invalid_model / no_candidate / …）根本不调 SetError，
// 错误码只经 BuildFailureEntry 落到 entry.ErrorKind。
//
// 这里同时钉两件事：
//  1. final_status 必须是 failed（不是 success）；
//  2. failed_at_stage 必须是真实分类（no_candidate → route_resolve）。
//     修复前 ErrCode 为空 → ClassifyFailureToStage("") 落 catch-all
//     request_complete，所以这条断言同时覆盖「分类变细」这一行为变更。
//
// 热表 / 父分区两个落点都要覆盖：真实流量两者都出现过
// （session_turns_hot 与月分区 session_turns 交替承载）。
func TestRoutingStageFailureFinalizesTraceAsFailed(t *testing.T) {
	pool := retireStreamingPG(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name       string
		known      bool
		wantStatus int
		wantErr    string
		wantStage  gwtrace.Stage
	}{
		// no_candidate → ClassifyFailureToStage 表内有 route_resolve 这一映射，
		// 所以它同时是终态与阶段的判据。
		{"no_candidate", true, http.StatusServiceUnavailable, "no_candidate", gwtrace.StageRouteResolve},
		// invalid_model 不在映射表内 → 落 catch-all request_complete，但终态
		// 仍必须是 failed（缺陷本身与是否可分类无关）。
		{"invalid_model", false, http.StatusBadRequest, "invalid_model", gwtrace.StageRequestComplete},
	} {
		for _, landing := range []string{"session_turns_hot", "session_turns"} {
			t.Run(tc.name+"/"+landing, func(t *testing.T) {
				mini := miniredis.RunT(t)
				rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
				t.Cleanup(func() { _ = rdb.Close() })
				rec := gwtrace.NewRedisRecorder(rdb)

				var entries []*telemetry.RequestLogEntry
				h := retireHandler(rec, func(e *telemetry.RequestLogEntry) {
					entries = append(entries, e)
				}, retireNoCandidateResolver{known: tc.known})

				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(retireChatBody))
				req.Header.Set("Authorization", "Bearer sk-retire")
				w := httptest.NewRecorder()
				middleware.NewRequestIDMiddleware().Wrap(h).ServeHTTP(w, req)

				requestID := w.Header().Get("X-Request-Id")
				if requestID == "" {
					t.Fatal("no X-Request-Id on the response; cannot correlate the trace row")
				}
				// 前提 1：客户端确实收到了失败响应（否则「终态=success」可能
				// 只是因为这次请求根本没失败 —— 那种情况下本用例是假绿）。
				if w.Code != tc.wantStatus {
					t.Fatalf("client status = %d, want %d; body=%s", w.Code, tc.wantStatus, w.Body.String())
				}
				// 前提 2：确实走的是 failAndMark 出口（ErrorKind 有值）。
				// 若这条不成立，下面的终态断言就只是在测一个没失败的请求。
				var sawFailureEntry bool
				for _, e := range entries {
					if e.ErrorKind != nil && *e.ErrorKind == tc.wantErr {
						sawFailureEntry = true
					}
				}
				if !sawFailureEntry {
					t.Fatalf("no emitted request_log entry carried error_kind=%q; entries=%d", tc.wantErr, len(entries))
				}

				if _, err := pool.Exec(ctx, "INSERT INTO "+landing+" (request_id) VALUES ($1)", requestID); err != nil {
					t.Fatal(err)
				}
				if err := rec.FlushToPG(ctx, pool, requestID); err != nil {
					t.Fatalf("FlushToPG: %v", err)
				}

				var raw []byte
				if err := pool.QueryRow(ctx, "SELECT trace_events FROM "+landing+" WHERE request_id=$1", requestID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if len(raw) == 0 {
					t.Fatalf("%s.trace_events is NULL — the flush wrote nothing, so any "+
						"final_status assertion below would be vacuous", landing)
				}
				var tr struct {
					FinalStatus   string `json:"final_status"`
					FailedAtStage string `json:"failed_at_stage"`
					Events        []struct {
						Stage   string `json:"stage"`
						Status  string `json:"status"`
						Details struct {
							FailedStage string `json:"failed_stage"`
						} `json:"details"`
					} `json:"events"`
				}
				if err := json.Unmarshal(raw, &tr); err != nil {
					t.Fatal(err)
				}
				if tr.FinalStatus != string(gwtrace.FinalFailed) {
					t.Fatalf("final_status = %q, want %q (a failed request was finalized as %q)",
						tr.FinalStatus, gwtrace.FinalFailed, tr.FinalStatus)
				}
				if tr.FailedAtStage != string(tc.wantStage) {
					t.Fatalf("failed_at_stage = %q, want %q", tr.FailedAtStage, tc.wantStage)
				}
				// 规范化事件表里也必须有一条 failed 的 request_complete ——
				// 只改 Redis 顶层字段而不落规范化行同样是保真缺口。
				var stageRows, failedComplete int
				if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status='failed')
					FROM request_stage_events WHERE request_id=$1 AND stage='request_complete'`, requestID).
					Scan(&stageRows, &failedComplete); err != nil {
					t.Fatal(err)
				}
				if stageRows != 1 || failedComplete != 1 {
					t.Fatalf("request_stage_events request_complete rows=%d failed=%d, want 1/1", stageRows, failedComplete)
				}

				// 逐事件判据（假绿守卫）：同一条 trace 里必须既有成功事件又有
				// 终态 failed 事件。若一次「把所有事件都标 failed」的改动也能
				// 满足上面的断言，这条会红。
				var nonFailedEvents int
				for _, ev := range tr.Events {
					if ev.Status != string(gwtrace.StatusFailed) {
						nonFailedEvents++
					}
				}
				if nonFailedEvents == 0 {
					t.Fatalf("every event in the trace is status=failed (%d events); the recorder "+
						"is not discriminating per-event status", len(tr.Events))
				}
			})
		}
	}
}

// 缺陷A 的反向对照（记录器层控制组）：FinalSuccess 仍必须落 success。
// 真实 handler 的成功出口需要上游，本轮不铺那条脚手架；这个控制组只钉住
// 「记录器没有把两个终态混为一谈」这一条，判据主体仍是上面那个真实
// handler 用例。用 GET 探针当控制组是无效的——它不写任何 trace 事件，
// trace_events 会是 NULL，断言会变成真空。
func TestRecorderStillPersistsSuccessTerminalDistinctly(t *testing.T) {
	pool := retireStreamingPG(t)
	ctx := context.Background()

	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	rec := gwtrace.NewRedisRecorder(rdb)

	if err := rec.Append(ctx, "retire-success-control", gwtrace.ReceiveRequest(http.MethodPost, "/v1/chat/completions", "127.0.0.1").Build()); err != nil {
		t.Fatal(err)
	}
	if err := rec.Finalize(ctx, "retire-success-control", gwtrace.FinalSuccess, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO session_turns_hot (request_id) VALUES ($1)", "retire-success-control"); err != nil {
		t.Fatal(err)
	}
	if err := rec.FlushToPG(ctx, pool, "retire-success-control"); err != nil {
		t.Fatalf("FlushToPG: %v", err)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, "SELECT trace_events FROM session_turns_hot WHERE request_id=$1", "retire-success-control").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("trace_events is NULL for the success control — the flush wrote nothing")
	}
	var tr struct {
		FinalStatus string `json:"final_status"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.FinalStatus != string(gwtrace.FinalSuccess) {
		t.Fatalf("final_status = %q, want %q for a success terminal", tr.FinalStatus, gwtrace.FinalSuccess)
	}
}

// ── 缺陷B（入口侧）────────────────────────────────────────────────────────────
//
// session_turns.status_code 曾把客户端实收的 401 全部落成 500：网关自造的失败
// 根本不经过上游，UpstreamStatusCode 恒 nil，下游只能退回字面 500。
// 这里钉「客户端实收状态进入 entry」这一半（落库那半在
// internal/sessionv2mirror 的对应用例）。
func TestGatewayAuthoredRejectionCarriesClientReceivedStatus(t *testing.T) {
	reject := retireRejectVerifier{}
	run := func(t *testing.T) (*telemetry.RequestLogEntry, int) {
		t.Helper()
		var got []*telemetry.RequestLogEntry
		h := NewChatHandler(nil, nil, nil, nil, nil, nil)
		h.setRequestKeyVerifierForTest(reject)
		// executor/provider 必须非 nil 且 provider.Enabled()，否则 ServeHTTP
		// 会在 executor_unavailable 那一支提前返回，根本走不到鉴权。
		h.executor = &executors.Executor{}
		h.provider = retireNoCandidateResolver{}
		h.SetRequestLogHook(func(e *telemetry.RequestLogEntry) { got = append(got, e) })
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(retireChatBody))
		req.Header.Set("Authorization", "Bearer sk-retire")
		w := httptest.NewRecorder()
		middleware.NewRequestIDMiddleware().Wrap(h).ServeHTTP(w, req)
		for _, e := range got {
			if e.ErrorKind != nil && *e.ErrorKind == "invalid_key" {
				return e, w.Code
			}
		}
		t.Fatalf("no emitted entry carried error_kind=invalid_key; entries=%d", len(got))
		return nil, 0
	}

	t.Run("client_received_status_enters_entry", func(t *testing.T) {
		e, code := run(t)
		if code != http.StatusUnauthorized {
			t.Fatalf("client status = %d, want 401", code)
		}
		if e.ClientStatusCode == nil {
			t.Fatal("entry.ClientStatusCode is nil — the status the client actually received never entered the entry")
		}
		if *e.ClientStatusCode != http.StatusUnauthorized {
			t.Fatalf("entry.ClientStatusCode = %d, want 401", *e.ClientStatusCode)
		}
		// 上游视角必须保持 nil：网关自造的失败没有上游状态可记，
		// 把 401 塞进 UpstreamStatusCode 会污染「上游返回了什么」这一列语义。
		if e.UpstreamStatusCode != nil {
			t.Fatalf("UpstreamStatusCode = %d, want nil for a gateway-authored rejection", *e.UpstreamStatusCode)
		}
	})

	// 反向对照：成功路径不得凭空发明 ClientStatusCode。nil 的含义是
	// 「本出口没有实收信息」，下游据此回落到 200/500 推导。
	t.Run("success_carries_no_client_status", func(t *testing.T) {
		var got []*telemetry.RequestLogEntry
		mini := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		rec := gwtrace.NewRedisRecorder(rdb)
		h := retireHandler(rec, func(e *telemetry.RequestLogEntry) { got = append(got, e) },
			retireNoCandidateResolver{known: true})
		req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
		w := httptest.NewRecorder()
		middleware.NewRequestIDMiddleware().Wrap(h).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET probe status = %d, want 200", w.Code)
		}
		for _, e := range got {
			if e.ClientStatusCode != nil {
				t.Fatalf("success entry invented ClientStatusCode=%d", *e.ClientStatusCode)
			}
		}
	})
}

type retireRejectVerifier struct{}

func (retireRejectVerifier) Enabled() bool { return true }
func (retireRejectVerifier) Verify(context.Context, string) (*authentication.KeyInfo, error) {
	return nil, &authentication.InvalidKeyError{}
}
func (retireRejectVerifier) VerifyByID(context.Context, int) (*authentication.KeyInfo, error) {
	return nil, &authentication.InvalidKeyError{}
}
func (retireRejectVerifier) CheckBudget(context.Context, int) error { return nil }
func (retireRejectVerifier) LookupKeyMeta(context.Context, string) (*authentication.KeyLookupMeta, error) {
	return nil, nil
}
