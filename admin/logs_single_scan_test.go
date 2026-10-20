package admin

// ★ 这道门钉两个不变量：
//
//	① **列表端点对同一行集只做一次非分组聚合扫描**（COUNT 与 SUM 合并成一条
//	   查询；此前是两条 ⇒ 窗口内的行集被过两遍）。
//	② 合并后的**数值与旧的两次扫描逐值相等**（count 与各 SUM 都不许漂）。
//
// ─────────────────────────────────────────────────────────────────────────────
// 为什么要有这道门（2026-10-20 复核轮，
//   docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §5.12）
//
// 上一轮把「列表端点三扫」记成遗留 L3，理由是「在本地小数据集上无法证明
// 收益」。本轮用**查询计数**（而不是墙钟或 buffers）来钉它：本地数据集只有
// ~1.2k 行，三种写法的 EXPLAIN BUFFERS **完全相同**（实测 137/137/137，
// 第二趟全命中缓存 ⇒ 量不出差别），但**执行次数**是结构性的、与数据量无关
// 的事实：同一行集被独立扫描了几次。
//
// 量法：pgx QueryTracer 记录 handler 实际发出的每条 SQL，统计其中
// 「读日志源 + 带聚合 + **无 GROUP BY**」的条数：
//   · 旧写法：COUNT 一条 + SUM 一条 ⇒ **2**（by_model 的 GROUP BY 不算）
//   · 新写法：合并的一条 ⇒ **1**
//
// 变异（改回旧写法会红在哪）：
//   · 把 COUNT(*) 从合并查询里拆回独立查询 ⇒ 计数变 2 ⇒ "expected exactly 1
//     non-grouped aggregate scan over the logs source" 失败；
//   · 把合并查询的 WHERE 或 FROM 改窄（与旧口径不等价）⇒ 数值对账失败 ⇒
//     "aggregate mismatch" 失败。
//
// ⚠ 门禁纪律：跳过**不构成**证据（没配 TEST_PG_URL 时 t.Skip）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// logsScanTracer 记录 handler 发出的每条 SQL。
type logsScanTracer struct {
	mu  sync.Mutex
	sql []string
}

func (tr *logsScanTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	tr.sql = append(tr.sql, data.SQL)
	tr.mu.Unlock()
	return ctx
}

func (tr *logsScanTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {}

func (tr *logsScanTracer) snapshot() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	out := make([]string, len(tr.sql))
	copy(out, tr.sql)
	return out
}

// isLogsSourceMarker 判断一条 SQL 是否读了日志源（视图形态或原生投影形态）。
func isLogsSourceMarker(sql string) bool {
	return strings.Contains(sql, "request_logs_with_current_month") ||
		strings.Contains(sql, "session_turns_hot")
}

// countNonGroupedAggregateScans 数「读日志源 + 带聚合 + 无 GROUP BY」的查询条数。
func countNonGroupedAggregateScans(sqls []string) int {
	n := 0
	for _, s := range sqls {
		if !isLogsSourceMarker(s) {
			continue
		}
		if !strings.Contains(s, "COUNT(*)") && !strings.Contains(s, "SUM(rl.") {
			continue
		}
		if strings.Contains(s, "GROUP BY") {
			continue // by_model 的分组查询是另一件事
		}
		n++
	}
	return n
}

// logsRealDBPoolWithTracer 连真库并装上查询计数器；没有 TEST_PG_URL 就跳过。
func logsRealDBPoolWithTracer(t *testing.T) (*pgxpool.Pool, *logsScanTracer) {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the list endpoint scans the row set once")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Skipf("cannot parse TEST_PG_URL: %v", err)
	}
	tracer := &logsScanTracer{}
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("cannot ping TEST_PG_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, tracer
}

// TestListLogs_ScansRowSetOnceAndNumbersUnchanged 钉 ① 与 ②。
func TestListLogs_ScansRowSetOnceAndNumbersUnchanged(t *testing.T) {
	pool, tracer := logsRealDBPoolWithTracer(t)
	h := &Handler{db: pool}

	// 固定窗口 ⇒ 参考口径可复算（handler 与参考用同一对 from/to）。
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-24 * time.Hour)
	q := fmt.Sprintf("/api/logs?from=%s&to=%s&page=1&page_size=5",
		start.Format(time.RFC3339), end.Format(time.RFC3339))
	req := httptest.NewRequest(http.MethodGet, q, nil)
	rec := httptest.NewRecorder()

	h.listLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("listLogs status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Count     int                 `json:"count"`
		Aggregate requestLogAggregate `json:"aggregate"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, rec.Body.String())
	}

	// ── 判据 ①：对同一行集只有一条非分组聚合查询 ─────────────────────
	scans := countNonGroupedAggregateScans(tracer.snapshot())
	if scans != 1 {
		t.Fatalf("expected exactly 1 non-grouped aggregate scan over the logs source "+
			"(COUNT and SUM merged into one query), got %d — the row set is scanned %d times; "+
			"recorded SQL:\n%s", scans, scans, strings.Join(tracer.snapshot(), "\n---\n"))
	}

	// ── 判据 ②：数值与旧的两次扫描逐值相等 ───────────────────────────
	// 参考口径 = 旧写法（一条 COUNT + 一条 SUM），用**同一个源**与同一对 from/to。
	ctx := context.Background()
	source := logsSourceFromSQL()
	var refCount int
	if err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM "+source+" WHERE rl.ts >= $1 AND rl.ts <= $2",
		start, end).Scan(&refCount); err != nil {
		t.Fatalf("reference count: %v", err)
	}
	var refPrompt, refCompletion, refCacheRead, refCacheWrite, refTotal, refCredits int64
	var refCost float64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(rl.prompt_tokens),0)::bigint,
		       COALESCE(SUM(rl.completion_tokens),0)::bigint,
		       COALESCE(SUM(rl.cache_read_tokens),0)::bigint,
		       COALESCE(SUM(rl.cache_write_tokens),0)::bigint,
		       COALESCE(SUM(rl.total_tokens),0)::bigint,
		       COALESCE(SUM(rl.cost_usd),0)::float8,
		       COALESCE(SUM(rl.credits_charged),0)::bigint
		  FROM `+source+` WHERE rl.ts >= $1 AND rl.ts <= $2`,
		start, end).Scan(&refPrompt, &refCompletion, &refCacheRead, &refCacheWrite,
		&refTotal, &refCost, &refCredits); err != nil {
		t.Fatalf("reference sums: %v", err)
	}

	if resp.Count != refCount {
		t.Errorf("count mismatch: response=%d reference=%d — the merged aggregate must not change the count", resp.Count, refCount)
	}
	if resp.Aggregate.TotalRequests != int64(refCount) {
		t.Errorf("aggregate.total_requests mismatch: response=%d reference=%d",
			resp.Aggregate.TotalRequests, refCount)
	}
	agg := resp.Aggregate
	type pair struct {
		name string
		got  *int64
		want int64
	}
	for _, p := range []pair{
		{"prompt_tokens", agg.PromptTokens, refPrompt},
		{"completion_tokens", agg.CompletionTokens, refCompletion},
		{"cache_read_tokens", agg.CacheReadTokens, refCacheRead},
		{"cache_write_tokens", agg.CacheWriteTokens, refCacheWrite},
		{"total_tokens", agg.TotalTokens, refTotal},
		{"credits_charged", agg.CreditsCharged, refCredits},
	} {
		if p.got == nil {
			t.Errorf("aggregate.%s is null — the merged aggregate must always fill the sums", p.name)
			continue
		}
		if *p.got != p.want {
			t.Errorf("aggregate.%s mismatch: response=%d reference=%d", p.name, *p.got, p.want)
		}
	}
	if agg.CostUSD == nil {
		t.Errorf("aggregate.cost_usd is null")
	} else if *agg.CostUSD != refCost {
		t.Errorf("aggregate.cost_usd mismatch: response=%v reference=%v", *agg.CostUSD, refCost)
	}
}
