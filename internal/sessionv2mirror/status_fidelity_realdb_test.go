//go:build integration

package sessionv2mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// ─────────────────────────────────────────────────────────────────────────────
// 缺陷B（消费侧）：session_turns.status_code 曾把客户端实收的 401 全部落成
// 500。网关自造的失败根本不经过上游 → UpstreamStatusCode 恒 nil → statusCode()
// 的三段推导只能落字面 500。真库证据：260 行 error_kind=invalid_key（客户端实收
// 401）status_code 全是 500。
//
// 这里驱动**真实镜像入口** internal/sessionv2mirror.PersistHook（不是直接调
// statusCode / entryToProcessedRequest），断言它交给下游 writer 的
// ProcessedRequest.StatusCode。
//
// 为什么不在这一步真写 PG：domains/session/v2/turn_writer.go 的 SQL 把表名
// 硬写死成 public.session_turns_hot / public.session_turns（第 50/382/448 行），
// 只有「独立可丢的 *_test 库 + DROP SCHEMA public」才能重定向（见
// ingest_schema_realdb_test.go 的既有约定）。在共享库上改写 public 被明确禁止，
// 所以落库那一段拆成两步、各自可判：
//   1. 本用例：镜像产出的 StatusCode 值本身（真实 hook 出口）。
//   2. TestWriterCopiesStatusCodeVerbatim + TestRealColumnAcceptsRealClientStatus：
//      writer 把该值原样搬进 session_turns.status_code（源码钉桩 + 真实
//      PG17 隔离 schema 上的列契约）。
// ─────────────────────────────────────────────────────────────────────────────

func retireMirrorPG(t *testing.T) *pgxpool.Pool {
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
	schema := fmt.Sprintf("retire_mirror_%d", time.Now().UnixNano())
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
	return pool
}

// retireRecordingWriter 是 V2Writer 的真实接口实现，只把 hook 交给它的
// ProcessedRequest 存下来。判据针对的是「hook 算出了什么」，不是 writer 的
// SQL —— 后者由下面两个用例分别覆盖。
type retireRecordingWriter struct {
	mu  sync.Mutex
	req *v2.ProcessedRequest
}

func (w *retireRecordingWriter) Write(_ context.Context, req *v2.ProcessedRequest) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.req = req
	return nil
}

func retireDriveHook(t *testing.T, entry *telemetry.RequestLogEntry) *v2.ProcessedRequest {
	t.Helper()
	prev := shadowWriteEnabledPtr.Load().(shadowEnabledFn)
	prevAsync := shadowWriteDispatchAsync
	setShadowWriteEnabledForTest(func() bool { return true })
	shadowWriteDispatchAsync = false
	t.Cleanup(func() { setShadowWriteEnabledForTest(prev); shadowWriteDispatchAsync = prevAsync })

	w := &retireRecordingWriter{}
	hook := PersistHook(w)
	hook(entry)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.req == nil {
		t.Fatal("PersistHook produced no ProcessedRequest — the entry never reached the writer")
	}
	return w.req
}

func retireFailureEntry(clientStatus *int) *telemetry.RequestLogEntry {
	now := time.Now().UTC()
	session := "retire-mirror-session"
	status := telemetry.RequestStatusFailure
	body := `{"messages":[{"role":"user","content":"hi"}]}`
	return &telemetry.RequestLogEntry{
		RequestID:          "retire-mirror-req",
		GwSessionID:        &session,
		TenantID:           "default",
		EventAt:            &now,
		Success:            false,
		RequestStatus:      &status,
		ErrorKind:          strPtr("invalid_key"),
		ClientStatusCode:   clientStatus,
		RequestBody:        &body,
		UpstreamStatusCode: nil, // 网关自造失败：没有上游状态
	}
}

func TestMirrorPersistsClientReceivedStatusNotSynthetic500(t *testing.T) {
	// 前提：上游视角必须确实是「没有信息」。若这条不成立，下面 500 的期望值
	// 就可能只是碰巧命中，我们会分不清是 fallback 还是巧合。
	entry := retireFailureEntry(nil)
	if entry.UpstreamStatusCode != nil {
		t.Fatal("fixture precondition broken: a gateway-authored rejection must carry no upstream status")
	}

	t.Run("no_received_status_falls_back", func(t *testing.T) {
		req := retireDriveHook(t, retireFailureEntry(nil))
		// nil 的含义是「本出口没有实收信息」，500 只是兜底推导 —— 不是谎称
		// 客户端收到了 500。这条钉住 fallback 仍然存在。
		if req.StatusCode != 500 {
			t.Fatalf("StatusCode = %d, want 500 fallback when no client status was recorded", req.StatusCode)
		}
	})

	t.Run("received_status_wins", func(t *testing.T) {
		received := 401
		req := retireDriveHook(t, retireFailureEntry(&received))
		if req.StatusCode != 401 {
			t.Fatalf("StatusCode = %d, want 401 — the status the client actually received was replaced", req.StatusCode)
		}
		if req.UpstreamStatusCode != 0 {
			t.Fatalf("UpstreamStatusCode = %d, want 0; the client status must not be laundered into the upstream column",
				req.UpstreamStatusCode)
		}
	})
}

// 上游状态仍在第二位：代理形态的失败（上游 429 被原样转给客户端）必须保留
// 上游码，而不是被一个 nil 的 ClientStatusCode 顶掉。
func TestMirrorKeepsUpstreamStatusWhenNoClientStatusRecorded(t *testing.T) {
	entry := retireFailureEntry(nil)
	upstream := 429
	entry.UpstreamStatusCode = &upstream
	req := retireDriveHook(t, entry)
	if req.StatusCode != 429 {
		t.Fatalf("StatusCode = %d, want the relayed upstream 429", req.StatusCode)
	}
}

// writer 侧：StatusCode 必须原样搬运，不得在写侧再推导一次（推导一次就会把
// 保真重新丢掉，正是本缺陷的形状）。沿用本包既有的源码钉桩约定
// （final_success_turn_wiring_test.go / final_success_turn_gate_test.go）。
func TestWriterCopiesStatusCodeVerbatim(t *testing.T) {
	src, err := os.ReadFile("../../domains/session/v2/session_writer_v2.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	if !strings.Contains(code, "StatusCode: req.StatusCode,") {
		t.Fatal("session_writer_v2.go no longer assigns session_turns.status_code verbatim from " +
			"req.StatusCode — a write-side re-derivation would silently drop the received status again")
	}
	for _, forbidden := range []string{"StatusCode: 500,", "StatusCode: 200,"} {
		if strings.Contains(code, forbidden) {
			t.Fatalf("session_writer_v2.go hardcodes %q — the received status must not be re-derived", forbidden)
		}
	}
}

// 列契约：真实 PG17 上，隔离 schema 内按真实 DDL 克隆的 session_turns_hot
// 必须原样接受真实状态码（不被 CHECK 拒掉、不被默认值/类型改写成 500）。
func TestRealColumnAcceptsRealClientStatus(t *testing.T) {
	pool := retireMirrorPG(t)
	ctx := context.Background()
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]

	// 只读 public 的真实 DDL，不写 public：LIKE ... INCLUDING ALL 把列/默认值/
	// 约束/索引克隆进隔离 schema。
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".session_turns_hot (LIKE public.session_turns INCLUDING ALL)"); err != nil {
		t.Fatalf("clone real session_turns DDL into the isolated schema: %v", err)
	}
	for _, code := range []int{401, 402, 403, 429, 500, 503} {
		id := fmt.Sprintf("retire-col-%d", code)
		if _, err := pool.Exec(ctx, `INSERT INTO `+schema+`.session_turns_hot
			(session_id, tenant_id, request_id, turn_no, status_code, success, partition_date, ts)
			VALUES ($3,'default',$1,1,$2,false,CURRENT_DATE,now())`, id, code, "retire-session-"+id); err != nil {
			t.Fatalf("real column rejected status_code=%d: %v", code, err)
		}
		var got int
		if err := pool.QueryRow(ctx, "SELECT status_code FROM "+schema+".session_turns_hot WHERE request_id=$1", id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != code {
			t.Fatalf("status_code round-trip = %d, want %d", got, code)
		}
	}
}

// 契约遗留（记录在案，不是本轮修的）：session_turns.status_code 没有列注释
// 说明它是「客户端实收状态」而不是「上游状态」。upstream_status_code 是独立列，
// 两者语义不同，缺注释时读者会把 status_code 当成上游码。
func TestStatusCodeColumnCommentGapIsRecorded(t *testing.T) {
	pool := retireMirrorPG(t)
	ctx := context.Background()
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	if _, err := pool.Exec(ctx, "CREATE TABLE "+schema+".session_turns_hot (LIKE public.session_turns INCLUDING ALL)"); err != nil {
		t.Fatal(err)
	}
	var comment string
	err := pool.QueryRow(ctx, `SELECT COALESCE(col_description(($1||'.session_turns_hot')::regclass::oid, a.attnum), '')
		FROM pg_attribute a
		WHERE a.attrelid = ($1||'.session_turns_hot')::regclass AND a.attname = 'status_code'`, schema).Scan(&comment)
	if err != nil {
		t.Fatal(err)
	}
	if comment == "" {
		t.Logf("KNOWN CONTRACT GAP (not fixed in this change): session_turns.status_code has no column " +
			"comment, so readers cannot tell it is the CLIENT-received status rather than the upstream one. " +
			"upstream_status_code is a separate column; upstream_status_code IS commented.")
	}
}

var _ = json.Marshal
