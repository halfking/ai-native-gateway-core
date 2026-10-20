//go:build !integration

package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// v1ReaderRetirementTiering —— 读 v1 的生产文件 × **消费面分档**（审计 §9.262）。
//
// # 它和已有那张表不是一回事
//
// `requestLogsReadInventory`（108 文件 / 256 调用点，R48-A10 订正）答的是「谁在文本上写了
// `from _request_logs…`」；`cmd/tools/sql_source_indirection_audit` 答的是
// 「哪些读点的关系名是 Go 标识符拼进去的」。**两张表是两个总体，不是一张表的两半**
// —— 这正是 §9.259 那句「114」踩的坑，下面 TestV1ReaderTieringDomainIsBothPopulations
// 把两个总体各自的成员钉住，就是为了不让下一次再把它们当成一个数。
//
// 本表答第三个问题：**v1 退役后，这个读点的消费者还在不在**。四档：
//
//	① 随 v1 一起退役   —— 读 v1 的唯一存在理由就是 v1 自身（生命周期 DDL、一次性迁移工具）
//	② 必须迁           —— 消费者在 v1 退役后仍存在（对外 API / 计费 / 聚合 / 会话读取）
//	③ 按设计继续读 v1  —— 消费者就是「观测 v1/凭证/节点」本身，**没有外部契约依赖它**
//	④ 待拍板           —— 判据不唯一，把具体疑点写进 Reason
//
// # 为什么不自动分档
//
// `requestLogsReadInventory` 的文件头已经记过一次教训：自动分 A/B/C/D 判错 5 个。
// 本表的分档判据在**消费面**（不是语句窗口），但仍然是**人判 + 门核**：
// 每条都带一句可核的 Reason，门逐条断言 Reason 非空、并断言每个文件确实读 v1。
//
// # 它只出表，不改指
//
// 本表**不修改任何读方**。② 档的迁移动作是属主的决定，不是本门的。
type retirementTieringEntry struct {
	Tier   string
	Reason string
}

var v1ReaderRetirementTiering = map[string]retirementTieringEntry{
	// ★ 2026-10-20（迁移 890，v1 族真删）：本表**已清空**。
	//
	// 这张表是「读 v1 的文件按消费面分档」——而 890 之后 **没有任何文件再读 v1**
	// ⇒ 「这个读方退役后怎么办」这个问题**没有对象**了。
	//
	// ⇒ 本表保留（不删）：它守的是「**不许出现新的 v1 读方**」
	// （域必须恒等于「字面量总体 ∪ 间接总体」，见 TestV1ReaderTieringDomainIsBothPopulations）。
	//
	// ⚠ 不要在这里手填「已知的例外」—— 已退役关系的残留引用登记在
	// retiredV1ReferenceExceptions（生产缺陷台账）。
}

// ★ 「与运行时对账」不是形式：推导源是 `sql/objects/views/*.sql` +
// `db/request_logs_view_schema.go`，而后面那两个 `…_without_*` **没有仓库 DDL 文件**
// （composer 运行时建）。只看目录会漏掉它们，而漏掉的方向是
// **把读它们的文件判成 canonical（安全）**。
//
// ⚠ 本集合**只答「有没有 v1 臂」，不答「v1 臂占多大比例」**。
var v1ArmViewNamesRuntimeChecked = []string{
	// ★ 2026-10-20（迁移 890，v1 族真删）：本集合**已清空** ——
	// **没有任何视图带 v1 臂**了：
	//
	//	· request_logs_with_current_month —— 890 之后是会话族两臂体（无 v1 臂）；
	//	· request_logs_with_current_month_without_customer_id
	//	· request_logs_with_current_month_without_request_class_due_at
	//	· request_logs_bodies_with_current_month
	//	  —— 三张都被 890 直接 DROP（sql/migrations/startup/890_drop_request_logs_v1_family.sql）。
	//
	// ⇒ 「v1 臂视图」这个概念本身退役了。
}

// v1ArmLiteralBlindSpot 是「读 v1 臂视图、但关系名是字面量 ⇒ 拼接点工具完全看不见」的文件。
//
// ★ 规模比 §9.260 登记的 3 个大一个量级：**39 个**（当时点名的
// attempt_quality_api / usage_trend_series / auto_route_correlations 只是其中三个）。
//
// 后果要说准：`sql_source_indirection_audit` 的收尾行
// 「★ 退役读方清单 = v1 基表 12 处 + v1 臂视图 26 处 = 38 处 / 21 个文件」
// **不是退役读方清单** —— 115 个读 v1 的文件里它只覆盖 21 个。
// 那 39 个不是「被判成安全」，是**连站点都不产生**（工具只枚举 `+` 拼接点）。
//
// ⇒ 这 39 个由本门覆盖，而不是没人管：下面的
// TestV1ArmLiteralBlindSpotIsPinnedAndTiered 逐个点名。
var v1ArmLiteralBlindSpot = []string{
	// ★ 2026-10-20（迁移 890，v1 族真删）：本集合**已清空** ——
	// 没有 v1 臂视图 ⇒ 没有「读 v1 臂视图且工具看不见」的文件。
	//
	// ⚠ 本集合保留（不删）：它守的是「**不许出现新的工具盲区**」
	// （盲区定义：读 v1 臂视图 + 无拼接 FROM ⇒ 拼接点工具看不见）。
}

// v1ReaderIndirectPopulation 是**第二个总体**：读 v1 但关系名不是 FROM 字面量的文件，
// 22 个 = 拼接点工具的 v1∪v1臂两个桶（21 个）+ §9.258 表格里那 3 处手验确认读
// v1 基表所在的文件（`bg/auto_route_settle_sql.go`，`src.TurnsTable`）。
//
// ★ **这道门存在的理由就是「两个总体必须分别钉住」**。
// §9.259 写「并集 114」时，公式里的 21 用的是工具桶（**不含**
// `bg/auto_route_settle_sql.go`），而同一段那份「7 个不在字面量表里」的手写清单
// **含**它、**不含** `admin/dashboard_board_queries.go` —— 后者直到 §9.260
// 把 composer 建的视图补进推导源才变成 v1 读方。两个总体各 7 个、总数差 1，
// 而**没有任何一道门会红**。
//
// 实测：字面量 107 ∩ 间接 22 = 14，并集 = **115**。
var v1ReaderIndirectPopulation = []string{
	// ★ 2026-10-20（迁移 890，v1 族真删）：本总体**已清空** ——
	// 没有「读 v1 但关系名不是 FROM 字面量」的文件。
	//
	// ⚠ 本总体保留（不删）：它与「字面量总体」成对，构成
	// TestV1ReaderTieringDomainIsBothPopulations 的两个总体。
	// 两个总体都为空 ⇒ 并集为空 ⇒ 分档域必须为空。
}

// TestV1ReaderTieringDomainIsBothPopulations 把两个总体分别钉住。
//
// ★ 这道门存在的唯一理由：§9.259 写下「并集 114 文件」时，
// 公式里的「21」用的是工具的 v1∪arm 桶（**不含** bg/auto_route_settle_sql.go），
// 而同一段的「7 个不在字面量表里」那份手写清单**含**它、**不含**
// admin/dashboard_board_queries.go（§9.260 修好推导器之后它才变成 v1 读方）。
// 两个总体各 7 个、总数差 1，**没有任何一道门会红** —— 114 就这么写进了文档。
//
// 实测：域 = 108 ∪ 22 − 14 = **116**。
func TestV1ReaderTieringDomainIsBothPopulations(t *testing.T) {
	literal := map[string]bool{}
	for f := range requestLogsReadInventory {
		literal[f] = true
	}
	indirect := map[string]bool{}
	for _, f := range v1ReaderIndirectPopulation {
		if indirect[f] {
			t.Errorf("v1ReaderIndirectPopulation 里 %s 重复登记", f)
		}
		indirect[f] = true
	}
	domain := map[string]bool{}
	for f := range v1ReaderRetirementTiering {
		domain[f] = true
	}

	// ★ 890 之后（2026-10-20）：两个总体都必须为空。
	//
	// 原来这两条「双向比较」在总体非空时才是主判据；总体归零之后它变成
	// 「∅ == ∅ ⇒ 恒绿」⇒ 必须把「总体为空」本身写成断言。
	if len(literal) > 0 || len(indirect) > 0 {
		t.Errorf("两个总体非空（字面量 %d + 间接 %d）—— 890 之后 v1 族已物理删除，"+
			"不该再有读 v1 的文件。", len(literal), len(indirect))
	}

	// ① 域 == 字面量 ∪ 间接（双向，少一个就红）
	var missingFromDomain, notInEither []string
	for f := range domain {
		if !literal[f] && !indirect[f] {
			notInEither = append(notInEither, f)
		}
	}
	for f := range literal {
		if !domain[f] {
			missingFromDomain = append(missingFromDomain, f)
		}
	}
	// 两个总体各查一遍，**去重**后再报 —— 同一文件在两遍里都缺时只报一次，
	// 否则报错里会出现同一个文件两行，读的人要再判一次哪个是真因。
	seenMissing := map[string]bool{}
	for _, f := range v1ReaderIndirectPopulation {
		if !domain[f] {
			seenMissing[f+"（间接总体但无分档）"] = true
		}
	}
	for f := range indirect {
		if !literal[f] && !domain[f] {
			seenMissing[f+"（间接总体但无分档）"] = true
		}
	}
	for m := range seenMissing {
		missingFromDomain = append(missingFromDomain, m)
	}
	sort.Strings(missingFromDomain)
	sort.Strings(notInEither)
	if len(missingFromDomain) > 0 {
		t.Errorf("有 %d 个读 v1 的文件没有分档（新增读方必须分档，不能靠「表还在」蒙过去）：%s",
			len(missingFromDomain), strings.Join(missingFromDomain, " "))
	}
	if len(notInEither) > 0 {
		t.Errorf("分档域里有 %d 个文件既不在字面量总体也不在间接总体（收录了从不读 v1 的文件）：%s",
			len(notInEither), strings.Join(notInEither, " "))
	}

	// ② 四个数必须与器眼一致。「114 → 115」就是死在这条上的：
	//    三个数各自都对，**只有并集**因为两个总体换了成员而错了 1。
	inter := 0
	for f := range indirect {
		if literal[f] {
			inter++
		}
	}
	union := len(domain)
	t.Logf("两个总体：字面量 %d + 间接 %d − 交集 %d = 并集 %d（域实测 %d）",
		len(literal), len(indirect), inter, len(literal)+len(indirect)-inter, union)
	for _, c := range []struct {
		name     string
		got, exp int
	}{
		// 2026-10-13（S3 波 2 停写切换）：request_summary / model_alternatives
		// 两文件读源整体迁会话族原生源，自字面量总体与分档域同步除名
		//（106/24/16/114）；分档域只收 v1 读方，两个文件的退役负担随
		// _request_logs 族的边界一起消失。
		// 2026-10-13（S3 波 3，同日第二分片）：admin/routing.go 同款除名
		//（popularModelsHotSQL → 会话族原生源；106→105、114→113）。
		// 2026-10-13（S4 波 4）：compression_sessions / scenario_driver 整体
		//		出 v1 族（105→103；114→113→111）。
		// 2026-10-15（24h 审计）：admin/session_online.go 两条腿都出 v1
		//		（online → session_summaries；timeline → 会话族原生源），
		//		同款除名（字面量 101→100、域 110→109）。
		// 2026-10-16（v1 退役轮）：admin/auto_route_outcome_freshness.go 的读点
		//		改成拼接 bg.ActiveSettleTurnsTable()，自字面量总体移入间接总体
		//		（字面量 100→99、间接 26→27）。**交集 17 与并集 109 都不变**——
		//		它换的是总体归属，不是读方数量。
		// ★ 2026-10-20（迁移 890，v1 族真删）：四个数全部归零。
		// v1 族被物理删除 ⇒ 字面量总体为空、间接总体为空、交集为空、
		// 并集（域）为空。这四个数从此刻起是**棘轮**：任何一个新的 v1 读方
		// 出现都会让其中至少一个不再等于 0 ⇒ 门红。
		{"字面量总体", len(literal), 0},
		// 2026-10-13（S4 停写交接）：body_resolver / unified_detail 因加了
		// session 兜底拼接点自盲区桶迁入（+2）；两者同时仍是字面量读方
		//（交集 14→16），并集不变。
		// 2026-10-14（合并轮）：data_lifecycle 趋势跨族化后同款迁入
		//（间接 25→26、交集 16→17，并集不变——它本就在字面量总体里）。
		// 2026-10-16（v1 退役轮）：outcome_freshness 拼接化后同款迁入
		//（间接 26→27）。交集仍为 17：它是从字面量总体**移出**再进本总体，
		//	入表前不在交集里、入表后仍不在交集里（两处总体互换了同一个成员）。
		// candidate monitor moved its staleness view name into a constant: +1 indirect and intersection.
		{"间接总体", len(indirect), 0},
		{"交集", inter, 0},
		{"并集（域）", union, 0},
	} {
		if c.got != c.exp {
			t.Errorf("%s = %d，期望 %d", c.name, c.got, c.exp)
		}
	}
	if got, exp := len(literal)+len(indirect)-inter, union; got != exp {
		t.Errorf("★ 并集算式 %d ≠ 域实测 %d —— 「114」那类错误就发生在这里："+
			"三个数各自都对，但两个总体换了成员，并集差 1 且没有任何门会红",
			got, exp)
	}

	// ③ 间接总体里**不在**字面量总体的那几个，逐个列名。
	//    §9.259 手写的那份是 7 个（maas×3 / usage_credits / session_compare /
	//    session_export / auto_route_settle_sql），实测是 8 个 ——
	//    多的那个是 admin/dashboard_board_queries.go（§9.260 之后才变成 v1 读方）。
	var indirectOnly []string
	for f := range indirect {
		if !literal[f] {
			indirectOnly = append(indirectOnly, f)
		}
	}
	sort.Strings(indirectOnly)
	t.Logf("只在间接总体、不在字面量总体的 %d 个：%s", len(indirectOnly), strings.Join(indirectOnly, " "))
}

// TestV1ReaderTieringEveryFileProvenToReadV1 让分档表**自证**总体。
//
// 一张 116 行的手写表，最典型的腐烂形态是某个文件早就改指会话源了，
// 而它还留在表里被当成退役工作量。这里对每个文件独立取证：剥掉 Go 与 SQL 注释后，
// 它必须仍然含 v1 关系的 `FROM/JOIN` 字面量，或含一个 v1 关系名字面量。
//
// ★ 带阴性对照：判据在注释里写 `FROM _request_logs_hot` 时必须给 0。
// 没有对照的话，「剥注释」写错了也会一直绿。
func TestV1ReaderTieringEveryFileProvenToReadV1(t *testing.T) {
	root := repoRootFromCaller(t)

	// 阴性 + 阳性对照（两条通路各一组）。没有对照，「剥注释没生效」
	// 与「正则写窄了」都会表现为全体假绿，而假绿看起来和通过一模一样。
	if l, _ := v1ReadEvidence("-- FROM _request_logs_hot\n// FROM _request_logs_bodies_hot\n"); l != 0 {
		t.Errorf("阴性对照失败：注释里的 FROM _request_logs 被判成 %d 个读点，期望 0 —— "+
			"剥注释没生效，这道门会恒绿", l)
	}
	// ★ 890 之后（2026-10-20）：阳性对照改用**真 v1 底表**。
	//
	// 原来的对照是 `FROM request_logs_with_current_month` ⇒ 它依赖
	// `isV1Relation` 认得 canonical 视图 —— 而那份认知来自
	// `v1ArmViewNamesRuntimeChecked`（canonical 视图当时**带 v1 臂**）。
	// 890 之后 canonical 视图是会话族两臂体、**不再是 v1** ⇒ 它从 v1 关系集里
	// 退出 ⇒ 原对照失效。⇒ 对照改用 `FROM _request_logs_hot`（真 v1 底表）。
	if l, sp := v1ReadEvidence("SELECT 1 FROM _request_logs_hot"); l == 0 || sp != 0 {
		t.Errorf("阳性对照失败：字面量 FROM _request_logs_hot → literal=%d spliced=%d，"+
			"期望 literal≥1 且 spliced=0（下面的逐文件取证会全体假绿）", l, sp)
	}
	// 顺带钉住一件新事实：canonical 视图**不再**被算成 v1 读点。
	if l, _ := v1ReadEvidence("SELECT 1 FROM request_logs_with_current_month"); l != 0 {
		t.Errorf("canonical 视图被算成 %d 个 v1 读点，期望 0 —— 890 之后它是会话族两臂体，"+
			"不是 v1；把它算成 v1 会让「v1 读方总体为零」这句话不成立", l)
	}
	if l, sp := v1ReadEvidence("SELECT 1 FROM ` + logsTable + ` WHERE 1=1"); sp == 0 || l != 0 {
		t.Errorf("拼接对照失败：FROM + logsTable + → literal=%d spliced=%d，期望 spliced≥1 且 literal=0", l, sp)
	}
	// ★ 这条对照来自 domains/streaming/model_alternatives.go —— 那个文件里
	// `-- request_logs_with_current_month …` 只在 SQL 注释中，真读点是
	// `FROM _request_logs_hot`。剥注释若不钻进原始字符串，它会被误判成臂视图读方。
	if readsAnyV1ArmView("`\n-- request_logs_with_current_month is a UNION\nFROM _request_logs_hot\n`") {
		t.Errorf("对照失败：原始字符串里的 SQL 注释被判成 v1 臂视图读点 —— " +
			"剥注释没有钻进字符串里（domains/streaming/model_alternatives.go 就是这个形状）")
	}
	if l, _ := v1ReadEvidence("SELECT 1 FROM session_turns_hot"); l != 0 {
		t.Errorf("会话族对照失败：FROM session_turns_hot 被判成 %d 个 v1 读点，期望 0 —— "+
			"这说明 v1 判据宽到会把会话族收进来", l)
	}

	indirect := map[string]bool{}
	for _, f := range v1ReaderIndirectPopulation {
		indirect[f] = true
	}

	// ★ 890 之后（2026-10-20）：分档表必须为空 —— 没有读 v1 的文件 ⇒ 没有可分档的。
	//
	// ⚠ 下面那段逐文件取证在表为空时是**空转**，所以「表为空」这条必须显式断言；
	// 而「取证器还有效」由本函数开头的阴性/阳性/拼接三组对照保证。
	if len(v1ReaderRetirementTiering) > 0 {
		t.Errorf("分档表仍有 %d 条 —— 890 之后 v1 族已物理删除，没有读 v1 的文件可分档。",
			len(v1ReaderRetirementTiering))
	}

	var unproven, splicedNotInPopulation []string
	for f := range v1ReaderRetirementTiering {
		b, err := readFileForEvidence(filepath.Join(root, f))
		if err != nil {
			unproven = append(unproven, f+"（读不到文件："+err.Error()+"）")
			continue
		}
		literal, spliced := v1ReadEvidence(b)
		switch {
		case literal > 0:
			// 指名了 v1 —— 直接成立
		case spliced > 0 && indirect[f]:
			// 拼接形态 **且** 在第二个总体里：两个器眼互相印证。
			// 只满足拼接是不够的 —— 一个 `FROM ` + x + ` 也可能拼的是会话族。
		case spliced > 0:
			splicedNotInPopulation = append(splicedNotInPopulation, f)
		default:
			unproven = append(unproven, f)
		}
	}
	sort.Strings(unproven)
	sort.Strings(splicedNotInPopulation)
	if len(splicedNotInPopulation) > 0 {
		t.Errorf("以下 %d 个文件是「FROM 拼 Go 表达式」形态，但**不在** v1ReaderIndirectPopulation 里 —— "+
			"要么它拼的是会话族，要么第二个总体漏了它：%s",
			len(splicedNotInPopulation), strings.Join(splicedNotInPopulation, " "))
	}
	if len(unproven) > 0 {
		t.Errorf("以下 %d 个已分档文件在当前源码里**取不到任何 v1 读点证据**"+
			"（既不指名 v1 关系，也不是拼接形态；可能已改指会话源，或分档表收录了从不读 v1 的文件）：%s",
			len(unproven), strings.Join(unproven, " "))
	}
}

// TestV1ReaderTiersAssertMembers 断言**成员**，不只断言计数。
//
// 为什么必须是成员：一个计数可以被「一个退出、一个进入」换掉而不变。
// §9.173 的 ledger 门就是这么放行的，这里不重复那个错。
func TestV1ReaderTiersAssertMembers(t *testing.T) {
	byTier := map[string][]string{}
	for f, e := range v1ReaderRetirementTiering {
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("%s 的分档 Reason 为空 —— 没有理由的分档就是猜", f)
		}
		byTier[e.Tier] = append(byTier[e.Tier], f)
	}
	for k := range byTier {
		sort.Strings(byTier[k])
	}
	// ★ 890 之后（2026-10-20）：分档表必须为空。
	if len(v1ReaderRetirementTiering) > 0 {
		t.Errorf("消费面分档表仍有 %d 条 —— 890 之后 v1 族已物理删除，没有读 v1 的文件可分档。",
			len(v1ReaderRetirementTiering))
	}
	t.Logf("消费面分档（域 %d 个文件）：①随v1退役 %d · ②必须迁 %d · ③按设计继续读v1 %d · ④待拍板 %d",
		len(v1ReaderRetirementTiering), len(byTier["①"]), len(byTier["②"]),
		len(byTier["③"]), len(byTier["④"]))

	// ① 逐个点名。这 4 个是「读 v1 的理由就是 v1 自己」，少一个就有一个 DDL/一次性工具
	// 在退役后还留在排期里。
	// ★ 890 之后（2026-10-20）：四档全部为空 —— 没有读 v1 的文件可分档。
	wantRetireWithV1 := []string{}
	assertTierMembers(t, byTier, "①", wantRetireWithV1)

	// ③ 逐个点名。这一档是「按设计继续读 v1」，属主若要改判，必须在这里改，
	// 而不是让某个文件在某次改动里悄悄换档。
	wantKeepReadingV1 := []string{}
	assertTierMembers(t, byTier, "③", wantKeepReadingV1)

	// ④ 钉住：待拍板这一档只许缩小，不许静默增长。增长意味着有人遇到了新疑点
	// 却没登记 —— 那正是「114 写成 114」那类问题的温床。
	wantUndecided := []string{}
	assertTierMembers(t, byTier, "④", wantUndecided)

	// ② 是补集，不点名 —— 但它的规模要与算式对得上，否则前三档被改也会无人察觉
	if got, want := len(byTier["②"]), len(v1ReaderRetirementTiering)-
		len(byTier["①"])-len(byTier["③"])-len(byTier["④"]); got != want {
		t.Errorf("② 档 %d 个，与补集算式 %d 个不符", got, want)
	}
	for _, f := range byTier["②"] {
		for _, g := range append(append([]string{}, byTier["①"]...), byTier["③"]...) {
			if f == g {
				t.Errorf("%s 同时出现在 ② 与 ①/③", f)
			}
		}
	}
}

// assertTierMembers 双向断言：期望集合里每个都在该档，且该档**没有多余**成员。
func assertTierMembers(t *testing.T, byTier map[string][]string, tier string, want []string) {
	t.Helper()
	// ★ 期望名单自身必须无重复。下面的比较全是集合语义（have 是 map），
	//   所以**名单里多写一遍同一个文件永远不会被下面任何一条抓到** ——
	//   集合相等，缺 0 多 0，门照样绿，而「字面量 29 / 唯一 28」这种事就静默存在。
	//   §9.265 追加 `cmd/gateway/v1_write_liveness.go` 时就是这么把
	//   `cmd/gateway/dual_read_validator.go` 又写了一遍的。
	seen := map[string]bool{}
	var dup []string
	for _, f := range want {
		if seen[f] {
			dup = append(dup, f)
		}
		seen[f] = true
	}
	if len(dup) > 0 {
		sort.Strings(dup)
		t.Errorf("档位 %s 的期望名单自身有 %d 个重复项（%d 项字面量 / %d 个唯一文件）：%s —— "+
			"集合相等，下面两条比较抓不到这种错；名单必须逐个唯一",
			tier, len(dup), len(want), len(seen), strings.Join(dup, " "))
	}
	have := map[string]bool{}
	for _, f := range byTier[tier] {
		have[f] = true
	}
	var missing, extra []string
	for _, f := range want {
		if !have[f] {
			missing = append(missing, f)
		}
	}
	for _, f := range byTier[tier] {
		found := false
		for _, g := range want {
			if f == g {
				found = true
				break
			}
		}
		if !found {
			extra = append(extra, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("档位 %s 缺 %d 个成员（应在此档却不在）：%s", tier, len(missing), strings.Join(missing, " "))
	}
	if len(extra) > 0 {
		t.Errorf("档位 %s 多出 %d 个成员（在此档却不在期望集合里；改判要同时改门）：%s",
			tier, len(extra), strings.Join(extra, " "))
	}
}

// TestV1ArmViewSetMatchesRuntimeTruth 把 v1 臂视图集合钉在**运行时真库**对过账的 4 个上。
//
// 这不是抄一份名单：门要回答的是「这个集合会不会悄悄少一个」。
// §9.260 的实测就是少两个 —— 少的方向是把有风险的报成安全。
func TestV1ArmViewSetMatchesRuntimeTruth(t *testing.T) {
	// ★ 890 之后（2026-10-20）：**没有任何视图带 v1 臂**了。
	//
	//	· request_logs_with_current_month —— 890 之后是会话族两臂体（无 v1 臂）；
	//	· request_logs_with_current_month_without_customer_id
	//	· request_logs_with_current_month_without_request_class_due_at
	//	· request_logs_bodies_with_current_month
	//	  —— 三张都被 890 直接 DROP。
	//
	// ⇒ 本门从「集合必须与运行时真库对账」翻成
	//「**集合必须为空，且 composer 不得再重建那三张被 DROP 的视图**」。
	if len(v1ArmViewNamesRuntimeChecked) > 0 {
		t.Errorf("v1 臂视图集合仍有 %d 个 —— 890 之后没有任何视图带 v1 臂：%s",
			len(v1ArmViewNamesRuntimeChecked), strings.Join(v1ArmViewNamesRuntimeChecked, " "))
	}

	b, err := readFileForEvidence(filepath.Join(repoRootFromCaller(t), "db", "request_logs_view_schema.go"))
	if err != nil {
		t.Fatalf("读 composer 失败：%v", err)
	}
	for _, name := range []string{
		"request_logs_with_current_month_without_customer_id",
		"request_logs_with_current_month_without_request_class_due_at",
		"request_logs_bodies_with_current_month",
	} {
		if strings.Contains(b, "CREATE VIEW public."+name) ||
			strings.Contains(b, "CREATE OR REPLACE VIEW public."+name) {
			t.Errorf("composer 里又出现 CREATE VIEW public.%s —— 890 已经把它 DROP，"+
				"重建它会让「v1 臂视图集合为空」这句话不成立", name)
		}
	}
	// 阳性对照：canonical 视图必须**仍然**被 composer 建出来（它是活的读面）。
	if !strings.Contains(b, "request_logs_with_current_month") {
		t.Error("composer 里找不到 canonical 视图 request_logs_with_current_month —— " +
			"它是 890 之后唯一还活着的读面，丢了它整条读链就没源了")
	}
	t.Logf("v1 臂视图（与真库 pg_class 对账）%d 个：%s",
		len(v1ArmViewNamesRuntimeChecked), strings.Join(v1ArmViewNamesRuntimeChecked, " "))
}

// TestV1ArmLiteralBlindSpotIsPinnedAndTiered 把那 39 个「工具看不见」的文件钉住，
// 并证明它们**已被本门覆盖**（都在分档域里、都有档位）。
//
// 这道门是 §9.260 那个盲区的收口：盲区本身在工具里补不了
// （它只枚举拼接点），但可以在**总体**这一层补 ——
// 只要这些文件一个都不许漏出门。
func TestV1ArmLiteralBlindSpotIsPinnedAndTiered(t *testing.T) {
	root := repoRootFromCaller(t)

	// ★ 声明的清单必须与**从源码树重算**的结果双向相等。
	// 只做「列表里每个都还成立」是不够的：那是一条只许不变的单向检查，
	// 有人把一个条目删掉，门照样绿 —— 而清单变小的方向恰好是
	// 「少了一个有风险的读方」，也就是最坏的方向。
	declared := map[string]bool{}
	for _, f := range v1ArmLiteralBlindSpot {
		if declared[f] {
			t.Errorf("v1ArmLiteralBlindSpot 里 %s 重复登记", f)
		}
		declared[f] = true
	}

	recomputed := map[string]bool{}
	var badEvidence []string
	for f := range v1ReaderRetirementTiering {
		b, err := readFileForEvidence(filepath.Join(root, f))
		if err != nil {
			badEvidence = append(badEvidence, f+"（读不到文件）")
			continue
		}
		literal, spliced := v1ReadEvidence(b)
		// 盲区定义：读 v1 臂视图（literal 通路），且**没有**拼接 FROM
		// （有拼接就会被拼接点工具看见，那就不是盲区）。
		if readsAnyV1ArmView(b) && spliced == 0 {
			recomputed[f] = true
		}
		_ = literal
	}

	var missing, extra []string
	for f := range recomputed {
		if !declared[f] {
			missing = append(missing, f)
		}
	}
	for _, f := range v1ArmLiteralBlindSpot {
		if !recomputed[f] {
			extra = append(extra, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(badEvidence)
	if len(missing) > 0 {
		t.Errorf("以下 %d 个文件现在满足盲区定义（读 v1 臂视图 + 无拼接 FROM）却不在 v1ArmLiteralBlindSpot 里：\n"+
			"  %s\n"+
			"—— 清单变小 = 少了一个有风险的读方，这是最坏的方向，必须显式登记",
			len(missing), strings.Join(missing, " "))
	}
	if len(extra) > 0 {
		t.Errorf("以下 %d 个文件已不再满足盲区定义（可能改指了、或改成了拼接形态因而工具能看见了）：%s",
			len(extra), strings.Join(extra, " "))
	}
	if len(badEvidence) > 0 {
		t.Errorf("读不到源文件：%s", strings.Join(badEvidence, " "))
	}

	// 每一个盲区读方都必须有档位 —— 盲区不是「没人管」的同义词。
	var untiered []string
	for f := range recomputed {
		if _, ok := v1ReaderRetirementTiering[f]; !ok {
			untiered = append(untiered, f)
		}
	}
	sort.Strings(untiered)
	if len(untiered) > 0 {
		t.Errorf("以下 %d 个盲区读方没有分档：%s", len(untiered), strings.Join(untiered, " "))
	}
	t.Logf("拼接点工具看不见的 v1 臂读方 %d 个 —— 工具收尾行「退役读方清单 21 个文件」"+
		"覆盖不到它们；覆盖它们的是 %d 个文件的分档域",
		len(recomputed), len(v1ReaderRetirementTiering))
}

// readFileForEvidence 读文件。取证只需要文本。
func readFileForEvidence(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// fromJoinV1RE 捕获 `FROM <关系名>` / `JOIN <关系名>` 后的**完整标识符**。
//
// ★ 为什么捕获再判、而不是把 v1 表名直接写进正则：
// Go 用的是 RE2，**不支持 lookahead**（`(?![a-z0-9_])` 编译不过），
// 而 `\b` 在这里**不可用** —— `_` 是词字符，所以 `_request_logs_bodies_hot`
// 会在 `_bodies` 之后要求一个词边界，而下一个字符是 `_` ⇒ 永不成立
// ⇒ bodies 视图被漏判。首次对账时 4 个 v1 臂视图全部报「不含 v1 基表」，
// 就是这个坑。所以改成「捕获标识符 → 用集合判定」。
var fromJoinV1RE = regexp.MustCompile(`(?i)\b(?:from|join)\s+(?:public\.)?([a-z_][a-z0-9_]*)`)

// splicedFromRE 匹配「`FROM` 后面跟的是 Go 表达式」——即关系名是拼进去的。
//
// 这是**间接**读方的形态：文件自己的源码里**没有任何 v1 关系名**，
// 名字由另一个文件的切换层（或结构体字段）送进来。
var splicedFromRE = regexp.MustCompile(`(?i)\b(?:from|join)\s+(?:` + "`" + `|"|[a-zA-Z_]\w*(?:\.[a-zA-Z_]\w*)*\s*\+)`)

// quotedV1RelationRE 捕获字符串字面量的内容，用于切换层 / 结构体字段形态
// （`TurnsTable: "_request_logs_hot"`、`return "request_logs_with_current_month"`）。
var quotedV1RelationRE = regexp.MustCompile(`"([^"\n]*)"`)

// v1RelationNames 是 v1 族关系的判定集合（**闭合**，不是通配）。
func isV1Relation(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	// 两代物理名并存：迁移 860（2026-10-14）把 v1 底表改名为 `_` 前缀
	// （待删除标志）。生产 Go 源码已随清扫只用前缀名，但 SQLite 侧
	// （storage/sqlite/request_log_store.go，另一个存储面）与历史迁移
	// 文本仍是旧名——分档域的「读 v1」证据对两代名字都要认得。
	case "request_logs", "request_logs_hot", "request_logs_bodies",
		"request_logs_bodies_hot", "request_logs_archive",
		"_request_logs", "_request_logs_hot", "_request_logs_bodies",
		"_request_logs_bodies_hot", "_request_logs_archive":
		return true
	}
	for _, v := range v1ArmViewNamesRuntimeChecked {
		if strings.EqualFold(strings.TrimSpace(name), v) {
			return true
		}
	}
	return false
}

// firstRelationToken 取 `FROM _request_logs_hot r` 里的第一个 token，
// 并剥掉 `AS` 别名。`FROM x, y` 这种逗号列表不在本判据内（v1 读点不会这样写）。
func firstRelationToken(rest string) string {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	if len(fields) > 1 && strings.EqualFold(fields[1], "as") {
		return fields[0]
	}
	return fields[0]
}

// stripGoAndSQLComments 剥掉 Go 注释**与** SQL 注释，**两遍**。
//
// ★ 为什么必须两遍、且第二遍要钻进字符串里：
// 本轮两次都栽在这上面。
//
//	第 1 次：把 `'` 当引号 ⇒ SQL 常量 `IN ('success','failure')` 里的单引号
//	        被当字符串开始，一路吃到下一个 `'` ⇒ 半条 SQL 当注释删掉。
//	第 2 次（更隐蔽）：把 Go 原始字符串（反引号）当**不透明整体**跳过，
//	        于是字符串**内部**的 SQL 注释活了下来 ——
//	        `domains/streaming/model_alternatives.go` 的第 230 行
//	        `-- request_logs_with_current_month is a UNION of …` 被当成真读点，
//	        而它真正的读点是第 244 行的 `FROM _request_logs_hot`（**基表**，不是臂视图）。
//	        那个文件正是 `request_logs_reader_population_test.go` 举的反例，
//	        原因一字不差。
//
// 做法：第 1 遍只剥 Go 注释（**保留**字符串内容）；第 2 遍对整体剥 SQL 注释 ——
// 此时字符串外的 `--` 已经是注释，Go 代码里不存在合法的 `--`，
// 所以这一遍不会误伤。
func stripGoAndSQLComments(src string) string {
	return stripSQLComments(stripGoCommentsKeepingStrings(src))
}

// stripGoCommentsKeepingStrings 已移到 `admin/go_comment_strip_test.go`。
//
// ★ R69 轮：它原来在本文件里，而本文件带 `//go:build !integration`
// ⇒ `-tags=integration` 的树里它不存在 ⇒ §143.36 那道门
// （`admin/section_14336_board_single_scan_test.go`）**在 integration 树里编译不过**
// ⇒ ⇒ 整棵 integration 树编译失败 ⇒ 连带拉红 `sql/schema` 的
// `TestIntegrationTaggedTreeCompiles`（那道门本来就是为了抓这种问题而存在的）。
// ⇒ **共享 helper 的定义文件不能带 build tag**。

// v1ReadEvidence 返回两条独立通路的计数：
//
//	literal —— 源码里**指名**了 v1 关系（FROM/JOIN 字面量，或只含 v1 关系名的字符串字面量）
//	spliced —— `FROM` 后面跟的是 Go 表达式（关系名从别的文件拼进来）
//
// ★ 为什么必须分成两条：这 115 个文件里有 6 个**自己源码里一个 v1 关系名都没有**
// （admin/dashboard_board_queries.go · session_compare.go · session_export.go ·
// bg/auto_route_settle_sql.go · maas/consumption_detail.go · maas/credit_buckets.go）——
// 名字全在切换层那一份文件里。合并成一个数会把它们判成「没读 v1」，
// 那就是**把有风险的报成安全**，与本门要防的方向相反。
func v1ReadEvidence(src string) (literal, spliced int) {
	stripped := stripGoAndSQLComments(src)
	for _, m := range fromJoinV1RE.FindAllStringSubmatch(stripped, -1) {
		if isV1Relation(firstRelationToken(m[1])) {
			literal++
		}
	}
	for _, m := range quotedV1RelationRE.FindAllStringSubmatch(stripped, -1) {
		if isV1Relation(firstRelationToken(m[1])) {
			literal++
		}
	}
	return literal, len(splicedFromRE.FindAllString(stripped, -1))
}

// readsAnyV1ArmView 判断源码里是否读到了 4 个 v1 臂视图之一。
func readsAnyV1ArmView(src string) bool {
	stripped := stripGoAndSQLComments(src)
	for _, name := range v1ArmViewNamesRuntimeChecked {
		if strings.Contains(stripped, name) {
			return true
		}
	}
	return false
}
