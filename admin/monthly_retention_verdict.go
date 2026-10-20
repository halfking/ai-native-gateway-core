package admin

// monthly_retention_verdict.go —— §143.46 月度分区族留存的**纯决策表**（零 IO）。
//
// 为什么要决策表（与 §143.43 的 retention_shape_verdict.go 同一思路）：
// 「哪些月度分区该被 DROP」这件事如果只写在 runbook 的散文里，
// 就会重演 §143.40「假清单」那类事故（数字看起来对，判据是错的）。
// 于是把规则抽成**无 IO 的纯函数**，由门逐行重算：
// 登记册每行的 verdict 必须等于决策表重算出来的结果，不等即红。
//
// ── 三类**独立**的留存机制（★ 别把它们混成一条）──────────────────────
//
//	路径 A（Go 侧，stateTableTTLSpecs）：
//	    bg/partition_manager.go 的 stateTableTTLSpecs() 登记了 8 个族的 TTL，
//	    由 SQL helper drop_old_state_partition_table(parent, days) 执行
//	    ⇒ **按【天】算**，但**只在每月 2 号那轮跑**（archiveSpecs 的调度）
//	    ⇒ 实际留存 = TTL .. TTL+30 天。
//	路径 B（SQL 侧，迁移 841）：
//	    配置表 llm_gateway_partition_retention 每族一行 retain_months，
//	    由 llm_gateway_drop_expired_month_partitions() 执行
//	    ⇒ **按【月】算**，配置表为空 ⇒ 一个分区都不会被删（机制空转）。
//	路径 C（各族的专门机制）：
//	    archive_*（整月搬进 *_archive_*）/ 专门的分区 DROP helper /
//	    只剪 hot 表（HOT_TRIM ⇒ 月分区仍累积）。
//
// ★ 本表回答「这个族有没有被任何一条路径管住」与「这个分区过没过期」，
//   **不回答**「该保留几个月」—— 那是属主的语义决策（审计/账期窗口）。
import (
	"fmt"
	"time"
)

// MonthlyRetentionScope 是登记册一行属于哪一类。
type MonthlyRetentionScope string

const (
	// ScopeMonthlyFamily：一个月度分区族（父表）整体。
	ScopeMonthlyFamily MonthlyRetentionScope = "MONTHLY_FAMILY"
	// ScopeMechanismState：机制状态（配置表 / sweep / 审计表）。
	ScopeMechanismState MonthlyRetentionScope = "MECHANISM_STATE"
)

// MonthlyMechanism 是「这个族被哪种机制管住」。
type MonthlyMechanism string

const (
	// MonthlyMechNone：**没有任何**留存机制 ⇒ 无界增长。
	MonthlyMechNone MonthlyMechanism = "NONE"
	// MonthlyMechStateTableTTL：路径 A（按天 TTL，每月 2 号扫）。
	MonthlyMechStateTableTTL MonthlyMechanism = "STATE_TABLE_TTL"
	// MonthlyMechArchive：archive_* 整月归档（源月分区被搬空）。
	MonthlyMechArchive MonthlyMechanism = "ARCHIVE"
	// MonthlyMechPartitionDrop：专门的分区 DROP helper。
	MonthlyMechPartitionDrop MonthlyMechanism = "PARTITION_DROP"
	// MonthlyMechHotTrim：只剪 hot 表 ⇒ **月分区仍然累积**（半覆盖）。
	MonthlyMechHotTrim MonthlyMechanism = "HOT_TRIM"
	// MonthlyMechPolicyTable：路径 B（配置表里已有该族）。
	MonthlyMechPolicyTable MonthlyMechanism = "POLICY_TABLE"
)

// MonthlyRetentionVerdict 是判定结果。
type MonthlyRetentionVerdict string

const (
	// MonthlyVerdictUnbounded：没有任何机制管住它 ⇒ 无界增长。
	MonthlyVerdictUnbounded MonthlyRetentionVerdict = "UNBOUNDED"
	// MonthlyVerdictCovered：被某个机制管住（具体机制见 mechanism 列）。
	MonthlyVerdictCovered MonthlyRetentionVerdict = "COVERED"
)

// MonthlyRetentionInput 是覆盖判定的全部输入。
type MonthlyRetentionInput struct {
	Scope MonthlyRetentionScope
	// Mechanism：穷举 DB 侧 public 函数 + Go 侧 DELETE/DROP 后确定的真实机制。
	Mechanism MonthlyMechanism
	// TTLDeclaredDays：路径 A 声明的保留天数（-1 = 未声明）。
	TTLDeclaredDays int
}

// ClassifyMonthlyRetention 判「这个族有没有被任何一条机制管住」。
//
// 规则（顺序即规则）：
//  1. 机制状态行 ⇒ 不参与覆盖判定（它不是族）。
//  2. mechanism == NONE ⇒ UNBOUNDED（真正会无界增长的族）。
//  3. 其余 ⇒ COVERED（理由里必须带上机制与它的**实际**保留口径）。
func ClassifyMonthlyRetention(in MonthlyRetentionInput) (MonthlyRetentionVerdict, string) {
	if in.Scope == ScopeMechanismState {
		return MonthlyVerdictUnbounded, "机制状态行：不参与覆盖判定"
	}
	if in.Mechanism == MonthlyMechNone {
		return MonthlyVerdictUnbounded,
			"穷举后仍无任何留存机制（无行级 DELETE、无分区 DROP、无归档）⇒ 无界增长"
	}
	switch in.Mechanism {
	case MonthlyMechStateTableTTL:
		lo, hi := EffectiveRetentionDays(in.TTLDeclaredDays)
		return MonthlyVerdictCovered,
			fmt.Sprintf("路径 A 覆盖（TTL=%d 天，但每月 2 号才扫）⇒ 实际留存 %d..%d 天",
				in.TTLDeclaredDays, lo, hi)
	case MonthlyMechArchive:
		return MonthlyVerdictCovered, "路径 C 覆盖：archive_* 整月归档（源月分区被搬空）"
	case MonthlyMechPartitionDrop:
		return MonthlyVerdictCovered, "路径 C 覆盖：专门的分区 DROP helper"
	case MonthlyMechHotTrim:
		return MonthlyVerdictCovered,
			"路径 C **半**覆盖：只剪 hot 表 ⇒ 月分区仍会累积"
	case MonthlyMechPolicyTable:
		return MonthlyVerdictCovered, "路径 B 覆盖（配置表已有该族 + retain_months）"
	default:
		return MonthlyVerdictUnbounded, fmt.Sprintf("未知机制 %q ⇒ 按无留存处理（保守）", in.Mechanism)
	}
}

// EffectiveRetentionDays 给出路径 A 的**实际**保留天数区间。
//
// ★ 为什么不能只报 TTL：路径 A 按【天】声明，却只在**每月 2 号**那轮执行
//
//	⇒ 一个 TTL=7 天的族，分区最长可活到 7+30 = 37 天。
//	⇒ 只报 7 天会给出「比真实更干净」的错误预期。
func EffectiveRetentionDays(ttlDays int) (int, int) {
	if ttlDays <= 0 {
		return 0, 0
	}
	return ttlDays, ttlDays + 30
}

// IsMonthlyPartitionExpired 判「某个月度分区按 retain_months 是否已过期」。
//
// ★★ 语义必须与迁移 841 **逐字一致**（这是本轮踩过的坑）：
//
//	841:  cutoff := cur_month - ((retain_months - 1) || ' months')::interval
//	      mth    := to_date(substring(relname from '([0-9]{4}_[0-9]{2})$'), 'YYYY_MM')
//	      过期 ⇔ mth < cutoff
//
//	换算成「距今月数」：过期 ⇔ months_old >= retain_months。
//	⚠ 写成 months_old < retain_months 会得到**反向**的结论
//	  （保留月数越多反而回收越多）⇒ 数字会骗人。
//
// ★★ **当月与未来月永不过期**（841 安全约束 5）：
//
//	分区月 >= 当月月初 ⇒ mth >= cur_month > cutoff ⇒ 恒不满足 ⇒ 恒保留。
//	这是**承重**性质：一旦有人把 `<` 改成 `<=`，当月与预建的未来分区会被 DROP
//	⇒ 写入路径报 23514。本函数与门都把这条钉住。
func IsMonthlyPartitionExpired(partMonth, currentMonth time.Time, retainMonths int) bool {
	if retainMonths < 1 {
		// 没有策略 ⇒ 不过期（841：配置表里没有该族时根本不会进入判定循环）。
		return false
	}
	cutoff := currentMonth.AddDate(0, -(retainMonths - 1), 0)
	return partMonth.Before(cutoff)
}

// MonthlyMonthDistance 返回「分区月距当月月数」（partMonth 早于 currentMonth 时为正）。
//
// ★ 必须用**月序号差**（year*12 + month 之差），不能用 `timestamp - date`：
//
//	`timestamp - date` 得到的是**以天计**的 interval（"30 days"）
//	⇒ EXTRACT(month FROM ...) **恒为 0** ⇒ 「几个月前」全算成 0
//	⇒ 在「算过期」的方向上极危险（把一切判成过期）。
//	（841 自己用的是 `date - interval 'N months'`，是对的；本函数是同一语义的 Go 版。）
func MonthlyMonthDistance(partMonth, currentMonth time.Time) int {
	py, pm := partMonth.Year(), int(partMonth.Month())
	cy, cm := currentMonth.Year(), int(currentMonth.Month())
	return (cy*12 + cm) - (py*12 + pm)
}
