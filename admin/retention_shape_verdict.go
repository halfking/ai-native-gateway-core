package admin

// retention_shape_verdict.go —— §143.43 留存形态的**纯决策表**（零 IO）。
//
// 为什么要决策表而不是把结论写在文档里（§143.42 同族经验）：
// 登记册（scripts/audit/retention_shape_findings.tsv）里的 verdict 列如果
// 靠人手写，它就可能与事实脱节 —— 一份"自说自话"的登记册等于没有登记册。
// 于是把判定规则抽成**无 IO 的纯函数**，由门逐行重算：
// 登记册每行的 verdict 必须等于 ClassifyRetentionShape(inputs) 的输出，
// 不等即红。登记册因此不可能自说自话。
//
// ⚠ 决策表只回答「**是什么形态 / 能不能回收 / 该用什么手段**」，
//   **不回答**「这个形态要不要改」—— 后者是属主的语义决策
//   （例如 usage_facts 的 TTL 取多少天属于计费语义，不在本表内）。
import (
	"fmt"
	"strconv"
	"strings"
)

// RetentionScope 是登记册的一行属于哪一类。
type RetentionScope string

const (
	// ScopeSnapshotFamily：快照族父表整体（形态决定走哪条留存路径）。
	ScopeSnapshotFamily RetentionScope = "SNAPSHOT_FAMILY"
	// ScopeSnapshotObject：快照族的单个对象（legacy 锚点等）。
	ScopeSnapshotObject RetentionScope = "SNAPSHOT_OBJECT"
	// ScopeUsageFactsFamily：usage_facts 族父表整体。
	ScopeUsageFactsFamily RetentionScope = "USAGE_FACTS_FAMILY"
	// ScopeUsageFactsObject：usage_facts 族的单个分区/对象。
	ScopeUsageFactsObject RetentionScope = "USAGE_FACTS_OBJECT"
	// ScopeStructuralGap：机制缺口（不随单实例数据变化，恒定存在）。
	ScopeStructuralGap RetentionScope = "STRUCTURAL_GAP"
)

// RetentionVerdict 是判定结果。
type RetentionVerdict string

const (
	// VerdictPartitionDropActive：分区父表 ⇒ 逐分区 DROP 型留存生效。
	// 占用 O(保留天数)，空间**真归还**文件系统。
	VerdictPartitionDropActive RetentionVerdict = "PARTITION_DROP_ACTIVE"
	// VerdictRowDeleteBloat：普通 heap ⇒ 行级 DELETE 型留存。
	// 行数被保留窗口限定（实测超期行 = 0），但**空间不归还**
	// （死元组与索引膨胀留在文件里）⇒ 可回收字节 > 0。
	VerdictRowDeleteBloat RetentionVerdict = "ROW_DELETE_BLOAT"
	// VerdictNotReclaimable：不可回收（仍有行，或是回滚锚点）。
	VerdictNotReclaimable RetentionVerdict = "NOT_RECLAIMABLE"
	// VerdictReclaimableNow：0 行但仍占空间 ⇒ 立即可回收。
	VerdictReclaimableNow RetentionVerdict = "RECLAIMABLE_NOW"
	// VerdictGapNoRetention：该族**没有任何留存登记** ⇒ 唯一真正的无界增长族。
	VerdictGapNoRetention RetentionVerdict = "GAP_NO_RETENTION"
	// VerdictStructuralGapNoAutoPath：机制缺口，仓内**没有**自动生效的路径。
	VerdictStructuralGapNoAutoPath RetentionVerdict = "STRUCTURAL_GAP_NO_AUTO_PATH"
)

// RetentionShapeInput 是决策表的全部输入（刻意只有这几个字段 ——
// 多一个字段就多一个"输入被悄悄改小"的面）。
type RetentionShapeInput struct {
	Scope RetentionScope
	// Relkind：'p' 分区父表 / 'r' 普通表。
	Relkind string
	// Rows：实测行数（不是 n_live_tup 估算 —— 估算会假空）。
	Rows int64
	// TotalBytes：pg_total_relation_size。
	TotalBytes int64
	// ReclaimableBytes：可回收字节（口径由测量脚本给出）。
	ReclaimableBytes int64
	// TTLRegistered：该族是否有留存登记（值无关判据：键在不在）。
	TTLRegistered bool
	// IsRollbackAnchor：是否是迁移的回滚锚点（删了就断了唯一退路）。
	IsRollbackAnchor bool
}

// ClassifyRetentionShape 是决策表本体。
//
// 规则按顺序匹配，**第一条命中即返回**（顺序本身就是规则的一部分）。
//
//  1. 回滚锚点      ⇒ 永不可回收（与行数无关）
//  2. 机制缺口      ⇒ STRUCTURAL_GAP_NO_AUTO_PATH
//  3. usage_facts 族且无 TTL 登记 ⇒ GAP_NO_RETENTION
//  4. 父表形态：'p' ⇒ PARTITION_DROP_ACTIVE；'r' ⇒ ROW_DELETE_BLOAT
//  5. 单对象：有行 ⇒ NOT_RECLAIMABLE；0 行但占空间 ⇒ RECLAIMABLE_NOW
//
// ⚠ 第 1 条**刻意放在最前**：它是**语义**保护而不是统计结论，
//
//	所以它必须压过"0 行 ⇒ 可回收"这条规则 —— 否则有人把 legacy 清空之后，
//	决策表就会开始说它可以回收，而那时回滚路径已经断了。
func ClassifyRetentionShape(in RetentionShapeInput) (RetentionVerdict, string) {
	if in.IsRollbackAnchor {
		return VerdictNotReclaimable, "回滚锚点：删它就断了该迁移的唯一退路"
	}

	switch in.Scope {
	case ScopeStructuralGap:
		return VerdictStructuralGapNoAutoPath,
			"机制缺口：仓内没有自动生效的路径（830 是 manual-by-design）"

	case ScopeUsageFactsFamily:
		if !in.TTLRegistered {
			return VerdictGapNoRetention,
				"该族没有任何留存登记（无 TTL 键、无 worker、无 DROP 登记）⇒ 唯一真正的无界增长族"
		}
		// 有登记 ⇒ 与快照族同构，按形态判。
		return classifyParentShape(in)

	case ScopeSnapshotFamily:
		return classifyParentShape(in)

	case ScopeSnapshotObject, ScopeUsageFactsObject:
		if in.Rows > 0 {
			return VerdictNotReclaimable, "仍有真实行 ⇒ 不可回收"
		}
		if in.TotalBytes > 0 {
			return VerdictReclaimableNow,
				"0 行但仍占空间 ⇒ 可回收（保名手段：VACUUM FULL / REINDEX；空分区可用 DROP）"
		}
		return VerdictNotReclaimable, "0 行且不占空间 ⇒ 无事可做"

	default:
		return VerdictNotReclaimable, fmt.Sprintf("未知 scope %q", in.Scope)
	}
}

// classifyParentShape 判父表形态 ⇒ 哪条留存路径在生效。
func classifyParentShape(in RetentionShapeInput) (RetentionVerdict, string) {
	switch in.Relkind {
	case "p":
		return VerdictPartitionDropActive,
			"分区父表 ⇒ 逐分区 DROP 型留存生效（占用 O(保留天数)，空间真归还）"
	case "r":
		return VerdictRowDeleteBloat,
			"普通 heap ⇒ 行级 DELETE 型留存（行数有界，但空间不归还）"
	default:
		return VerdictNotReclaimable, fmt.Sprintf("未知 relkind %q", in.Relkind)
	}
}

// RetentionShapeRow 是登记册的一行（解析后的形态）。
type RetentionShapeRow struct {
	Instance         string
	Scope            RetentionScope
	Object           string
	Relkind          string
	Rows             int64
	TotalBytes       int64
	ReclaimableBytes int64
	TTLRegistered    bool
	Verdict          RetentionVerdict
	Method           string
	Evidence         string

	// IsRollbackAnchor 由 Object 名单推导（**不**来自 TSV 列 ——
	// 刻意让它不能被登记册自己改掉）。
	IsRollbackAnchor bool
}

// retentionShapeAnchorObjects 是**回滚锚点**对象名单（硬编码，不来自登记册）。
//
// ★ 为什么要硬编码：回滚锚点是"删了就断退路"的语义事实，
//
//	它的判据**不能**放在可编辑的登记册里 —— 否则登记册一改，
//	决策表就跟着改，保护就没了（同「判据的输入集合要另有一道门打桩」）。
var retentionShapeAnchorObjects = map[string]bool{
	"public.ursm_node_snapshot_min_legacy": true,
}

// ParseRetentionShapeRow 解析登记册的一行（TSV）。
//
// 解析失败**返回错误**，不返回零值行 —— 零值行会让「解析失败」看起来像
// 「一个 0 行的对象」，那正是 §143.42 踩过的假空坑。
func ParseRetentionShapeRow(line string) (RetentionShapeRow, error) {
	fields := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
	if len(fields) != 11 {
		return RetentionShapeRow{}, fmt.Errorf("期望 11 列，实际 %d 列：%q", len(fields), line)
	}
	rows, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil {
		return RetentionShapeRow{}, fmt.Errorf("rows 列解析失败 %q：%w", fields[4], err)
	}
	total, err := strconv.ParseInt(fields[5], 10, 64)
	if err != nil {
		return RetentionShapeRow{}, fmt.Errorf("total_bytes 列解析失败 %q：%w", fields[5], err)
	}
	reclaim, err := strconv.ParseInt(fields[6], 10, 64)
	if err != nil {
		return RetentionShapeRow{}, fmt.Errorf("reclaimable_bytes 列解析失败 %q：%w", fields[6], err)
	}
	return RetentionShapeRow{
		Instance:         fields[0],
		Scope:            RetentionScope(fields[1]),
		Object:           fields[2],
		Relkind:          fields[3],
		Rows:             rows,
		TotalBytes:       total,
		ReclaimableBytes: reclaim,
		TTLRegistered:    fields[7] == "yes",
		Verdict:          RetentionVerdict(fields[8]),
		Method:           fields[9],
		Evidence:         fields[10],
		IsRollbackAnchor: retentionShapeAnchorObjects[fields[2]],
	}, nil
}

// RetentionShapeInputOf 把登记册行转成决策表输入。
func RetentionShapeInputOf(r RetentionShapeRow) RetentionShapeInput {
	return RetentionShapeInput{
		Scope:            r.Scope,
		Relkind:          r.Relkind,
		Rows:             r.Rows,
		TotalBytes:       r.TotalBytes,
		ReclaimableBytes: r.ReclaimableBytes,
		TTLRegistered:    r.TTLRegistered,
		IsRollbackAnchor: r.IsRollbackAnchor,
	}
}
