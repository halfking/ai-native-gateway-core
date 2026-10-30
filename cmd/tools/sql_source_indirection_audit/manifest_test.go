//go:build !integration

package main

import (
	"fmt"
	"path/filepath"
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
// 但「输出是证据」意味着**没有任何东西要求有人去看第二桶**。§9.226.3 当时的实测：
//
//	解析到 canonical / 会话族（退役安全）:  5 处
//	解析到 v1 宽族                        : 12 处 / 6 个文件
//	不可静态解析（需手验）                : 35 处 / 20 个文件
//
// ⚠ **这三行是 §9.226.3 的快照，已过期**。当前实测见
// `documentedSnapshot`（由 `TestDocumentedSnapshotMatchesMeasurement` 钉住，
// 2026-10-21 / R65 轮更新）：
//
//	全仓拼接点 157 处（v1 0 文件/0 处、canonical 63/131、unresolved 14/26、v1arm 0/0）
//
//	（2026-10-30 24h 审计第 8 轮刷新：156 → 157。★ 不是只改数字 ——
//	  ff029a7fc（输出合规审计轮）新增 admin/sanitize_rules_review_context.go 1 处
//	  会话族窗口拼接 ⇒ 已补登记判定；无任何桶变小。
//	  ★★ 附带查出：这组旧数字（147/125/22）**既不等于**快照所在提交 a2ac50e37 的
//	  实测（142/122/20），**也不等于**今天的实测 ⇒ 它在一次合并之后就没再被对拍过，
//	  而「合并后没人重测」这件事没有任何东西会报警 ⇒ **事实源漂移家族的最纯形态**。）
//
//	（2026-10-25 R152 轮刷新：142 → 147。★ 不是只改数字 —— 逐站点对拍过：
//	  净增 5 处 = admin/compression_sessions.go +1、admin/compression_stats.go +2
//	  （R76/R77 把 SQL 构造搬进函数后，同一批会话族源在更多调用点上被拼接）、
//	  db/request_logs_view_schema.go +2（R92/R93/R94 改写后第一次进入扫描范围，
//	  已补登记判定：唯二入参是 session_turns_hot / session_turns）。
//	  ⇒ 无桶变小 ⇒ 不是「扫描范围被改小 / 解析失败被吞」；
//	  ⇒ 5 处 resolved 全是会话族 ⇒ 无 v1 读方。）
//
//	（2026-10-20 迁移 890 收口轮：v1 桶 -2 文件/-5 处、v1 臂桶 -16 文件/-27 处、
//	canonical 桶 +29 文件/+72 处、unresolved 一栏未动（12 文件/19 处）。
//	⚠ **三处变小/变大的成因逐条核过**（门规：「某一桶变小 ⇒ 先怀疑扫描范围被改小
//	或解析失败被吞」）：
//	  · v1 -2 文件/-5 处 = admin/tenants.go（切到会话族两臂，4 处）
//	    + cmd/tools/backfill_session_bodies/main.go（工具退役，1 处）——
//	    两处都**逐行读过源码**，不是扫描盲区；
//	  · v1 臂 -16 文件/-27 处 = 推导集合本身空了（composer 不再定义任何含 v1 臂
//	    的视图，4 张 v1-only 视图随 890 删除）⇒ 那 16 个文件整体落进 canonical，
//	    不是「看不见了」。
//	    ⚠★ **2026-10-21 订正（§19.10 跨会话订正）**：原文在这里写的是
//	    「sql/objects/views/ 里的 canonical 视图已是会话两臂体」——
//	    **这句在 10-20 09:58（e9a96c26c）之后就不成立了**：
//	    那 5 张分析视图**必须回到 v1 fallback 体**（基线是**逐语句**应用的，
//	    而 CREATE VIEW 会**急切校验**它引用的关系 ⇒ 写成会话族形态会让
//	    **全新安装**在基线阶段就报 42P01 并中止），
//	    部署形态由迁移 883/884 换上（两条路径跑完启动序列后形态一致）。
//	    ⇒ ★★ **「源里出现过某个形态」≠「源错了」**：源可能是**某个时点的快照**；
//	    判源之前先问「这个源是哪个时点的形态、被用在哪一步」；
//	    ★ **判据的「不该有」必须带时点限定**，否则它会要求一个**打挂另一条路径**
//	      的改动（那道旧门 `TestComposerDefinesNoV1ArmView` 就是这么生的：写它的时候
//	      前提就已经是假的 ⇒ 它**生来就红**）。
//	  · canonical +29 文件/+72 处 = 890 把一批读面从 v1 **字面量**改写成会话族
//	    **拼接调用** ⇒ 方向=变大=「把看不见变成看得见」，按门规改数字即可。
//	    这批新文件（17 个）逐个读过源码确认读的是会话族。）
//
//	（2026-10-21 R65 轮：canonical +2 文件/+6 处、总处数 +6 ⇒ 135→141。
//	★ 逐条核过（逐站点 diff，见下面的 `documentedSnapshot` 说明段）：
//	  · **+7 处** = 三个**新增文件**：admin/attempt_quality_api.go（1 处）、
//	    admin/auto_route.go（1 处）、admin/auto_route_correlations.go（5 处）——
//	    三处都读 `dbpkg.SessionFamilyTurnsSourceSQL()` ⇒ 会话族 ⇒ canonical；
//	  · **−1 处** = bg/routing_health_checks.go 那个拼接点被 8531596a6 改写成
//	    **纯字面量**（`public.session_turns_hot` UNION ALL `public.session_turns`）——
//	    本工具量的是「**关系名不是字面量**的拼接点」（resolve.go:Site 定义），
//	    所以它**离开了本工具的口径**；而它的目标是**会话族**不是 v1
//	    ⇒ ⇒ **不是「又看不见了」**，只是读法从「间接（拼接调用）」变成「直接（字面量）」。
//	    ⇒ 这一条**特意单独核过**：桶变小必须先排除「解析失败被吞」。)
//
// ★ **§9.257 的实测结论，是本工具最要紧的一条**：
// 修完跨包解析后，unresolved 从 53 处降到 14 处（**多解析出 39 处**），
// 而 **v1 仍然是 12 处，一处没多**。
//
// 原因是分类的**语义**：`reads-v1` 回答「这个关系名在 DROP 时会不会消失」，
// `canonical` 回答「不会消失」。而 §9.256 手验出的那 26 处读的是
// `request_logs_with_current_month` / `request_logs_bodies_with_current_month`
// ——**两个都不在 `v1Tables`（那里只有 4 张基表）**，所以它们解析成功后
// 必然落进 `canonical`。
//
// ⇒ ★ **「12 处」永远不可能是 v1 读方清单**，再多解析也改变不了这一点。
// 退役要回答的是**行来源**（我读到的行里有多少来自 v1），
// 那需要第四个类（v1 臂视图），**本工具目前没有这一类**。
// ⇒ 别再拿「12 处」排期，也别指望把解析率提到 100% 就能修好这件事。
//
// ⚠ unresolved 那一桶从 20 个文件涨到 26 再降到 7 个，
// 中途那次上涨**不是新读方，是老读方换了写法**：
// §9.232 把 7 个 admin 内的 bodies 读方从**字面量**改成拼接调用、§9.233 又迁了 3 个
// 跨包读方 ⇒ 它们从「字面量（不进本工具）」变成「拼接点不可判定（进 unresolved）」。
// §9.257 修的正是这个：`calleeName` 对带包前缀的调用返回 `""`（`resolve.go`），
// 于是 `db.SessionBodiesSourceSQL()` 这类跨包切换层**根本查不到**。
// ⇒ **本工具只管关系名不是字面量的拼接点**；把字面量改成拼接会让桶看起来在恶化，
// 但那是把「看不见」变成「看得见且已定级」，方向上是变好。
//
// 其中 3 个 v1 文件里有 **3 个在四张登记表里一处都没有**：
//
//	maas/usage.go  maas/consumption_detail.go  maas/credit_buckets.go
//
// 这三个走**自己包内**的切换层 `requestLogsSource(days)`（maas/usage.go:66-71），
// 而 `ClampUsageDays` 对 `days < 1` 返回 **1** ⇒ 默认分支是
// **`_request_logs_hot AS r`，一张 v1 底表**。
// `maas/usage.go` 唯一的 `from _request_logs` 文本在**第 27 行的注释里**
// ⇒ 按行扫描的 `requestLogsReadInventory` 看不见它，
// 按 AST 抽列的 exposure 门也只覆盖直接读方 ⇒ **四道门全部看不见**。
//
// 而这是**计费/用量**路径（`credits_charged` / `cost_usd` 聚合）。
//
// ★ 2026-10-20（迁移 890）：这三个是**全仓仅剩的 v1 基表读点** —— 迁移把 v1 族
// 整族删了，而这三个读点的取源函数**没跟着改** ⇒ 它们的默认支指向一张**已不存在**
// 的表（42P01）。本工具的 reads-v1 桶因此**不是空的**，这是事实而不是工具误报。
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
	// verdictReadsV1：读 v1 宽族基表（_request_logs / _hot / _bodies / _bodies_hot）。
	verdictReadsV1 verdict = "reads-v1"
	// verdictCanonicalOnly：读 canonical 视图或会话族。
	// ⚠ **「读 canonical 视图」不等于「退役后照常工作」**：视图的 v1 臂在
	// DROP 时会消失（§9.226.2 的 admin/session_online.go 就是这一类，
	// 而且它是 JOIN，连不上行、整页空）。
	verdictCanonicalOnly verdict = "canonical-only"
	// verdictReadsV1Arm：读的是**体里含 v1 臂的视图**（§9.258）。
	//
	// ⚠ 它与 reads-v1 的区别是**失效形态**，不是严���程度：
	// reads-v1 的关系名在 DROP 后**消失**（查询报错或拿 NULL）；
	// 本类的关系名**还在**，但它今天的体含 v1 臂 ⇒ **行集会变小**，
	// 而多数读法用 COALESCE/COUNT 包装过 ⇒ **静默少行，不报错**。
	// 集合由 viewsWithV1Arm 从 sql/objects/views/ 的 DDL **推导**，不手写。
	verdictReadsV1Arm verdict = "reads-v1-arm"
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
	// ⚠ 2026-10-20（迁移 890）：原先挂在 map 顶部的 bg/candidate_failure_monitor.go
	// 条目已移到下方「§9.233 新进 unresolved 桶」那一组里——它现在的判定依据
	// （源从 v1-only 视图换成会话族热腿）属于 890 的改写，与那组同源。

	// ── 读 v1 宽族基表（3 文件 / 6 处）──────────────────────────────────
	// ★ 2026-10-20（迁移 890）之后，这一桶是**全仓仅剩**的 v1 基表读点集合。
	// 迁移把 v1 族（`_request_logs*` 及其分区）整族删了，而这三个读点的取源
	// 函数**没跟着改** ⇒ 它们的默认支指向一张**已不存在**的表。
	// ⇒ 「v1 桶非空」是**事实**，不是工具误报；这三条也是本清单里最该先处置的。
	//
	// ⚠ 下面 3 个在 admin 侧四张登记表里一处都没有（§9.227）。
	// 它们全部走切换层，而切换层的表名以 Go 字符串进 SQL；
	// `maas/usage.go` 唯一的 `from _request_logs` 文本在**第 27 行的注释里**，
	// 所以按行扫描的 requestLogsReadInventory 看不见它。
	"maas/usage.go": {
		Verdict: verdictCanonicalOnly,
		Via: "requestLogsSource(days)（usage.go:66-71）：**恒**返回 " +
			"`request_logs_with_current_month AS r`（canonical 视图，890 后是会话两臂体）。" +
			"用在 106/116/134/170 四处 `FROM `+logsTable+`（TotalRequests/TotalCredits/按模型/趋势）。" +
			"★ 890 之后**两个分支合并成一个**：原 `days<=7 → _request_logs_hot AS r` 那支" +
			"（v1 底表，42P01）已删除——canonical 视图本身就是会话族两臂体" +
			"（`session_turns_hot ∪ session_turns` + details），已覆盖热窗 + 冷窗。",
		Consequence: "租户用量/计费 API（QueryUsageSummary 及 WithCost）。" +
			"⚠ **口径变化**（不是纯重构）：canonical 视图**不含镜像链建立之前的历史**" +
			"（那批行只存在于 v1 族，已随 890 删除）⇒ 7 天窗的请求数与 credits 会**变少**。" +
			"那是「那段历史已经不在库里了」，不是计数缺陷。" +
			"⇒ 读面口径与会话族一致（会话族的 27.6% request_id 覆盖率缺口一并登记）。",
	},
	"maas/consumption_detail.go": {
		Verdict: verdictCanonicalOnly,
		Via: "同一套 requestLogsSource(days)（usage.go:66-71，现恒返回 canonical 视图），" +
			"消费明细查询在 81 行 `FROM ` + logsTable + `。",
		Consequence: "消费明细按小时/按模型分组。★ 890 之后源从 v1 底表换成 canonical 视图" +
			"⇒ 镜像链建立之前的历史不再计入（那段行随 v1 族一起消失）。" +
			"错误处理未变：源不可用时仍是 `return ConsumptionDetail{}, fmt.Errorf(...)`" +
			"（consumption_detail.go:100-103）⇒ **响亮失败**（500），不是空分组。",
	},
	"maas/credit_buckets.go": {
		Verdict: verdictCanonicalOnly,
		Via: "同一套 requestLogsSource(days)（usage.go:66-71，现恒返回 canonical 视图），" +
			"47 行 `FROM `+logsTable+`，写入小时级 credit 桶（ON CONFLICT DO UPDATE）。",
		Consequence: "⚠ **与其它几条不同类**：它不是读后展示，而是**写聚合桶**。" +
			"★ 890 之后源从 v1 底表换成 canonical 视图 ⇒ 桶里会**继续写入会话族的 credits**" +
			"（不再是 42P01，也不再有「桶被写成 0」那条不可逆后果）。" +
			"⚠ 口径随之变化：镜像链建立之前的历史的 credits 不再回填进桶" +
			"（那段行随 v1 族一起消失）⇒ 桶的起点是会话族开始记录的时刻。",
	},

	// ── 读 canonical 视图 / 会话族（54 文件 / 110 处）─────────────────────
	// ⚠ 「读 canonical 视图」**不等于**「退役后照常工作」：视图的 v1 臂在
	// DROP 时消失。admin/logs.go 与 admin/usage_enhanced.go 都是这一类，
	// 且它们的读法是 struct 字段 ⇒ 四道门全都看不见。
	"admin/tenants.go": {
		Verdict: verdictCanonicalOnly,
		Via: "logsTable := tenantStatsLogsTableSQL(settings.RequestLogsWriteEnabled())（tenants.go:795）。" +
			"★ 2026-10-20（迁移 890）该函数从**四臂**（会话 hot/父表 + v1 hot/父表）" +
			"收敛成**两臂**（只有 session_turns_hot ∪ session_turns，" +
			"见 admin/tenant_stats_logs_table.go:30-53）——v1 两臂与那两条对 v1 的" +
			"NOT EXISTS 双子反连接一起删了（反连接的对象已随 890 退役）。" +
			"4 处 `FROM `+logsTable+`（828/847/880/914）是租户 credits/按模型/按应用/按天聚合；" +
			"第 5 处（813）是 usageTable = \"usage_ledger_with_current_month\"，与 v1 无关。",
		Consequence: "与 v1 退役**无关**：四段聚合现在只读会话族两臂，冻结点已退化为常数 " +
			"-infinity（db/v1_freeze_cutoff.go）⇒ 谓词恒真。" +
			"⚠ 真正的后果是**口径变化**而不是失效：v1 族的历史行随 890 一起没了，" +
			"而会话族只镜像**有会话头**的流量（探针/自检回环、in_progress 占位、标题/摘要生成器回环都不镜像）" +
			"⇒ 无会话头的流量在这四段聚合里**不计入**。" +
			"★ 与本条目退役前那版（「COALESCE(SUM(...),0) 把读不到变成没花过钱 ⇒ 伪造计费数字」）" +
			"**形态不同**：现在是**总体变小**，不是伪造；而且查询是响亮的（没有静默 0 的路径）。",
	},
	"admin/usage_credits.go": {
		Verdict: verdictCanonicalOnly,
		Via: "requestLogsFromClause(days)：短窗（days<=7）经 `db.SessionFamilyTurnsHotSourceSQL()` 取会话族热腿；" +
			"长窗（days>7）仍读 canonical 视图 `request_logs_with_current_month`" +
			"（usage_credits.go:167 的 `FROM `+logsTable+`）。" +
			"★ 2026-10-20（迁移 890）之后该视图**没有 v1 臂了**：它的体只剩 session_turns_hot ∪ session_turns 两臂。",
		Consequence: "两个分支都只读会话族行 ⇒ v1 退役对它**无影响**（视图还在，且行集里已没有 v1 那部分）。" +
			"⚠ 与退役前那版（「长窗仍含 v1 历史臂，DROP 时历史行减少」）**形态不同**：那是「少行」，" +
			"现在是「那一部分行本来就不存在」——历史行随 890 一起被删，不是被读丢的。",
	},
	"admin/auto_route_tuning.go": {
		Verdict: verdictCanonicalOnly,
		Via: "viewName := \"tuning_signals_daily\"，days<=7 时改成 " +
			"\"tuning_signals_5m\"（auto_route_tuning.go:765-768），769 行拼进 FROM。" +
			"两个都是 tuning 信号视图，不在 v1 关系名里。",
		Consequence: "与 _request_logs 退役无关。**残留噪声**：本文件报的是" +
			"真关系名，所以这条不是误报。",
	},
	"admin/dashboard_board_queries.go": {
		Verdict: verdictCanonicalOnly,
		Via: "fallbackBoardSummary 与 queryBoardCreditsExcludingProbes 共用 " +
			"boardRequestLogsFromClause；r1033（f5f7b1d1b）将其改为 " +
			"`db.SessionFamilyTurnsSourceSQL() AS r`，直接投影会话族，不再读 v1 包装视图。",
		Consequence: "两处聚合均不依赖 v1 基表或 v1 历史臂；v1 DROP 不改变该读源。",
	},
	"admin/provider_cred_lifecycle.go": {
		Verdict:     verdictCanonicalOnly,
		Via:         "usageTable 解析为 `usage_ledger`（483 行）——计费台账表，不在 v1 族。",
		Consequence: "与 _request_logs 退役无关。",
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

	// ── 工具解析不出、人读过源码确认不是 v1 ─────────────────────────────
	// 绝大多数是**跨包调用**（db.SessionFamilyTurns*SQL / probemode.GuardStateTable），
	// 工具的 *ast.SelectorExpr 分支明写「不猜」。
	//
	// ★★ 2026-10-20（迁移 890）之后有一组**共同形态**，先在这里说一次，
	// 下面的条目只写各自的差异：
	//
	//	「bodies 腿经切换层 `db.SessionBodiesSourceSQL()` 取源」——
	//	而该切换层**今天仍是**：
	//
	//	    func SessionBodiesSourceSQL() string {
	//	        if settings.GetPlatformBool(SessionBodiesNativeReadSetting, false) {
	//	            return SessionFamilyBodiesSourceSQL()   // 会话侧
	//	        }
	//	        return "request_logs_bodies_with_current_month"  // ★ v1-only 视图
	//	    }
	//
	//	而 `request_logs_bodies_with_current_month` **已被迁移 890 删除**
	//	（890 第 4 步「删 v1-only 视图」显式列名它）。
	//	⇒ 默认臂指向一个**已不存在**的关系 ⇒ 该臂运行期 **42P01**（响亮失败）。
	//
	//	⚠⚠ **本工具看不见这一条**（这正是它要被发现的地方）：
	//	  · 那个名字**不是 v1 基表**（不在 v1Tables 里）⇒ 不进 reads-v1 桶；
	//	  · 它的 DDL 文件已随 890 从 sql/objects/views/ 删除 ⇒ viewsWithV1Arm
	//	    **推导不出**它 ⇒ 不进 v1-arm 桶。
	//	⇒ 工具只能把它判成 canonical-only。**清单必须替工具说这句**，
	//	否则「canonical-only」会被读成「退役后照常工作」。
	"admin/logs.go": {
		Verdict: verdictCanonicalOnly,
		Via: "9 处：① 6 处 `FROM `/`JOIN ` + logsFrom（669/671/682/685/732/736），" +
			"logsFrom := logsSourceFromSQL()（admin/logs_turns_source.go:60-68）——" +
			"开关 `storage.admin_logs_native_turns_read` 默认 false 时返回 " +
			"`request_logs_with_current_month rl`，打开才返回会话族原生源；" +
			"② 2 处 bodies 腿（1152/1198）`dbpkg.SessionFamilyBodiesSourceSQL()`" +
			"（**固定**会话族，已不走开关）；③ 1 处 turnsSrc（1279）" +
			"`dbpkg.SessionFamilyTurnsSourceSQL()` / Hot 版（按窗口选）。",
		Consequence: "★ 迁移 890 之后 canonical 视图**没有 v1 臂**（体只剩 session_turns_hot ∪ session_turns 两臂）" +
			"⇒ 日志列表/聚合看到的是**只有会话族行**的视图。" +
			"⚠ 口径损失要记在案：原生源**不是**全量日志视图的等价替代" +
			"（真库实测 27.6% 的 request_id 在原生源查不到 —— 无会话头流量：探针/自检按设计排除、" +
			"in_progress 占位、标题/摘要生成器回环，见 logs_turns_source.go:40-55）" +
			"⇒ **默认支（视图）比原生支全**，开关打开反而少 27.6% 的行。" +
			"★ 退役前那版的后果（「v1 那部分行消失 ⇒ 少行」）已不适用：那一部分行随 890 一起被删，" +
			"不是被读丢的。",
	},
	"admin/usage_enhanced.go": {
		Verdict: verdictCanonicalOnly,
		Via: "baseTable 来自 planCostTrend 的 `BaseTable: \"request_logs_with_current_month rl\"`" +
			"（usage_enhanced.go:120），168 行 `\"FROM \" + baseTable + plan.JoinClause`。" +
			"⚠ 这是**裸表名字符串**，按行扫描与 AST 抽列两道门都看不见" +
			"（admin/request_logs_read_inventory_test.go 的注释已把它登记为已知缺口）。" +
			"★ 890 之后该视图已是会话两臂体 ⇒ 只看得到会话族行。",
		Consequence: "维度归因报表（用量趋势的成本拆分）只覆盖**会话族行**；v1 那部分历史随 890 一起没了。" +
			"与 logs.go 同属切换清单项。",
	},
	// ── 2026-10-29 输出合规审计轮（ff029a7fc）新增的间接读点 ──────────────
	"admin/sanitize_rules_review_context.go": {
		Verdict: verdictCanonicalOnly,
		Via: "1 处：`FROM ` + dbpkg.SessionFamilyTurnsSourceWindowedSQL(pdFrom, pdTo)" +
			"（sanitize_rules_review_context.go:218）——按审核单时刻推月份窗口的会话族 turns 读法" +
			"（跨包调用，工具只能判 canonical-only）。",
		Consequence: "审核单的原始上下文（请求/响应预览、模型、错误与 token 计数）只覆盖**会话族行**；" +
			"无会话头流量（探针/自检按设计排除等）在原生源查不到 ⇒ 该类审核单上下文列为空是**预期行为**。" +
			"v1 历史随 890 一起没了，与 logs.go 同属切换清单项。",
	},
	// ── §9.232：bodies 腿改走切换层的消费点（7 文件 / 10 处）──────────────
	// ⚠ §9.233 起这些调用**全部变成跨包**（切换层下沉到 db），原先「同包函数调用」
	// 的说法不再成立；工具仍判 unresolved（`resolve` 的 CallExpr 只查 env.funcs），
	// 但理由从「同包函数」变成「跨包函数」——结论不变，成因变了，必须改写而非留旧句。
	//
	// ★ 2026-10-20（迁移 890）：这批的**判定全部改写**：
	//   · 工具改判 canonical-only（推导集合空了 ⇒ 第四桶恒为 0）；
	//   · 而人读的结论也变了：**默认臂指向的视图被 890 删了**（见本节开头那段）。
	// ⇒ 「canonical-only」在这里**不等于**「退役后照常工作」——
	//	开关默认关 ⇒ 默认臂 42P01。这正是「工具的桶」与「人的结论」必须分开写的原因。
	"admin/auto_title_generator.go": {
		Verdict: verdictCanonicalOnly,
		Via: "841 行 `LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` rb`（1 处，§9.232 从字面量 v1 bodies 改走切换层）。" +
			"跨包函数调用 ⇒ 工具判 canonical-only（该函数的两臂解析出的是会话族 bodies 与 " +
			"`request_logs_bodies_with_current_month`，**都不是 v1 基表**；后者又不是任何视图的 v1 臂" +
			"——890 已删掉它的 DDL ⇒ 推导器认不出它）。" +
			"消费点由 admin 侧 indirectSourceConsumers 机器识别；切换层在 db/request_logs_view_schema.go:788-793。",
		Consequence: "★ **默认臂指向已被 890 删除的视图** `request_logs_bodies_with_current_month`" +
			"⇒ 开关 `storage.session_bodies_native_read`（默认 **false**）下这条 JOIN **42P01**，" +
			"接口报错——是**响亮失败**，不是静默少行（与本条目退役前那版「静默变空、接口仍 200」相反）。" +
			"⇒ 处置：把 bodies 切换层的默认臂改指会话族（或强制翻开关），" +
			"⚠ 但翻开关前必须成对翻写端 `storage.session_turns_bodies_enabled`" +
			"（admin/v1_bodies_read_path_test.go 的文件头记着：只翻读端会读到**空正文**）。",
	},
	"admin/compression_stats.go": {
		Verdict: verdictCanonicalOnly,
		Via: "`LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` `（3 处：62/151/272 行的 JOIN，" +
			"拼接点在 53/144/269；§9.232/§9.237。第 3 处在**包级 var** compressionStatsEstimatedOrigSQL 里，" +
			"该常量**因此由 const 改成 var**——函数调用不能出现在 const 声明中）。" +
			"**默认臂读 `request_logs_bodies_with_current_month`**（890 已删的 v1-only 视图）。",
		Consequence: "★ 与本条目退役前那版（「静默归 0、接口 200」）**形态相反**：现在默认臂 42P01，" +
			"压缩统计查询**直接报错**。⇒ 处置同 auto_title_generator（bodies 切换层默认臂改指会话族）。" +
			"⚠ 换源时要连分项一起看（with_outbound / compressed / estimated_original_tokens / summary_mode_rows），" +
			"只看 total 会被骗过去。",
	},
	"admin/logs_summary.go": {
		Verdict: verdictCanonicalOnly,
		Via: "`LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` `（2 处：202/264 行的 JOIN，拼接点在 199/261；§9.232）。" +
			"**默认臂读 `request_logs_bodies_with_current_month`**（890 已删的 v1-only 视图）。",
		Consequence: "★ 默认臂 42P01（视图已被 890 删除）⇒ 正文两列的查询**直接报错**，" +
			"而不是本条目退役前那版的「静默变空、计数类仍有数」。",
	},
	"admin/memora_handlers.go": {
		Verdict: verdictCanonicalOnly,
		Via: "`LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` `（1 处：812 行的 JOIN，拼接点在 792；§9.232）。" +
			"**默认臂读 `request_logs_bodies_with_current_month`**（890 已删的 v1-only 视图）。",
		Consequence: "★ 默认臂 42P01（视图已被 890 删除）⇒ 记忆面板的正文查询**直接报错**。" +
			"⚠ 停写分类表记 degraded_content，且该条目历史上因「同一文件取更危险档」被改判过一次（§9.35）" +
			"——改它之前先读那条 Note。",
	},
	"admin/no_topic_session.go": {
		Verdict: verdictCanonicalOnly,
		Via: "`LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` `（2 处：147/322 行的 JOIN，拼接点在 137/319；§9.232）。" +
			"**默认臂读 `request_logs_bodies_with_current_month`**（890 已删的 v1-only 视图）。",
		Consequence: "★ 默认臂 42P01（视图已被 890 删除）⇒ 查询**直接报错**。" +
			"（退役前那版写的是「降级的是正文两列、不是行数」，那是「表还在但被清空」时代的形态。）",
	},
	"admin/session_sanitize_matches.go": {
		Verdict: verdictCanonicalOnly,
		Via: "226 行 `LEFT JOIN ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb`（拼接点在 182；§9.232）。" +
			"★ 2026-10-20（890）该腿已**固定**为会话族源（不再经开关），" +
			"所以它**没有**上面那组「默认臂 42P01」的问题。",
		Consequence: "与 v1 无关（会话族直连）。" +
			"⚠ 与本条目退役前那版（「bodies 腿取不到时匹配依据变空 ⇒ 结果变少但不报错」）**形态不同**：现在取不到就是报错。",
	},
	"admin/session_compare.go": {
		Verdict: verdictCanonicalOnly,
		Via: "2 处：① 215/959 行 `FROM ` + db.SessionFamilyTurnsForSessionSQL() + ` rl`，会话族；" +
			"② 963/964 行 `LEFT JOIN ` + db.SessionBodiesSourceSQL() + ` rb`（拼接点在 963）——" +
			"**默认臂读 `request_logs_bodies_with_current_month`**（890 已删的 v1-only 视图）。",
		Consequence: "turn 腿与 v1 无关。★ **bodies 腿**：默认臂 42P01（视图已被 890 删除）⇒ 会话对比接口**直接报错**，" +
			"而不是本条目退役前那版的「COALESCE 成 `{}` 而接口仍 200」。" +
			"已登记在 admin/request_logs_indirect_readers_test.go 与 admin/request_logs_bodies_retirement_gate_test.go。",
	},
	"admin/session_list.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "138/162 行 `FROM ` + db.SessionFamilyTurnsSourceSQL() + ` rl`、" +
			"384/443 行 ForSessionSQL()。全是会话族。",
		Consequence: "与 v1 无关。",
	},
	// ── S4 波 4（2026-10-13）新增的会话族拼接点 ──────────────────────────
	// ── S4 波 4（2026-10-13）补登：波 2/波 3 引入但当时未进本清单的 8 文件 ──
	// （pristine HEAD 本门已红 8 条；本轮补齐使其复绿。判定依据=逐文件读拼接点。）
	//
	// ★ 2026-10-20（迁移 890）：这批条目的**行号与机制**都随 v1 族退役重写过了
	// （v1 字面腿被删、会话族拼接点搬家）。逐条按今天的源码核对过。
	"admin/body_resolver.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         ":215-220 lookupControlledBody 主查询 `FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl` + `LEFT JOIN ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb`（JOIN 键由 (request_id, ts) 改成 request_id 单键——会话族正文源**刻意不投影 ts**）；:237 阶段3 会话族兜底腿 `FROM ` + dbpkg.SessionFamilyBodiesSourceSQL()。",
		Consequence: "本拼接点与 v1 无关。★ 890 之后本文件**没有任何 v1 腿**：原阶段1/2 的 v1 字面腿（v1 hot 主行 JOIN v1 hot 正文表 / canonical 视图 JOIN v1 bodies 视图）已随 890 删除 ⇒ 本条从「阶段1/2 由 bodies 退役门管辖」变成「**整条读法只剩会话族**」。",
	},
	"admin/routing.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         ":2729 popularModelsHotSQL `FROM ` + db.SessionFamilyTurnsSourceSQL()（S3 波 3 会话族原生源）。",
		Consequence: "与 v1 无关；模型列回退链 client→outbound→canonical。",
	},
	"admin/unified_detail.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         ":272 pgBodyReader.loadOutboundBody 会话族兜底腿 `FROM ` + dbpkg.SessionFamilyBodiesSourceSQL()（S4 停写交接加）。",
		Consequence: "本拼接点与 v1 无关。★ 890 之后 hot/view 字面腿也一并没了 ⇒ 本文件只剩会话族一条腿。",
	},
	"cmd/gateway/main_v3_wiring.go": {
		Verdict: verdictNonV1ByInspection,
		Via: ":159-181 lastOutboundSessionSQL：SessionFamilyTurnsForSessionSQL() turns 腿 + " +
			"SessionFamilyBodiesSourceSQL() bodies 腿（LEFT JOIN）+ " +
			"public.session_bodies_unified 快照腿（LEFT JOIN LATERAL，kind='final_full'）" +
			"（压缩缓存 L3 冷启动回退，§143.55 补上 final_full 读侧）。",
		Consequence: "与 v1 无关（两会话族源直连 + 会话族 bodies 视图）。",
	},
	"domains/analysis/request_summary.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         ":127 requestSummaryTurnsSource = db.SessionFamilyTurnsForSessionSQL()（包级变量，工具判 unresolved(tool)；S3 波 2 起整体脱离 v1 族）。",
		Consequence: "与 v1 无关。",
	},
	"domains/hooks/goal/history_store.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         ":89-92 historySessionSQL：SessionFamilyTurnsForSessionSQL() + SessionFamilyBodiesSourceSQL()（主路径）。",
		Consequence: "本拼接点与 v1 无关。★ 890 之后 historyV1SQL 回退臂（v1 字面量）**已被删掉** ⇒ 本文件只剩会话族一条路。",
	},
	"domains/streaming/model_alternatives.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         ":184 `FROM ` + db.SessionFamilyTurnsSourceSQL()（S3 波 2 会话族化）。",
		Consequence: "与 v1 无关。",
	},
	"internal/summarystore/store.go": {
		Verdict:     verdictNonV1ByInspection,
		Via:         ":262 countTurnsSessionSQL `FROM ` + db.SessionFamilyTurnsForSessionSQL()（S3 波 2 会话族化）。",
		Consequence: "与 v1 无关。",
	},
	"admin/compression_sessions.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "countSQL + 主查询 + LATERAL 三段：SessionFamilyTurnsSourceSQL() ×3、" +
			"SessionFamilyBodiesSourceSQL() ×3（2026-10-13 S4 波 4 会话族化，" +
			"原 v1 视图两腿整体替换；拼接点在 115/144/149）。",
		Consequence: "与 v1 无关。退役门登记见 admin/request_logs_bodies_retirement_gate_test.go" +
			"（该文件已整体出列 v1 族）。",
	},
	// 2026-10-14（S4 退役改名轮）：data_lifecycle 趋势端点跨族 + 冻结点切分。
	"admin/data_lifecycle.go": {
		Verdict: verdictCanonicalOnly,
		Via: "dataLifecycleTrendSQL 的会话腿经 `+ dbpkg.V1FreezeCutoffSQL +` 拼接（data_lifecycle.go:214）；" +
			"带租户支另经 dataLifecycleCompressedExists（:33）——它是 `EXISTS (SELECT 1 FROM ` + " +
			"dbpkg.SessionFamilyBodiesSourceSQL() + ` sb …)`，**跨族的 v1 腿已在 890 删除**。" +
			"⚠ V1FreezeCutoffSQL 现在是**常数** `(SELECT '-infinity'::timestamptz)`" +
			"（db/v1_freeze_cutoff.go 的 890 一节）⇒ 谓词恒真；工具按跨包常量判 canonical-only。",
		Consequence: "★ 与本条目退役前那版（「DROP v1 后 V1FreezeCutoffSQL 退化为常数、v1 两表不存在 ⇒ 该 SQL 报错，响亮失败」）" +
			"**形态不同**：现在它**已经是**那个常数，不再有任何报错路径。" +
			"⇒ 趋势端点只覆盖会话族行；v1 族的历史行随 890 一起没了。" +
			"趋势端点的处置随 v1 存储治理面板一并评估（退役门 Tier ② 登记在案）。",
	},
	// S4 波 4 补记（2026-10-13）：quality_correlations 重写后经切换层取 bodies。
	"admin/quality_correlations.go": {
		Verdict: verdictCanonicalOnly,
		Via: "buildBreakdownQuery：turns 腿 `FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl`（:249）、" +
			"bodies 腿 `LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` rb`（:255-256）" +
			"（2026-10-13 S4 波 4 从 _request_logs 基表字面量改来）。" +
			"★ 890 之后 bodies 腿的**默认臂指向已被删除的视图** `request_logs_bodies_with_current_month`。",
		Consequence: "turns 腿与 v1 无关。★ **bodies 腿默认臂 42P01**（视图已被 890 删除）⇒ 分桶查询直接报错，" +
			"而不是本条目退役前那版的「切到会话族供数」。探针流量按 R51 本就不在会话族" +
			"（口径变更已拍板登记，退役门条目）。分桶读的是 rb.outbound_body（完整上游请求对象），列在四列合同内。",
	},
	"admin/session_bodies_batch.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "主批量取数器 sessionBodiesByRequestIDSQL = `FROM ` +" +
			"dbpkg.SessionFamilyBodiesSourceSQL() + ` rb`（2026-10-13 S4 波 4；拼接点在 78/107）。",
		Consequence: "本拼接点与 v1 无关。★ 890 之后**没有遗留的 v1 字面腿**了：" +
			"v1BodiesByRequestIDSQL 现在也是 `FROM ` + dbpkg.SessionFamilyBodiesSourceSQL()，" +
			"sessionBodiesByRequestIDAndTSSQL 内联会话族两腿——标识符里的 \"v1\" 现在只是**历史名**" +
			"（文件头自己这么写）。⇒ 退役门登记的那条「DROP 时直接删除」已随 890 执行完毕。",
	},
	"admin/session_export.go": {
		Verdict: verdictCanonicalOnly,
		Via: "2 处：① :217 `FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL() + ` rl`（会话族）；" +
			"② :230-235 `LEFT JOIN ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb`（拼接点在 230）。" +
			"★ 2026-10-20（890）该 bodies 腿已从 `db.SessionBodiesSourceSQL()`（带 v1 臂的运行时开关）" +
			"改成**固定的会话族源** ⇒ 它**没有**那组「默认臂 42P01」的问题。",
		Consequence: "与 v1 无关（两条腿都是会话族直连）。⚠ 与本条目退役前那版" +
			"（「COALESCE(rb.request_body,'{}') ⇒ 导出的会话包每条正文都是 {}，而且不报错」）**形态不同**：现在取不到就报错，" +
			"不会产出「结构自洽、正文全空」的包。已登记在 admin/request_logs_indirect_readers_test.go" +
			"与 admin/request_logs_bodies_retirement_gate_test.go。",
	},
	"admin/session_online.go": {
		Verdict: verdictNonV1ByInspection,
		Via: ":448 行 `FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL() + ` rl`，会话族。" +
			"★ 2026-10-20（890）之后同文件那个 `JOIN request_logs_with_current_month rl` 的 v1 视图腿" +
			"**已被删掉**（今天文件里只剩注释提到那个名字）⇒ 本文件现在只剩会话族一条路。",
		Consequence: "与 v1 无关。",
	},
	"admin/session_title.go": {
		Verdict: verdictCanonicalOnly,
		Via: "**两个拼接点，一句话说不清所以必须写全**：\n" +
			"  ① :325 `FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL() + ` t` → 会话族，与 v1 无关；\n" +
			"  ② :189-192 `LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` ` → **默认臂指向已被 890 删除的视图**" +
			"`request_logs_bodies_with_current_month`（§9.232 从字面量迁移过来，890 未改）。\n" +
			"⚠ 原条目只写了 ①、结论是「与 v1 无关」——迁移前那**确实成立**（bodies 腿是字面量，" +
			"由 exposure / bodies 退役门管，不在本清单范围）。" +
			"迁移后 ② 进了本工具的桶，而**若仍沿用「与 v1 无关」就会让清单说谎**。",
		Consequence: "① 会话族腿不受影响。② ★ **默认臂 42P01**（视图已被 890 删除）⇒ 标题生成查询**直接报错**，" +
			"而不是本条目退役前那版的「bodies 腿静默变空、接口仍 200，语料从全文降级为 preview 片段」。",
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
		Verdict: verdictNonV1ByInspection,
		Via: "`FROM ` + src.TurnsTable + ` rl`（73/99/120 行）。src 来自 " +
			"settleSourceFor(logsWriteEnabled)（auto_route_settle_source.go:80-87）。" +
			"★ 2026-10-20（890）该函数**恒返回会话族**（TurnsTable = \"session_turns_hot\"、" +
			"SessionKeyCol = \"session_id\"、Family = settleFamilySession），**v1 臂整条删除**——" +
			"理由记在文件头：v1 族没了以后「写门开着 ⇒ 读 v1」这条分支**永远走不到**，" +
			"而 `settings.RequestLogsWriteEnabled()` 在 settings 存储未初始化时回落**代码默认**" +
			"（★ 2026-10-20 订正：G-0 已把代码默认翻成 `false`，这条理由已从「当前风险」" +
			"降级为「历史动机」；v1 臂删除仍是对的，它是纵深防御）⇒ 留着它就是新装/早启进程直接 42P01。",
		Consequence: "与 v1 无关（结算三条腿同源同表，都读会话族热表）。" +
			"⚠ 与本条目退役前那版（「条件性：S4 关上写门后自动切到会话族；切换那一刻之前仍读 v1 底表」）" +
			"**形态不同**：现在是**恒**会话族，没有「切换那一刻」。",
	},
	// 2026-10-17 审计轮补登（df9a213de 漏项）。
	//
	// 漏登的性质值得记：df9a213de 把本文件的 `FROM _request_logs_hot` 字面量
	// 改成 `FROM ` + bg.ActiveSettleTurnsTable()，并在提交信息里写明
	// 「已按仓库既有的间接读方登记表归族并同步计数」——**它确实登了，
	// 但只登进了 admin/request_logs_indirect_readers_test.go（:115），
	// 漏了本清单**。两张登记表各管一件事（前者是「停写后果」六族分档，
	// 后者是本工具三桶的判定来源），改读法时必须**同时**动两张。
	"admin/auto_route_outcome_freshness.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "124 行 `SELECT MAX(ts) FROM ` 拼的是 bg.ActiveSettleTurnsTable()" +
			"（bg/auto_route_settle_source.go:101-103），后者 = " +
			"settleSourceFor(settings.RequestLogsWriteEnabled()).TurnsTable。" +
			"★ 2026-10-20（890）settleSourceFor **恒返回会话族**（session_turns_hot）" +
			"⇒ 本探针现在盯的正是结算 worker 真在读的那张表（这正是它被改写的原意）。" +
			"⚠ 工具对跨包函数调用 + 方法链返回空 calleeName ⇒ 判 unresolved；" +
			"这里靠读源码定级。与 bg/auto_route_settle_sql.go **完全同形**：" +
			"同一个 settleSourceFor、同一张 turns 表。",
		Consequence: "与 v1 无关（探针与结算腿同源同门，都读会话族热表）。" +
			"⚠ 与本条目退役前那版（「条件性：写门开着 ⇒ 读 v1 底表 ⇒ 退役顺序上必须排在 S4 之后」）" +
			"**形态不同**：现在没有 v1 那一支。",
	},
	// ★ 2026-10-21（R65 轮，跨会话订正 §19.10 的连带补登）：这 3 个文件是
	// 890 真删轮把读面换成**会话族原生源**时改的，但 890 轮的清单登记**漏了它们**
	// （那一轮自己登了 17 个新进桶文件，漏了这 3 个 ⇒ 清单门与快照门一直红）。
	// 三个文件的拼接点都指向 db.SessionFamilyTurnsSourceSQL()（会话族原生源：
	// hot ∪ parent + details 特征层 LEFT JOIN）⇒ 与 v1 退役**无关**。
	"admin/attempt_quality_api.go": {
		Verdict: verdictCanonicalOnly,
		Via: "146 行 `FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl`。" +
			"★ 890 真删轮从 `request_logs_with_current_month_without_request_class_due_at`" +
			"（那张包装视图**直读 v1 族**，真库实测其定义体第一行就是 `FROM _request_logs_hot`）" +
			"换成会话族原生源 ⇒ 旧读面在 v1 停写后恒 0 行、删族后会变 42P01。",
		Consequence: "质量指标端点（请求量 / 成功率 / token 汇总）现在读会话族全量 ⇒ " +
			"与 v1 退役**无关**。★ 口径提示：provider_id 来自 details 特征层，" +
			"**没有 details 行的轮次 provider_id 为 NULL** ⇒ 按 provider 过滤时" +
			"那部分行本来就不该计入（会话族既定口径，见 db/retirement_column_exposure.go）。",
	},
	"admin/auto_route.go": {
		Verdict: verdictCanonicalOnly,
		Via:     "156 行 `FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` r`（同源同上）。",
		Consequence: "自动路由任务列表（ts / request_id / task_type / auto_* / 凭据 / 裁决 / 延迟）" +
			"⇒ 读会话族 ⇒ 与 v1 退役**无关**。",
	},
	"admin/auto_route_correlations.go": {
		Verdict: verdictCanonicalOnly,
		Via: "5 处拼接点（132 / 153 / 174 / 195 / 239 行）全是 `FROM ` + " +
			"dbpkg.SessionFamilyTurnsSourceSQL() + ` …`（按模型 / 按策略 / 按任务类型 / " +
			"模型×任务类型 / top-3 裁决五段查询）。★ 890 真删轮从那两张**直读 v1 族**的" +
			"包装视图换过来 ⇒ 旧读面恒返回空（不报错）、删族后会 42P01。",
		Consequence: "自动路由相关性分析端点 ⇒ 读会话族 ⇒ 与 v1 退役**无关**。" +
			"★ 这 5 段依赖的 model_chosen / strategy_used / task_type_chosen 三列" +
			"来自会话族原生源自带的 details 特征层 LEFT JOIN ⇒ 无 details 行的轮次这三列为 NULL，" +
			"而查询本来就用 IS NOT NULL 过滤。",
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
			"DROP _request_logs 时这套逻辑会因 to_regclass 为 NULL 而走空分支，" +
			"不报错——与 §9.221 记的「视图被带外操作删除后没有机制重建」同族。",
	},

	// ── 2026-10-05（832/833 收口轮）：modality 核实闸带来的 2 个新 unresolved 读点 ──
	// modalityVerifyAddressableSource（bg/modality_verification.go:551 定义）是
	// credential_model_bindings ⨝ credentials ⨝ providers ⨝ provider_models
	// ⨝ models_canonical（LEFT JOIN 825 的 model_modality_verification）的
	// **纯 canonical 侧**子查询。核实 worker 的 dueTargets 与健康检查
	// modality_gate_readiness_floor 刻意共用**同一份**常量（抄第二份谓词会让
	// 「地板数」变成另一个集合的数），于是工具在两个文件各报 1 处同一操作数。
	"bg/modality_verification.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "1 处（598）`FROM ` + modalityVerifyAddressableSource + ` pp`。" +
			"常量本体（551）读位全是现行表：credential_model_bindings / credentials / " +
			"providers / provider_models / models_canonical + model_modality_verification。",
		Consequence: "与 v1 无关——退役 v1 时本读点无需动作。" +
			"⚠ 常量同时被 bg/routing_health_checks.go:481 引用（同一读点的第二处），两条目同源。",
	},
	// ⚠ 2026-10-20（890）：bg/routing_health_checks.go 条目已移到下方
	// 「§9.233 新进 unresolved 桶」那一组——它现在多了一个会话族拼接点
	//（893 行），旧条目只写了 modality 那一处。

	// ── §9.233：切换层下沉到 db 之后**新进** unresolved 桶的跨包读方 ────────
	// 这三个文件在 §9.232 结束时是「看得见但换不了」：它们在 admin 之外，
	// 看不见 admin 包里未导出的 sessionBodiesFromSQL()。§9.233 把切换层
	// 放进 db 包（无 import 环，已核实）之后才把它们迁过来 ⇒ 这三条是
	// **迁移的直接产物**，不是新发现的读方。
	//
	// ⚠ 迁移前它们的 bodies 腿是**字面量** `request_logs_bodies_with_current_month`，
	// 而本工具**只管关系名不是字面量的拼接点** ⇒ 迁移前它们一条都不进清单。
	// 迁移后进 unresolved 桶，于是**清单从 32 条长到 35 条**。
	// ⇒ 「清单条数增长」在这里是正确信号，不是工具误报。
	//
	// ★ 同一批迁的 `domains/sessionforensics/export.go`（2 处）**没有**出现在本清单，
	// 这不是漏登记，而是**本工具看不见它**：resolve.go 的点位枚举只走 `*ast.FuncDecl`，
	// 包级 `var`（GenDecl/VAR）只被 collectStringBindings 收进 env.globals 供**解析**，
	// 不进点位枚举（admin/compression_stats.go 第 3 处是同一形态，清单里早已注明）。
	// 那两处由 admin 侧 allKnownRequestLogsReaderFiles / retirementExposurePopulation
	// 与 TestV1BodiesScanIncludesSwitchConsumers 覆盖——**但覆盖它的是另一组门，不是本工具**。
	"bg/passive_probe_listener.go": {
		Verdict: verdictCanonicalOnly,
		Via: "183 行 `LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` rb`" +
			"（拼接点在 170；§9.233 从字面量 v1 bodies 改走切换层）。" +
			"★ 890 之后该切换层的**默认臂指向已被删除的视图** `request_logs_bodies_with_current_month`。",
		Consequence: "被动探针错误面板；`MAX(response_body)` 只取前 200 字符做卡片摘要。" +
			"★ **默认臂 42P01**（视图已被 890 删除）⇒ 该 INSERT 整条失败、**不报错给用户**" +
			"（worker 侧静默）——与本条目退役前那版的「COALESCE 到 ''、卡片照常渲染、摘要为空」**形态相反**。" +
			"⚠ 这一条比其它 bodies 读方更隐蔽：错误被 worker 吞掉 ⇒ 面板只是**不再更新**。",
	},
	"domains/sessionsummary/summarizer.go": {
		Verdict: verdictCanonicalOnly,
		Via: "652/695 行 `LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` rb`" +
			"（拼接点在 644/689；§9.233 迁移）。" +
			"★ 890 之后该切换层的**默认臂指向已被删除的视图** `request_logs_bodies_with_current_month`。",
		Consequence: "会话摘要的语料源。★ **默认臂 42P01**（视图已被 890 删除）⇒ 摘要查询**直接报错**，" +
			"而不是本条目退役前那版的「COALESCE 到 ''，摘要退化成只有元数据没有语料，接口仍 200」。",
	},
	"domains/sessionsummary/system_prompt_prefix.go": {
		Verdict: verdictCanonicalOnly,
		Via: "178 行 `JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` rb`（拼接点在 175；§9.233 迁移；" +
			"注意是 **INNER** JOIN 不是 LEFT）。" +
			"★ 890 之后该切换层的**默认臂指向已被删除的视图** `request_logs_bodies_with_current_month`。",
		Consequence: "⚠ **这是本批里唯一的 INNER JOIN**，而 INNER JOIN 的降级形态与其它 LEFT JOIN **不同类**：" +
			"不是「字段变空」，而是**整个系统提示词前缀查不到任何一行**（rl 有行但 rb 无行 ⇒ 被 JOIN 滤掉）。" +
			"★ 但 890 之后它**先撞上 42P01**（默认臂的视图已被删）⇒ 报错在前，滤空在后。" +
			"处置与其它 bodies 读方同：把切换层默认臂改指会话族。",
	},
	"domains/sessionforensics/export.go": {
		Verdict: verdictCanonicalOnly,
		Via: "**两处都在包级 `var` 里**（forensicsExportMessagesSQL 与 ...SQLAlt，各 1 处 bodies 腿，" +
			"`LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` rb ON rb.request_id = rl.request_id`；" +
			"拼接点在 47/67）。" +
			"§9.232 前它们是 `const`；§9.233 因函数调用不能出现在 const 里改成 `var`，" +
			"**就是这个形状让它们掉出了本工具的枚举范围**（点位只扫 FuncDecl）⇒ §9.237 才被重新看见。" +
			"★ 890 之后该切换层的**默认臂指向已被删除的视图** `request_logs_bodies_with_current_month`。" +
			"⚠ 文件刻意保留两份字面量并由 TestForensicsExportSQLVariantsStayInSync 强制同步，**该设计保留**。",
		Consequence: "★ **默认臂 42P01**（视图已被 890 删除）⇒ 导出**直接失败**，" +
			"而不是本条目退役前那版的「产出一份结构自洽、逐轮齐全、正文全空的证据包」" +
			"（那形态是「表还在但被清空」时代的产物；现在导出是响亮失败，反而更安全）。",
	},
	"bg/candidate_failure_monitor.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "2 处（290、295）：① `(SELECT max(ts) FROM ` + checkStalenessLastRequestSurface + ` rl`，" +
			"② `(SELECT max(id) FROM ` + checkStalenessLastRequestSurface + ` rl`" +
			"（aee31a117 的 S4 停写假阳性二分类给 staleness 查询加的第二个子查询，" +
			"读的是**同一张**会话族热腿）；" +
			"而 `var checkStalenessLastRequestSurface = db.SessionFamilyTurnsHotSourceSQL()`" +
			"（candidate_failure_monitor.go:267）。" +
			"★ 2026-10-20（890）它**从 v1-only 视图**（`request_logs_with_current_month_without_request_class_due_at`，" +
			"已被 890 删除）换成**会话族热腿**，并显式加了探针排除谓词" +
			"（v1-only 腿原来顺带把探针流量挡在外面）。" +
			"⚠ 工具判 unresolved：包级 var 的值是**跨包函数调用**，而 globals 收集阶段的 " +
			"env.crossAlias 是按包内最后一个文件设的 ⇒ 解析不到。靠读源码定级。",
		Consequence: "与 v1 无关（读会话族热腿）。" +
			"⚠ 与本条目退役前那版（「读 v1-only 视图 ⇒ 移除中间层 v1 视图会让 staleness 检查每 tick 报警」）" +
			"**形态不同**：源已换成会话族热腿，v1-only 视图被删**不再影响它**。",
	},
	"bg/credential_recovery.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "2 处：① 534 行 `FROM ` + probeGuardStateTable(...) + ` mps`——探针锁护航状态表，不在 v1 族；" +
			"② 1720 行 `FROM ` + db.SessionFamilyTurnsSourceSQL() + ` r`（lookbackCandidateSQL，" +
			"★ 890 之后从 v1 族换成会话族 hot ∪ parent 两个存储面）。",
		Consequence: "与 v1 无关。",
	},
	"bg/routing_health_checks.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "2 处：① 481 行 `FROM ` + modalityVerifyAddressableSource + ` pp`——" +
			"modality_gate_readiness_floor 检查的 reachable 集合，与 dueTargets 同一份常量（全是现行表）；" +
			"② 893 行 `FROM ` + db.SessionFamilyTurnsSourceSQL() + ` r`（负成本流量检查，" +
			"★ 890 之后从 v1 族换成会话族 hot ∪ parent）。",
		Consequence: "与 v1 无关。",
	},

	// ── 2026-10-20（迁移 890）**新进** canonical 桶的会话族读点（17 文件）────
	// 这 17 个文件在 890 之前**一条都不进本工具**：它们的取源是 v1 族的
	// **字面量**（`FROM _request_logs_hot` / `FROM _request_logs`），而本工具
	// 只管「关系名不是字面量的拼接点」⇒ 迁移前它们是**看不见**的。
	// 890 把它们改写成**会话族拼接调用**（`+ db.SessionFamilyTurns*SQL() +`）⇒
	// 进桶。
	//
	// ⇒ **「canonical 桶变大」在这里是正确信号**：方向是「把看不见变成看得见」。
	// 下面每一条的 Via 都是**逐文件读源码**写出来的（不是抄工具输出）。
	"admin/data_lifecycle_attachments.go": {
		Verdict: verdictCanonicalOnly,
		Via: "4 处（199/281/373/646）`FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl`——" +
			"附件列表与附件字节统计的取数。⚠ 刻意**不用** fmt.Sprintf 拼会话族 SQL" +
			"（那份投影含 `LIKE 'sys:%'`，`%` 会被当成动词吃掉）。",
		Consequence: "与 v1 无关（只读会话族）。⚠ 口径：v1 族的历史行随 890 一起没了，" +
			"而会话族只镜像有会话头的流量 ⇒ 附件统计不覆盖无会话头的流量。",
	},
	"admin/provider_diagnose.go": {
		Verdict: verdictCanonicalOnly,
		Via: "2 处（251/534）`FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl`——" +
			"provider 错误分类聚合。★ 890 之后从 `_request_logs_hot` 腿换成会话族：" +
			"provider_id 在会话族里只存在于 details 层（session_turns 没有这一列）" +
			"⇒ 必须走带 details LEFT JOIN 的族源。",
		Consequence: "与 v1 无关。⚠ 窗口仍覆盖 hot ∪ 母表两腿（promote 会把 8h 前的行搬进月分区，" +
			"只读热表会漏掉已 promote 的那一段）。",
	},
	"autoroute/recommend_v2.go": {
		Verdict: verdictCanonicalOnly,
		Via: "2 处（394/547）`FROM ` + db.SessionFamilyTurnsSourceSQL() + ` rl`——" +
			"最近一次任务取样与模型用量计数。★ 890 之后从 v1 母表（48h 窗跨过 hot 保留期）" +
			"换成会话族同款两个存储面。",
		Consequence: "与 v1 无关。⚠ 口径：会话族只镜像有会话头的流量 ⇒ " +
			"无会话头的流量不进入推荐样本。",
	},
	"bg/auto_index_refresher.go": {
		Verdict: verdictCanonicalOnly,
		Via: "1 处（802）`FROM ` + db.SessionFamilyTurnsHotSourceSQL() + ` t`——" +
			"rollupModelTaskIndexSQL（包级 var，因为投影源由函数生成、const 表达式里不能调函数）。" +
			"★ 890 之后从 v1 热表换成会话族热腿。",
		Consequence: "与 v1 无关。",
	},
	"bg/credential_selfcheck.go": {
		Verdict: verdictCanonicalOnly,
		Via: "2 处（598/644）`FROM ` + db.SessionFamilyTurnsHotSourceSQL() + ` t`——" +
			"近窗模型使用集合与自检回退样本。★ 890 之后从 v1 热表换成会话族热腿。",
		Consequence: "与 v1 无关。",
	},
	"bg/integrity_fingerprint_probe.go": {
		Verdict: verdictCanonicalOnly,
		Via: "2 处（36/50）`FROM ` + db.SessionFamilyTurnsHotSourceSQL() / db.SessionFamilyTurnsSourceSQL() + ` t`——" +
			"指纹流量存在性探测（hot 臂 + 母表臂）。★ 890 之后从 v1 族换成会话族两个存储面。",
		Consequence: "与 v1 无关。",
	},
	"bg/ledger_reconciliation.go": {
		Verdict: verdictCanonicalOnly,
		Via: "1 处（350）`FROM ` + db.SessionFamilyTurnsHotSourceSQL() + ` t`——usageCreditSQL 的用量腿。" +
			"★ 890 之后从 v1 族换成会话族热腿。",
		Consequence: "与 v1 无关。⚠ 该 SQL 的注释仍写着「不该在 S4 停写期间运行」——" +
			"890 之后 v1 腿已经不存在，这条约束的**前提**变了（两腿都在会话族时，" +
			"对账的两侧才是同一总体）。",
	},
	"bg/model_probe.go": {
		Verdict: verdictCanonicalOnly,
		Via: "3 处（416/500/926）`FROM ` + db.SessionFamilyTurnsHotSourceSQL() + ` t`——" +
			"模型探针的静态/近期使用证据与失败证据子查询。★ 890 之后从 v1 热表换成会话族热腿。",
		Consequence: "与 v1 无关。",
	},
	"bg/model_tier.go": {
		Verdict: verdictCanonicalOnly,
		Via: "1 处（170）`FROM ` + db.SessionFamilyTurnsHotSourceSQL() + ` t`——探针窗口内的原始模型集合。" +
			"★ 890 之后从 v1 热表换成会话族热腿。",
		Consequence: "与 v1 无关。",
	},
	"bg/today_success_probe.go": {
		Verdict: verdictCanonicalOnly,
		Via: "1 处（145）`FROM ` + db.SessionFamilyTurnsHotSourceSQL() + ` t`——" +
			"今日两连成功早停探针的最近使用时间。★ 890 之后从 v1 热表换成会话族热腿。",
		Consequence: "与 v1 无关。",
	},
	"cmd/compression-bench/main.go": {
		Verdict: verdictCanonicalOnly,
		Via: "2 处（242/252）：`FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl` 与 " +
			"`JOIN ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` b ON b.request_id = rl.request_id`——" +
			"压缩率基准工具的取数。★ 890 之后两腿都是会话族（原来 v1 那条 ts 等值 JOIN 搬不过来：" +
			"会话族正文源**刻意不投影 ts**）。",
		Consequence: "与 v1 无关。⚠ 这是**离线基准工具**，不是在线读面 ⇒ 它的失败不会影响网关。",
	},
	"cmd/traffic-replay/main.go": {
		Verdict: verdictCanonicalOnly,
		Via: "2 处（145/155）：`FROM ` + dbpkg.SessionFamilyTurnsSourceSQL() + ` rl` 与 " +
			"`LEFT JOIN ` + dbpkg.SessionFamilyBodiesSourceSQL() + ` rb`——历史行回放取数。",
		Consequence: "与 v1 无关。⚠ 文件头记着一条口径损失：会话族的 request_preview（600/890 有值）" +
			"仍是「最后一条用户消息」主信号，但「这一轮带了几个工具」会比 v1 口径**偏小**" +
			"（宁可让信号偏小，也不要拿累积正文冒充增量）。",
	},
	"discovery/discovery.go": {
		Verdict: verdictCanonicalOnly,
		Via: "1 处（1200）`FROM ` + db.SessionFamilyTurnsSourceSQL() + ` rl`——" +
			"model_offers 可用性刷新的取数。★ 890 之后从 v1 族换成会话族。",
		Consequence: "与 v1 无关。",
	},
	"domains/analysis/optimizer.go": {
		Verdict: verdictCanonicalOnly,
		Via: "3 处（191/204/205）`FROM ` + db.SessionFamilyTurnsForSessionSQL() + ` rl`——" +
			"会话统计的 turns 聚合。⚠ 口径损失（如实登记）：outbound_token_est 在会话族里**恒为 NULL**" +
			"（会话族没有这个事实来源，投影里是 NULL 补位）⇒ 第三条聚合恒为 0。",
		Consequence: "与 v1 无关。⚠ 那条恒 0 的聚合是**读源换族的代价**，不是 890 的退役后果。",
	},
	"domains/analysis/projectattr/store.go": {
		Verdict: verdictCanonicalOnly,
		Via: "2 处（252/295）`FROM ` + db.SessionFamilyTurnsForSessionSQL() + ` rl`——" +
			"项目归因信号的取数（包级 var：投影源由函数生成，const 表达式里不能调函数）。",
		Consequence: "与 v1 无关。",
	},
	"domains/credentialstate/popularity_tracker.go": {
		Verdict: verdictCanonicalOnly,
		Via: "1 处（92）`FROM ` + db.SessionFamilyTurnsHotSourceSQL() + ` t`——模型热度榜的取数。" +
			"★ 890 之后从 v1 热表换成会话族热腿；顺带把过滤列从 created_at 改成 ts" +
			"（v1 热表**没有 created_at 这一列** ⇒ 那条查询**一直**在报 42703、热度榜从未产出过）。",
		Consequence: "与 v1 无关。⚠ 这条是**修复**（原来一直报错），不是退役风险。",
	},
	"domains/providerprofile/adapters.go": {
		Verdict: verdictCanonicalOnly,
		Via: "4 处（141/179/209/259）`FROM ` + db.SessionFamilyTurnsHotSourceSQL() + ` t`——" +
			"凭据画像的请求总数/错误类型分布/限流命中率/时间分桶。",
		Consequence: "与 v1 无关。⚠ 口径：只读会话族热表 ⇒ 无会话头的流量（探针/自检回环）不计入画像。",
	},

	// ── 2026-10-25（R152 轮）：新进 unresolved 桶的 1 个文件 ──────────────────
	// 触发：R92/R93/R94 三轮（369531475 / a073b6558 / f204e4c6a）把
	// 「会话列表 enrich」从 v1 视图改写成会话族原生源，并把它抽进
	// db/request_logs_view_schema.go ⇒ 该文件第一次进入本工具的扫描范围。
	//
	// ⚠ **文件名有误导性**：文件名叫 `request_logs_view_schema.go`，但它**不是**
	//   v1 族的读方 —— 它是 canonical 视图 `request_logs_with_current_month`
	//   的**会话两臂形态**定义与自愈逻辑（890 之后该视图只剩 session_turns_hot
	//   ∪ session_turns 两臂）。★ 文件头第 33 行自陈「下面**没有**任何
	//   `_request_logs` 字面量」，本轮**逐条核对属实**（见下）。
	"db/request_logs_view_schema.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "2 处（271、274），都在 `SessionFamilyLatestTurnPerSessionSQL(extraPred string)` 的" +
			"**内层闭包** `leg := func(src string) string { … }` 里：" +
			"① 271 行 `\"FROM (SELECT session_id, max(turn_no) AS turn_no FROM \" + src + \" t \"`、" +
			"② 274 行 `\"JOIN \" + src + \" t ON t.session_id = l.session_id AND t.turn_no = l.turn_no\"`。" +
			"★ `src` 是**形参** ⇒ 工具解析不到 ⇒ 判 unresolved。" +
			"★★ **唯一的两个调用点在 276 行**：`leg(\"public.session_turns_hot\")` 与 " +
			"`leg(\"public.session_turns\")` ⇒ **两个入参都是会话族字面量**，没有任何第三个入参。" +
			"⚠ 工具判 unresolved 的原因与 bg/candidate_failure_monitor.go 同族：**跨作用域形参**，" +
			"不是「解析失败被吞」。",
		Consequence: "与 v1 无关（读会话族 hot ∪ 父表）。" +
			"★ **该文件全篇无 v1 关系读点**，逐条核对过：" +
			"(a) 两处拼接点的形参入参只有 session_turns_hot / session_turns 两个（271/274/276 行）；" +
			"(b) 全文出现 `_request_logs` 的地方**只有三类**，没有一处是关系名——" +
			"注释/文档（24/33/35/84/119/415/451/548/662/671/732/1030/1034/1085/1158/1198 行）、" +
			"**对视图定义文本的探针谓词**（74 行 `strpos(pg_get_viewdef(...), '_request_logs') > 0`，" +
			"那是「视图体里还有没有 v1 臂」的**检测器**，不是读 v1 表）、" +
			"以及日志文案里的字面量（610 行 COMMENT ON VIEW 的文本）。" +
			"⇒ 890 删掉 v1 族之后，本文件**不受任何影响**；" +
			"反而是它**主动**保证 canonical 视图不再长回 v1 臂（bodyHasV1Arm 探针 + 不做 DDL 的退化分支）。",
	},
	// ── 2026-10-28（r1040「事实源漂移」线）：本工具自身的清单漂移 ──────────
	//
	// ★ 这四条是**样板自身的漂移**：r1038b 轮实测时是「清单 72 vs 实测 75」，
	//   本轮实测已是「清单 72 vs 实测 76」（见 §r1040 审计档）。
	//   ⇒ 漂移**只会单向变大**（新增读点没人登记），不会自己收敛。
	//
	// ★ 本轮把 r1040 新立的「事实源漂移」线**先落在样板自己身上**：
	//   样板是这条线的**形态来源** ⇒ 一个自身还红的样板立不住。
	//   ⇒ 四条都**逐个读源码**后判定（不是抄工具输出），机制与后果写在下面。
	"admin/credential_monitor.go": {
		Verdict: verdictCanonicalOnly,
		Via: "1 处（384 行）：`COALESCE((SELECT COUNT(*) FROM ` + " +
			"`dbpkg.SessionFamilyCredentialCountSourceSQL(\"c.id\")` + ` q), 0) AS total_requests`。" +
			"★ 该 helper（db/request_logs_view_schema.go:761）返回**会话族两臂**：" +
			"`(SELECT t.credential_id FROM public.session_turns_hot t WHERE …) UNION ALL " +
			"(SELECT t.credential_id FROM public.session_turns t WHERE …)` ⇒ 两个关系名都是**会话族**。",
		Consequence: "凭据面板的 total_requests 走**会话族窄投影**（session_turns_hot ∪ session_turns），" +
			"不读 v1 族 ⇒ 退役 v1 对它无影响。" +
			"⚠ 该文件另有 `request_logs_with_current_month` 的**字面量**读法（546 行的 canonical 臂、" +
			"468 行的 p95 live 腿）—— 那些是**字面量**读点，不在本工具的口径里（本工具只管" +
			"「关系名不是字面量」的拼接点）。",
	},
	"bg/shared_pick.go": {
		Verdict: verdictCanonicalOnly,
		Via: "1 处（52 行）：`FROM ` + `dbpkg.SessionFamilyMostUsedClientModelSourceSQL()` + ` ` + alias。" +
			"★ 该 helper（db/request_logs_view_schema.go:836）返回**会话族两臂**：" +
			"`(SELECT … FROM public.session_turns_hot t LEFT JOIN public.session_turn_details_hot d …) UNION ALL " +
			"(SELECT … FROM public.session_turns t LEFT JOIN public.session_turn_details d …)` ⇒ 会话族。",
		Consequence: "探针选型（most-used client_model）走会话族窄投影（session_turns_hot+details_hot ∪ " +
			"session_turns+details）⇒ 不读 v1 族 ⇒ 退役 v1 对它无影响。" +
			"⚠ 该文件其余 SQL（158/197/237 行）都是**字面量**读（credential_model_bindings / provider_models），" +
			"与 v1 无关、也不在本工具口径内。",
	},
	"internal/sessionfamily/turns_reads.go": {
		Verdict: verdictCanonicalOnly,
		Via: "3 处（262、271、281 行）：`TurnConflictLookupSQL` / `TurnByRequestSQL` / `TurnsBySessionSQL` " +
			"都走 `TurnsArmsSQL`（236 行）⇒ 拼出 `(SELECT … FROM public.session_turns_hot hot …) UNION ALL " +
			"(SELECT … FROM public.session_turns)`。★ **两个关系名都是字面量** ⇒ 工具能解析 ⇒ 判 canonical。",
		Consequence: "写路径的幂等检查与冲突读回（按 (tenant_id, request_id) / (tenant_id, session_id) 读轮次）" +
			"读的是**会话族两臂** ⇒ 与 v1 退役无关。" +
			"⚠ hot 臂带**相关反连接**（`antiJoinPredicate`，剔除已晋级到父表的重复行）⇒ " +
			"「该键是否存在」的答案与视图形态恒等（文件头 49~57 行给了双向论证）。",
	},
	"internal/sessionfamily/exists_by_key.go": {
		Verdict: verdictNonV1ByInspection,
		Via: "1 处（69 行）：`\"(SELECT 1 FROM \" + src + \" WHERE tenant_id = \" + tenantExpr + …`。" +
			"★ `src` 是**形参** ⇒ 工具解析不到 ⇒ 判 unresolved。" +
			"★★ 但**唯一的两个调用点在同文件 71 行**：`leg(\"public.session_turns_hot\")` 与 " +
			"`leg(\"public.session_turns\")` ⇒ **两个入参都是会话族字面量**，没有第三个入参。",
		Consequence: "写路径每请求的幂等存在性检查，读**会话族两臂**（session_turns_hot ∪ session_turns）⇒ " +
			"与 v1 退役无关。★ 该文件刻意**不用视图**（22~57 行：视图形态多一次相关反连接 + 12 处分区引用，" +
			"252 生产规模实测规划时间中位 ~67 ms vs 两臂形态 ~4 ms）⇒ 两臂形态是**更省**的那一支，" +
			"不是退化。",
	},
}

// repoRoot 已移到同目录 tmproot_test.go（无 build tag）。
// 原因见那里的注释：原先它住在这个带 `//go:build !integration` 的文件里，
// 而 resolve_test.go（无标签）调用它 ⇒ `go vet -tags=integration` 编译失败。

// measureBuckets 跑一次全仓审计，返回「文件 → 三桶各自出现次数」。
func measureBuckets(t *testing.T, root string) (v1, v1arm, canonical, unresolved map[string]int, total int) {
	t.Helper()
	sites, err := AuditRepo(root)
	if err != nil {
		t.Fatalf("AuditRepo(%s): %v", root, err)
	}
	v1, v1arm, canonical, unresolved = map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	for _, s := range sites {
		total++
		switch s.Classification() {
		case ClassReadsV1:
			v1[s.File]++
		case ClassReadsV1Arm:
			// ★ §9.258：这一类**不能**并进 canonical。
			// 第一版漏了它，于是第四桶只出现在报告里、
			// 而 measureBuckets 的 default 分支把它算成 canonical
			// ⇒ 清单门把 25 处「行集会变小」的读法判成「关系名还在」而**照样绿**。
			v1arm[s.File]++
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
	return norm(v1), norm(v1arm), norm(canonical), norm(unresolved), total
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

func dumpBuckets(t *testing.T, v1, v1arm, canonical, unresolved map[string]int, total int) {
	t.Helper()
	t.Logf("全仓拼接点：%d 处 / %d 文件（v1 %d、v1 臂 %d、canonical %d、unresolved %d）",
		total, len(mergeCounts(v1, v1arm, canonical, unresolved)),
		len(v1), len(v1arm), len(canonical), len(unresolved))
	for _, b := range []struct {
		name string
		m    map[string]int
	}{{"reads-v1", v1}, {"reads-v1-arm", v1arm}, {"canonical-only", canonical}, {"unresolved", unresolved}} {
		var files []string
		for f, n := range b.m {
			files = append(files, f+"("+strconv.Itoa(n)+")")
		}
		sort.Strings(files)
		t.Logf("── %s(%d): %s", b.name, len(files), strings.Join(files, " "))
	}
}

// measuredVerdict 把一个文件的四桶计数折成「工具眼里的判定」。
//
// unresolved 优先：只要还有一处不可判定，工具就**不敢**声称这个文件安全。
// 这是本工具刻意的保守（resolve.go：「宁可不可判定也不猜」），
// 所以工具的判定是**上界**而不是结论——结论由人给。
func measuredVerdict(v1n, v1armN, canonN, unresN int) verdict {
	switch {
	case unresN > 0:
		return verdictUnresolvedTool
	case v1n > 0:
		return verdictReadsV1
	case v1armN > 0:
		return verdictReadsV1Arm
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
	case tool == verdictReadsV1Arm && mine == verdictCanonicalOnly:
		// §9.258：**降级到 canonical-only 是这一类最危险的错**。
		// canonical-only 在词表里只承诺「关系名还在」，
		// 而本类的后果是「行集会变小且不报错」——
		// 把它记成 canonical-only，等于把 25 处静默少行登记成「安全」。
		return []string{fmt.Sprintf(
			"%s: 工具判 reads-v1-arm，清单判 canonical-only —— **这是降级**。"+
				"该文件读的是体里含 v1 臂的视图：DROP 后关系名还在、但行集变小，"+
				"多数读法用 COALESCE/COUNT 包装过 ⇒ 静默少行而不是报错。"+
				"请改成 verdictReadsV1Arm 并在 Consequence 里写清会少哪部分行。", file)}
	case tool == verdictReadsV1Arm && mine == verdictNonV1ByInspection:
		// ★ 这一条是**最严重的方向**：清单里写的是「人读过源码，确认不是 v1」，
		// 而工具现在判它是 v1 臂读点。前者是**自信的否定**，不是「还不知道」。
		// 极可能的原因：那条判定写于 bodies 读方还是**字面量**的年代，
		// §9.232 把它们改成走 `dbpkg.SessionBodiesSourceSQL()` 之后就过期了，
		// 而 nonv1-by-inspection **不会**像 unresolved 那样被规则 ③ 抓到。
		return []string{fmt.Sprintf(
			"%s: 清单判 nonv1-by-inspection（「人读过源码，确认不是 v1」），"+
				"工具判 reads-v1-arm —— **这是自信的否定，且现在被证伪**。\n"+
				"多半是该文件在 §9.232 把字面量改成走切换层之后，这条判定就过期了；"+
				"nonv1-by-inspection 与 unresolved 的区别正是「查过没问题」与「还没查」，"+
				"所以它不会被过期检测抓到。请重读源码并改成 verdictReadsV1Arm。", file)}
	case tool == verdictReadsV1Arm && mine == verdictUnresolvedTool:
		// §9.258：清单停在「工具查不出」而工具**现在查得出**，且答案是
		// 「读 v1 臂的视图」⇒ 这不是「判定错误」，是**判定过期**，
		// 而且过期方向是「从不知道变成了知道有风险」⇒ 必须更新。
		return []string{fmt.Sprintf(
			"%s: 清单判 unresolved(tool)，但工具**现在能解析**并判为 reads-v1-arm —— "+
				"判定过期了（§9.257 修了跨包解析 + §9.258 加了 v1 臂这一类）。"+
				"请把 Verdict 改成 verdictReadsV1Arm，并在 Consequence 里写清会少哪部分行。", file)}
	case mine == verdictReadsV1Arm && tool == verdictCanonicalOnly:
		return []string{fmt.Sprintf(
			"%s: 清单判 reads-v1-arm，工具判 canonical-only —— **两者矛盾**。"+
				"要么是 viewsWithV1Arm 认错了视图（检查该视图的 DDL 是否真含 v1 臂），"+
				"要么是清单过期了（视图的 v1 臂已被去掉）。", file)}
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
	v1, v1arm, canonical, unresolved, total := measureBuckets(t, root)
	dumpBuckets(t, v1, v1arm, canonical, unresolved, total)

	measured := mergeCounts(v1, v1arm, canonical, unresolved)

	var errs []string
	for f, n := range measured {
		got := measuredVerdict(v1[f], v1arm[f], canonical[f], unresolved[f])
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
// bucketSnapshot 是三桶的实测快照，用来**钉住本文件头部那张表**。
//
// # 为什么需要它（2026-10-06 实测触发）
//
// 头部注释里的数字已经漂了两次：
//
//	§9.226.3 快照 : 全仓 67 处 / 35 文件（v1 6、canonical 5、unresolved 25）
//	2026-10-06 实测: 全仓 70 处 / 37 文件（v1 6/12、canonical 5/5、unresolved 26/53）
//
// 而「§9.226.3 快照已过期」那句**自己**也已经过期了 —— 它当时把 67/25
// 标成「当前值」，此后再没人回来核过。
//
// ⚠ 漂移的**方向**在这里不是缺陷：§9.232/§9.233 把一批读方从**字面量**
// 改成拼接调用，于是它们从「不进本工具」变成「进 unresolved」⇒ unresolved
// 桶单调变大是**把看不见变成看得见**，变好不变坏。
// 真正的问题是**没人要求它被核过**：那张表是纯注释，注释不会红。
//
// ⇒ 这道门只做一件事：**注释里的数字与实测不一致时立刻红**。
// 它不判断漂移是好是坏（那要人判断），只保证「人至少被叫过来看一眼」。
//
// # ★ 2026-10-20（迁移 890）：「v1 桶非空」与「v1 臂桶非空」两条**取反**
//
// 迁移把 v1 族整族删了 ⇒ 这两个桶**理应**归零，而「归零」在输出里与
// 「分类器坏了」**长得一模一样**。所以这两条不能删——删了就等于把
// 「工具还能不能看见 v1」这个唯一的自检信号一起删掉。
//
// ⇒ 换成**等价强度**的三件事：
//
//	① 总体非 0（保留原条款）；
//	② **解析率不能塌**：canonical 桶非空，且「可静态解析的站点数」不低于门槛
//	   ⇒ 挡住「抽取器/解析器整体坏掉 ⇒ 全部落进 unresolved」这个形态；
//	③ **正向对照**：把**合成的** v1 站点喂给**同一个**分类器，它必须落进
//	   reads-v1 / v1-arm 桶 ⇒ 证明「v1 桶为空」是**仓库的状态**，不是
//	   **工具的失明**。
//
// ③ 是这道门的关键：没有它，「v1 桶空了」既可以解释成「v1 退役干净了」，
// 也可以解释成「分类器认不出 v1 了」——而这两件事的处置完全相反。
// ⚠ 推导**层**（viewsWithV1Arm 还能不能认出含 v1 臂的视图）由
// resolve_test.go 的 TestV1ArmViewsIncludeComposerDefinedOnes 承担，
// 那道门用两段合成夹具做正向对照 ⇒ 两层合起来才覆盖「看不见 v1」的全部形态。
type bucketSnapshot struct {
	TotalSites int
	V1Files    int
	V1Sites    int
	V1ArmFiles int
	V1ArmSites int
	CanonFiles int
	CanonSites int
	UnresFiles int
	UnresSites int
}

// documentedSnapshot 是 §9.257 修完跨包解析后写入的实测值。
//
// §9.257 的变化：unresolved **53 处/26 文件 → 14 处/7 文件**，
// canonical 5 → 44 处（24 文件），**v1 仍然是 12 处**。
// ⇒ ★ **解析率提高 39 处，而 v1 读点数一动不动** —— 见下面那条注释，
// 这是本轮最要紧的一条实测结论。
//
// ⚠ 改这三个数字之前先读上面的注释：确认是「桶真的变了」而不是
// 「扫描范围被改小/解析失败被吞」。`TestRepoAuditIsNotSilentlyVacuous`
// 只挡总体为 0，挡不住**部分**文件解析失败后被 `continue` 静默跳过。
// 2026-10-13（S4 波 4）更新：S3 波 2/波 3 的读方会话族化在本清单之外发生了
// （那些轮次没跑本工具测试）：字面量改拼接 + 会话族新拼接点 ⇒ canonical 桶
// 12/18 → 22/36、unresolved 9/16 → 10/17、总数 73 → 92。方向全部是
// 「把看不见变成看得见」与「字面量 → 拼接」，无桶变小；v1 桶 6/12 一动不动
// （★ 与 §9.256 结论一致：解析率再怎么变，v1 基表读方数不变）。
//
// ★★ 2026-10-20（迁移 890，v1 族整体退役）更新 —— **这一版是「退役之后」的形状**，
// 三个桶的**语义**都变了，所以这一版的注释不是在上一版上打补丁，而是重写：
//
//	总数 95 → **141 处（71 文件）**
//	v1      5/11 → **0/0**    （文件 / 处数）★ 全仓 v1 基表读面归零
//	v1 臂  16/27 → 0/0
//	canonical 25/38 → **59/122**
//	unresolved 12/19 → 12/19（**未变**）
//
// # 逐项判方向（门规：桶变小 ⇒ 先怀疑扫描范围被改小 / 解析失败被吞）
//
//	① **v1 5/11 → 0/0**（清零）—— 三个成因都核过，每一条都读过源码：
//	   · admin/tenants.go 的 4 处：取源函数 tenantStatsLogsTableSQL 从四臂
//	     （会话 hot/父表 + v1 hot/父表）收敛成**两臂**（只剩会话族）——
//	     逐行读过 admin/tenant_stats_logs_table.go:30-53；
//	   · cmd/tools/backfill_session_bodies/main.go 的 1 处：该工具**退役**（文件还在，
//	     但 main.go 已不读 v1 族，改成报「v1 family retired」后退出）⇒
//	     审计不再报出它 ⇒ 清单里那条判定**已删**；
//	   · maas 的 3 个文件（usage.go / consumption_detail.go / credit_buckets.go）
//	     共 6 处：包内切换层 `requestLogsSource(days)` 的 `days<=7` 支返回
//	     `_request_logs_hot AS r`（v1 底表）⇒ **这 6 处是 890 之后仅剩的
//	     v1 基表读点**，而它们指向一张**已被删**的表 ⇒ 42P01。
//	     ★ 本轮**改掉了**：`requestLogsSource` 的两个分支合并成一个
//	     （恒返回 canonical 视图 —— 它本身就是会话族两臂体，已覆盖热窗+冷窗）。
//	     ⇒ v1 桶归零，且归零的**原因**是「读点被改掉」，不是「扫描变瞎了」：
//	       那 3 个文件仍然被报出（只是落在 canonical 桶）⇒ 文件数不变。
//	② **v1 臂 -16 文件 / -27 处**（变小）—— 成因是**推导集合本身空了**：
//	   · composer（db/request_logs_view_schema.go）现在只定义一个视图
//	     `request_logs_with_current_month`，而它的体是**会话两臂**
//	     （canonicalSessionOnlyDDL，:561-587）⇒ 没有任何 v1 臂；
//	   · 4 张 v1-only 视图（…_without_customer_id / …_without_request_class_due_at /
//	     request_logs_bodies_with_current_month / request_logs_bodies_progress）
//	     随 890 删除 ⇒ 它们的 DDL 文件也不在对象库里了。
//	   ⇒ 那 16 个文件整体落进 canonical 桶（**没有**一个变成「看不见」）。
//	   ⚠★ **2026-10-21 订正**：本段原先还有一句「sql/objects/views/ 里的同名 DDL
//	   已重新采集为会话两臂体」——**它在 10-20 09:58（e9a96c26c）之后就不成立**：
//	   那 5 张分析视图（customer_cost_view / model_cost_per_task_view /
//	   v_continuation_effectiveness / v_node_switch_analysis / v_timeout_effectiveness）
//	   **必须回到 v1 fallback 体** —— 基线是**逐语句**应用的，而 CREATE VIEW 会
//	   **急切校验**它引用的关系 ⇒ 若写成会话族形态（session_turns_hot /
//	   session_turn_details*），**全新安装**会在基线阶段就报 42P01 并中止
//	   （实测见 §19.10 跨会话订正）⇒ 部署形态改由**迁移 883 / 884** 换上。
//	   ⇒ 源文件是「**基线时点**的形态」，不是「部署形态」；
//	   ⇒ 判据因此必须**分两个时点**（见 resolve_test.go 的
//	   TestDeploymentTimeHasNoV1ArmView 门 17 + v1arm_deployment.go）。
//	③ **canonical +32 文件 / +78 处**（变大）—— 方向=变大=「把看不见变成看得见」：
//	   890 把一批读面从 v1 族**字面量**（`FROM _request_logs_hot`）改写成会话族
//	   **拼接调用**（`+ db.SessionFamilyTurns*SQL() +`）⇒ 它们第一次进本工具。
//	   新进桶的 17 个文件逐个读过源码确认读的是会话族（见清单里那 17 条）；
//	   再加 maas 那 3 个文件（v1 → canonical 的重分类）。
//	④ **unresolved 12 文件 / 19 处**（**未变**）—— 一个数都没动。
//	⑤ **2026-10-21（R65 轮）canonical +2 文件 / +6 处（总处数 135 → 141）** ——
//	   ★ 这一条是**逐站点 diff** 出来的（不是「总数对不上，猜一猜」）：
//	   在 `5f64031cb`（写下 135/57/116 的那次提交）跑一次本工具、存下报告，
//	   与当前 HEAD 的报告**逐行 diff** ⇒ 精确得到：
//	  · **+7 处 / +3 文件** = 三个新增文件：
//	    admin/attempt_quality_api.go:146、admin/auto_route.go:156、
//	    admin/auto_route_correlations.go:132/153/174/195/239
//	    ⇒ 三处都读 `dbpkg.SessionFamilyTurnsSourceSQL()`（**逐行读过源码**）
//	    ⇒ 会话族 ⇒ canonical ⇒ 方向=变大=「把看不见变成看得见」；
//	  · **−1 处 / −1 文件** = bg/routing_health_checks.go 的拼接点被 8531596a6
//	    改写成**纯字面量**（`public.session_turns_hot` UNION ALL
//	    `public.session_turns`）⇒ 本工具的 Site 定义是「**关系名不是字面量**的
//	    拼接点」（resolve.go）⇒ 它**离开了本工具的口径**。
//	    ⚠ 桶变小必须先排除「解析失败被吞」—— 这次排除的依据是**读法**：
//	    目标表是**会话族**（不是 v1）⇒ 没有 v1 读点被藏起来；
//	    且同一文件在 unresolved 桶里仍有一处（行号平移）⇒ 该文件没被跳过。
//	  ⇒ 净 +6 处 / +2 文件；unresolved 12/19 **未变**（逐行 diff 确认只是行号平移）。
//
// # 一致性自检（0 + 0 + 59 + 12 = 71 = 总文件数 ✔）
//
// 实测 71 个文件 = v1 0 + canonical 59 + unresolved 12 = 71，**不多不少**
// （上一版是「多出 1」：bg/routing_health_checks.go 同时落在 canonical 与
// unresolved 两桶 —— 2026-10-21 那次改写把它的 canonical 拼接点变成了纯字面量，
// 于是它只剩 unresolved 一处 ⇒ 差集归零）。
// 处数自检：0 + 0 + 122 + 20 = 142 = 总数 ✔
var documentedSnapshot = bucketSnapshot{
	// 同日晚间二次：compression_sessions 的 LATERAL 性能修复消除 2 处拼接
	//（LATERAL 体重跑 canonical 全源改单趟 CTE，e01ffed39）→ 94→92、
	// canonical 37→35。文件集合不变，非扫描盲区。
	// 2026-10-14（S4 退役改名轮）：data_lifecycle 趋势端点跨族化新增 1 处
	// V1FreezeCutoffSQL 拼接（工具判 canonical）→ 92→93、canonical 23→24 文件 /
	// 35→36 处。文件集合其余不变。
	// 2026-10-15（r1033，f5f7b1d1b）：board_queries 两处从 v1 臂切到会话族；
	// usage_credits 的短窗从 v1 基表切到会话族、长窗仍有 v1 臂。
	// 因此 v1 -1/文件 -1、v1 臂净 -1、canonical +2/文件 +1，总数不变。
	// 已逐读 board_time_range.go / usage_credits.go / dashboard_board_queries.go，
	// 不是扫描范围缩小或解析错误被吞。
	// 2026-10-17（审计轮补登）：df9a213de 把 admin/auto_route_outcome_freshness.go
	// 的 `FROM _request_logs_hot` **字面量**改成 `FROM ` + bg.ActiveSettleTurnsTable()
	// ⇒ 该拼接点从「不进本工具」变成「跨包调用不可静态解析」，落进 unresolved。
	// 三项 +1（总数 93→94 / unresolved 文件 10→11 / 处数 17→18），
	// v1、v1 臂、canonical 三桶**均不变**。
	// 方向 = **变大 = 把看不见变成看得见**（正是门注释允许的「改数字即可」那一类）；
	// 已逐读该文件 124 行确认不是扫描范围缩小或解析错误被吞。
	// ⚠ 同批发现 df9a213de 提交信息声称「已按仓库既有的间接读方登记表归族并
	// 同步计数」是**不实**的：它只登了 admin/request_logs_indirect_readers_test.go，
	// 漏了本清单。详见本文件 admin/auto_route_outcome_freshness.go 条目的注释。
	// 2026-10-19: staleness changed a literal FROM into a named view constant.
	// One additional unresolved site, not a newly introduced reader.
	//
	// ★ 2026-10-20（迁移 890）：上面对「逐项判方向」的整段说明就是本版的依据。
	//
	// ★ 2026-10-28（分支审计轮）：147 → 156（canonical 59 → 62 文件 / 125 → 130 处、
	//   unresolved 13 → 14 文件 / 22 → 26 处）。+9 处全部是「变大 = 把看不见变成看得见」
	//   或新增会话族读方：① origin a321cd963（R50 探针选型读点改走会话族窄投影）
	//   新增 admin/credential_monitor.go(1) / bg/shared_pick.go(1)（canonical）；
	//   ② origin e5651bd69（热路径 turn INSERT 幂等检查两臂直查）新增
	//   internal/sessionfamily/exists_by_key.go(1)（unresolved，src 是形参、两臂字面量在同函数内）；
	//   ③ origin R172 两臂窄投影新增 internal/sessionfamily/turns_reads.go(3)（canonical）；
	//   ④ wip/01~07 合入新增读点（recording_consent / session_trash / maas 账单族）。
	//   4 个文件已逐站点登记进 indirectSiteAssessments（含 Via 机制与退役后果）。
	// ★ 2026-10-21（R65 轮）：135 → 141（canonical 57 → 59 文件 / 116 → 122 处）。
	//   成因逐站点核过（见上面第 ⑤ 条）：+7 处来自三个新增的会话族读方文件，
	//   −1 处是一个拼接点被改写成纯字面量（离开了本工具的口径，目标仍是会话族）。
	//   ⇒ 方向=变大=「把看不见变成看得见」+ 一处「读法变了」；无桶变小 ⇒ 改数字即可。
	// ★ 2026-10-21（B4 轮）：141 → 142，unresolved 19 → 20 处。
	//   成因：`bg/candidate_failure_monitor.go` 的 aee31a117（S4 停写假阳性
	//   二分类）给 staleness 查询**加了第二个** `max(id)` 子查询，
	//   它与既有的 `max(ts)` 子查询读**同一张**会话族热腿
	//   （`checkStalenessLastRequestSurface`）⇒ 新增 1 处拼接点。
	//   ⇒ 方向 = **变大**（同一操作数、同一张表、同一个文件）
	//   ⇒ 不是「新增读方」，也不是「扫描范围变了」⇒ 改数字即可（无桶变小）。
	// ★ 2026-10-25（R152 轮）：142 → 147（canonical 122 → 125 处、
	//   unresolved 12 文件/20 处 → 13 文件/22 处；**文件数 canonical 仍 59**，
	//   v1 与 v1 臂两桶**逐字未动**）。
	//   ★ **本轮没有只改数字**：逐站点对拍过（把快照所在的 a2ac50e37 与今天
	//   各自的桶落盘后做逐文件逐类 diff），5 处净增的来源是三个文件：
	//   ① admin/compression_sessions.go 3 → 4 处、② admin/compression_stats.go 3 → 5 处
	//      —— R76/R77 两轮把 SQL 构造搬进 `compressionSessionsSQL()` 等函数后，
	//      **同一批会话族源**（public.session_turns / public.session_turn_details /
	//      public.session_bodies*）在**更多调用点**上被拼接 ⇒ 方向 = 变大
	//      =「把看不见变成看得见」，**不是新增读方**（与 B4 轮同族）；
	//   ③ db/request_logs_view_schema.go **新进 unresolved 桶（2 处）** ——
	//      R92/R93/R94 三轮把「会话列表 enrich」改写成会话族原生源后，
	//      该文件第一次进入扫描范围 ⇒ 本轮**补登记**了它的判定
	//      （唯一两个调用点的入参都是 session_turns_hot / session_turns）。
	//   ⚠ **方向判据逐条核过**：没有任何一个桶变小 ⇒ 不是「扫描范围被改小/
	//     解析失败被吞」；全部 5 处的 resolved 关系名都是会话族 ⇒ 无 v1 读方。

	// ★ 2026-10-28（r1040「事实源漂移」线）：147 → 156。
	//   **不是只改数字** —— 逐文件逐桶对拍过（两个时点各跑一次本工具）：
	//
	//   ① 方向判定（先问「桶有没有变小」）：
	//      v1 0→0、v1 臂 0→0（**两个 v1 桶逐字未动** ⇒ 没有新的 v1 读方）、
	//      canonical 59→62 文件 / 125→130 处、unresolved 13→14 文件 / 22→26 处
	//      ⇒ **没有任何一个桶变小** ⇒ 不是「扫描范围被改小 / 解析失败被吞」
	//      ⇒ 是「把看不见变成看得见 + 上游新增读点」⇒ 按本文件自己的规则可改数字。
	//
	//   ② 成因（逐个文件读过源码，并回溯到上游提交）：
	//      · admin/credential_monitor.go（+1，canonical）—— 5a58cde8b
	//        「凭据面板 total_requests 改走底表两臂窄投影」；
	//      · bg/shared_pick.go（+1，canonical）—— a321cd963
	//        「R50 探针选型读点改走会话族窄投影」；
	//      · internal/sessionfamily/turns_reads.go（+3，canonical）—— 465a2d7c4
	//        「R172 会话族轮次读点去视图化（两臂窄投影直查）」；
	//      · internal/sessionfamily/exists_by_key.go（+1，unresolved）—— e5651bd69
	//        「热路径 turn INSERT 的幂等检查改走两臂直查」；
	//      · db/request_logs_view_schema.go（unresolved 处数 +4）—— 同上那轮的
	//        写路径改写（该文件在 R152 已登记，此处只是处数变多）。
	//      ⇒ **五处新拼接点读的都是会话族**（session_turns_hot / session_turns）⇒ 无 v1 读方。
	//
	//   ③ ★★ 本轮附带查出的一条**更值得记的事**：
	//      文档里这组数字（147 / canonical 125 / unresolved 22）**既不等于**
	//      快照所在提交 a2ac50e37 的实测（142 / 122 / 20），**也不等于**今天的实测
	//      （156 / 130 / 26）⇒ ⇒ **它在一次合并之后就没有再被对拍过**，
	//      而「合并之后没人重测」这件事本身**没有任何东西会报警**。
	//      ⇒ 这正是「事实源漂移」家族的**最纯形态**：数字没有错，
	//      只是**不再描述任何一棵存在的树**。
	//      ⇒ 本轮把它改成**今天的实测值**，并把上面的成因写进注释 ——
	//      下一次漂移时，**下一个读这段注释的人能知道上一次是怎么判的**。
	// ★ 2026-10-30（24h 审计第 8 轮）：156 → 157（canonical 62 → 63 文件 /
	//   130 → 131 处；unresolved 14/26 逐字未动，两个 v1 桶仍为 0）。
	//   成因：ff029a7fc（输出合规审计轮）新增 admin/sanitize_rules_review_context.go
	//   的 1 处会话族窗口拼接（SessionFamilyTurnsSourceWindowedSQL，按审核单
	//   时刻推月份边界）⇒ 方向 = 变大 =「把看不见变成看得见」⇒ 已补登记
	//   indirectSiteAssessments 判定（Via 机制 + 退役后果）。
	TotalSites: 157,
	V1Files:    0,
	V1Sites:    0,
	// ★ v1 臂桶**归零**：不是「推导器坏了」，是**没有任何视图还有 v1 臂**。
	// 归零之后第四桶恒为 0，而 0 与「推导器坏了」在输出里长得一模一样 ⇒
	// 由 resolve_test.go 的 TestV1ArmViewsIncludeComposerDefinedOnes 承担
	// 「取反 + 正向对照」：那个测试现在断言 composer **不得**定义任何 v1 臂视图，
	// 并用两段合成夹具证明推导器**仍能**认出来（见该测试的注释）。
	//
	// ⚠★ 2026-10-21 订正：上面那句「composer 不得定义任何 v1 臂视图」的**范围**
	// 原来被写成了「全仓」⇒ 它要求 `sql/objects/views/` 的 5 个基线形态视图
	// 也不能有 v1 臂 ⇒ 而那 5 个视图**必须**有 v1 臂（基线逐语句应用 ⇒ 新装 42P01）
	// ⇒ 那道门**生来就红**，且按它去改源会**打挂全新安装路径**。
	// ⇒ 现在判据**分两个时点**：部署时点不得有 v1 臂（门 17），
	// 基线时点允许（那 5 个视图以 `dbinit:baseline-v1-fallback` 声明豁免，
	// 且豁免要过三道可证伪的腿：切换迁移存在 / 在启动链里 / 排在 v1 退役之前）。
	V1ArmFiles: 0,
	V1ArmSites: 0,
	CanonFiles: 63,
	CanonSites: 131,
	UnresFiles: 14,
	UnresSites: 26,
}

func TestDocumentedSnapshotMatchesMeasurement(t *testing.T) {
	root := repoRoot(t)
	v1, v1arm, canonical, unresolved, total := measureBuckets(t, root)
	got := bucketSnapshot{
		TotalSites: total,
		V1Files:    len(v1), V1Sites: sumMap(v1),
		V1ArmFiles: len(v1arm), V1ArmSites: sumMap(v1arm),
		CanonFiles: len(canonical), CanonSites: sumMap(canonical),
		UnresFiles: len(unresolved), UnresSites: sumMap(unresolved),
	}
	if got == documentedSnapshot {
		return
	}
	// 逐项报，而不是只报一个总数：总数不变而某一桶变过，是「文件在桶之间
	// 搬家」（例如某读方从 unresolved 变成可解析的 v1），那与「新增了读方」
	// 要做的事完全不同。
	var diff []string
	cmp := func(name string, want, have int) {
		if want != have {
			diff = append(diff, fmt.Sprintf("%s: 文档 %d → 实测 %d (%+d)",
				name, want, have, have-want))
		}
	}
	cmp("拼接点总数", documentedSnapshot.TotalSites, got.TotalSites)
	cmp("v1 文件数", documentedSnapshot.V1Files, got.V1Files)
	cmp("v1 处数", documentedSnapshot.V1Sites, got.V1Sites)
	cmp("v1 臂视图 文件数", documentedSnapshot.V1ArmFiles, got.V1ArmFiles)
	cmp("v1 臂视图 处数", documentedSnapshot.V1ArmSites, got.V1ArmSites)
	cmp("canonical 文件数", documentedSnapshot.CanonFiles, got.CanonFiles)
	cmp("canonical 处数", documentedSnapshot.CanonSites, got.CanonSites)
	cmp("unresolved 文件数", documentedSnapshot.UnresFiles, got.UnresFiles)
	cmp("unresolved 处数", documentedSnapshot.UnresSites, got.UnresSites)
	t.Errorf("本文件头部的三桶快照与实测不一致（%d 项漂移）：\n  %s\n"+
		"请先判断漂移的方向再改数字：\n"+
		"  · 读方从**字面量**改成拼接调用 ⇒ unresolved 变大，是「把看不见变成看得见」，改数字即可；\n"+
		"  · 某一桶**变小** ⇒ 先怀疑扫描范围或解析失败被吞（AuditRepo 里的 `continue`），\n"+
		"    那不是「变干净了」，是「又看不见了」；\n"+
		"  · 真的新增/删除读方 ⇒ 同步更新头部注释与下面的 documentedSnapshot 两处。",
		len(diff), strings.Join(diff, "\n  "))
}

// resolvedSitesFloor 是「可静态解析的站点数」的下限（ratchet）。
//
// 取 50 的理由：当前实测 122 处（v1 0 + v1 臂 0 + canonical 122），
// 门槛取**不到一半**，这样日常增删读方不会误报；但「解析器整体坏掉
// ⇒ 全部落进 unresolved（19 处）」这个形态一定会撞到它。
const resolvedSitesFloor = 50

func TestRepoAuditIsNotSilentlyVacuous(t *testing.T) {
	root := repoRoot(t)
	v1, v1arm, canonical, unresolved, total := measureBuckets(t, root)
	if total == 0 {
		t.Fatal("全仓审计报出 0 个拼接点 —— 这不是「仓库里没有间接读点」，" +
			"是审计坏了（WalkDir 范围 / 解析失败被 continue 吞掉 / 模式被改）。" +
			"在这种状态下上面那道门会因「清单有几十条而实测 0」报出一堆噪音，" +
			"把真正的病因埋在最后。")
	}

	// ② 解析率不能塌。
	//
	// ⚠ 这一条**代替**了退役前那两条「v1 桶非空」「v1 臂桶非空」——
	// 890 之后那两桶理应为空，而「空」与「分类器坏了」在输出里长得一样。
	// ⇒ 这里守住的是**另一个方向**的塌陷：解析器整体坏掉、所有站点都掉进
	// unresolved 桶（那样 canonical 桶会空，而清单门只会报「判定矛盾」）。
	if len(canonical) == 0 {
		t.Errorf("canonical 桶**一个文件都没有**（%d 处全部落在 v1/v1 臂/unresolved）——\n"+
			"要么仓库真的没有任何可静态解析的拼接点了（那要有人明确确认），\n"+
			"要么解析器坏了：isV1Relation / fromRelationRE 被改窄是最常见的形态。", total)
	}
	resolvedSites := sumMap(v1) + sumMap(v1arm) + sumMap(canonical)
	if resolvedSites < resolvedSitesFloor {
		t.Errorf("可静态解析的站点数 %d 处 < 门槛 %d（总拼接点 %d 处）——\n"+
			"解析率塌了：大量站点掉进了 unresolved 桶。\n"+
			"方向检查：unresolved %d 文件 / %d 处。\n"+
			"⚠ 门规：某一桶变大到「几乎全部」时，先怀疑**解析失败被吞**"+
			"（AuditRepo 里的 `continue`），那不是「变干净了」，是「又看不见了」。",
			resolvedSites, resolvedSitesFloor, total,
			len(unresolved), sumMap(unresolved))
	}

	// ③ 正向对照：分类器**仍认得** v1 关系。
	//
	// ★ 这是整道门存在的理由。迁移 890 之后 v1 桶非空**只**因为 maas 三个
	// 文件还没改；而「v1 桶空了」与「工具认不出 v1 了」必须长得不一样。
	// ⇒ 用**合成的**站点喂**同一个** Site.Classification()：
	//   · 基表名（带别名形态）必须落进 reads-v1；
	//   · v1 臂视图名（在 v1ArmViews 集合里）必须落进 v1-arm。
	// 这两条判据与仓库现状**无关** ⇒ 它们是这道门的「仪器自检」。
	if got := (Site{Resolved: []string{"_request_logs_hot AS r"}}).Classification(); got != ClassReadsV1 {
		t.Errorf("★ 正向对照失败：合成的 v1 基表站点（`_request_logs_hot AS r`）被判成 %v，"+
			"而不是 reads-v1。\n"+
			"⇒ **分类器认不出 v1 了**：全仓 v1 桶为空就**不能**再被解释成「v1 退役干净了」。\n"+
			"查 isV1Relation / v1Tables。", got)
	}
	if got := (Site{
		Resolved:   []string{"request_logs_with_current_month rl"},
		v1ArmViews: map[string]bool{"request_logs_with_current_month": true},
	}).Classification(); got != ClassReadsV1Arm {
		t.Errorf("★ 正向对照失败：合成的「v1 臂视图」站点被判成 %v，而不是 reads-v1-arm。\n"+
			"⇒ **分类器认不出 v1 臂视图了**：v1 臂桶为空就**不能**再被解释成「没有视图还有 v1 臂」。\n"+
			"查 ReadsV1ArmView / Site.v1ArmViews 的挂载。", got)
	}

	t.Logf("v1 %d 文件/%d 处，v1 臂 %d 文件/%d 处，canonical %d 文件/%d 处，unresolved %d 文件/%d 处",
		len(v1), sumMap(v1), len(v1arm), sumMap(v1arm),
		len(canonical), sumMap(canonical), len(unresolved), sumMap(unresolved))
	if len(v1) == 0 && len(v1arm) == 0 {
		t.Logf("★ v1 与 v1 臂两桶皆空 —— 这在 890 之后是**预期**状态（v1 族已整体退役）。" +
			"它的可信度由上面两条正向对照保证：分类器仍认得 v1 关系。")
	}
}
