package v2

import "time"

// calendarDate returns a midnight UTC timestamp suitable for a PostgreSQL DATE
// partition key. Truncate(24*time.Hour) aligns to the absolute Unix epoch and
// can produce the wrong calendar day for non-UTC timestamps.
//
// FIXME-14368：★ 本函数取的是 **UTC 日历日**，而分区正典是 **Asia/Shanghai 零点**
// （见 811 / 694 头注「生产分区边界 = Asia/Shanghai 零点」）⇒ ⇒ **口径错位 8 小时**。
//
//	实测（252，2026-10-22 只读）：session_bodies_2026_09 分区里的 3,749 行，
//	partition_date 全是 2026-09-30，而 ts 全在 +08 的 2026-10-01 00:00~07:59
//	⇒ ⇒ 证明「每月 1 日 00:00~08:00 (+08) 的数据落进上月分区」。
//
//	⇒ ⇒ 三条后果：
//	  ① 「某月」的语义错位 8 小时（库里「9 月分区」= +08 的 09-01 08:00 ~ 10-01 08:00）；
//	  ② 保留期判定（841 retain_months 按月界判过期）把边界数据算到上月；
//	  ③ 按 +08 日历日的查询会漏掉边界那 8 小时（它们在上月分区里）⇒ 分区裁剪错位。
//
//	⇒ ⇒ **不要顺手改**：改口径不是一行改动——存量行的 partition_date 需要配套搬家，
//	  否则同一日历月会分裂在两个分区里。⇒ 本轮（§143.68）**只登记、不改行为**。
//	⇒ ⇒ 门 `TestSection14368PartitionDateCalendarOffsetIsRegistered` 钉住「不许无声存在」。
func calendarDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
