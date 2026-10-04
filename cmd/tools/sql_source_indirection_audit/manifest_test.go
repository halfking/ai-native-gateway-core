//go:build !integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 全仓门：把 AuditRepo 的三桶输出变成**受维护的判定清单**。
//
// # 这道门补的是什么
//
// `main.go` 的收尾自己写着：
//
//	本工具不是门、不进 CI；输出是证据，不是待维护的登记表。
//
// 这句话在 §9.45 当时是**诚实的**——工具算错没人拦（§9.226.3 修掉了算错），
// 但「输出是证据」意味着**没有任何东西要求有人去看第二桶**。实测（2026-10-05）：
//
//	解析到 canonical / 会话族（退役安全）:  5 处
//	解析到 v1 宽族                        : 12 处 / 6 个文件
//	不可静态解析（需手验）                : 35 处 / 20 个文件
//
// 其中 6 个 v1 文件里有 **4 个在四张登记表里一处都没有**：
//
//	maas/usage.go  maas/consumption_detail.go  maas/credit_buckets.go
//	admin/usage_credits.go
//
// 这四个走 `requestLogsSource(days)` / `requestLogsFromClause(days)` 切换层，
// 而 `ClampUsageDays` 对 `days < 1` 返回 **1** ⇒ 默认分支是
// **`request_logs_hot AS r`，一张 v1 底表**。
// `maas/usage.go` 唯一的 `from request_logs` 文本在**第 27 行的注释里**
// ⇒ 按行扫描的 `requestLogsReadInventory` 看不见它，
// 按 AST 抽列的 exposure 门也只覆盖直接读方 ⇒ **四道门全部看不见**。
//
// 而这是**计费/用量**路径（`credits_charged` / `cost_usd` 聚合）。
//
// # 这道门为什么是**三桶都管**，而不是只管 v1 那一桶
//
// 只门 v1 桶的话，那 20 个「不可判定」的文件仍然无人负责——
// 而它们里任何一个都可能是个没被发现的 v1 读方。
// 本门要求**每个被工具报出的文件**都有一条判定，且这条判定
// **必须与工具自己的分类一致**（见下面的交叉核对）。
//
// # 交叉核对是承重部分
//
// 清单是人写的，工具是机器算的，两者独立。判定与实测不符 ⇒ 红。
// 方向很重要：
//
//   - 清单说 reads-v1、工具说 canonical ⇒ **红**。
//     这正是 §9.226.3 的 `admin/tenants.go`：`isV1Relation` 取第一个 token，
//     把一段 UNION 子查询判成 canonical，于是工具输出「退役安全」。
//     本轮修好了分类器；这道门保证**同类错误不会再悄悄通过**。
//   - 清单说 canonical-only、工具说 v1 ⇒ 红。
//   - 清单说 unresolved、工具却能解析 ⇒ 红（判定过期了）。
//
// 也就是说：这道门不是「把工具输出抄成一张表」，而是**让表和工具互相监督**。
// 单向抄表的话，工具算错时表会跟着错，两边一起绿。

// verdict 是清单里对「这个文件在本工具眼里是什么」的判定。
type verdict string

const (
	// verdictReadsV1：读 v1 宽族基表（request_logs / _hot / _bodies / _bodies_hot）。
	verdictReadsV1 verdict = "reads-v1"
	// verdictCanonicalOnly：读 canonical 视图或会话族。
	// ⚠ **「读 canonical 视图」不等于「退役后照常工作」**：视图的 v1 臂在
	// DROP 时会消失（§9.226.2 的 admin/session_online.go 就是这一类，
	// 而且它是 JOIN，连不上行、整页空）。
	verdictCanonicalOnly verdict = "canonical-only"
	// verdictNonV1ByInspection：工具解析不出，但**人读过源码**，确认拼进去的
	// 关系名不是 v1（跨包调用如 db.SessionFamilyTurnsForSessionSQL()、
	// probemode.GuardStateTable()、字面量表名列表等）。
	verdictNonV1ByInspection verdict = "nonv1-by-inspection"
	// verdictStillUnknown：还没查。**当前仓库里这一类必须为空**——
	// 见 TestManifestHasNoStillUnknownEntries。
	verdictStillUnknown verdict = "still-unknown"
)

// siteAssessment 是清单里的一条文件级判定。
type siteAssessment struct {
	// Verdict 是上面三者之一。必填。
	Verdict verdict

	// ResolvesTo 是 Verdict==reads-v1 时它读到的 v1 关系名。
	// 必填，且必须在 v1Tables 里 —— 这样「它读的是 v1」这句话可核，而不是断言。
	ResolvesTo string

	// Via 说明机制：哪个切换层 / 哪个变量把表名带进 SQL，
	// 以及**默认入参落在哪一支**。必填非空。
	//
	// ⚠ 「默认入参落在哪一支」是承重的，不是补充说明：
	// `maas.requestLogsSource(days)` 对 days<=7 返回 v1 底表、对 days>7 返回
	// canonical 视图。只写「条件性读 v1」而不写默认支，读者会以为两条路一样危险；
	// 实际上一旦有人把默认窗口调大，**风险会静默换一支**。
	Via string

	// Consequence 说明退役时它会怎样。必填非空。
	// ⚠ 对 verdictUnresolved，Consequence 必须写清「还差什么才能判定」，
	// 而不是写「无影响」——「还没查」与「查过没问题」在退出码上不可区分。
	Consequence string
}

// indirectSiteAssessments 是全仓三桶的**判定清单**。键是仓库相对路径。
// 判定依据是逐个文件读源码，不是抄工具输出。
var indirectSiteAssessments = map[string]siteAssessment{

	// ── 读 v1 宽族基表（6 文件 / 12 处）──────────────────────────────────
	// 这 6 个里有 **4 个在 admin 侧四张登记表里一处都没有**（§9.227）。
	// 它们全部走切换层，而切换层的表名以 Go 字符串进 SQL；
	// `maas/usage.go` 唯一的 `from request_logs` 文本在**第 27 行的注释里**，
	// 所以按行扫描的 requestLogsReadInventory 看不见它。
	"admin/tenants.go": {
		Verdict:    verdictReadsV1,
		ResolvesTo: "request_logs_hot",
		Via: "logsTable 是段 UNION 子查询：`(SELECT … FROM request_logs_hot UNION ALL " +
			"SELECT … FROM request_logs) -- sqlreadguard:allow …`（tenants.go:770），" +
			"用在 `FROM `+logsTable+` 的租户 credits/tokens/latency 聚合上（798/809/826/873/916）。" +
			"⚠ 两张表都在，登记只写第一个是因为 v1Tables 是集合查询；" +
			"另一张是 request_logs。",
		Consequence: "DROP 后这 5 个聚合**返回 0/NULL 而不是报错**：" +
			"COALESCE(SUM(...),0) 把「读不到」变成「这个租户没花过钱」。" +
			"⚠ 这是本清单里最危险的一条——它伪造的是**计费数字**。",
	},
	"admin/usage_credits.go": {
		Verdict:    verdictReadsV1,
		ResolvesTo: "request_logs_hot",
		Via: "requestLogsFromClause(days)（usage_credits.go:120）：days<=7 返回 " +
			"`request_logs_hot AS r`，否则 `request_logs_with_current_month AS r`。" +
			"默认支在 149 行的 `FROM `+logsTable+`。",
		Consequence: "同 tenants.go：credits 聚合读不到时 COALESCE 成 0，" +
			"租户用量页显示 0 credits 而非报错。",
	},
	"maas/usage.go": {
		Verdict:    verdictReadsV1,
		ResolvesTo: "request_logs_hot",
		Via: "requestLogsSource(days)（usage.go:66）：days<=7 → `request_logs_hot AS r`。" +
			"⚠ **默认支就是 v1 底表**：`ClampUsageDays`（usage.go:39）对 days<1 返回 **1**，" +
			"所以不传 days 或传 0 的调用全部落在 v1 一支。" +
			"用在 106/116/134/170 四处 `FROM `+logsTable+`（TotalRequests/TotalCredits/按模型/趋势）。",
		Consequence: "租户用量/计费 API（QueryUsageSummary 及 WithCost）。" +
			"DROP 后请求数与 credits 变 0，**对外仍然是 200 + 一份合法 JSON**。" +
			"⚠ 与 tenants.go 叠加：两条路径都伪造 0 的话，" +
			"「这个租户从没调用过」会被当成事实。",
	},
	"maas/consumption_detail.go": {
		Verdict:    verdictReadsV1,
		ResolvesTo: "request_logs_hot",
		Via: "同一套 requestLogsSource(days)（usage.go:66），" +
			"消费明细查询在 81 行 `FROM ` + logsTable + `。默认支同为 v1 底表。",
		Consequence: "消费明细按小时/按模型分组；读不到时**返回空分组而不是报错**，" +
			"页面上表现为「这段时间没有消费」。",
	},
	"maas/credit_buckets.go": {
		Verdict:    verdictReadsV1,
		ResolvesTo: "request_logs_hot",
		Via: "同一套 requestLogsSource(days)，49 行 `FROM `+logsTable+`，" +
			"写入小时级 credit 桶（ON CONFLICT DO UPDATE）。",
		Consequence: "⚠ **与其它几条不同类**：它不是读后展示，而是**写聚合桶**。" +
			"DROP 后桶会被写成 0（或被 ON CONFLICT 更新成 0），" +
			"而**已有的小时桶会被覆盖** ⇒ 不可逆。",
	},
	"cmd/tools/backfill_session_bodies/main.go": {
		Verdict:    verdictReadsV1,
		ResolvesTo: "request_logs_bodies",
		Via: "bodiesTable := \"request_logs_bodies\"，`--use-hot` 时改成 " +
			"\"request_logs_bodies_hot\"（main.go:88-90），96 行 " +
			"`SELECT request_body, response_body FROM ` + bodiesTable + ` WHERE request_id = $1`。",
		Consequence: "⚠ **与运行时读方不同类**：这是**迁移工具**，" +
			"DROP 后它没有任何替代源 ⇒ session_bodies 的回填**永久做不了**。" +
			"它必须在 DROP 之前跑完（或被废弃并留档），不是「换个源」的问题。",
	},

	// ── 读 canonical 视图 / 会话族（4 文件 / 5 处）────────────────────────
	// ⚠ 「读 canonical 视图」**不等于**「退役后照常工作」：视图的 v1 臂在
	// DROP 时消失。admin/logs.go 与 admin/usage_enhanced.go 都是这一类，
	// 且它们的读法是 struct 字段 ⇒ 四道门全都看不见。
	"admin/auto_route_tuning.go": {
		Verdict: verdictCanonicalOnly,
		Via: "viewName := \"tuning_signals_daily\"，days<=7 时改成 " +
			"\"tuning_signals_5m\"（auto_route_tuning.go:765-768），769 行拼进 FROM。" +
			"两个都是 tuning 信号视图，不在 v1 关系名里。",
		Consequence: "与 request_logs 退役无关。**残留噪声**：本文件报的是" +
			"真关系名，所以这条不是误报。",
	},
	"admin/dashboard_board_queries.go": {
		Verdict: verdictCanonicalOnly,
		Via: "logsTable 解析为 `request_logs_with_current_month_without_customer_id AS r`" +
			"（119 行）—— canonical 视图的包装视图，**带 v1 臂**。",
		Consequence: "⚠ 视图的 v1 臂在 DROP 时消失 ⇒ 看板的这张表会少掉 v1 那部分行。" +
			"归 D29-d 切换清单，**不是** DROP 前的 blocker。",
	},
	"admin/provider_cred_lifecycle.go": {
		Verdict:     verdictCanonicalOnly,
		Via:         "usageTable 解析为 `usage_ledger`（483 行）——计费台账表，不在 v1 族。",
		Consequence: "与 request_logs 退役无关。",
	},
	"autoroute/metrics.go": {
		Verdict: verdictCanonicalOnly,
		Via: "⚠ **这里根本不是读点**。383 行是 Prometheus Gauge 的 Help 文本，" +
			"拼接里恰好出现了 `from ` 而被 fragmentTailRE 认成 FROM 子句。" +
			"操作数是 Help 文案的下一段。",
		Consequence: "无。**已知残留噪声**：§9.226.3 已记录。" +
			"方向安全（不进 v1 桶），但它让清单里多一条无法评估的条目。" +
			"本轮已修掉同族的 IS DISTINCT FROM（见门 7），这一条**未修**——" +
			"因为它的操作数不是关系名，要靠「拼接里有没有关系名形状」来判，" +
			"那是另一个更大的改动。",
	},

	// ── 工具解析不出、人读过源码确认不是 v1（14 文件）────────────────────
	// 绝大多数是**跨包调用**（db.SessionFamilyTurns*SQL / probemode.GuardStateTable），
	// 工具的 *ast.SelectorExpr 分支明写「不猜」。
	"admin/logs.go": {
		Verdict: verdictCanonicalOnly,
		Via: "logsFrom := logsSourceFromSQL()（logs.go:630）。logs_turns_source.go:62-68：" +
			"`storage.admin_logs_native_turns_read` 平台开关为 **false（默认）** 时返回 " +
			"`request_logs_with_current_month rl`，为 true 才返回会话族源。" +
			"6 处拼接点（632/633/675/677/726/730）。",
		Consequence: "⚠ **默认支是 canonical 视图的 v1 臂**。DROP 后日志列表的" +
			"v1 那部分行消失（不是缺列，是少行）。" +
			"归 D29-d 切换清单；灰度开关转 true 即可整体切到会话族。",
	},
	"admin/usage_enhanced.go": {
		Verdict: verdictCanonicalOnly,
		Via: "baseTable 来自 planCostTrend 的 `BaseTable: \"request_logs_with_current_month rl\"`" +
			"（usage_enhanced.go:120），168 行 `\"FROM \" + baseTable + plan.JoinClause`。" +
			"⚠ 这是**裸表名字符串**，按行扫描与 AST 抽列两道门都看不见" +
			"（admin/request_logs_read_inventory_test.go 的注释已把它登记为已知缺口）。",
		Consequence: "维度归因报表（用量趋势的成本拆分）会少掉 v1 臂的行。" +
			"与 logs.go 同属切换清单项。",
	},
	"admin/session_compare.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "221/902 行 `FROM ` + db.SessionFamilyTurnsForSessionSQL() + ` rl`。" +
			"跨包调用 ⇒ 工具判 unresolved；读源码确认是会话族。",
		Consequence: "与 v1 无关。⚠ 但同文件的 **bodies 腿**仍是 v1（见" +
			"admin/request_logs_bodies_retirement_gate_test.go）。",
	},
	"admin/session_list.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "138/162 行 `FROM ` + db.SessionFamilyTurnsSourceSQL() + ` rl`、" +
			"384/443 行 ForSessionSQL()。全是会话族。",
		Consequence: "与 v1 无关。",
	},
	"admin/session_export.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "227 行 `FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL() + ` rl` + " +
			"`LEFT JOIN request_logs_bodies_with_current_month rb`（turn 腿是会话族）。" +
			"跨包调用 ⇒ 工具判 unresolved。",
		Consequence: "⚠ turn 腿与 v1 无关，但**bodies 腿是 v1**，且写法是 " +
			"COALESCE(rb.request_body,'{}') ⇒ DROP 后**导出的会话包每条正文都是 {}**，" +
			"而且**不报错**。已登记在 admin/request_logs_bodies_retirement_gate_test.go。",
	},
	"admin/session_online.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "448 行 `FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL() + ` rl`，会话族。" +
			"⚠ 同文件 112 行那个 `JOIN request_logs_with_current_month rl` **是 v1 视图**，" +
			"但它是字面量、被 exposure/inventory 门覆盖，不在本清单范围（本清单只管" +
			"关系名不是字面量的拼接点）。",
		Consequence: "与 v1 无关。",
	},
	"admin/session_title.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         "323 行 `FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL() + ` t`，会话族。",
		Consequence: "与 v1 无关。",
	},
	"admin/session_turns_tree.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         "240/375 行 `FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL()，会话族。",
		Consequence: "与 v1 无关。",
	},
	"admin/turns_sessions.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         "479/693 行 `JOIN ` + db.SessionFamilyTurnsSourceSQL() + ` rl`，会话族。",
		Consequence: "与 v1 无关。",
	},
	"bg/auto_route_settle_sql.go": {
		Verdict:    verdictReadsV1,
		ResolvesTo: "request_logs_hot",
		Via: "`FROM ` + src.TurnsTable + ` rl`（73/99/120 行）。src 来自 " +
			"settleSourceFor(logsWriteEnabled)（auto_route_settle_source.go:66-77）：" +
			"写门**开着** ⇒ request_logs_hot，**关掉** ⇒ session_turns_hot。" +
			"⚠ 工具对 *ast.SelectorExpr（struct 字段）返回 nil ⇒ 判 unresolved；" +
			"这里靠读源码定级。已登记在 admin/request_logs_indirect_readers_test.go。",
		Consequence: "条件性：S4 关上写门后自动切到会话族。" +
			"⚠ **切换那一刻之前**，结算仍读 v1 底表 ⇒ 退役顺序上它必须排在 S4 之后。",
	},
	"bg/credential_recovery.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "625 行 `FROM ` + probeGuardStateTable() + ``，是探针锁护航状态表，" +
			"不在 v1 族。",
		Consequence: "与 v1 无关。",
	},
	"credentialhealth/checker.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         "644 行 `FROM ` + probemode.GuardStateTable() + ` mps`，同上。",
		Consequence: "与 v1 无关。",
	},
	"provider/client.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "2625 行 `b.WriteString(\"… SELECT 1 FROM \" + probemode.GuardStateTable() + \" \" + alias …)`，" +
			"锁护航表，不在 v1 族。",
		Consequence: "与 v1 无关。",
	},
	"domains/stats/minute_flush.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "113 行 `DELETE FROM ` + table + ` WHERE bucket < …`，table 遍历的是" +
			"分钟统计表名列表（minute_flush.go:105-111），不含 v1 族。",
		Consequence: "与 v1 无关。",
	},
	"domains/session/v2/test_helpers.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "54 行 `DELETE FROM ` + table + ` WHERE tenant_id = 'test_tenant'`，" +
			"table 遍历 public.sessions / session_bodies / session_turns / session_turn_logs（46-51）。" +
			"⚠ 注意它**不是** `_test.go`（所以 AuditRepo 会扫它），" +
			"但它是测试清理助手。",
		Consequence: "与 v1 无关。",
	},
	"bg/partition_manager.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "6 处（449/1146/1317/1705/1738/2125）都是分区 DDL，" +
			"表名经 pgxIdent() / s.fnName / 形参传入。",
		Consequence: "与 v1 无关。⚠ 但它**管理** v1 分区：" +
			"DROP request_logs 时这套逻辑会因 to_regclass 为 NULL 而走空分支，" +
			"不报错——与 §9.221 记的「视图被带外操作删除后没有机制重建」同族。",
	},
}

// repoRoot 走 go.mod 向上找仓根。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}

// measureBuckets 跑一次全仓审计，返回「文件 → 三桶各自出现次数」。
func measureBuckets(t *testing.T, root string) (v1, canonical, unresolved map[string]int, total int) {
	t.Helper()
	sites, err := AuditRepo(root)
	if err != nil {
		t.Fatalf("AuditRepo(%s): %v", root, err)
	}
	v1, canonical, unresolved = map[string]int{}, map[string]int{}, map[string]int{}
	for _, s := range sites {
		total++
		switch s.Classification() {
		case ClassReadsV1:
			v1[s.File]++
		case ClassUnresolved:
			unresolved[s.File]++
		default:
			canonical[s.File]++
		}
	}
	norm := func(m map[string]int) map[string]int {
		out := map[string]int{}
		for k, v := range m {
			rel, err := filepath.Rel(root, k)
			if err != nil {
				out[k] = v
				continue
			}
			out[filepath.ToSlash(rel)] += v
		}
		return out
	}
	return norm(v1), norm(canonical), norm(unresolved), total
}

func mergeCounts(maps ...map[string]int) map[string]int {
	out := map[string]int{}
	for _, m := range maps {
		for k, v := range m {
			out[k] += v
		}
	}
	return out
}

func dumpBuckets(t *testing.T, v1, canonical, unresolved map[string]int, total int) {
	t.Helper()
	t.Logf("全仓拼接点：%d 处 / %d 文件（v1 %d、canonical %d、unresolved %d）",
		total, len(mergeCounts(v1, canonical, unresolved)), len(v1), len(canonical), len(unresolved))
	for _, b := range []struct {
		name string
		m    map[string]int
	}{{"reads-v1", v1}, {"canonical-only", canonical}, {"unresolved", unresolved}} {
		var files []string
		for f, n := range b.m {
			files = append(files, f+"("+strconv.Itoa(n)+")")
		}
		sort.Strings(files)
		t.Logf("── %s(%d): %s", b.name, len(files), strings.Join(files, " "))
	}
}

// measuredVerdict 把一个文件的三桶计数折成「工具眼里的判定」。
//
// unresolved 优先：只要还有一处不可判定，工具就**不敢**声称这个文件安全。
// 这是本工具刻意的保守（resolve.go：「宁可不可判定也不猜」），
// 所以工具的判定是**上界**而不是结论——结论由人给。
func measuredVerdict(v1n, canonN, unresN int) verdict {
	switch {
	case unresN > 0:
		return verdictUnresolvedTool
	case v1n > 0:
		return verdictReadsV1
	case canonN > 0:
		return verdictCanonicalOnly
	default:
		return ""
	}
}

// verdictUnresolvedTool 是**工具**的判定，不是清单里可填的值。
// 单独命名，是为了在错误信息里把「工具说不知道」与「清单说还没查」
// 摆在一起——它们是两件事，混成一个词就会以为「工具不知道 = 我也不用查」。
const verdictUnresolvedTool verdict = "unresolved(tool)"

// checkVerdictAgreement 交叉核对清单判定与工具判定。
//
// # 为什么不是「两者必须相等」
//
// 工具对**跨包调用**永远返回 unresolved（resolve.go 的 `*ast.SelectorExpr`
// 分支明写「struct 字段 / 跨包常量：不猜」）。而本仓库最大的一批间接读点
// 恰恰是跨包的：`db.SessionFamilyTurnsForSessionSQL()`、
// `SessionFamilyTurnsSourceSQL()`、`probemode.GuardStateTable()`。
// 若要求相等，这批文件永远只能填 `still-unknown` ⇒ 门永远红 ⇒ 没人看。
//
// # 真正要拦的是**方向**（三条，缺一不可）
//
//	① 工具说 reads-v1，清单说不是      → 工具比人更严重，人不能把它降级
//	② 清单说 reads-v1，工具说 canonical-only → **§9.226.3 的 tenants.go**：
//	    工具「解析成功」却解析错了，还自信地报「退役安全」。人已经看出是
//	    两张 v1 底表，工具说 canonical ⇒ 两者矛盾 ⇒ 红。
//	③ 清单说 still-unknown，工具却能完全解析 → 判定过期，白填了
//
// 反向（工具 unresolved、人给出更细的结论）**允许**：
// 那是「工具不知道、我查过了」，正是这道门存在的意义。
func checkVerdictAgreement(file string, mine, tool verdict) []string {
	switch {
	case tool == verdictReadsV1 && mine != verdictReadsV1:
		return []string{fmt.Sprintf(
			"%s: 工具判定 reads-v1，清单判 %s —— 工具判得更严重时不能降级。"+
				"请复核 %s 到底读不读 v1 宽族。", file, mine, file)}
	case mine == verdictReadsV1 && tool == verdictCanonicalOnly:
		return []string{fmt.Sprintf(
			"%s: 清单判 reads-v1，工具判 canonical-only —— **两者矛盾**。"+
				"这一类历史上出现过：§9.226.3 的 admin/tenants.go 被工具判成"+
				"「退役安全」，而它读的是两张 v1 底表。"+
				"请先确认是工具错（修 isV1Relation）还是清单错（改判定）。", file)}
	case mine == verdictStillUnknown && tool != verdictUnresolvedTool:
		return []string{fmt.Sprintf(
			"%s: 清单判 still-unknown，但工具已能完全解析（%s）—— 判定过期了，"+
				"请按工具给出的关系名填 Consequence。", file, tool)}
	}
	return nil
}

// TestIndirectSiteManifestCoversEveryReportedFile 是双向门 + 交叉核对。
//
// 四条规则，缺一条都会让这道门变成又一张「看起来在防」的表：
//
//	① 被工具报出、但清单里没有 ⇒ 红（新文件无人判定）
//	② 清单里有、但工具不再报出 ⇒ 红（判定过期，或文件被删）
//	③ 清单的 Verdict 与工具的实测分类不一致 ⇒ 红（表与工具互相监督）
//	④ 清单条目的必填字段为空 ⇒ 红（空理由的登记等于没有登记）
func TestIndirectSiteManifestCoversEveryReportedFile(t *testing.T) {
	root := repoRoot(t)
	v1, canonical, unresolved, total := measureBuckets(t, root)
	dumpBuckets(t, v1, canonical, unresolved, total)

	measured := mergeCounts(v1, canonical, unresolved)

	var errs []string
	for f, n := range measured {
		got := measuredVerdict(v1[f], canonical[f], unresolved[f])
		a, ok := indirectSiteAssessments[f]
		if !ok {
			errs = append(errs, fmt.Sprintf(
				"%s: 被审计报出 %d 处（工具判定 %s）但清单里没有判定 —— "+
					"新增一个间接读点时必须在这里登记（含机制与退役后果）", f, n, got))
			continue
		}
		errs = append(errs, checkVerdictAgreement(f, a.Verdict, got)...)
	}
	for f := range indirectSiteAssessments {
		if _, ok := measured[f]; !ok {
			errs = append(errs, fmt.Sprintf(
				"%s: 清单里有判定但审计不再报出它 —— 文件被删/改名，或读法已改。"+
					"请确认后删掉这条判定，别让它当占位。", f))
		}
	}

	// ④ 字段完整性。⚠ 放在与 ①③ 同一道门里，不是单独一道 ——
	// 单独一道的话，「登记了 5 条、其中 3 条是空壳」会显示成两道门各红一半，
	// 而真正该看的只有一条。
	for f, a := range indirectSiteAssessments {
		if strings.TrimSpace(a.Via) == "" {
			errs = append(errs, f+": Via 为空 —— 必须写清机制，"+
				"且**默认入参落在哪一支**（条件性读 v1 的两个分支风险不同）")
		}
		if strings.TrimSpace(a.Consequence) == "" {
			errs = append(errs, f+": Consequence 为空 —— 必须写清退役时会怎样；"+
				"对 unresolved 尤其不许写「无影响」，"+
				"「还没查」与「查过没问题」在退出码上不可区分")
		}
		if a.Verdict == verdictReadsV1 {
			if strings.TrimSpace(a.ResolvesTo) == "" {
				errs = append(errs, f+": Verdict=reads-v1 但 ResolvesTo 为空 —— "+
					"「它读的是 v1」就成了断言。写上表名才能被 v1Tables 核对。")
			} else if !v1Tables[a.ResolvesTo] {
				errs = append(errs, fmt.Sprintf(
					"%s: ResolvesTo=%q 不在 v1Tables 里（%v）",
					f, a.ResolvesTo, v1TableNamesSorted()))
			}
		} else if strings.TrimSpace(a.ResolvesTo) != "" {
			errs = append(errs, f+": Verdict≠reads-v1 却填了 ResolvesTo —— "+
				"要么改判定，要么删字段；两者不一致时无法判断该信哪个")
		}
	}

	sort.Strings(errs)
	for _, e := range errs {
		t.Error(e)
	}
	t.Logf("清单 %d 条 / 实测 %d 个文件（v1 %d 文件 / %d 处）",
		len(indirectSiteAssessments), len(measured), len(v1), sumMap(v1))
	// ⚠ **数的是清单里的 still-unknown，不是工具的 unresolved 桶。**
	// 两者完全不是一回事：工具的 unresolved 是「我解析不出」，
	// 清单的 still-unknown 是「解析不出**且没人查过**」。
	// 把工具的桶数当成待办数，会让「26 个文件都有人读过」显示成「26 个都还没查」。
	var stillUnknown []string
	for f, a := range indirectSiteAssessments {
		if a.Verdict == verdictStillUnknown {
			stillUnknown = append(stillUnknown, f)
		}
	}
	sort.Strings(stillUnknown)
	if len(stillUnknown) > 0 {
		t.Logf("⚠ 清单里 still-unknown %d 个（工具判 unresolved 的共 %d 文件 / %d 处，"+
			"其中 %d 个已由人读源码定级）：\n  %s",
			len(stillUnknown), len(unresolved), sumMap(unresolved),
			len(unresolved)-len(stillUnknown), strings.Join(stillUnknown, "\n  "))
	} else {
		t.Logf("清单内无 still-unknown：工具判 unresolved 的 %d 文件 / %d 处"+
			"**全部已由人读源码定级**（跨包调用为主）。", len(unresolved), sumMap(unresolved))
	}
}

func sumMap(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func v1TableNamesSorted() []string {
	out := make([]string, 0, len(v1Tables))
	for k := range v1Tables {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestManifestHasNoStillUnknownEntries 是一道 **ratchet**：
// 清单里不允许存在 still-unknown。
//
// # 为什么允许「工具解析不出」却不允许「没人查」
//
// 工具对跨包调用永远给 unresolved，那不是欠账，是它的设计边界。
// 但「人也没查」是欠账——而且它和「查过没问题」在退出码上完全一样。
// ⇒ 新文件进来时**不允许**先填 still-unknown 蒙过去：
// 要么读源码给出 nonv1-by-inspection / canonical-only / reads-v1，
// 要么这道门红着，逼出一个真正的结论。
//
// ⚠ 没有这道 ratchet，清单会退化成「工具报什么我抄什么」，
// 而 §9.226.3 的 tenants.go 证明**抄工具的那一侧会跟着工具一起错**。
func TestManifestHasNoStillUnknownEntries(t *testing.T) {
	var unknown []string
	for f, a := range indirectSiteAssessments {
		if a.Verdict == verdictStillUnknown {
			unknown = append(unknown, f)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("清单里有 %d 个 still-unknown（工具解析不出、且没有人查）：\n  %s\n"+
			"请读源码定级后改写：非 v1 → nonv1-by-inspection（写清拼进去的是什么）；"+
			"canonical 视图 → canonical-only（⚠ 视图的 v1 臂会随 DROP 消失）；"+
			"v1 宽族 → reads-v1（ResolvesTo 填表名）。"+
			"「先填 still-unknown 等以后再说」是这张表退化成抄表的入口。",
			len(unknown), strings.Join(unknown, "\n  "))
	}
}

// TestRepoAuditIsNotSilentlyVacuous 挡住「审计扫不到东西 ⇒ 清单全绿」。
//
// 这道门当前是绿的。若某次重构让 AuditRepo 崩掉、返回 0 个站点，
// ① 会因为「清单里有 27 条而实测 0」而红——**但那是 27 条噪音**，
// 真正的失败原因（扫描器坏了）被埋在末尾。
// ⇒ 单列一道：总体为 0 必须立刻指名扫描器。
func TestRepoAuditIsNotSilentlyVacuous(t *testing.T) {
	root := repoRoot(t)
	v1, canonical, unresolved, total := measureBuckets(t, root)
	if total == 0 {
		t.Fatal("全仓审计报出 0 个拼接点 —— 这不是「仓库里没有间接读点」，" +
			"是审计坏了（WalkDir 范围 / 解析失败被 continue 吞掉 / 模式被改）。" +
			"在这种状态下上面那道门会因「清单有 27 条而实测 0」报出一堆噪音，" +
			"把真正的病因埋在最后。")
	}
	if len(v1) == 0 {
		t.Errorf("全仓审计**一个 v1 读点都没解析出来**（%d 处全部落在 canonical/unresolved）—— "+
			"要么仓库真的干净了（那要有人明确确认），要么 isV1Relation 又坏了。", total)
	}
	t.Logf("v1 %d 文件/%d 处，canonical %d 文件/%d 处，unresolved %d 文件/%d 处",
		len(v1), sumMap(v1), len(canonical), sumMap(canonical), len(unresolved), sumMap(unresolved))
}
