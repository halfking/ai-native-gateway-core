// Command backfill_session_bodies derives and writes per-turn message deltas into
// public.session_bodies for one session, sourced from the accumulated
// request-log bodies (the matryoshka). It is part of the "full switch to
// Sessions V2" preparation: historical sessions that predate the V2 dual-write
// have no session_bodies, so this tool backfills them so V2 can serve the detail
// page.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：**本工具退役**。
//
// # 为什么不能改指会话族（这是本文件唯一需要说明的事）
//
// 这个工具的输入是**每轮的累积正文**，输出是**每轮的增量**（matryoshka 拆解）。
// 而 v1 累积正文那一族（v1 bodies 表）已随迁移 890 一起被 DROP ⇒ 输入没了。
//
// 会话族里剩下的两个东西**都不能**当输入：
//
//   - session_bodies 存的是**每轮增量**（request_delta / response_delta），
//     拿增量再拆增量是**恒等操作**——算出来的还是增量，等于把已有数据原样写回；
//   - session_bodies 里 kind='final_full' 的行是整个会话的**扁平消息列表**，
//     它没有轮次边界 ⇒ 无法据此还原「每一轮各带哪些消息」。
//
// ⇒ 没有输入 ⇒ 没有可回填的东西 ⇒ 工具报「v1 族已退役」并退出，
// 而不是发一条会 42P01 的查询。
//
// 保留 derive.go（DeriveTurnDeltas / ParseRequestMessages）：它们是**纯函数**，
// 与数据库无关，且有既有的单测覆盖；将来若再有「从累积正文拆增量」的需求
// （例如从别的存储面接回累积正文），它们仍然可用。
//
// The _request_logs → session_turns metadata backfill (backfill_session_v2_turns
// SQL function, see cmd/tools/backfill_sessions_v2_v2) should be run first so the
// session_turns rows exist; session_bodies is written independently here.
//
// Usage:
//
//	go run ./cmd/tools/backfill_session_bodies \
//	    --dsn="$DB" --tenant=tenant_xxx --session=gw_abc... --dry-run=true
//
// Exit codes:
//
//	0 — completed (rows written or dry-run reported)
//	1 — argument / DB / write error
//	1 — v1 family retired (no source to derive from; see the note above)
package main

import (
	"flag"
	"log"
)

func main() {
	dsn := flag.String("dsn", "", "Postgres DSN (required)")
	tenant := flag.String("tenant", "", "tenant id (optional; empty = any)")
	session := flag.String("session", "", "gw_session_id (required)")
	dryRun := flag.Bool("dry-run", true, "when true, derive and report but do not write session_bodies")
	useHot := flag.Bool("use-hot", false, "query the hot body table instead of the partitioned table")
	flag.Parse()

	if *dsn == "" || *session == "" {
		log.Fatal("--dsn and --session are required")
	}

	// 三个 flag 都被读了（避免「声明了却不用」的编译噪音），但**不再有作用**：
	// 工具的输入源已随 v1 族退役而消失。
	_ = tenant
	_ = dryRun
	_ = useHot

	log.Fatalf("backfill_session_bodies: v1 family retired — the accumulated request-log bodies "+
		"were dropped (migration 890), and the session family already stores per-turn deltas, "+
		"so there is nothing left to derive from (session=%s)", *session)
}
