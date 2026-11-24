package reconciliation

// attempt_store_bounds_test.go —— provider_call_attempts 写入侧的值域门
// （2026-11-24，12h 修订审计轮 L5）。
//
// # 这道门在防什么
//
// ★ **背景事实（本地真 PG 17 实测，982 真实 DDL）**：
//
//	探针 A：MarkSent 传零值时间 ⇒ sent_at 写成 0001-01-01（**不是** 23514）
//	探针 B：http_status=0 ⇒ **23514** ⇒ 整条 UPDATE 被拒
//	          ⇒ delivery / sent_at / provider_request_id **整条丢失**
//	探针 C：生产写入序列（prepare→dispatch→sent→http）⇒ CHECK 41 **不违反**
//	探针 E：手写 delivery='sent' 不带 sent_at ⇒ CHECK 41 **拒收**（有效防线）
//
// ★ 所以要防的是两件**具体**的事，不是「CHECK 与写入顺序冲突」：
//
//	① 越界的 http_status 让**整条 UPDATE** 被 23514 拒收
//	   ⇒ 传输证据跟着一起丢（丢不起的是证据，不是状态码）；
//	② 零值时间把 sent_at 写成 0001-01-01
//	   ⇒ 且两个部分索引的谓词含义同时失真
//	   （idx_..._sent 的 `sent_at IS NOT NULL` 把它算作「已确认发送」，
//	     idx_..._unconfirmed 的 `sent_at IS NULL` 把它排除出「未确认」）。
//
// ★ 门用**迁移文件本身**建表（不内联 DDL）⇒ CHECK 口径与生产同源；
//   另有一条漂移门钉住「值域常量 == 迁移里的 CHECK 表达式」。

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const providerAttemptsMigrationPath = "../../sql/migrations/startup/982_provider_call_attempts.sql"

// providerAttemptsDDL 从 982 迁移文件里取出 CREATE TABLE 块。
//
// ★ 为什么要从迁移文件取而不是内联一份：
//
//	内联的 DDL 会在有人改迁移时**静默漂移**（门还在绿，但测的不是生产约束）★
//	⇒ 这是上一轮「假绿闭环」教训的直接应用。
func providerAttemptsDDL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(providerAttemptsMigrationPath)
	if err != nil {
		// ★ 读不到就是硬失败，不是 Skip：仓内这个文件必然存在 ⇒
		//   静默 Skip 会让这道漂移门在坏掉的路径下**一直是绿的**（假绿）。
		t.Fatalf("982 migration not readable at %s: %v", providerAttemptsMigrationPath, err)
	}
	block := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS public\.provider_call_attempts \(.*?\n\);`).FindString(string(raw))
	if block == "" {
		t.Fatalf("982 migration no longer contains the provider_call_attempts CREATE TABLE block")
	}
	return block
}

// TestProviderAttemptsValueDomainMatchesMigration 是**漂移门**：
// Go 侧的值域常量必须与迁移里的 CHECK 表达式同源。
//
// ★ 若有人改了 982 的 CHECK（例如放宽到 0..999），这道门会红，
//
//	提示同步 providerHTTPStatusMin / providerHTTPStatusMax —— 而不是让
//	「Go 侧判一个值域、数据库判另一个值域」这种双口径悄悄活下来。
func TestProviderAttemptsValueDomainMatchesMigration(t *testing.T) {
	raw, err := os.ReadFile(providerAttemptsMigrationPath)
	if err != nil {
		t.Fatalf("982 migration not readable at %s: %v", providerAttemptsMigrationPath, err)
	}
	body := string(raw)
	for _, want := range []string{
		"CHECK (http_status BETWEEN 100 AND 599)",
		"CHECK ((delivery = 'sent') = (sent_at IS NOT NULL))",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("982 migration no longer contains %q ⇒ the Go-side value domain must be re-derived from it", want)
		}
	}
}

// TestProviderHTTPStatusForWriteBounds 是**纯函数腿**（不需要数据库 ⇒ CI 恒跑）。
func TestProviderHTTPStatusForWriteBounds(t *testing.T) {
	cases := []struct {
		status int
		ok     bool
	}{
		{status: 200, ok: true},
		{status: 100, ok: true},
		{status: 599, ok: true},
		{status: 0, ok: false},
		{status: 99, ok: false},
		{status: 600, ok: false},
		{status: -1, ok: false},
	}
	for _, tc := range cases {
		got, ok := providerHTTPStatusForWrite(tc.status)
		if ok != tc.ok {
			t.Fatalf("providerHTTPStatusForWrite(%d) ok = %v, want %v", tc.status, ok, tc.ok)
		}
		if ok && got != tc.status {
			t.Fatalf("providerHTTPStatusForWrite(%d) = %d, want %d", tc.status, got, tc.status)
		}
	}
}

// providerAttemptsFixturePool 连到隔离 fixture 库并确保表存在（用迁移真实 DDL）。
func providerAttemptsFixturePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// ★ 用**自己的** DSN 变量，不复用 RECONCILIATION_INSTALLED_TEST_DSN：
	//   那个变量背后是「已装好整套 installer schema」的库形态
	//   （report_integration_test.go 的 installer-schema 腿依赖它），
	//   而本门只需要一张 provider_call_attempts ⇒ 共用会互相把对方的
	//   库形态前提打破（实测：那条腿报 relation 不存在 ⇒ 假红）。
	dsn := os.Getenv("RECONCILIATION_ATTEMPT_BOUNDS_TEST_DSN")
	if dsn == "" {
		t.Skip("set RECONCILIATION_ATTEMPT_BOUNDS_TEST_DSN to an isolated reconciliation_*_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("invalid fixture connection configuration")
	}
	t.Cleanup(pool.Close)
	var db string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&db); err != nil {
		t.Fatal("could not verify fixture isolation")
	}
	if !strings.HasPrefix(db, "reconciliation_") || !strings.HasSuffix(db, "_test") {
		t.Fatalf("fixture must live in an isolated reconciliation_*_test database, got %q", db)
	}
	if _, err := pool.Exec(ctx, providerAttemptsDDL(t)); err != nil {
		t.Fatalf("could not create the provider_call_attempts fixture: %v", err)
	}
	return pool
}

// readAttemptRow 在**同一连接**上开 bypass_rls 后读回证据行。
//
// ★ 为什么需要 bypass_rls：982 给这张表开了 RLS，策略要求
//
//	`current_setting('app.bypass_rls', true) = 'true'`；生产写入路径
//	（writeReconciliationEvidence）自己 set_config ⇒ 断言必须用同一口径，
//	否则读回来的是「零行」而不是「证据」。
func readAttemptRow(t *testing.T, pool *pgxpool.Pool, id string) (delivery string, sentAt *time.Time, status *int, requestID *string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT set_config('app.bypass_rls','true',false)`); err != nil {
		t.Fatal(err)
	}
	err = conn.QueryRow(ctx, `SELECT delivery, sent_at, http_status, provider_request_id
		FROM public.provider_call_attempts WHERE attempt_id=$1`, id).
		Scan(&delivery, &sentAt, &status, &requestID)
	if err != nil {
		t.Fatalf("could not read attempt %s: %v", id, err)
	}
	return delivery, sentAt, status, requestID
}

// TestAttemptRecorderOutOfRangeStatusKeepsEvidence：越界状态码**不得**让
// 整条 UPDATE 被拒 ⇒ 传输证据（delivery / sent_at / provider_request_id）
// 必须仍然落库。
//
// ★ 修前的事实：整条 UPDATE 被 23514 拒收 ⇒ 该行停在 prepared、sent_at 为
//
//	NULL、provider_request_id 也写不上（真 PG 实测 probe-b）。
func TestAttemptRecorderOutOfRangeStatusKeepsEvidence(t *testing.T) {
	pool := providerAttemptsFixturePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	recorder := NewAttemptRecorder(pool)
	id := "bounds-out-of-range-status"
	if _, err := pool.Exec(ctx, `DELETE FROM public.provider_call_attempts WHERE attempt_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Prepare(ctx, Attempt{ID: id, TrafficClass: "unknown", Source: "bounds-fixture",
		AttemptNo: 1, PreparedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.MarkDispatched(ctx, id); err != nil {
		t.Fatal(err)
	}
	// 越界状态码（真实 HTTP 响应不会是这个值，但接口层不该裸奔）。
	if err := recorder.RecordHTTP(ctx, id, 0, "provider-request-bounds", time.Now().UTC()); err != nil {
		t.Fatalf("out-of-range status must not fail the whole evidence write: %v", err)
	}
	delivery, sentAt, status, requestID := readAttemptRow(t, pool, id)
	if delivery != "sent" {
		t.Fatalf("delivery = %q, want %q ⇒ the evidence write was rejected as a whole", delivery, "sent")
	}
	if sentAt == nil {
		t.Fatal("sent_at is NULL ⇒ the transport evidence was lost")
	}
	if status != nil {
		t.Fatalf("http_status = %v, want NULL (out-of-range status must be skipped, not written)", *status)
	}
	if requestID == nil || *requestID != "provider-request-bounds" {
		t.Fatalf("provider_request_id = %v, want %q ⇒ the evidence write was rejected as a whole", requestID, "provider-request-bounds")
	}
}

// TestAttemptRecorderMarkSentZeroTimeKeepsRealTimestamp：零值时间**不得**
// 写成 0001-01-01。
func TestAttemptRecorderMarkSentZeroTimeKeepsRealTimestamp(t *testing.T) {
	pool := providerAttemptsFixturePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	recorder := NewAttemptRecorder(pool)
	id := "bounds-zero-sent-time"
	if _, err := pool.Exec(ctx, `DELETE FROM public.provider_call_attempts WHERE attempt_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Prepare(ctx, Attempt{ID: id, TrafficClass: "unknown", Source: "bounds-fixture",
		AttemptNo: 1, PreparedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.MarkDispatched(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := recorder.MarkSent(ctx, id, time.Time{}); err != nil {
		t.Fatal(err)
	}
	delivery, sentAt, _, _ := readAttemptRow(t, pool, id)
	if delivery != "sent" {
		t.Fatalf("delivery = %q, want %q", delivery, "sent")
	}
	if sentAt == nil {
		t.Fatal("sent_at is NULL ⇒ MarkSent lost the timestamp")
	}
	if sentAt.Year() <= 1 {
		t.Fatalf("sent_at = %v ⇒ the zero value was written through as 0001-01-01", sentAt.UTC())
	}
}
