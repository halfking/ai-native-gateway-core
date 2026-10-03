// turn_logs_aggregator_crossmonth_realdb_test.go — 2026-10-04 §9.117 真库门。
//
// 缺陷：`public.sessions` 是 PARTITION BY RANGE (partition_date)，唯一键只能是
// UNIQUE (session_id, partition_date)，所以一个跨月会话**每月各有一行**
// （生产 252 实测 413 个）。`AggregateAndFlush` 的读改写却假设一个
// (tenant, session) 只有一行：
//
//	flushLockQuery   SELECT turn_logs_summary … FOR UPDATE   （无 partition_date 谓词）
//	flushUpdateQuery UPDATE … SET turn_logs_summary = …       （无 partition_date 谓词）
//
// ⇒ 锁语句匹配 2+ 行、`QueryRow` 静默取其中一行当 merge 种子，
// 而 UPDATE 把结果写进**所有**行 ⇒ 用一个月的累积摘要盖掉另一个月的。
//
// 夹具必须是"两行摘要不同"才有判别力：若两行摘要本来就相同，写错行也看不出来。
// 这是本门与「只断行数」的门的关键区别。
//
// **本缺陷在生产从未触发**（`session_turn_logs` 0 行、`turn_logs_summary`
// 177500/177500 全 NULL），所以本门是构造出来的场景，不是对现网损失的追认。
// 这一点必须写在门里，否则下一个人会以为它在证明一起真实事故。
//
// 建池照抄产品 db/db.go 的 SimpleProtocol。零足迹：cleanup 删自己的行。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func turnLogsRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 本门不构成跨月 flush 安全的证据")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Skipf("parse dsn: %v", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// sessionsMonthBounds 返回 sessions 分区下界，降序。
func sessionsMonthBounds(t *testing.T, pool *pgxpool.Pool) []time.Time {
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
		t.Fatalf("iterate partition bounds: %v", err)
	}
	return out
}

// TestAggregateAndFlush_CrossMonthSessionWritesOnlyNewestPartitionRow 是本门的
// 核心断言：跨月会话的 flush 只应改最新分区那行，另一行必须逐字节不变。
func TestAggregateAndFlush_CrossMonthSessionWritesOnlyNewestPartitionRow(t *testing.T) {
	pool := turnLogsRealDBPool(t)
	ctx := context.Background()

	bounds := sessionsMonthBounds(t, pool)
	if len(bounds) < 2 {
		t.Skipf("sessions 只有 %d 个分区，本门需要 >= 2 个才能造跨月会话", len(bounds))
	}
	newest, older := bounds[0], bounds[1]

	suffix := fmt.Sprintf("xtlogs-%d", time.Now().UnixNano())
	sessionID := "xtlogs-" + suffix
	tenantID := "xtlogs-tenant-" + suffix

	// 两个分区各一行，turn_logs_summary 刻意不同 —— 判别力全在这里。
	//
	// 必须是**合法的 LogSummary 形状**：mergeSummaries 把 existing 反序列化成
	// map[string]LogSummary，任一顶层键解析失败就整份丢弃（已文档化的容错路径，
	// 见 TestMergeSummaries_NullAndGarbageExistingFallBackToPayload）。
	// 我第一版塞了 `{"turn_1":{"request":…},"__marker":"…"}` 这种形状，
	// existing 直接变垃圾被整份丢弃，门红在"丢了累积值"上 —— 看着像产品 bug，
	// 实际是夹具写错了。所以判别标记放进 turn_1 的 stage 名里：合法，且两行必然不同。
	oldSummary := `{"turn_1":{"stages":[{"stage":"routing","status":"success","latency_ms":7,"started_at":"2026-01-01T00:00:00Z"}],"turn_no":1,"built_at":"2026-01-01T00:00:00Z"}}`
	newSummary := `{"turn_1":{"stages":[{"stage":"compression","status":"success","latency_ms":9,"started_at":"2026-09-01T00:00:00Z"}],"turn_no":1,"built_at":"2026-09-01T00:00:00Z"}}`

	insertSession := `
		INSERT INTO public.sessions
			(session_id, tenant_id, partition_date, status, turn_logs_summary)
		VALUES ($1,$2,$3,'active',$4::jsonb), ($1,$2,$5,'active',$6::jsonb)`
	if _, err := pool.Exec(ctx, insertSession,
		sessionID, tenantID, older, oldSummary, newest, newSummary); err != nil {
		t.Fatalf("insert cross-month session rows: %v", err)
	}

	// 一条待聚合的 stage 行，挂在 turn 2 上（与 turn_1 区分开）。
	//
	// latency_ms / completed_at 必须给：两列在 schema 里可空，而
	// flushSelectQuery 把 latency_ms 扫进**非指针** int
	// （turn_logs_aggregator.go:262），NULL 会让 flush 直接报 scan 错。
	// 这不是缺陷——生产写侧 turn_logs_writer.go:136 恒计算 latencyMs（负值归 0），
	// 从不写 NULL。我第一版夹具漏了这两列，门红在 scan 错上，看着像产品 bug。
	// 记在这里，免得下一个用直接 SQL 写这张表的人把它当成真的。
	insertStage := `
		INSERT INTO public.session_turn_logs
			(session_id, turn_no, tenant_id, request_id, stage, stage_status, event_data,
			 started_at, completed_at, latency_ms, expires_at)
		VALUES ($1, 2, $2, $3, 'routing', 'success', '{}'::jsonb,
			NOW() - interval '1 second', NOW(), 1000, NOW() + interval '1 hour')`
	if _, err := pool.Exec(ctx, insertStage, sessionID, tenantID, sessionID+"-req"); err != nil {
		t.Fatalf("insert pending stage row: %v", err)
	}

	t.Cleanup(func() {
		c := context.Background()
		if _, err := pool.Exec(c, `DELETE FROM public.session_turn_logs WHERE session_id = $1`, sessionID); err != nil {
			t.Logf("cleanup session_turn_logs: %v", err)
		}
		if _, err := pool.Exec(c,
			`DELETE FROM public.sessions WHERE session_id = $1 AND tenant_id = $2`,
			sessionID, tenantID); err != nil {
			t.Logf("cleanup sessions: %v", err)
		}
	})

	// 先确认前提成立：确实两行、且摘要不同。
	var n int
	var distinctSummaries int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(DISTINCT turn_logs_summary::text)
		FROM public.sessions WHERE session_id = $1 AND tenant_id = $2`,
		sessionID, tenantID).Scan(&n, &distinctSummaries); err != nil {
		t.Fatalf("count fixture rows: %v", err)
	}
	if n != 2 || distinctSummaries != 2 {
		t.Fatalf("夹具前提不成立：期望 2 行且摘要互不相同，实际 %d 行 / %d 种摘要", n, distinctSummaries)
	}

	agg := &TurnLogsAggregator{db: pool}
	// flush 之前先读一次旧行的内容。**不能做字节比较**：turn_logs_summary 是
	// jsonb，PG 存取时会规范化空白（`"a":1` 存成 `"a": 1`），所以"逐字节保持"
	// 会在第一次读回时就假红——我第一版就是这么红的，红的还是没被改动的行。
	// 要比的是**语义**：解析成结构后比较。
	var beforeOld map[string]stageOnlyOfTurn
	if err := pool.QueryRow(ctx, `
		SELECT turn_logs_summary FROM public.sessions
		WHERE session_id = $1 AND tenant_id = $2 AND partition_date = $3`,
		sessionID, tenantID, older).Scan(&beforeOld); err != nil {
		t.Fatalf("read older row before flush: %v", err)
	}

	if err := agg.AggregateAndFlush(ctx, tenantID, sessionID); err != nil {
		t.Fatalf("AggregateAndFlush: %v", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT partition_date, turn_logs_summary FROM public.sessions
		WHERE session_id = $1 AND tenant_id = $2 ORDER BY partition_date DESC`, sessionID, tenantID)
	if err != nil {
		t.Fatalf("read back summaries: %v", err)
	}
	defer rows.Close()
	type got struct {
		date    time.Time
		summary string
	}
	var byDate []got
	for rows.Next() {
		var g got
		var raw []byte
		if err := rows.Scan(&g.date, &raw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		g.summary = string(raw)
		byDate = append(byDate, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(byDate) != 2 {
		t.Fatalf("期望读回 2 行，实际 %d", len(byDate))
	}

	// 断言一：最新分区那行必须被合并 —— 含新 stage 落的 turn_2，
	// 且它自己原有的 turn_1（stage=compression）必须被保留。
	type stageOnly struct {
		Stages []struct {
			Stage string `json:"stage"`
		} `json:"stages"`
	}
	var newestPayload map[string]stageOnly
	if err := json.Unmarshal([]byte(byDate[0].summary), &newestPayload); err != nil {
		t.Fatalf("unmarshal newest summary: %v (%s)", err, byDate[0].summary)
	}
	// 用 Errorf 不用 Fatalf：两条断言各自独立成立，短路掉第二条就等于
	// 只验了半个性质。变异（去掉 partition_date 谓词）时实测两条都要报。
	if _, ok := newestPayload["turn_2"]; !ok {
		t.Errorf("最新分区行未被合并：缺少 turn_2。实际=%s", byDate[0].summary)
	}
	if stages := newestPayload["turn_1"].Stages; len(stages) != 1 || stages[0].Stage != "compression" {
		t.Errorf("最新分区行合并时丢了它自己的累积值：turn_1.stages=%+v（期望 stage=compression）", stages)
	}

	// 断言二（要害）：旧分区那行不得被这次 flush 改写。
	//
	// 修复前 flushUpdateQuery 没有 partition_date 谓词，会把同一份 merged
	// payload（**含 turn_2**）写进两行 ⇒ 旧行被覆盖，静默内容丢失。
	// 所以判据不只看"内容没变"，更看"不得出现只有新数据才有的 turn_2"。
	var afterOld map[string]stageOnlyOfTurn
	if err := pool.QueryRow(ctx, `
		SELECT turn_logs_summary FROM public.sessions
		WHERE session_id = $1 AND tenant_id = $2 AND partition_date = $3`,
		sessionID, tenantID, older).Scan(&afterOld); err != nil {
		t.Fatalf("read older row after flush: %v", err)
	}
	if _, leaked := afterOld["turn_2"]; leaked {
		t.Fatalf("旧分区行被这次 flush 写入了 turn_2 —— merged payload 落到了不该落的行上"+
			"（flush 的锁语句与更新语句必须都限定在 MAX(partition_date) 那一行）。"+
			"读回=%s", mustJSONOf(afterOld))
	}
	if !reflect.DeepEqual(afterOld, beforeOld) {
		t.Fatalf("旧分区行内容被改写：flush 前=%s flush 后=%s",
			mustJSONOf(beforeOld), mustJSONOf(afterOld))
	}
}

type stageOnlyOfTurn struct {
	Stages []struct {
		Stage     string `json:"stage"`
		Status    string `json:"status"`
		LatencyMs int    `json:"latency_ms"`
		StartedAt string `json:"started_at"`
	} `json:"stages"`
	TurnNo  int    `json:"turn_no"`
	BuiltAt string `json:"built_at"`
}

func mustJSONOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<unmarshalable: %v>", err)
	}
	return string(b)
}
