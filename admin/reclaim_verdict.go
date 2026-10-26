package admin

// reclaim_verdict.go —— 「可回收对象」的**判定层**（§143.42 轮，2026-10-21）
//
// # 为什么要有这一层
//
// §143.40 用「pg_stat_user_tables.n_live_tup = 0」+「pg_inherits 里没有父」
// 判出「20 个零行对象 / 4 个 orphan」，交付了一个「1,293 MB 零语义风险回收包」。
// 2026-10-21（§143.42）逐条复核后：
//
//   - 20 个里 **17 个实际有行**（n_live_tup 是陈旧估算 ⇒ 系统性假空）；
//   - 4 个「orphan」**没有一个是分区**（relispartition 全为 f）⇒ 它们是普通表
//     ⇒ 「不在父表下 ⇒ 任何读面都碰不到」是**空命题**；
//   - 真 orphan 分区（relispartition=true 且无父）在 252 上**一个都没有**。
//
// ⇒ ⇒ 判定必须从「猜对象有没有用」改成「**用精确的事实 + 固定的决策表**」，
// 且决策表要能被单元测试逐条钉住。
//
// # 三条量具纪律（决策表的输入全部建立在这三条上）
//
//	① 判空【必须】用 EXISTS(SELECT 1 FROM t LIMIT 1)（O(1) 且精确）。
//	   ⇒ 禁止用 pg_stat_user_tables.n_live_tpu：对「长期无写入 + 从未 analyze」
//	     的表它恒为 0 ⇒ §143.40 因此误判 17 个对象。
//	② 判「孤儿分区」【必须】用 pg_class.relispartition。
//	   ⇒ 「pg_inherits 里没有父」对普通表恒真 ⇒ 不是孤儿证据。
//	③ 判「回收了多少字节」看磁盘，不看 catalog 汇总。
//
// # 决策表（顺序敏感：先排除不可回收，再分派手段）
//
//	1. 有行                      ⇒ NOT_RECLAIMABLE     —— 回收会销毁数据
//	2. DEFAULT 兜底分区           ⇒ NOT_RECLAIMABLE     —— 删除会打断写入路径
//	3. 真孤儿分区 + 0 行
//	     仍被 public 函数体引用   ⇒ RECLAIMABLE_TRUNCATE —— 只能保名回收
//	     无引用                   ⇒ RECLAIMABLE_DROP     —— 可以 DROP
//	4. 普通表 + 0 行             ⇒ RECLAIMABLE_TRUNCATE —— 只能保名回收
//
// ★★ 第 4 条的**含义**要说清：普通表 + 0 行**不是**「零风险可 DROP」，
//   而是「**保名重写（VACUUM FULL）零风险**」—— 表名、结构、被引用它的
//   函数体全部不变 ⇒ 没有任何读面受影响 ⇒ 回收的只是文件膨胀。
//   ⇒ 「能不能 DROP」与「能不能回收」是**两件事**，§143.40 把它们混成了一件。
import "fmt"

// ReclaimFacts 是一个对象的**回收判定所需事实**。
//
// ★ 每个字段都必须来自精确量具（见包注释的三条纪律），不允许用估算值。
type ReclaimFacts struct {
	// Object 对象名（表或分区）。
	Object string
	// TotalBytes pg_total_relation_size 的字节数。
	TotalBytes int64
	// Empty 精确判空结果：EXISTS(SELECT 1 FROM t LIMIT 1) 为假。
	Empty bool
	// RelIsPartition pg_class.relispartition —— **唯一的**孤儿判据。
	RelIsPartition bool
	// HasParent pg_inherits 里是否存在父记录。
	HasParent bool
	// IsDefaultPartition relpartbound = 'DEFAULT'（兜底分区）。
	IsDefaultPartition bool
	// FuncRefs public 函数体按名引用的个数。
	FuncRefs int
}

// 判定结果常量。
const (
	ReclaimVerdictDrop     = "RECLAIMABLE_DROP"
	ReclaimVerdictTruncate = "RECLAIMABLE_TRUNCATE"
	ReclaimVerdictNone     = "NOT_RECLAIMABLE"
)

// 回收手段常量。
const (
	ReclaimMethodDrop       = "DROP"
	ReclaimMethodVacuumFull = "VACUUM_FULL"
	ReclaimMethodNone       = "-"
)

// ReclaimVerdict 是一个对象的判定结论。
type ReclaimVerdict struct {
	// Verdict 三个常量之一。
	Verdict string
	// Method 回收手段（DROP / VACUUM_FULL / "-"）。
	Method string
	// Reason 判定依据（人可读）。
	Reason string
}

// Reclaimable 报告该判定是否允许回收。
func (v ReclaimVerdict) Reclaimable() bool { return v.Verdict != ReclaimVerdictNone }

// ClassifyReclaim 按决策表判定一个对象能否回收、以什么手段回收。
//
// ★ 这是一层**纯函数**：没有 IO、没有时间依赖、没有环境变量 ⇒
//
//	它的每一条分支都能被表驱动测试逐条钉住（见
//	admin/section_14342_reclaim_measurement_test.go 的决策表自测）。
func ClassifyReclaim(f ReclaimFacts) ReclaimVerdict {
	switch {
	case !f.Empty:
		return ReclaimVerdict{
			Verdict: ReclaimVerdictNone,
			Method:  ReclaimMethodNone,
			Reason:  "有行：回收会销毁数据（★ 判空必须用 EXISTS，禁用 n_live_tup）",
		}
	case f.IsDefaultPartition:
		return ReclaimVerdict{
			Verdict: ReclaimVerdictNone,
			Method:  ReclaimMethodNone,
			Reason:  "DEFAULT 兜底分区：删除会让不落在具名分区里的行报 23514 ⇒ 打断写入路径",
		}
	case f.RelIsPartition && !f.HasParent:
		// 真正的孤儿分区（被 DETACH 过）：表名不在任何父表下 ⇒ 可以 DROP。
		if f.FuncRefs > 0 {
			return ReclaimVerdict{
				Verdict: ReclaimVerdictTruncate,
				Method:  ReclaimMethodVacuumFull,
				Reason: fmt.Sprintf(
					"真孤儿分区 + 0 行，但仍被 %d 个 public 函数体引用 ⇒ 只能保名回收（VACUUM FULL）",
					f.FuncRefs),
			}
		}
		return ReclaimVerdict{
			Verdict: ReclaimVerdictDrop,
			Method:  ReclaimMethodDrop,
			Reason:  "真孤儿分区（relispartition=true 且 pg_inherits 无父）+ 0 行 + 无函数体引用 ⇒ 可 DROP",
		}
	default:
		// 普通表 + 0 行。
		// ★ 注意措辞：这里**不是**「零风险可 DROP」——普通表的「不在父表下」是恒真命题，
		//   不能当「无读面」证据。可回收的是**文件膨胀**，手段是保名重写。
		reason := "普通表 + 0 行 ⇒ 只能保名回收（VACUUM FULL：表名/结构/函数体引用全不变）"
		if f.FuncRefs > 0 {
			reason = fmt.Sprintf(
				"普通表 + 0 行 + 被 %d 个 public 函数体引用 ⇒ 只能保名回收（VACUUM FULL）",
				f.FuncRefs)
		}
		return ReclaimVerdict{
			Verdict: ReclaimVerdictTruncate,
			Method:  ReclaimMethodVacuumFull,
			Reason:  reason,
		}
	}
}
