// Command backfill-session-opened generates retroactive session.opened.v1 outbox
// events for sessions that were never given an opener (e.g. historical rows
// predating the terminal-path补发 fix, or rows whose opener was lost).
//
// Default mode is DRY-RUN: it only prints the candidate count and a sample.
// Pass --execute to perform inserts. Inserts are idempotent (ON CONFLICT
// DO NOTHING on event_id), so re-running is safe.
//
// Design (2026-10-31, audit §8.2):
//   - Source of truth for "which sessions exist" = public.session_turns_hot
//     (and its parent public.session_turns), grouped by (tenant_id, session_id).
//     ★ 2026-11-01（12h 审计轮）：这条口径现在是**真的**——原实现只读 hot 表，
//     而 hot 表 8 小时后被排水到父表 ⇒ 历史会话根本不在候选源里（实测 13 个
//     会话只捞到 3 个）。修正见下面候选查询的两臂并读注释。
//   - A session is a candidate iff no row in outbox_events has
//     event_type='session.opened.v1' and aggregate_id=session_id.
//   - user_id is derived like the live path (sessionOpenedUserID): end_user_id
//     when present, else api_key_id, else "unknown".
//   - occurred_at is taken from the earliest turn's ts (not now()), so ASM
//     ordering stays chronological.
//     ★ 2026-11-01 修正：取的是该会话的**全局最早** turn（窗口谓词作用在
//     会话级，不是 turn 行级）——否则跨窗口会话的 occurred_at 会错位。
//
// This tool does NOT dispatch events. After backfill, run outbox-dlq-replay
// (or wait for the dispatcher) to deliver them.
//
// ★ 2026-11-01 12h 审计轮（0310 档 §3.5；数据源/窗口语义两修由并行 0720 轮
// 先落为 38dbec6c9，本层取两边并集）：补发工具的**失败面**——失败不计数 /
// 退出码恒 0 / 单一 10 分钟 ctx 同时覆盖候选查询与全部 INSERT（--limit 10000
// 时插入阶段可能整体 deadline 且失败被吞 ⇒ 静默零产出）。⇒ 查询与写入各有
// 独立预算、写入按块提交事务（每块独立 ctx）、失败按块计数、failed>0 时非零退出。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	_ "github.com/lib/pq"
)

// insertChunkSize 是每个事务里提交的候选会话数。
//
// ★ 为什么分块提交而不是逐条 autocommit（2026-11-01 12h 审计轮）★：
// 逐条 INSERT = 每条一次往返 ⇒ --limit 10000 就是 10000 次网络往返 ⇒
// 在生产库上极易把写入阶段的预算耗尽 ⇒ 而预算耗尽的表现是「context deadline
// exceeded」逐条刷屏 ⇒ 失败又没被计数 ⇒ **静默零产出**。
// 分块事务把往返数降下来，且**一块失败只丢一块**（不是整批 10000 条）。
const insertChunkSize = 200

// cand 是一个待补发的会话候选。
type cand struct {
	tenantID   string
	sessionID  string
	userID     string
	occurredAt time.Time
}

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "PostgreSQL DSN (default: $DATABASE_URL)")
	fromRaw := flag.String("from", "", "only sessions whose earliest turn ts >= this RFC3339 UTC time")
	toRaw := flag.String("to", "", "only sessions whose earliest turn ts < this RFC3339 UTC time (exclusive)")
	limit := flag.Int("limit", 1000, "maximum sessions to process (max 10000)")
	execute := flag.Bool("execute", false, "perform inserts; default is dry-run")
	verbose := flag.Bool("verbose", false, "print every candidate")
	flag.Parse()

	if strings.TrimSpace(*dsn) == "" {
		log.Fatal("--dsn required (or set DATABASE_URL)")
	}
	if *limit < 1 || *limit > 10000 {
		log.Fatal("--limit must be in [1,10000]")
	}
	// ★ 执行模式要求 --from（2026-11-01 12h 审计轮）★：
	// session_turns 是**按 partition_date 分区**的大表，而本工具按 ts 过滤
	// ⇒ 分区裁剪**不保证生效** ⇒ 不限定范围就是一次全历史扫描 + 排序
	// ⇒ ⇒ 对生产库是高代价操作。⇒ 强制操作者显式给出时间下界。
	if *execute && strings.TrimSpace(*fromRaw) == "" {
		log.Fatal("--execute requires --from <RFC3339 UTC>: session_turns is a monthly-partitioned table filtered on ts, " +
			"so an unbounded scan is a full-history sort on the production database. Scope the run explicitly.")
	}

	db, err := sql.Open("postgres", *dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	// 查询阶段与写入阶段各有独立预算（2026-11-01 12h 审计轮）：单一 ctx 会把
	// 候选查询的耗时算进写入预算里 ⇒ --limit 大时插入阶段整体 deadline。
	queryCtx, cancelQuery := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelQuery()
	if err := db.PingContext(queryCtx); err != nil {
		log.Fatalf("ping db: %v", err)
	}

	// Build the candidate query. We group by (tenant_id, session_id) and pick
	// the earliest turn's ts + user derivation. NOT EXISTS against outbox_events
	// keeps already-opened sessions out.
	//
	// ★ 2026-11-01（12h 审计轮）：两条口径修正。
	//
	// ① **两侧并读**（原来只读 hot 表）：`session_turns_hot` 是**独立堆写表**，
	//	冷行由 `promote_session_turns_hot_to_partition` 搬走（DELETE hot +
	//	INSERT parent，单语句 CTE 原子）⇒ 保留期一过（生产 8h，见
	//	`lifecycle.hot_retention_hours` / `bg/partition_manager.go` 的
	//	`promote_session_turns_hot_to_partition` 常态调度）⇒ **历史会话根本不在
	//	hot 表里** ⇒ 只读 hot 的工具在生产上捞不到它要补发的历史会话
	//	（实测：13 个会话里只捞到 3 个，漏掉的 10 个全在父表）。
	//	⇒ 改用仓内 canonical 形态（`internal/sessionfamily/exists_by_key.go` 的
	//	`ExistsByKeySQL` 同款两臂 UNION ALL），**不依赖视图**——视图
	//	`session_turns_with_current_month` 不投影 `end_user_id`/`api_key_id`，
	//	而本工具的 user_id 派生要用这两列 ⇒ 必须显式两臂并读。
	//
	// ② **窗口谓词作用在会话级**（原来作用在 turn 行级）：原来
	//	`WHERE ts >= $1 AND ts < $2` 先过滤 **turn 行**，`DISTINCT ON` 再在
	//	过滤后的集合里取最早 ⇒ 一个「窗口之前开始、窗口之内有后续 turn」的
	//	会话，其 `occurred_at` 会变成**窗口内那个 turn** 的时间，而不是会话
	//	真正的开始时间 ⇒ 补发的事件时间线错位（实测：真开始 2026-08-15 的
	//	会话被写成 2026-09-10）。
	//	⇒ 改为：先按 (tenant_id, session_id) 取该会话的**全局最早 turn**
	//	（DISTINCT ON 不带窗口谓词），外层再对 `occurred_at` 施加窗口过滤
	//	⇒ 窗口语义变成「会话整体落在窗内」。
	//
	// ③ `NOT EXISTS` 的反连接也按两侧分别下推（与 ② 同理）：
	//	若只在 hot 侧查「是否已有 opened 事件」，父表里已有的 opened 事件
	//	会被漏判 ⇒ 同一会话可能被重复补发（幂等键 event_id 相同 ⇒ INSERT
	//	会被 ON CONFLICT DO NOTHING 吞掉，但 `skipped` 计数会失真）。
	q := `
		SELECT s.tenant_id, s.session_id, s.user_id, s.occurred_at
		FROM (
			SELECT DISTINCT ON (tenant_id, session_id)
				tenant_id, session_id,
				COALESCE(NULLIF(end_user_id, ''), NULLIF(CAST(api_key_id AS text), ''), 'unknown') AS user_id,
				ts AS occurred_at
			FROM (
				SELECT tenant_id, session_id, end_user_id, api_key_id, ts
				FROM session_turns_hot
				UNION ALL
				SELECT tenant_id, session_id, end_user_id, api_key_id, ts
				FROM session_turns
			) all_turns
			ORDER BY tenant_id, session_id, ts ASC
		) s
		WHERE ($1::timestamptz IS NULL OR s.occurred_at >= $1)
		  AND ($2::timestamptz IS NULL OR s.occurred_at < $2)
		  AND NOT EXISTS (
			SELECT 1 FROM outbox_events o
			WHERE o.event_type = 'session.opened.v1'
			  AND o.aggregate_id = s.session_id
			  AND o.tenant_id = s.tenant_id
		  )
		ORDER BY s.occurred_at ASC
		LIMIT $3
	`
	var fromPtr, toPtr interface{}
	if strings.TrimSpace(*fromRaw) != "" {
		t, err := time.Parse(time.RFC3339, *fromRaw)
		if err != nil {
			log.Fatalf("--from must be RFC3339 UTC: %v", err)
		}
		fromPtr = t.UTC()
	}
	if strings.TrimSpace(*toRaw) != "" {
		t, err := time.Parse(time.RFC3339, *toRaw)
		if err != nil {
			log.Fatalf("--to must be RFC3339 UTC: %v", err)
		}
		toPtr = t.UTC()
	}

	rows, err := db.QueryContext(queryCtx, q, fromPtr, toPtr, *limit)
	if err != nil {
		log.Fatalf("candidate query: %v", err)
	}
	defer rows.Close()

	var collected []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.tenantID, &c.sessionID, &c.userID, &c.occurredAt); err != nil {
			log.Fatalf("scan candidate: %v", err)
		}
		collected = append(collected, c)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows: %v", err)
	}

	fmt.Printf("candidates=%d (limit=%d, from=%s, to=%s)\n", len(collected), *limit, *fromRaw, *toRaw)
	if len(collected) == 0 {
		fmt.Println("nothing to do")
		return
	}
	for i, c := range collected {
		if i < 5 || *verbose {
			fmt.Printf("  sample tenant=%s session=%s user=%s occurred=%s\n",
				c.tenantID, c.sessionID, c.userID, c.occurredAt.UTC().Format(time.RFC3339))
		}
	}

	if !*execute {
		fmt.Println("DRY-RUN: no inserts performed. Re-run with --execute to apply.")
		return
	}

	inserted, skipped, failed := 0, 0, 0
	for start := 0; start < len(collected); start += insertChunkSize {
		end := start + insertChunkSize
		if end > len(collected) {
			end = len(collected)
		}
		chunk := collected[start:end]
		chunkInserted, chunkSkipped, chunkFailed := insertChunk(db, chunk)
		inserted += chunkInserted
		skipped += chunkSkipped
		failed += chunkFailed
	}

	// ★ 失败必须出现在收尾行里（2026-11-01 12h 审计轮）★：原来的收尾行只有
	// inserted / skipped ⇒ 失败既不在数字里、也不影响退出码 ⇒ "done" 一行
	// 看起来像「全部成功」。⇒ 现在三段计数齐发，failed>0 时非零退出。
	fmt.Printf("done: inserted=%d skipped_already_present=%d failed=%d (candidates=%d)\n",
		inserted, skipped, failed, len(collected))
	if failed > 0 {
		log.Fatalf("backfill incomplete: %d of %d candidates failed; re-run is safe (inserts are idempotent)", failed, len(collected))
	}
}

// insertChunk 在**一个事务**里提交一块候选，失败时按块计失败。
//
// ★ 2026-11-01 12h 审计轮：失败计数是本函数存在的理由 ★
// ——逐条 autocommit 的写法里，任何一条失败都会被 log.Printf 吞掉，
// 而收尾数字看不见它 ⇒ 工具可以「打印成功但实际一条没写进去」。
// 注意：块内单条失败只丢那一条（continue），**commit 失败才整块回滚**——
// 失败面收窄到「真正写不进去的那几条」，而不是「同块的 200 条」。
func insertChunk(db *sql.DB, chunk []cand) (inserted, skipped, failed int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("begin chunk failed (%d candidates): %v", len(chunk), err)
		return 0, 0, len(chunk)
	}
	defer tx.Rollback()

	for _, c := range chunk {
		env, err := outbox.BuildSessionOpenedEventV1(c.tenantID, c.sessionID, c.userID)
		if err != nil {
			failed++
			log.Printf("build event failed tenant=%s session=%s: %v", c.tenantID, c.sessionID, err)
			continue
		}
		// Override occurred_at with the earliest turn's ts (chronological).
		env.OccurredAt = c.occurredAt.UTC()

		payloadJSON, err := marshalPayload(env)
		if err != nil {
			failed++
			log.Printf("marshal payload tenant=%s session=%s: %v", c.tenantID, c.sessionID, err)
			continue
		}
		tag, err := tx.ExecContext(ctx, `
			INSERT INTO outbox_events (
				event_id, event_type, schema_version, tenant_id,
				aggregate_id, aggregate_version, occurred_at, payload, status
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
			ON CONFLICT (event_id) DO NOTHING
		`,
			env.EventID, env.EventType, env.SchemaVersion, env.TenantID,
			env.AggregateID, env.AggregateVersion, env.OccurredAt,
			string(payloadJSON),
		)
		if err != nil {
			failed++
			log.Printf("insert failed tenant=%s session=%s: %v", c.tenantID, c.sessionID, err)
			continue
		}
		rowsAffected, _ := tag.RowsAffected()
		if rowsAffected == 0 {
			skipped++
		} else {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		// 提交失败 ⇒ 整块的写入都没落库（事务回滚）⇒ 按整块计失败。
		log.Printf("commit chunk failed (%d candidates): %v", len(chunk), err)
		return 0, 0, len(chunk)
	}
	return inserted, skipped, failed
}

func marshalPayload(env outbox.EventEnvelope) ([]byte, error) {
	// BuildSessionOpenedEventV1 already populated env.Payload; marshal it.
	b, err := json.Marshal(env.Payload)
	if err != nil {
		return nil, err
	}
	return b, nil
}
