// turns_sessions_crossmonth_realdb_test.go — 2026-10-04 §9.114 真库门。
//
// 缺陷（§9.112/§9.113）：`public.sessions` 是 PARTITION BY RANGE (partition_date)，
// 而 PostgreSQL 硬规则要求分区表唯一约束必须含分区键，故唯一键只能是
// UNIQUE (session_id, partition_date) —— **一会话多行是合法状态**（252 上 413 个
// (session_id, tenant_id) 组合多行，全部跨 partition_date）。
//
// 会话列表查询 turnsSessionsListSQL() 直接 `FROM public.sessions s` 而无去重，
// 于是跨月会话在列表里出现**重影**：同一 session_id 两行，各显示一半状态。
// 写侧 titlestore（store.go CommitTitle/DeleteTitle）早已用
// `partition_date = (SELECT MAX(partition_date) ...)` 只投影最新行，读侧没跟上。
//
// 本门打穿**真实 handler 路径**（构造 Handler 走 handleTurnsSessions），而不是
// 自己拼 SQL —— 否则量的不是被检验对象。两个子场景：
//
//	A. 无额外过滤：跨月同 session_id 必须只出现 1 行，**且是最新分区那行**
//	   （只断行数不够：任意去重都可能凑够 1 行却取错行，所以同时断内容）。
//	B. `?client=` 过滤且只有**旧**行匹配：必须 0 行。去重谓词若与调用方过滤
//	   拼成 `latest AND a OR b OR c`（漏括号），旧行会从 OR 支路漏进来变成 1 行。
//	   这是"括号正确性"的语义证据 —— 字符串里有没有 '(' 断不了这件事。
//
// 建池照抄产品 db/db.go 的 SimpleProtocol（否则量的不是生产语义）。
// 零足迹：测试自建 session_id 前缀并在 cleanup 里 DELETE 自己的行。
package admin

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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func crossMonthRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 本门不构成跨月去重已修的证据")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Skipf("parse dsn: %v", err)
	}
	// 生产全量 SimpleProtocol（db/db.go:72）——参数内联语义只有该模式可证。
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// sessionsPartitionLowerBounds 返回 sessions 各分区的下界日期，降序（最新月在前）。
//
// ORDER BY 不是装饰：没有它时 pg_inherits 的返回顺序与分区月份无关，
// bounds[0]/bounds[1] 的"最新/次新"标签会随机反转，于是本门会去断言一条
// 根本没写的期望（我第一版就踩了：两个子场景都红，但红的原因是夹具标签
// 反了，不是产品缺陷）。
func sessionsPartitionLowerBounds(t *testing.T, pool *pgxpool.Pool) []time.Time {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT (regexp_match(pg_get_expr(c.relpartbound, c.oid),
			'FROM \(''([0-9]{4}-[0-9]{2}-[0-9]{2})'''))[1]::date AS lo
		FROM pg_class c JOIN pg_inherits i ON i.inhrelid = c.oid
		WHERE i.inhparent = 'public.sessions'::regclass
		ORDER BY lo DESC`)
	if err != nil {
		t.Fatalf("probe sessions partitions: %v", err)
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("scan partition lower bound: %v", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate partition lower bounds: %v", err)
	}
	return out
}

// callTurnsSessions 打真实 handler，返回 items。
func callTurnsSessions(t *testing.T, pool *pgxpool.Pool, query string) []map[string]any {
	t.Helper()
	h := &Handler{db: pool, secret: "crossmonth-realdb-test-secret"}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/turns/sessions"+query, nil)
	rec := httptest.NewRecorder()
	h.handleTurnsSessions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleTurnsSessions(%s) = %d, body=%s", query, rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return payload.Items
}

func TestTurnsSessionsList_CrossMonthSessionAppearsOnce_RealDB(t *testing.T) {
	pool := crossMonthRealDBPool(t)
	ctx := context.Background()

	// 至少要两个分区才能造出「跨月」；单分区库上本门不构成证据。
	bounds := sessionsPartitionLowerBounds(t, pool)
	if len(bounds) < 2 {
		t.Skipf("sessions 只有 %d 个分区，本门需要 >= 2 个才能造跨月会话", len(bounds))
	}
	newest, older := bounds[0], bounds[1]

	suffix := fmt.Sprintf("xturns-xmonth-%d", time.Now().UnixNano())
	sessionID := "xturns-xmonth-" + suffix
	tenantID := "xturns-xmonth-tenant-" + suffix
	const (
		oldClient = "MATCH-OLD-ROW"
		newClient = "no-match-new-row"
	)

	// 同一 (tenant_id, session_id) 两行，唯一差别是 partition_date / status / client_type。
	// 这正是 PG 分区规则下唯一键允许的形态。
	insert := `
		INSERT INTO public.sessions
			(session_id, tenant_id, partition_date, status, client_type, created_at, updated_at, total_turns)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW(), $6), ($1, $2, $7, $8, $9, NOW(), NOW(), $10)`
	if _, err := pool.Exec(ctx, insert,
		sessionID, tenantID, older, "active", oldClient, 1,
		newest, "closed", newClient, 2); err != nil {
		t.Fatalf("insert cross-month session rows: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM public.sessions WHERE session_id = $1 AND tenant_id = $2`,
			sessionID, tenantID); err != nil {
			t.Logf("cleanup cross-month rows: %v", err)
		}
	})

	// 先确认前提成立：库里确实存在两行。若这一步就不成立，后面所有断言都是空的。
	var rawRows int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM public.sessions
		WHERE session_id = $1 AND tenant_id = $2`, sessionID, tenantID).Scan(&rawRows); err != nil {
		t.Fatalf("count raw rows: %v", err)
	}
	if rawRows != 2 {
		t.Fatalf("前提不成立：跨月 fixture 应写入 2 行，实际 %d 行", rawRows)
	}

	t.Run("A_无过滤_只出现一行且取最新分区行", func(t *testing.T) {
		items := callTurnsSessions(t, pool, "?tenant="+tenantID+"&limit=200")
		var mine []map[string]any
		for _, it := range items {
			if it["session_id"] == sessionID {
				mine = append(mine, it)
			}
		}
		if len(mine) != 1 {
			t.Fatalf("跨月会话在列表里出现 %d 次（session_id=%s）—— 一会话多行未被去重；返回的 rows=%s",
				len(mine), sessionID, mustJSON(mine))
		}
		// 只断行数不够：必须断取的是**最新**分区那行（status='closed', total_turns=2）。
		if got := mine[0]["status"]; got != "closed" {
			t.Fatalf("取到的不是最新分区行：status=%v（最新分区应为 closed/旧分区为 active）", got)
		}
		if got := mine[0]["total_turns"]; got != float64(2) {
			t.Fatalf("取到的不是最新分区行：total_turns=%v（最新分区应为 2）", got)
		}
	})

	t.Run("B_顶层OR过滤_去重谓词必须被括号包住", func(t *testing.T) {
		// client_type 只有**旧**分区行等于 MATCH-OLD-ROW。正确行为：那一行不是最新
		// 分区行，先被去重谓词排除，于是整体 0 命中。若去重谓词与过滤条件拼成
		// `latest AND a OR b OR c`（漏括号），旧行会从 OR 支路漏进来 → 1 行 → 红。
		items := callTurnsSessions(t, pool, "?tenant="+tenantID+"&client="+oldClient+"&limit=200")
		for _, it := range items {
			if it["session_id"] == sessionID {
				t.Fatalf("只有旧分区行匹配 client=%s，却仍被返回 —— 去重谓词与调用方过滤的 AND/OR "+
					"结合顺序有误（少了括号）：返回 %s", oldClient, mustJSON(it))
			}
		}
	})
}

// TestTurnsSessionsFinalizeWhere_ParenPinsComposition 纯字符串层：钉住合成形态。
// 真库门（B 子场景）断的是**语义**；这条断的是"调用方过滤整体被括起来"这一
// 形态不会被后续重构悄悄拆掉——两者观测量不同，不是重复的门。
func TestTurnsSessionsFinalizeWhere_ParenPinsComposition(t *testing.T) {
	cases := []struct {
		name   string
		filter string
		want   string
	}{
		{"空过滤只有去重谓词", "", "MAX(x.partition_date)"},
		{"非空过滤被整体括起", "s.tenant_id = $1", "AND (s.tenant_id = $1)"},
		{"过滤含内部 OR 不被二次拆散", "(sd.client_id = $2 OR s.client_type = $2)",
			"AND ((sd.client_id = $2 OR s.client_type = $2))"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := turnsSessionsFinalizeWhere(c.filter)
			if !strings.Contains(got, c.want) {
				t.Fatalf("turnsSessionsFinalizeWhere(%q) 未含 %q\n实际=%q", c.filter, c.want, got)
			}
			if strings.Contains(got, "request_logs") {
				t.Fatalf("去重谓词不得把 request_logs 拖回主查询：%q", got)
			}
		})
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<unmarshalable: %v>", err)
	}
	return string(b)
}
