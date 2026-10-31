package admin

import "time"

// session_partition_bounds.go —— 会话族【分区键下界】折算（R181 轮新增）。
//
// # 这块在解决什么
//
// `public.session_turns` / `session_bodies` / `session_turn_details` 全部按
// **partition_date** 做 RANGE 分区（实测：`session_turns = RANGE (partition_date)`）。
// PostgreSQL 的**分区裁剪只认分区键谓词** ⇒ ⇒ 只按 **ts** 过滤的读方
// **一个都裁不掉** ⇒ ⇒ 一个 ts 窗口会命中【全部】月分区。
//
// ★ 真库 A/B 对照（本地 PG17，`session_turns` 有 5 个月分区 + default）：
//
//	-- 现状（只按 ts）
//	Append
//	  -> Index Scan using session_turns_2026_07_ts_idx on session_turns_2026_07
//	  -> Index Scan using session_turns_2026_08_ts_idx on session_turns_2026_08
//	  -> Seq Scan  on session_turns_2026_10          ← ★ 整分区顺序扫描 ★
//	  -> Index Scan using session_turns_2026_11_ts_idx on session_turns_2026_11
//	  -> Index Scan using session_turns_default_ts_idx on session_turns_default
//
//	-- 加 partition_date 下界后
//	Seq Scan on session_turns_2026_10                ← ★ 只剩 1 个分区 ★
//
// ★★ 生产侧同一形态已被台账记下：252 的 **EXPLAIN 计划形态**观测台账
// （`scripts/audit/asset/query-plan-observations.tsv`）里，/api/logs 的列表语句
// （stmt-1d7be58136）在**1 秒窗口**上 Seq Scan 了 **4 个整月分区**
// ⇒ ⇒ 这正是「不要有表扫描」这条诉求的实测落点。
//
// ★★★ R182 轮订正（★★ 事实性错误，不改会误导下一轮 ★★★）：★★
// ★ 这份台账**不是** `pg_stat_statements` 的统计快照 ★★ —— ★ 它的头注释写明
// ★ 「生成器：cmd/tools/query_plan_form（★ EXPLAIN，★ 不带 ANALYZE ⇒ 只读库
// ★ 安全）」⇒ ⇒ 它是**逐语句 EXPLAIN 的计划形态快照** ⇒ ⇒ 全文件里
// ★ `pg_stat_statements` 出现 **0 次** ⇒ ⇒ 「台账实测」指的是**计划形态**
// ★ （扫了哪些关系、用的什么索引），**不是**执行期统计。★ 同款错标在
// ★ `admin/logs_turns_source.go` 里也有一处，本轮一并订正。

// # 口径：为什么可以取【精确】边界（而不是 ±1 天外扩）
//
// ★ `partition_date` 的**列值** = `calendarDate(ts)` = **UTC 日历日**
//   （`domains/session/v2/partition_date.go` 的 calendarDate()）。
// ★ 所有写入方都用它 ⇒ 逐条核对：
//   `turn_writer.go:485` · `details_writer.go:159` · `bodies_writer.go:318,390`
//   · `family_writers.go:127` · `session_aggregator.go:114`
//   · `session_writer_v2.go:857`
// ⇒ ⇒ 对任意 ts ∈ [start, end] 的行，它**必然**满足
//
//	UTCDate(start) ≤ partition_date ≤ UTCDate(end)
//
// ★ ⇒ 加上这个谓词**不会丢任何一行**（它是 ts 谓词的蕴含条件，不是新筛选）。
//
// ★★ 与 FIXME-14368 的关系（★ 别把这个 FIXME 误读成「列值不可信」）：★★
//   那个 FIXME 说的是【**分区边界正典**是 Asia/Shanghai 零点，而【列值】取
//   UTC 日历日】⇒ ⇒ 后果是「+08 每月 1 日 00:00~07:59 的行落进**上月**分区」
//   ⇒ ⇒ **列值本身仍然是该行的 UTC 日历日** ⇒ ⇒ 按列值裁剪**不丢行** ★
//   ⇒ ⇒ 所以这里取精确边界即可（**不需要** ±1 天外扩）。
//
// ★ 顺带：正因为分区边界是 +08 零点，「月初那 8 小时」的行住在**上个月**的分区里
//   ⇒ ⇒ 精确边界下 `partition_date = UTCDate(ts)` 依然成立 ⇒ ⇒ 裁剪结果正确 ★

// sessionPartitionDateBounds 把 ts 窗口折算成 partition_date 的上下界。
//
// ★ 返回值是 **YYYY-MM-DD 字符串**（不是 time.Time）：★ 因为 PG 的分区裁剪要
//
//	拿**与分区键同类型（date）**的常量做比较 ⇒ ⇒ 传字符串让 PG 按 `date`
//	推断参数类型 ⇒ ⇒ 裁剪才能生效（传 time.Time 会被推成 timestamptz
//	⇒ ⇒ 比较变成 date→timestamptz 的隐式转换 ⇒ ⇒ 裁剪可能失效）。
//
// ★ start > end（非法窗口）时上下界自然反向 ⇒ ⇒ 谓词恒假 ⇒ ⇒ 与 ts 谓词
//
//	同样返回空集 ⇒ ⇒ 口径一致。
func sessionPartitionDateBounds(start, end time.Time) (string, string) {
	return utcCalendarDate(start).Format("2006-01-02"),
		utcCalendarDate(end).Format("2006-01-02")
}

// utcCalendarDate 取 UTC 日历日（与 domains/session/v2 的 calendarDate 同口径）。
//
// ★★ 这里**刻意不复用** domains/session/v2 的 calendarDate：★
//
//	① 那个函数是**未导出**的（domains/session/v2 是写路径包）；
//	② ★★ 而且它是**故意保留 FIXME-14368 口径**的实现（取 UTC 日历日）
//	⇒ ⇒ 读侧要的是**同一个**口径 ⇒ ⇒ 这里逐字复刻它，并写明为什么复刻
//	⇒ ⇒ 任何一边改口径都必须同步改另一边（★ 由本文件的门钉住 ★）。
func utcCalendarDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
