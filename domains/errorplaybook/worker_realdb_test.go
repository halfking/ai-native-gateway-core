package errorplaybook

// FetchWindow 的真库契约测试（2026-11-23 审计轮补）。
//
// ★ 为什么必须有这条：摄取 SQL 的两处缺陷曾让「开关已开」的 worker 在生产上
//   静默失败约 22 小时（2026-11-22 00:42 → 22:41 CST，现役 b5eb7d81）：
//     ① `COALESCE(provider_id, '')` —— provider_id 是 integer NOT NULL，
//        PG 把 '' 强转 integer ⇒ 运行期 22P02；
//     ② text[] 列用 `&[]string` 承装 —— 生产驱动是 pgx v5 stdlib，它把数组
//        当字符串返回 ⇒ `unsupported Scan`。
//   而 FetchWindow 当时**没有任何测试**：假 source 测不到 SQL；
//   「默认驱动的单测」也测不到 pgx 的取值行为。
//   ⇒ 只有「真库 + 与生产同款驱动」才能证明这条 SQL 每轮都跑得通。
//
// ★ 自建自清：用**连接局部的 TEMP 表**镜像 392 的列类型，全程钉在一条连接上
//   ⇒ 不依赖、也不触碰目标库里真实的 candidate_failure_logs_hot（它存在与否、
//   有无数据都不影响本测试），连接断开即消失。
//   DSN 取法与 store 真库契约测试同源；没有 DSN 就跳过。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRealDBSQLSourceFetchWindow(t *testing.T) {
	dsn := playbookRealDBDSN(t)
	// 与生产同款驱动族（cmd/gateway/main.go 用 pgx v5 stdlib）。
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	// TEMP 表是连接局部的 ⇒ 整个测试钉在一条物理连接上。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping db: %v", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin conn: %v", err)
	}
	// 列类型镜像 sql/migrations/startup/392（只取 FetchWindow 读到的列）。
	_, err = conn.ExecContext(ctx, `CREATE TEMP TABLE candidate_failure_logs_hot (
		ts              timestamp with time zone NOT NULL,
		error_kind      text NOT NULL,
		error_message   text,
		provider_id     integer NOT NULL,
		raw_model_name  text NOT NULL
	)`)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("create temp table: %v", err)
	}
	var pidSetup int
	if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pidSetup); err != nil {
		_ = conn.Close()
		t.Fatalf("backend pid: %v", err)
	}
	_ = conn.Close() // 归还池；MaxOpen/MaxIdle=1 ⇒ 后续查询仍是这条连接

	// 每次运行用带唯一后缀的 error_kind：万一读到的是目标库里的真实表，
	// 不可能恰好有这些 kind ⇒ 测试会响亮地红，而不是碰巧绿。
	kindA := fmt.Sprintf("fetchwindow_probe_a_%d", time.Now().UnixNano())
	kindB := fmt.Sprintf("fetchwindow_probe_b_%d", time.Now().UnixNano())
	kindEmpty := fmt.Sprintf("fetchwindow_probe_out_%d", time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Microsecond)
	at := func(d time.Duration) time.Time { return now.Add(d) }
	rows := []struct {
		ts      time.Time
		kind    string
		message any // nil ⇒ NULL
		model   string
	}{
		// A：窗口内 3 次（另有窗口外 1 次），带消息与模型。
		{at(-9 * time.Minute), kindA, "a-old", "model-x"},
		{at(-5 * time.Minute), kindA, "a-mid", "model-x"},
		{at(-1 * time.Minute), kindA, "a-new", "model-x"},
		{at(-30 * time.Minute), kindA, "a-before-window", "model-x"}, // since 之前
		// B：窗口内 1 次，模型为空串（NOT NULL 列的合法空值）。
		{at(-2 * time.Minute), kindB, "b-msg", ""},
		// 以下都不得进入结果。
		{at(10 * time.Minute), kindB, "b-after-window", ""}, // until 之后
		{at(-3 * time.Minute), "", "empty-kind", ""},        // error_kind 被过滤
		{at(-3 * time.Minute), kindEmpty, nil, ""},          // 另一个带后缀的 kind：在窗口内，应正常返回
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO candidate_failure_logs_hot (ts, error_kind, error_message, provider_id, raw_model_name)
			VALUES ($1, $2, $3, $4, $5)`, r.ts, r.kind, r.message, 7, r.model); err != nil {
			t.Fatalf("insert probe row: %v", err)
		}
	}

	src := NewSQLSource(db)
	since, until := at(-10*time.Minute), now
	events, maxTS, err := src.FetchWindow(ctx, since, until)
	if err != nil {
		t.Fatalf("FetchWindow (this query must run against INT NOT NULL + text[] with the pgx driver): %v", err)
	}

	// 同一条连接（否则读的不是上面的 TEMP 表）。
	var pidAfter int
	if err := db.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pidAfter); err != nil {
		t.Fatalf("backend pid after: %v", err)
	}
	if pidAfter != pidSetup {
		t.Fatalf("FetchWindow ran on another connection (pid %d, want %d) — TEMP table not visible", pidAfter, pidSetup)
	}

	// ORDER BY MAX(ts) ASC：各分组按窗口内最晚一次出现时间升序
	// ⇒ kindEmpty（-3m）、B（-2m）、A（-1m）。
	if len(events) != 3 {
		t.Fatalf("want 3 grouped events in window, got %d: %+v", len(events), events)
	}
	mid, b, a := events[0], events[1], events[2]
	if mid.ErrorType != kindEmpty || b.ErrorType != kindB || a.ErrorType != kindA {
		t.Fatalf("grouping/order broken: %q %q %q", mid.ErrorType, b.ErrorType, a.ErrorType)
	}
	// error_message 为 NULL（生产列可空）不得让整轮 Scan 失败，也不得变成字符串 "NULL"。
	if len(mid.Samples) != 0 || mid.Message != "" {
		t.Fatalf("NULL error_message must yield no sample, got samples=%q message=%q", mid.Samples, mid.Message)
	}
	if b.Occurrences != 1 || !b.LastSeen.Equal(at(-2*time.Minute)) || b.Message != "b-msg" || b.Model != "" {
		t.Fatalf("event B mismatch: %+v", b)
	}
	// A：窗口外那次不计；samples 按 ts DESC（最新在前）；数组列经 pq.Array 正确落进 []string。
	if a.Occurrences != 3 {
		t.Fatalf("A occurrences: want 3 (out-of-window row excluded), got %d", a.Occurrences)
	}
	if !a.FirstSeen.Equal(at(-9*time.Minute)) || !a.LastSeen.Equal(at(-1*time.Minute)) {
		t.Fatalf("A window bounds mismatch: first=%v last=%v", a.FirstSeen, a.LastSeen)
	}
	if want := "a-new,a-mid,a-old"; strings.Join(a.Samples, ",") != want {
		t.Fatalf("A samples: want %q, got %q", want, strings.Join(a.Samples, ","))
	}
	if a.Message != "a-new" || a.Provider != "7" || a.Model != "model-x" {
		t.Fatalf("A derived fields mismatch: %+v", a)
	}
	if !maxTS.Equal(at(-1 * time.Minute)) {
		t.Fatalf("maxTS: want %v, got %v", at(-time.Minute), maxTS)
	}

	// 窗口内无行 ⇒ 空结果，maxTS 回落到 since（worker 依赖这一点推进水位线）。
	emptySince := at(-2 * time.Hour)
	if evs, ts, err := src.FetchWindow(ctx, emptySince, at(-time.Hour)); err != nil || len(evs) != 0 || !ts.Equal(emptySince) {
		t.Fatalf("empty window: events=%d maxTS=%v err=%v", len(evs), ts, err)
	}
}
