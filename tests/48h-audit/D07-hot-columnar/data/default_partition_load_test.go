// Package data - D07 数据测试（第四批）：DEFAULT 分区堆积普查。
//
// # 这道门为什么不是「DEFAULT 必须为空」
//
// 直觉上最容易写成的一句断言是「DEFAULT 分区里有数据 = 缺陷」。**那是规则用错了对象。**
// DEFAULT 分区的设计目的就是接住「落在所有显式边界之外」的行，让写入永不失败。
// `bg/partition_manager.go:1256` 对 usage_facts 的注释明写「DEFAULT 分区保留作历史 catch-all」
// ——那是**有文档的设计决策**，拿它判红就是在惩罚一个正确的设计。
//
// # 真正要抓的是「静默堆积」
//
// 危险的不是 DEFAULT 有数据，而是：**分区创建滞后 / 从没为某张表接过分区，
// 于是写入一路落进 DEFAULT 而没有任何人察觉。** 写入不报错、分区裁剪悄悄失效、
// 某天发现某张表 90% 的行都在 DEFAULT 里，事前没有任何信号。
//
// 所以本门的判据不是「有没有数据」，而是**「有数据但没人登记过」**：
// 承载主力数据的 DEFAULT 分区必须在下方 allowlist 里写明理由与量级；
// 没登记 → 判红。登记了但实际不再堆积 → 判红（白名单必须自收缩）。
//
// # 真库实测（PostgreSQL 17.10，2026-09-29）
//
// 逐张普查带 DEFAULT 分区的父表：**18 张带 DEFAULT，其中 2 张为实质堆积（>=1MB）**，
// 其余 16 张为空/可忽略（reltuples ∈ {0, -1}，体积 16 kB–440 kB）。
//
//	usage_facts        / usage_facts_default        1,253,878 行 / 1168 MB（全表 88.5%）
//	    行区间 2026-08-19 01:02 → 2026-09-25 23:59，跨 36 个自然日
//	stats_event_inbox  / stats_event_inbox_default  1,419,612 行 / 1298 MB（**零显式分区**）
//	    行区间 2026-08-19 01:02 → 2026-09-29 00:21，跨 41 个自然日
//
// ## 这道门当场抓到了我手工普查漏掉的那一个
//
// 写下这道门之前，我先手工跑过一遍 census，结论是「只有 usage_facts 一张」。
// **手工 SQL 带了个 `AND (SELECT count(*) FROM pg_inherits ...) > 1` 的过滤**——
// 只保留「子分区多于一个」的父表。而 `stats_event_inbox` **恰好只有一个子分区**（DEFAULT 自己），
// 于是被过滤条件整条抹掉，正是全表最严重的那一张。
//
// **教训：普查脚本里的过滤条件会同时充当「筛选」和「掩盖」。**
// 「我想要的那些对象」和「我筛选之后还剩的对象」不是一回事，尤其是当过滤条件
// 描述的是**对象结构的退化情形**时——恰恰因为它退化，它才不会被那个条件选中。
// 写成门之后，同一条查询没有过滤条件，立刻报出 2 张。
//
// # 一条必须写下来的否证：DEFAULT 并非「永远破坏分区裁剪」
//
// 初始判断是「DEFAULT 装着 1168 MB，每次范围查询都要全扫一遍，分区裁剪等于白做」。
// **EXPLAIN 直接否掉了这个判断**，三次复跑稳定：
//
//	WHERE occurred_at >= '2026-09-28' AND < '2026-09-29'
//	  → Index Only Scan using usage_facts_20260928_occurred_at_idx   （无 Append，DEFAULT 不在计划里）
//	同一查询 SET enable_partition_pruning=off
//	  → Append 下游 4 个日分区全部出现                                 （裁剪确实在起作用）
//	对照：窗口落在 2026-07-01 / 2027-01-01（不被任何显式分区覆盖）
//	  → usage_facts_default 被扫
//
// 即 PG 17.10 在窗口被显式兄弟分区**完整覆盖**时会裁掉 DEFAULT。
// 我没有去读规划器源码确认其判定路径，**只登记实测行为，不登记机制解释**。
//
// 剩下的真实结论因此比初判窄得多：36 天历史块落在 DEFAULT，
// 覆盖那段的查询要扫它（单日 2026-09-25 实测 12.4 ms，Index Only Scan，可接受）；
// 落在 2026-09-26 之后的窗口不受影响。**P3 文档债，不是性能缺陷。**
// 唯一需要订正的是 `bg/partition_manager.go:1258` 那句
// 「partition pruning 对 WHERE 范围查询仅扫命中分区」——它对 2026-09-26 之前的数据不成立。
//
// 跑测（无库自动 skip；只需能读 pg_class/pg_inherits 的只读角色）：
//
//	D07_S01_PG_URL=postgres://reader@127.0.0.1:5432/llm_gateway?sslmode=disable \
//	  go test -timeout 180s ./tests/48h-audit/D07-hot-columnar/data/...
package data

import (
	"fmt"
	"testing"
)

// defaultPartitionLoad is one row of the census: how a single partitioned
// parent's DEFAULT child compares to all of its explicitly-bounded siblings.
type defaultPartitionLoad struct {
	parent      string
	defPart     string
	defRows     int64 // reltuples; negative means never ANALYZEd
	defSize     int64 // pg_total_relation_size
	sibRows     int64
	sibSize     int64
	siblingPart int
}

// material reports whether the DEFAULT partition is carrying a *share* of the
// table rather than a token catch-all.
//
// Row estimates come from pg_class.reltuples, which is -1 for a table that has
// never been ANALYZEd/VACUUMed. Treating -1 as 0 would under-report exactly the
// case we care about, so an unanalyzed DEFAULT whose on-disk size already
// rivals its siblings falls back to the size signal. Both signals must agree
// that DEFAULT is the majority before anything is flagged: a bloated
// non-DEFAULT sibling or a freshly-vacuumed empty table must not trigger it.
func (l defaultPartitionLoad) material() (bool, string) {
	// Size floor: an empty heap partition is ~0–16 kB. Below 1 MB there is
	// nothing here worth a human decision regardless of what reltuples says.
	if l.defSize < 1<<20 {
		return false, ""
	}
	switch {
	case l.defRows > 0 && l.sibRows > 0:
		if l.defRows <= l.sibRows {
			return false, ""
		}
	case l.defRows > 0 && l.sibRows <= 0:
		// Siblings unanalyzed/absent — rows still speak, they are the stronger
		// signal when they exist.
	case l.defRows < 0 && l.sibSize > 0:
		// Never analyzed: fall back to on-disk share.
		if l.defSize <= l.sibSize {
			return false, ""
		}
	default:
		// No row evidence and no sibling rows to compare against. If the
		// DEFAULT partition is on disk and larger than the bounded set, that is
		// still worth a human look.
		if l.sibSize > 0 && l.defSize <= l.sibSize {
			return false, ""
		}
	}
	return true, fmt.Sprintf("DEFAULT %s: 约 %d 行 / %d MB，压过 %d 个显式兄弟分区的 %d 行 / %d MB",
		l.defPart, max64(l.defRows, 0), humanMB(l.defSize),
		l.siblingPart, max64(l.sibRows, 0), humanMB(l.sibSize))
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func humanMB(b int64) int64 {
	const mb = 1 << 20
	return (b + mb/2) / mb
}

// justifiedDefaultPartitions is the allowlist. Every entry must carry a written
// reason AND be re-checked each run: an entry whose parent stopped carrying a
// material load turns the gate red, so the list cannot silently outlive its
// reason (self-shrinking, same rule as the promote_* allowlist).
var justifiedDefaultPartitions = map[string]string{
	// R68 (2026-09-26, migration 750) 把 usage_facts 改成按日分区，并**显式**
	// 保留 DEFAULT 作历史 catch-all（bg/partition_manager.go:1256-1262）。
	// 日分区从 2026-09-26 起接管新数据，2026-08-19 ~ 2026-09-25 这 36 天
	// 的历史行从未迁进日分区，故全部沉在 DEFAULT 里（实测 1,253,878 行 / 1168 MB）。
	// 不判红：写入永不失败是 DEFAULT 的设计目的，且实测窗口被兄弟分区覆盖时
	// PG 会裁掉它（见文件头对照）。待办：一次性回填这 36 天日分区，
	// 属迁移决策（需要 owner 决定 1168 MB 的 ATTACH 锁窗口），审计轮不代做。
	"usage_facts": "R68/750 按日分区的历史 catch-all：2026-08-19~09-25 共 36 天未回填日分区",

	// 实测 2026-09-29 新登记（P2，**无设计理由**，属漏接而非有意选择）：
	// `stats_event_inbox` 声明 `PARTITION BY RANGE (occurred_at)`，却只有 DEFAULT
	// 一个子分区 —— 即**名义分区、实际零分区**，全表 1,419,612 行 / 1298 MB 都在
	// DEFAULT 里，行区间 2026-08-19 → 2026-09-29（41 天）。
	//
	// 为什么不是「有意设计」：DEFAULT 分区的正当用途是接住**兜不住**的行；
	// 而这张表连一个显式分区都没有，说明分区创建任务从未接入——
	// `bg/partition_manager.go:1198-1262` 的 ensureSpecs() 里**没有任何条目**引用它，
	// `bg/` 整包对它零引用。
	//
	// 为什么不定 P1：声明查询本身有部分索引兜底
	// （`stats_event_inbox_default_occurred_at_created_at_idx ... WHERE processed_at IS NULL`），
	// 今天不慢。真实代价是**无界增长**：全仓**零处** DELETE/TRUNCATE stats_event_inbox，
	// 消费者只 markProcessed（inbox_consumer.go:378）且 replaySQL 刻意保留已处理行，
	// 即这是一本只增不减的重放账本。按实测 ~10–26K 行/天（尖峰 150K），
	// 一年量级 4M–10M 行 / 4–9 GB，全部堆在一张无分区表里。
	//
	// 待 owner 决策：(a) 接入 ensureSpecs() 按日分区 + 保留期清理，或
	// (b) 若确实不需要分区，则去掉 PARTITION BY 声明，别让下一个人以为有裁剪。
	"stats_event_inbox": "声明 PARTITION BY 但 ensureSpecs() 无任何条目、零显式分区；无保留期，全仓零 DELETE",
}

func TestData_DefaultPartition_LoadIsJustified(t *testing.T) {
	conn, ctx := connectAuditDB(t)
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, `
		SELECT p.relname,
		       d.relname,
		       d.reltuples::bigint,
		       pg_total_relation_size(d.oid),
		       COALESCE(s.sib_rows, 0),
		       COALESCE(s.sib_size, 0),
		       COALESCE(s.sib_parts, 0)
		  FROM pg_inherits inh
		  JOIN pg_class d ON d.oid = inh.inhrelid
		  JOIN pg_class p ON p.oid = inh.inhparent
		  JOIN pg_namespace n ON n.oid = p.relnamespace
		  LEFT JOIN LATERAL (
		        SELECT COALESCE(SUM(c2.reltuples), 0)::bigint AS sib_rows,
		               COALESCE(SUM(pg_total_relation_size(c2.oid)), 0)::bigint AS sib_size,
		               count(*)::bigint AS sib_parts
		          FROM pg_inherits i2
		          JOIN pg_class c2 ON c2.oid = i2.inhrelid
		         WHERE i2.inhparent = p.oid
		           AND pg_get_expr(c2.relpartbound, c2.oid) <> 'DEFAULT'
		  ) s ON TRUE
		 WHERE n.nspname = 'public'
		   AND p.relkind = 'p'
		   AND pg_get_expr(d.relpartbound, d.oid) = 'DEFAULT'
		 ORDER BY pg_total_relation_size(d.oid) DESC`,
	)
	if err != nil {
		// fail-closed: a census that cannot run is not a census that passed.
		t.Fatalf("DEFAULT partition census query failed: %v", err)
	}
	defer rows.Close()

	var (
		census     []defaultPartitionLoad
		seenParent = map[string]bool{}
	)
	for rows.Next() {
		var l defaultPartitionLoad
		if err := rows.Scan(&l.parent, &l.defPart, &l.defRows, &l.defSize,
			&l.sibRows, &l.sibSize, &l.siblingPart); err != nil {
			t.Fatalf("scan DEFAULT partition census row: %v", err)
		}
		census = append(census, l)
		seenParent[l.parent] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read DEFAULT partition census: %v", err)
	}
	if len(census) == 0 {
		var parents int
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			  WHERE n.nspname='public' AND c.relkind='p'`).Scan(&parents); err != nil {
			t.Fatalf("DEFAULT partition census returned 0 rows and the parent count also failed (%v); "+
				"either way this is not a clean database, it is a broken census", err)
		}
		t.Fatalf("DEFAULT partition census returned 0 rows, yet this database has %d partitioned "+
			"parents — an empty census means the query is wrong, not that the database is clean", parents)
	}

	var flagged, shrunk []string
	for _, l := range census {
		material, detail := l.material()
		reason, justified := justifiedDefaultPartitions[l.parent]
		switch {
		case material && !justified:
			flagged = append(flagged, fmt.Sprintf("%s: %s\n\t登记理由：写进 justifiedDefaultPartitions，"+
				"并注明这是有意的设计还是分区创建滞后（后者要补分区，不是补登记）", l.parent, detail))
		case material && justified:
			t.Logf("DEFAULT 堆积已登记：%s — %s", l.parent, reason)
		case !material && justified:
			shrunk = append(shrunk, fmt.Sprintf("%s（登记理由：%s）", l.parent, reason))
		}
	}

	// Census visibility: a DEFAULT holding nothing should stay visibly empty so
	// the next reader can tell "checked and clean" from "not checked".
	empty := 0
	for _, l := range census {
		if l.defSize < 1<<20 {
			empty++
		}
	}
	t.Logf("DEFAULT 分区普查：%d 张父表带 DEFAULT 分区，其中 %d 张为实质堆积（>=1MB），%d 张为空/可忽略",
		len(census), len(census)-empty, empty)

	for _, f := range flagged {
		t.Errorf("DEFAULT 分区承载主力数据但无登记理由 —— 这是静默堆积，写入不报错、分区裁剪悄悄失效：\n\t%s", f)
	}
	for _, s := range shrunk {
		t.Errorf("白名单条目已失效（该 DEFAULT 分区不再承载数据）——请从 justifiedDefaultPartitions 删除：\n\t%s", s)
	}

	// Every allowlist key must correspond to a real partitioned parent in this
	// database, so a stale key cannot hide behind "table not present here".
	for name := range justifiedDefaultPartitions {
		if !seenParent[name] {
			t.Errorf("白名单条目 %q 在本库没有对应的分区父表（或是没有 DEFAULT 分区）——请核对后删除", name)
		}
	}
}
