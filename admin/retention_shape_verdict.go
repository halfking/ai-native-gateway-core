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
//
// ★ ★ §143.59 扩充：新增 **mechanism_state（机制状态）** 维度。
//
//	为什么加这个维度：§143.58 盘点出三个「没有自动回收路径」的大对象，
//	它们**失效的形态完全不同**，而旧的登记册只有"能不能回收"一个结论
//	⇒ 登记册说不清"为什么不能回收" ⇒ 覆盖面缺口（第一大对象在册里根本没有行）。
//
//	★ §143.58 实测出的**四种失效形态**（本表把它们编码成可重算的判据）：
//
//	  A 跑了但回收为 0（窗口/前提**尚未满足**）  ⇒ MechanismStateNotYetDue
//	  B 实现完整但**零调用点**（死代码）          ⇒ MechanismStateNotWired
//	  C 机制**在跑**但目标集**恒空**              ⇒ MechanismStateTargetEmpty
//	  D 对象**不在**机制的处理范围内              ⇒ MechanismStateOutOfScope
//
//	⇒ 「有没有机制」**不是一个布尔量** ⇒ 三问法：
//	   ① 谁在调用它 ② 它删的那一类存在吗 ③ 它在不在它的处理范围内。
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
	// ScopeRetentionGapObject：§143.59 新增 —— 「当前不产生任何自动回收」
	// 的大对象（表级）。**不是**按族名开的 scope，而是按**失效形态**开的
	// ⇒ 以后再有新的无回收路径对象，直接用这个 scope + mechanism_state 表达，
	// 不用再为每个族加一套分支（那会让决策表随对象数线性膨胀）。
	ScopeRetentionGapObject RetentionScope = "RETENTION_GAP_OBJECT"
	// ScopeReferenceTotal：§143.59 新增 —— **参照基准行**（库级总量）。
	// 它不是留存形态，登记它是为了让门能算【相对量】（族 ÷ 库），
	// 从而不用钉会自然过期的绝对字节数（见下）。
	ScopeReferenceTotal RetentionScope = "REFERENCE_TOTAL"
)

// RetentionMechanismState 是「留存机制与该对象的关系」形态。
//
// ★ 它回答的是 §143.58 的三问法，不是"有没有 TTL 键"（那是 TTLRegistered）。
type RetentionMechanismState string

const (
	// MechanismStateActive：机制在跑，且当前**有对象被回收**。
	MechanismStateActive RetentionMechanismState = "ACTIVE"
	// MechanismStateNone：压根没有任何机制。
	MechanismStateNone RetentionMechanismState = "NONE"
	// MechanismStateNotWired：机制**实现完整**（索引/行为门都在），
	// 但生产**零调用方** ⇒ 死代码（§143.58 形态 B）。
	MechanismStateNotWired RetentionMechanismState = "NOT_WIRED"
	// MechanismStateTargetEmpty：机制**在跑**，但它要删的那一类**恒为空**
	// ⇒ 每轮执行、每轮删 0 行（§143.58 形态 C）。
	MechanismStateTargetEmpty RetentionMechanismState = "TARGET_EMPTY"
	// MechanismStateNotYetDue：机制在跑，但**当前无对象满足到期判据**
	// ⇒ 现在回收 0 是**正确**的，不是坏了（§143.58 形态 A）。
	MechanismStateNotYetDue RetentionMechanismState = "NOT_YET_DUE"
	// MechanismStateOutOfScope：按设计**不该**由任何机制管它
	// （回滚锚点 / 参照基准行 / 已被人工处置的对象）（§143.58 形态 D）。
	MechanismStateOutOfScope RetentionMechanismState = "OUT_OF_SCOPE"
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
	// VerdictMechanismNotWired：§143.59 新增 —— 机制实现完整、索引前提满足，
	// 但**生产零调用方** ⇒ 当前不自动回收（接线属属主决策，本表只陈述形态）。
	VerdictMechanismNotWired RetentionVerdict = "MECHANISM_NOT_WIRED"
	// VerdictMechanismTargetEmpty：§143.59 新增 —— 机制在跑，但**目标集恒空**
	// ⇒ 实际不回收（不是"坏了"，是"要删的那一类一个都没有"）。
	VerdictMechanismTargetEmpty RetentionVerdict = "MECHANISM_TARGET_EMPTY"
	// VerdictMechanismNotYetDue：§143.59 新增 —— 机制在跑，当前无对象到期。
	VerdictMechanismNotYetDue RetentionVerdict = "MECHANISM_NOT_YET_DUE"
	// VerdictMechanismOutOfScope：§143.59 新增 —— 按设计不在机制范围内。
	VerdictMechanismOutOfScope RetentionVerdict = "MECHANISM_OUT_OF_SCOPE"
	// VerdictRegistryInconsistent：§143.59 新增 —— **登记册自相矛盾**
	//（scope 与 mechanism_state 不相容）⇒ 这不是"结论"，是"数据坏了"。
	VerdictRegistryInconsistent RetentionVerdict = "REGISTRY_INCONSISTENT"
	// VerdictReferenceTotal：§143.59 新增 —— 参照基准行（库级总量）。
	VerdictReferenceTotal RetentionVerdict = "REFERENCE_TOTAL"
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
	// MechanismState：§143.59 新增 —— 机制与该对象的关系形态。
	MechanismState RetentionMechanismState
}

// retentionShapeAllowedMechanismStates 给出每个 scope **允许**的 mechanism_state。
//
// ★ 为什么要有这张表：登记册的每一行都同时声明了"它是什么形态"（scope）与
//
//	"机制与它什么关系"（mechanism_state）⇒ 这两个声明**可能互相矛盾**
//	（例如给 usage_facts 族写 mechanism_state=ACTIVE，而它根本没有 TTL 键）。
//	⇒ 矛盾时决策表必须说"数据坏了"，而不是照着矛盾的数据算出一个看似合理的结论。
//
// ★ 集合**故意不取全集**：每个 scope 只承认它语义上说得通的几种状态
//
//	⇒ 新增一种状态时，这里不放开，对应 scope 的行就会立刻红（而不是悄悄通过）。
var retentionShapeAllowedMechanismStates = map[RetentionScope][]RetentionMechanismState{
	// 快照族的机制是逐分区 DROP ⇒ 只有"生效中"说得通。
	ScopeSnapshotFamily: {MechanismStateActive},
	// legacy 锚点：按设计不该被任何机制管；也可以说"没有机制"。
	ScopeSnapshotObject: {MechanismStateOutOfScope, MechanismStateNone},
	// usage_facts 族：生产上无 TTL 键 ⇒ NONE；仓内已登记待部署后 ⇒ ACTIVE。
	ScopeUsageFactsFamily: {MechanismStateNone, MechanismStateActive},
	// usage_facts 的单个空分区：已人工处置，不在机制范围内。
	ScopeUsageFactsObject: {MechanismStateOutOfScope, MechanismStateNone, MechanismStateActive},
	// 机制缺口本身就是"仓内没有自动生效路径"⇒ 不适用。
	ScopeStructuralGap: {MechanismStateOutOfScope},
	// 无回收路径对象：四种失效形态都可能出现（外加"压根没机制"）。
	ScopeRetentionGapObject: {
		MechanismStateNone,
		MechanismStateNotWired,
		MechanismStateTargetEmpty,
		MechanismStateNotYetDue,
		MechanismStateOutOfScope,
	},
	// 参照基准行不是留存形态。
	ScopeReferenceTotal: {MechanismStateOutOfScope},
}

// ClassifyRetentionShape 是决策表本体。
//
// 规则按顺序匹配，**第一条命中即返回**（顺序本身就是规则的一部分）。
//
//  0. §143.59：scope 与 mechanism_state 不相容 ⇒ 登记册自相矛盾
//  1. 回滚锚点      ⇒ 永不可回收（与行数无关）
//  2. 机制缺口      ⇒ STRUCTURAL_GAP_NO_AUTO_PATH
//  3. 参照基准行    ⇒ REFERENCE_TOTAL
//  4. usage_facts 族且无 TTL 登记 ⇒ GAP_NO_RETENTION
//  5. 父表形态：'p' ⇒ PARTITION_DROP_ACTIVE；'r' ⇒ ROW_DELETE_BLOAT
//  6. 单对象：有行 ⇒ NOT_RECLAIMABLE；0 行但占空间 ⇒ RECLAIMABLE_NOW
//  7. 无回收路径对象 ⇒ 按 mechanism_state 落形态 B/C/A/D
//
// ⚠ 第 1 条**刻意放在最前**：它是**语义**保护而不是统计结论，
//
//	所以它必须压过"0 行 ⇒ 可回收"这条规则 —— 否则有人把 legacy 清空之后，
//	决策表就会开始说它可以回收，而那时回滚路径已经断了。
func ClassifyRetentionShape(in RetentionShapeInput) (RetentionVerdict, string) {
	// ── 规则 0（§143.59）：登记册自相矛盾检测 ────────────────────────────
	// ★ 放在最前面：矛盾的数据不该产出"结论"。
	if !retentionShapeMechanismStateAllowed(in.Scope, in.MechanismState) {
		return VerdictRegistryInconsistent, fmt.Sprintf(
			"登记册自相矛盾：scope %q 不接受 mechanism_state %q（允许：%s）",
			in.Scope, in.MechanismState, retentionShapeAllowedStatesText(in.Scope))
	}

	if in.IsRollbackAnchor {
		return VerdictNotReclaimable, "回滚锚点：删它就断了该迁移的唯一退路"
	}

	switch in.Scope {
	case ScopeStructuralGap:
		return VerdictStructuralGapNoAutoPath,
			"机制缺口：仓内没有自动生效的路径（830 是 manual-by-design）"

	case ScopeReferenceTotal:
		return VerdictReferenceTotal,
			"参照基准行：库级总量，用于计算相对量（族 ÷ 库），不是留存形态"

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

	case ScopeRetentionGapObject:
		return classifyGapObjectShape(in)

	default:
		return VerdictNotReclaimable, fmt.Sprintf("未知 scope %q", in.Scope)
	}
}

// classifyGapObjectShape 判「当前不产生任何自动回收」的对象处在哪种失效形态。
//
// ★ 这一段是 §143.58 的**四种失效形态**的落地：同一个结论（不回收），
//
//	背后是四种**完全不同的处置**（等它到期 / 接线 / 改归档口径 / 不该由它管），
//	混成一句话就会导致"归因错了、动作也就错了"。
func classifyGapObjectShape(in RetentionShapeInput) (RetentionVerdict, string) {
	switch in.MechanismState {
	case MechanismStateNone:
		return VerdictGapNoRetention,
			"该对象没有任何留存机制（无 TTL 键、无 worker、无 DROP 登记）⇒ 无界增长"
	case MechanismStateNotWired:
		return VerdictMechanismNotWired,
			"留存机制实现完整、索引前提满足，但生产零调用方 ⇒ 当前不自动回收（接线属属主决策）"
	case MechanismStateTargetEmpty:
		return VerdictMechanismTargetEmpty,
			"留存机制在跑，但它要删的那一类恒为空 ⇒ 每轮执行、每轮删 0 行"
	case MechanismStateNotYetDue:
		return VerdictMechanismNotYetDue,
			"留存机制在跑，当前无对象满足到期判据 ⇒ 现在回收 0 是正确的"
	case MechanismStateOutOfScope:
		return VerdictMechanismOutOfScope,
			"按设计不在任何留存机制的处理范围内 ⇒ 不由机制回收"
	default:
		return VerdictRegistryInconsistent, fmt.Sprintf("未知 mechanism_state %q", in.MechanismState)
	}
}

// retentionShapeMechanismStateAllowed 判断 scope 是否接受该 mechanism_state。
func retentionShapeMechanismStateAllowed(scope RetentionScope, state RetentionMechanismState) bool {
	for _, allowed := range retentionShapeAllowedMechanismStates[scope] {
		if allowed == state {
			return true
		}
	}
	return false
}

// retentionShapeAllowedStatesText 把允许集合渲染成可读文本（给错误信息用）。
func retentionShapeAllowedStatesText(scope RetentionScope) string {
	allowed := retentionShapeAllowedMechanismStates[scope]
	if len(allowed) == 0 {
		return "（无）"
	}
	parts := make([]string, 0, len(allowed))
	for _, a := range allowed {
		parts = append(parts, string(a))
	}
	return strings.Join(parts, ", ")
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
	MechanismState   RetentionMechanismState
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
//
// ★ §143.59：列数由 11 变 12 —— 新增 mechanism_state（插在 ttl_registered 之后，
//
//	verdict/method/evidence 顺延）。**列数是硬契约**：多一列少一列都报错，
//	否则"登记册少了一列"会被静默当成"数据行少了一个字段"。
func ParseRetentionShapeRow(line string) (RetentionShapeRow, error) {
	fields := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
	if len(fields) != 12 {
		return RetentionShapeRow{}, fmt.Errorf("期望 12 列，实际 %d 列：%q", len(fields), line)
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
	mechanism := RetentionMechanismState(fields[8])
	if mechanism == "" {
		return RetentionShapeRow{}, fmt.Errorf("mechanism_state 列为空：%q", line)
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
		MechanismState:   mechanism,
		Verdict:          RetentionVerdict(fields[9]),
		Method:           fields[10],
		Evidence:         fields[11],
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
		MechanismState:   r.MechanismState,
	}
}
