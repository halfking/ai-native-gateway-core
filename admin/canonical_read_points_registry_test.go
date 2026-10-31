package admin

// canonical_read_points_registry_test.go —— canonical 视图
// `request_logs_with_current_month` 的**读点登记簿**与配套门。
//
// # 这张表在守什么
//
// canonical 视图（118 列 = 会话族 `session_turns_hot ∪ session_turns` 两臂体，
// 迁移 890 之后 v1 族已物理退役）是全仓**最宽**的读面：它把两臂底表投影成
// v1 形状的 115 列 + 3 个尾列。于是「读它」有两种截然不同的后果：
//
//   - 读**底表直映列**（request_id / ts / tenant_id / success / latency_ms …）
//     ⇒ 这条读点其实**不需要**视图，只需要一条窄投影
//     （底表两臂 + 少量列）⇒ 可以逐个改走窄投影；
//   - 读**视图特有语义**（734 details 腿的列 / 视图里的 CASE 表达式列 /
//     恒 NULL 的 v1 遗留列）⇒ 改走窄投影要么拿不到值、要么把视图的展开成本
//     原样搬到调用点 ⇒ 必须留在视图上。
//
// ⇒ 这张表的价值不在「数一数」，在**逐个读点地登记「形态 / 分类 / 豁免」**，
// 好让「要不要改走底表两臂窄投影」这件事有一个**逐条可议**的台账，而不是
// 一句「全仓 607 处引用」这种没法下手的话。
//
// # 为什么**不**自动分类（§9.259 的教训，本表继承）
//
// 本仓曾试图按「语句窗口内有无 `gw_session_id` / `request_id =`」自动分
// A/B/C/D，拿审计 §5.5.5 已人工核定的 14 个点交叉验证，**判错 5 个**
// （见 admin/request_logs_read_inventory_test.go 的同名注释）。
//
// ⇒ **分类由人核定，门只数个数。** 本表里 `Class` 字段的值是**人工回读源码
// 之后**写下的；门**不**校验它对不对（那是自动分类，会重犯那个错）。
// 门校验的是**事实**：读点**有没有**被登记、登记的**条数**对不对、登记的
// **投影列集合**对不对、**形态**对不对。
//
// # 与既有登记簿的分工（不要重复造表）
//
//	admin/request_logs_read_inventory_test.go
//	    requestLogsReadInventory      —— **已退役 v1 族**的读方（890 之后恒空）
//	    retiredV1ReferenceExceptions  —— 已退役关系的残留引用（生产缺陷台账）
//	    indirectRequestLogsReaders    —— v1 读的**间接**形态（切换层）
//
//	admin/request_logs_indirect_readers_test.go
//	    indirectRequestLogsReaders / allKnownRequestLogsReaderFiles
//
//	本文件
//	    canonicalReadPointRegistry    —— **canonical 视图**（活着的那张）的读点
//
// ⇒ 两套总体**不重叠**：v1 族已死，canonical 视图活着。
// ⇒ 本表**不**放宽、不替换、不改写上面任何一道既有门。
//
// # 口径
//
//	grep -rn "request_logs_with_current_month" --include=*.go .
//	  | grep -v "_test\.go"
//
//	⚠ **标识符边界**：`request_logs_with_current_month_without_customer_id` /
//	`request_logs_with_current_month_without_request_class_due_at` 是**已退役**
//	的 v1 包装视图（迁移 890 已 DROP）⇒ 它们**不是** canonical 视图的读点。
//	⇒ 本表按**标识符边界**判定（`sqlMentionsRelation` 同款），那两张视图在
//	本表的口径里**零命中**。
//
//	⚠ **只扫生产 .go 文件**：`_test.go` 里的同名引用是测试夹具，不是线上读点。
//	（本表不覆盖测试文件 —— 那是已知的口径边界，不是遗漏。）
//
// # 本文件新增的四道门
//
//	① TestCanonicalReadPointRegistryCoversEveryMentionedFile
//	   —— 「扫描到的每个文件都登记了」+「登记的每个文件都还在」+ 逐条自检。
//	② TestCanonicalReadPointRegistryIsNotRot
//	   —— 「登记簿不许腐烂」：读点条数与读点身份必须逐条对得上。
//	③ TestCanonicalReadPointRegistryEntriesAreWellFormed
//	   —— 「登记了但说不清」：形态/分类/豁免理由/说明必须在已声明集合里；
//	      并用**合成条目**证明自检逻辑本身有牙（表里有真实条目时，
//	      「自检有没有牙」没人能证明 —— 空表 + 循环 = 恒绿）。
//	④ TestCanonicalReadPointScannerHasDiscriminatingPower
//	   —— 扫描器的**阳性/阴性对照**：真 canonical 读点必须命中；
//	      已退役的 v1 包装视图 / 会话族底表 / SQL 注释里的视图名必须不命中。
//
// # 豁免口径（什么时候**不**改走窄投影）
//
//	exemptNoReadPoint         本文件没有读点（只在注释/字符串里提及）
//	exemptViewDefinitionFace  视图自身的定义/DDL/探测面（它是**生产者**）
//	exemptSwitchLayerContract 切换层出口（源由开关/条件决定，调用点改不动）
//	exemptAuditOrToolingFace  审计 / 工具 / 离线汇总面（不在线上读路径上）
//	exemptWideRowSemantics    读点语义上要宽行（导出/取证），窄投影会丢语义
//
// ⇒ **未被豁免**的读点 = 「改走底表两臂窄投影」的**候选**，逐个评估。
// ⇒ 豁免理由是**受门管的枚举**（不能随便写一句话糊过去），但**归类**是人判的。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// canonicalRequestLogsView 是本表登记的 canonical 视图名。
//
// ⚠ 它**必须**与 db.CanonicalSessionOnlyProjectionForTest() 描述的那张视图
// 是同一张。判据由 TestCanonicalReadPointScannerHasDiscriminatingPower 的
// 阳性对照钉住（真 canonical 读点必须命中 / 已退役包装视图必须不命中）。
const canonicalRequestLogsView = "request_logs_with_current_month"

// ─────────────────────────── 形态（Form） ───────────────────────────
//
// 形态是**机械可判**的：扫描器能从源码结构上区分「这条 SQL 直接读视图」与
// 「表名被收进 Go 标识符再拼进 SQL」。形态**不含价值判断** ⇒ 可以由门核验。

const (
	// formDirectLiteralSQL：SQL 字面量里在 FROM/JOIN 读位置出现视图名。
	formDirectLiteralSQL = "direct-literal-sql"

	// formHelperConcat：Go 字符串**持有**视图名（表名/别名片段），
	// 由 Go 代码拼进更大的 SQL。持有点所在函数**没有**条件分支。
	formHelperConcat = "helper-concat"

	// formSwitchLayer：**切换层**间接 —— Go 字符串持有视图名，且产出它的
	// 函数**按条件分支**（开关 / 天数 / 参数）决定读哪一族。
	formSwitchLayer = "switch-layer"

	// formCommentOrStringOnly：只在**注释或字符串**里提及（文档注释、Go 消息
	// 串、SQL 里的 `--` 注释）—— 本文件**没有**读点。
	formCommentOrStringOnly = "comment-or-string-only"

	// formViewDDLDefinition：canonical 视图**自身的 DDL 定义面**
	//（CREATE OR REPLACE VIEW / COMMENT ON VIEW）—— 它是视图的**生产者**，
	// 不是读方。
	formViewDDLDefinition = "view-ddl-definition"

	// formViewDefinitionProbe：**探测**视图定义的语境
	//（pg_get_viewdef / pg_views / to_regclass）—— 读的是视图的**元数据**，
	// 不是它的数据。
	formViewDefinitionProbe = "view-definition-probe"
)

// canonicalReadPointForms 是已声明的形态集合。
var canonicalReadPointForms = map[string]bool{
	formDirectLiteralSQL:    true,
	formHelperConcat:        true,
	formSwitchLayer:         true,
	formCommentOrStringOnly: true,
	formViewDDLDefinition:   true,
	formViewDefinitionProbe: true,
}

// ─────────────────────────── 分类（Class） ───────────────────────────
//
// ★ **分类是人工核定的**，门**不**校验它对不对（§9.259 的教训：本仓历史上的
// 自动分类器在已人工核定的 14 个点上判错 5 个）。
//
// 分类回答的问题是：**这个读点要不要改走「底表两臂窄投影」？**

const (
	// classNarrowMigratable：只读**底表直映列**（投影表达式就是 `t.<col>`）
	// ⇒ 底表两臂窄投影可等价供给 ⇒ **迁移候选**。
	classNarrowMigratable = "narrow-migratable"

	// classNeedsDetailsLeg：读了 **734 details 腿**的列（`d.<col>`）
	// ⇒ 无 details 的窄投影给不出这些值 ⇒ 迁移要么把 details LEFT JOIN
	// 一起搬走（等于把视图展开成本搬到调用点），要么留在视图上。
	classNeedsDetailsLeg = "needs-details-leg"

	// classReadsNullPaddedColumn：读了 canonical 视图里**恒 NULL** 的列
	// （v1 遗留列：`id` 等）⇒ **视图根本给不出真值** ⇒ 该读点读到的
	// 永远是 NULL ⇒ 这不是「迁移候选」，这是**缺陷**。
	classReadsNullPaddedColumn = "reads-null-padded-column"

	// classNeedsExpressionReplication：读了**视图里的表达式列**
	//（`request_status` / `gw_session_id` / `credential_id` / `api_key_id`
	// 的 CASE 表达式）⇒ 窄投影要自己复刻那份表达式 ⇒ 不是「直接搬列」。
	classNeedsExpressionReplication = "needs-expression-replication"

	// classSwitchArm：源由**切换层**决定（按开关/天数/参数分支）
	// ⇒ 归切换层裁决，调用点自己改不动。
	classSwitchArm = "switch-arm"

	// classViewDefinitionFace：视图的定义/探测面 ⇒ 不是读点。
	classViewDefinitionFace = "view-definition-face"

	// classNoRead：本文件没有读点（只在注释/字符串里提及）。
	classNoRead = "no-read"
)

// canonicalReadPointClasses 是已声明的分类集合。
var canonicalReadPointClasses = map[string]bool{
	classNarrowMigratable:           true,
	classNeedsDetailsLeg:            true,
	classReadsNullPaddedColumn:      true,
	classNeedsExpressionReplication: true,
	classSwitchArm:                  true,
	classViewDefinitionFace:         true,
	classNoRead:                     true,
}

// ─────────────────────── 豁免理由（ExemptReason） ───────────────────────
//
// 豁免 = **这个读点不改走窄投影**，且必须写明理由。
// 理由本身是**受门管的枚举**（不能随便写一句话糊过去），但**归类**是人工的。

const (
	// exemptNoReadPoint：本文件根本没有读点（只在注释/字符串里提及）。
	exemptNoReadPoint = "no-read-point"

	// exemptViewDefinitionFace：视图自身的定义/DDL/探测面 ⇒ 它是**生产者**。
	exemptViewDefinitionFace = "view-definition-face"

	// exemptSwitchLayerContract：切换层出口 —— 源由开关/条件决定，
	// 单独改这一臂会破坏切换层契约（两边必须同源）。
	exemptSwitchLayerContract = "switch-layer-contract"

	// exemptAuditOrToolingFace：审计/工具面 —— 不在线上读路径上，
	// 不参与窄投影迁移。
	exemptAuditOrToolingFace = "audit-or-tooling-face"

	// exemptBodyLegOnly：读点的**数据**来自别的表（正文腿 / 附件腿），
	// 视图只提供连接键 ⇒ 窄投影改变不了这个读点的成本形状。
	exemptBodyLegOnly = "body-leg-only"

	// exemptFrozenProjection：视图投影**冻结**（R37 定案：118 列同名同序不许改）
	// ⇒ 读点依赖的是「冻结投影」这一契约本身。
	exemptFrozenProjection = "frozen-view-projection"
)

// canonicalExemptReasons 是已声明的豁免理由集合。
var canonicalExemptReasons = map[string]bool{
	exemptNoReadPoint:         true,
	exemptViewDefinitionFace:  true,
	exemptSwitchLayerContract: true,
	exemptAuditOrToolingFace:  true,
	exemptBodyLegOnly:         true,
	exemptFrozenProjection:    true,
}

// ─────────────────────── 登记簿的数据结构 ───────────────────────

// canonicalReadPoint 是一个**读点**。
//
// ⚠ 一个读点 = **一条 SQL 字面量**（或**一个 Go 持有点**）。
// 同一条 SQL 里出现两次视图名（子查询 + 外层 FROM）**仍是一个读点**。
type canonicalReadPoint struct {
	// Kind 是 "read"（SQL 字面量读）或 "holder"（Go 字符串持有视图名）。
	Kind string

	// Columns 是该读点**实际引用到的 canonical 视图列**，每项写作
	// "<列名>:<投影来源>"，投影来源取自 db 的权威投影文本：
	//
	//	base   —— 底表直映（`t.<col>`）⇒ 窄投影可等价供给
	//	detail —— 734 details 腿（`d.<col>`）⇒ 窄投影须带 details
	//	NULL   —— 视图里恒 NULL 的 v1 遗留列 ⇒ 视图给不出真值
	//	expr   —— 视图里的表达式列（CASE / 函数）⇒ 须复刻表达式
	//
	// ⚠ 这一列是**机器可核的**（门会重算并逐条比对）：投影一变 ⇒ 登记即红。
	// 这是本表防腐烂的主要抓手。
	Columns []string

	// Holder 是 Kind=="holder" 时，持有视图名的那条 Go 字符串的**规范化文本**。
	Holder string

	// Class 是**人工核定**的分类（见 canonicalReadPointClasses）。
	Class string

	// Exempt 为 true 表示「这个读点不改走窄投影」。
	Exempt bool

	// ExemptReason 是豁免理由（Exempt 为 true 时**必填**，且必须取自
	// canonicalExemptReasons）。
	ExemptReason string

	// Note 是**人工核定说明**：为什么是这个分类 / 为什么豁免。
	// 必填非空 —— 空说明的登记等于没有登记。
	Note string
}

// canonicalReadPointFile 是一个生产文件在登记簿里的条目。
type canonicalReadPointFile struct {
	// Form 是该文件的形态（见 canonicalReadPointForms）。
	Form string

	// Class 是**文件级**分类（人工核定）：该文件整体属于哪一类读面。
	// 逐读点的分类在 Points 里。
	Class string

	// Exempt / ExemptReason 是**文件级**豁免。
	//
	// ⚠ 只在「该文件**没有**读点」时允许为 true（那种文件整体就是
	// 「看过并确认没有读点」）；**有**读点的文件，豁免必须逐读点地写在
	// Points 里 —— 否则「整个文件被豁免」会把文件里的每个读点一起藏掉。
	Exempt       bool
	ExemptReason string

	// Points 是该文件的读点清单（读点个数与扫描结果必须**逐条对上**）。
	Points []canonicalReadPoint

	// Note 是文件级的人工核定说明（必填非空）。
	Note string
}

// canonicalReadPointRegistry 是 canonical 视图读点的**登记簿**。
//
// ★ 键 = 仓库根相对路径（斜杠分隔），值 = 该文件的形态与读点清单。
// ★ **每个在生产代码里提及 canonical 视图的文件都必须在这里**（含只在注释里
//
//	提及的 —— 那也是一种「看过并确认没有读点」的登记）。
//
// ⚠ 本表**不**按「每行 SQL」登记：一行可能只是拼接的碎片，一段可能是一整条
// 读点。登记的粒度是**读点**（一条 SQL 字面量 / 一个 Go 持有点）。
var canonicalReadPointRegistry = map[string]canonicalReadPointFile{
	"admin/attachments_routes.go": {
		Form:  formDirectLiteralSQL,
		Class: classNarrowMigratable,
		Note:  "附件路由的存在性检查（按 request_id + tenant_id 判附件是否已挂）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"request_id:base", "tenant_id:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "附件路由的存在性检查（按 request_id + tenant_id 判附件是否已挂）；本读点读 request_id:base,tenant_id:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
			{Kind: "read", Columns: []string{"request_id:base", "tenant_id:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "附件路由的存在性检查（按 request_id + tenant_id 判附件是否已挂）；本读点读 request_id:base,tenant_id:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/auto_title_generator.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "标题生成器：① 会话内存在性检查；② 取最近一轮的状态/错误/模型",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"gw_session_id:expr", "is_auto_request:base", "request_id:base", "success:base", "tenant_id:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "标题生成器：① 会话内存在性检查；② 取最近一轮的状态/错误/模型；本读点读 gw_session_id:expr,is_auto_request:base,request_id:base,success:base,tenant_id:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
			{Kind: "read", Columns: []string{"client_model:detail", "error_kind:base", "gw_session_id:expr", "request_id:base", "request_preview:base", "response_preview:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "标题生成器：① 会话内存在性检查；② 取最近一轮的状态/错误/模型；本读点读 client_model:detail,error_kind:base,gw_session_id:expr,request_id:base,request_preview:base,response_preview:base,tenant_id:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/compression_stats.go": {
		Form:  formDirectLiteralSQL,
		Class: classNarrowMigratable,
		Note:  "压缩带统计（token_band / success / ts）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"success:base", "token_band:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "压缩带统计（token_band / success / ts）；本读点读 success:base,token_band:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/credential_monitor.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "凭据监控：① live P95 回退；② 按凭据统计行数；③ 滑窗明细",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "latency_ms:base", "outbound_model:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "凭据监控：① live P95 回退；② 按凭据统计行数；③ 滑窗明细；本读点读 client_model:detail,credential_id:expr,latency_ms:base,outbound_model:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"credential_id:expr"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "凭据监控：① live P95 回退；② 按凭据统计行数；③ 滑窗明细；本读点读 credential_id:expr ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "error_kind:base", "latency_ms:base", "outbound_model:base", "success:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "凭据监控：① live P95 回退；② 按凭据统计行数；③ 滑窗明细；本读点读 client_model:detail,credential_id:expr,error_kind:base,latency_ms:base,outbound_model:base,success:base,tenant_id:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/credential_monitor_heatmap.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "凭据 × 模型热力图",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "error_kind:base", "latency_ms:base", "outbound_model:base", "request_id:base", "success:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "凭据 × 模型热力图；本读点读 client_model:detail,credential_id:expr,error_kind:base,latency_ms:base,outbound_model:base,request_id:base,success:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/credential_success_rate.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "凭据成功率",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "outbound_model:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "凭据成功率；本读点读 client_model:detail,credential_id:expr,outbound_model:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/dashboard_board.go": {
		Form:  formSwitchLayer,
		Class: classSwitchArm,
		Note:  "boardSource() 按 fromMinute 在 request_stats_minute 与 canonical 视图之间二选一 ⇒ 源由切换层决定",
		Points: []canonicalReadPoint{
			{Kind: "holder", Columns: nil, Holder: "request_logs_with_current_month", Class: classSwitchArm, Exempt: true, ExemptReason: exemptSwitchLayerContract, Note: "boardSource() 按 fromMinute 在 request_stats_minute 与 canonical 视图之间二选一 ⇒ 源由切换层决定；本读点读 request_logs_with_current_month ⇒ 源由切换层决定 ⇒ 调用点改不动"},
		},
	},
	"admin/dashboard_board_queries.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "看板查询只在注释里讨论 canonical 视图的耗时；本文件的实际读面走 request_stats 汇总表",
	},
	"admin/data_lifecycle.go": {
		Form:  formDirectLiteralSQL,
		Class: classNarrowMigratable,
		Note:  "数据生命周期：① 7 天趋势；② 清理预览的受影响行数（不取列，只要 COUNT(*)）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"request_id:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "数据生命周期：① 7 天趋势；② 清理预览的受影响行数（不取列，只要 COUNT(*)）；本读点读 request_id:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
			{Kind: "read", Columns: []string{}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "数据生命周期：① 7 天趋势；② 清理预览的受影响行数（不取列，只要 COUNT(*)）；本读点读 （不取列，只要行数） ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/diagnostics_credential.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "凭据诊断明细",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "error_kind:base", "provider_id:detail", "request_id:base", "request_status:expr", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "凭据诊断明细；本读点读 client_model:detail,credential_id:expr,error_kind:base,provider_id:detail,request_id:base,request_status:expr,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/live_stream_sse.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "SSE 实时流：① 全量事件行；② 按 request_id 数组取状态",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"cache_read_tokens:base", "cache_write_tokens:base", "canonical_id:base", "client_model:detail", "completion_tokens:base", "cost_usd:base", "credential_id:expr", "error_kind:base", "gw_session_id:expr", "latency_ms:base", "outbound_model:base", "prompt_tokens:base", "provider_id:detail", "request_id:base", "request_status:expr", "success:base", "tenant_id:base", "total_tokens:expr", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "SSE 实时流：① 全量事件行；② 按 request_id 数组取状态；本读点读 cache_read_tokens:base,cache_write_tokens:base,canonical_id:base,client_model:detail,completion_tokens:base,cost_usd:base,credential_id:expr,error_kind:base,gw_session_id:expr,latency_ms:base,outbound_model:base,prompt_tokens:base,provider_id:detail,request_id:base,request_status:expr,success:base,tenant_id:base,total_tokens:expr,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"request_id:base", "request_status:expr"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "SSE 实时流：① 全量事件行；② 按 request_id 数组取状态；本读点读 request_id:base,request_status:expr ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
		},
	},
	"admin/logs.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "日志列表/详情 API 的主入口；读面经切换层 logsSourceFromSQL()（默认 canonical 视图）⇒ 本文件无直接读点，视图读点登记在切换层那条",
	},
	"admin/logs_search_predicate.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "只在注释里说明视图第三条 UNION 臂的历史；本文件只贡献搜索谓词",
	},
	"admin/logs_summary.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "会话日志摘要（正文来自 SessionBodiesSourceSQL 的 LEFT JOIN，视图腿只供 metadata）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "error_kind:base", "request_id:base", "request_preview:base", "response_preview:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "会话日志摘要（正文来自 SessionBodiesSourceSQL 的 LEFT JOIN，视图腿只供 metadata）；本读点读 client_model:detail,error_kind:base,request_id:base,request_preview:base,response_preview:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"client_model:detail", "error_kind:base", "gw_session_id:expr", "request_id:base", "request_preview:base", "response_preview:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "会话日志摘要（正文来自 SessionBodiesSourceSQL 的 LEFT JOIN，视图腿只供 metadata）；本读点读 client_model:detail,error_kind:base,gw_session_id:expr,request_id:base,request_preview:base,response_preview:base,tenant_id:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/logs_turns_source.go": {
		Form:  formSwitchLayer,
		Class: classSwitchArm,
		Note:  "logsSourceFromSQL() 是日志读端的**灰度切换层**（默认 canonical 视图；开关打开走 db.SessionFamilyTurnsSourceSQL() 原生源）⇒ 改臂必须连开关一起改",
		Points: []canonicalReadPoint{
			{Kind: "holder", Columns: nil, Holder: "request_logs_with_current_month rl", Class: classSwitchArm, Exempt: true, ExemptReason: exemptSwitchLayerContract, Note: "logsSourceFromSQL() 是日志读端的**灰度切换层**（默认 canonical 视图；开关打开走 db.SessionFamilyTurnsSourceSQL() 原生源）⇒ 改臂必须连开关一起改；本读点读 request_logs_with_current_month rl ⇒ 源由切换层决定 ⇒ 调用点改不动"},
		},
	},
	"admin/memora_handlers.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "memora 上下文：① 任务/会话头；② 最近一条模型；③ 计数；④ 轮次明细",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"gw_session_id:expr", "gw_task_id:detail"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "memora 上下文：① 任务/会话头；② 最近一条模型；③ 计数；④ 轮次明细；本读点读 gw_session_id:expr,gw_task_id:detail ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"client_model:detail", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "memora 上下文：① 任务/会话头；② 最近一条模型；③ 计数；④ 轮次明细；本读点读 client_model:detail,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"client_model:detail", "completion_tokens:base", "cost_usd:base", "error_kind:base", "gw_session_id:expr", "latency_ms:base", "outbound_model:base", "prompt_tokens:base", "request_id:base", "request_mode:base", "request_preview:base", "request_status:expr", "response_preview:base", "ts:base", "work_type:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "memora 上下文：① 任务/会话头；② 最近一条模型；③ 计数；④ 轮次明细；本读点读 client_model:detail,completion_tokens:base,cost_usd:base,error_kind:base,gw_session_id:expr,latency_ms:base,outbound_model:base,prompt_tokens:base,request_id:base,request_mode:base,request_preview:base,request_status:expr,response_preview:base,ts:base,work_type:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/model_routing_diagnostic.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "模型路由诊断的 24h 统计",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"canonical_model:base", "client_model:detail", "error_kind:base", "latency_ms:base", "outbound_model:base", "stream_first_chunk_ms:base", "success:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "模型路由诊断的 24h 统计；本读点读 canonical_model:base,client_model:detail,error_kind:base,latency_ms:base,outbound_model:base,stream_first_chunk_ms:base,success:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/model_status.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "模型状态统计",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "latency_ms:base", "success:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "模型状态统计；本读点读 client_model:detail,latency_ms:base,success:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"client_model:detail", "success:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "模型状态统计；本读点读 client_model:detail,success:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/no_topic_session.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "无主题会话面：① 轮次明细；② 会话日志摘要；③ 按 request_id 取 api_key_id/tenant_id；④ 取 preview/work_type/request_mode",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "completion_tokens:base", "cost_usd:base", "error_kind:base", "gw_session_id:expr", "latency_ms:base", "outbound_model:base", "prompt_tokens:base", "request_id:base", "request_mode:base", "request_preview:base", "request_status:expr", "response_preview:base", "ts:base", "work_type:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "无主题会话面：① 轮次明细；② 会话日志摘要；③ 按 request_id 取 api_key_id/tenant_id；④ 取 preview/work_type/request_mode；本读点读 client_model:detail,completion_tokens:base,cost_usd:base,error_kind:base,gw_session_id:expr,latency_ms:base,outbound_model:base,prompt_tokens:base,request_id:base,request_mode:base,request_preview:base,request_status:expr,response_preview:base,ts:base,work_type:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"client_model:detail", "error_kind:base", "request_id:base", "request_preview:base", "response_preview:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "无主题会话面：① 轮次明细；② 会话日志摘要；③ 按 request_id 取 api_key_id/tenant_id；④ 取 preview/work_type/request_mode；本读点读 client_model:detail,error_kind:base,request_id:base,request_preview:base,response_preview:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"api_key_id:expr", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "无主题会话面：① 轮次明细；② 会话日志摘要；③ 按 request_id 取 api_key_id/tenant_id；④ 取 preview/work_type/request_mode；本读点读 api_key_id:expr,tenant_id:base,ts:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
			{Kind: "read", Columns: []string{"request_mode:base", "request_preview:base", "response_preview:base", "ts:base", "work_type:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "无主题会话面：① 轮次明细；② 会话日志摘要；③ 按 request_id 取 api_key_id/tenant_id；④ 取 preview/work_type/request_mode；本读点读 request_mode:base,request_preview:base,response_preview:base,ts:base,work_type:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/probe_history.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsExpressionReplication,
		Note:  "探针历史",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"failure_detail_code:base", "outbound_model:base", "request_status:expr", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "探针历史；本读点读 failure_detail_code:base,outbound_model:base,request_status:expr,ts:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
		},
	},
	"admin/provider_models.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "供应商模型面：① COUNT(*) 统计（不取列）；② 明细",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "供应商模型面：① COUNT(*) 统计（不取列）；② 明细；本读点读 （不取列，只要行数） ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
			{Kind: "read", Columns: []string{"client_model:detail", "completion_tokens:base", "cost_usd:base", "credential_id:expr", "error_kind:base", "latency_ms:base", "outbound_model:base", "prompt_tokens:base", "request_id:base", "success:base", "total_tokens:expr", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "供应商模型面：① COUNT(*) 统计（不取列）；② 明细；本读点读 client_model:detail,completion_tokens:base,cost_usd:base,credential_id:expr,error_kind:base,latency_ms:base,outbound_model:base,prompt_tokens:base,request_id:base,success:base,total_tokens:expr,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	// R172：会话族轮次读点的两臂直查 helper（internal/sessionfamily/turns_reads.go）。
	// ★ 本文件**只在一条注释里**提到 canonical 视图（用来说明「canonical 视图没有
	//   反连接」这个对比）⇒ 不是 canonical 读点 ⇒ 归 classNoRead。
	// ★ 它的真实读面是**会话族**（public.session_turns_hot ∪ public.session_turns），
	//   与 canonical 视图无关；R172 的等价性守卫在
	//   domains/session/v2/turn_writer_family_reads_test.go。
	"internal/sessionfamily/turns_reads.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "R172 会话族两臂直查 helper：canonical 视图名只出现在一条注释里（对比说明「canonical 视图没有反连接」）；本文件的读面是会话族 hot ∪ 父表，与 canonical 视图无关",
	},
	"admin/providers.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "视图名只出现在一条 SQL 字面量的 `--` 注释里（解释四路 UNION ALL）⇒ 不是读点",
	},
	"admin/request_trace.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "请求追踪：① 追踪明细；② 按 request_id 取 task_type_chosen",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"auto_decision:expr", "client_model:detail", "credential_id:expr", "error_kind:base", "failure_stage:base", "latency_ms:base", "outbound_model:base", "provider_id:detail", "request_id:base", "success:base", "task_type:base", "task_type_chosen:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "请求追踪：① 追踪明细；② 按 request_id 取 task_type_chosen；本读点读 auto_decision:expr,client_model:detail,credential_id:expr,error_kind:base,failure_stage:base,latency_ms:base,outbound_model:base,provider_id:detail,request_id:base,success:base,task_type:base,task_type_chosen:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"request_id:base", "task_type_chosen:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "请求追踪：① 追踪明细；② 按 request_id 取 task_type_chosen；本读点读 request_id:base,task_type_chosen:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/route_incidents.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "路由事故面",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "error_kind:base", "failure_stage:base", "outbound_model:base", "provider_id:detail", "request_id:base", "request_status:expr", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "路由事故面；本读点读 client_model:detail,credential_id:expr,error_kind:base,failure_stage:base,outbound_model:base,provider_id:detail,request_id:base,request_status:expr,tenant_id:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/session_analytics_breakdown.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsExpressionReplication,
		Note:  "会话分析维度下钻：① 按模型；② 按供应商",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"completion_tokens:base", "cost_usd:base", "gw_session_id:expr", "latency_ms:base", "outbound_model:base", "prompt_tokens:base", "request_status:expr"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "会话分析维度下钻：① 按模型；② 按供应商；本读点读 completion_tokens:base,cost_usd:base,gw_session_id:expr,latency_ms:base,outbound_model:base,prompt_tokens:base,request_status:expr ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
			{Kind: "read", Columns: []string{"completion_tokens:base", "cost_usd:base", "gw_session_id:expr", "latency_ms:base", "prompt_tokens:base", "provider_id:detail", "request_status:expr"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "会话分析维度下钻：① 按模型；② 按供应商；本读点读 completion_tokens:base,cost_usd:base,gw_session_id:expr,latency_ms:base,prompt_tokens:base,provider_id:detail,request_status:expr ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/session_analytics_timeseries.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsExpressionReplication,
		Note:  "会话分析时间序列：① 会话/请求/成本；② 成本拆分；③ 延迟分位",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"completion_tokens:base", "cost_usd:base", "end_user_id:base", "gw_session_id:expr", "prompt_tokens:base", "request_status:expr", "success:base", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "会话分析时间序列：① 会话/请求/成本；② 成本拆分；③ 延迟分位；本读点读 completion_tokens:base,cost_usd:base,end_user_id:base,gw_session_id:expr,prompt_tokens:base,request_status:expr,success:base,ts:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
			{Kind: "read", Columns: []string{"cache_read_tokens:base", "cache_write_tokens:base", "cost_usd:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "会话分析时间序列：① 会话/请求/成本；② 成本拆分；③ 延迟分位；本读点读 cache_read_tokens:base,cache_write_tokens:base,cost_usd:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
			{Kind: "read", Columns: []string{"latency_ms:base", "stream_first_chunk_ms:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "会话分析时间序列：① 会话/请求/成本；② 成本拆分；③ 延迟分位；本读点读 latency_ms:base,stream_first_chunk_ms:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/session_bodies_batch.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "只在注释里说明视图在 890 之后是两臂体；本文件的正文腿走会话族原生源",
	},
	"admin/session_export.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "只在注释里提到视图的 role 列；导出读面走会话族源",
	},
	"admin/session_extract.go": {
		Form:  formDirectLiteralSQL,
		Class: classNarrowMigratable,
		Note:  "会话抽取：① 取 api_key_id；② 取 tenant_id；③ 取 preview/work_type/request_mode",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"api_key_id:expr", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "会话抽取：① 取 api_key_id；② 取 tenant_id；③ 取 preview/work_type/request_mode；本读点读 api_key_id:expr,ts:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
			{Kind: "read", Columns: []string{"tenant_id:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "会话抽取：① 取 api_key_id；② 取 tenant_id；③ 取 preview/work_type/request_mode；本读点读 tenant_id:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
			{Kind: "read", Columns: []string{"request_mode:base", "request_preview:base", "response_preview:base", "ts:base", "work_type:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "会话抽取：① 取 api_key_id；② 取 tenant_id；③ 取 preview/work_type/request_mode；本读点读 request_mode:base,request_preview:base,response_preview:base,ts:base,work_type:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/session_list.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "只在注释里说明列表读面已迁到 session 族原生源",
	},
	"admin/session_management_api.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "会话管理列表",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "cost_usd:base", "gw_session_id:expr", "latency_ms:base", "request_id:base", "request_preview:base", "request_status:expr", "success:base", "total_tokens:expr", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "会话管理列表；本读点读 client_model:detail,cost_usd:base,gw_session_id:expr,latency_ms:base,request_id:base,request_preview:base,request_status:expr,success:base,total_tokens:expr,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/session_online.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "只在注释里描述旧链与扩链历史；在线读面走会话族源",
	},
	"admin/session_sanitize_matches.go": {
		Form:  formDirectLiteralSQL,
		Class: classNarrowMigratable,
		Note:  "正文片段读取：视图腿只提供 request_id 作连接键，**正文来自 SessionFamilyBodiesSourceSQL 的 LEFT JOIN**",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"request_id:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "正文片段读取：视图腿只提供 request_id 作连接键，**正文来自 SessionFamilyBodiesSourceSQL 的 LEFT JOIN**；本读点读 request_id:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/session_summary_v2.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsExpressionReplication,
		Note:  "会话摘要头",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"gw_session_id:expr", "request_id:base", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "会话摘要头；本读点读 gw_session_id:expr,request_id:base,ts:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
		},
	},
	"admin/session_timeline_query.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "会话时间线（宽列 + client_model 走 details 腿）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"cache_read_tokens:base", "client_model:detail", "completion_tokens:base", "compression_strategy:base", "cost_usd:base", "error_kind:base", "gw_session_id:expr", "latency_ms:base", "outbound_model:base", "prompt_tokens:base", "request_id:base", "request_preview:base", "response_preview:base", "success:base", "ts:base", "work_type:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "会话时间线（宽列 + client_model 走 details 腿）；本读点读 cache_read_tokens:base,client_model:detail,completion_tokens:base,compression_strategy:base,cost_usd:base,error_kind:base,gw_session_id:expr,latency_ms:base,outbound_model:base,prompt_tokens:base,request_id:base,request_preview:base,response_preview:base,success:base,ts:base,work_type:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/session_title.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "任务标题（LEFT JOIN 正文腿；视图腿供 metadata）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "error_kind:base", "request_id:base", "request_preview:base", "response_preview:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "任务标题（LEFT JOIN 正文腿；视图腿供 metadata）；本读点读 client_model:detail,error_kind:base,request_id:base,request_preview:base,response_preview:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/session_turns_tree.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "轮次树",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"latency_ms:base", "parent_request_id:base", "request_id:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "轮次树；本读点读 latency_ms:base,parent_request_id:base,request_id:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/session_turns_unified.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "统一轮次树",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"tenant_id:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "统一轮次树；本读点读 tenant_id:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/swim_lane_init.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "泳道初始化",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"canonical_id:base", "client_model:detail", "completion_tokens:base", "cost_usd:base", "credential_id:expr", "error_kind:base", "latency_ms:base", "outbound_model:base", "prompt_tokens:base", "request_id:base", "request_status:expr", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "泳道初始化；本读点读 canonical_id:base,client_model:detail,completion_tokens:base,cost_usd:base,credential_id:expr,error_kind:base,latency_ms:base,outbound_model:base,prompt_tokens:base,request_id:base,request_status:expr,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/tenants.go": {
		Form:  formDirectLiteralSQL,
		Class: classNarrowMigratable,
		Note:  "租户统计（tenant_id / tokens / cost / credits_charged）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"completion_tokens:base", "cost_usd:base", "credits_charged:base", "prompt_tokens:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "租户统计（tenant_id / tokens / cost / credits_charged）；本读点读 completion_tokens:base,cost_usd:base,credits_charged:base,prompt_tokens:base,tenant_id:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
			{Kind: "read", Columns: []string{"completion_tokens:base", "cost_usd:base", "credits_charged:base", "prompt_tokens:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "租户统计（tenant_id / tokens / cost / credits_charged）；本读点读 completion_tokens:base,cost_usd:base,credits_charged:base,prompt_tokens:base,tenant_id:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"admin/top_problems.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsExpressionReplication,
		Note:  "Top 问题：① 按凭据；② 按模型",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"credential_id:expr", "error_kind:base", "failure_detail_code:base", "success:base", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "Top 问题：① 按凭据；② 按模型；本读点读 credential_id:expr,error_kind:base,failure_detail_code:base,success:base,ts:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
			{Kind: "read", Columns: []string{"client_model:detail", "error_kind:base", "failure_detail_code:base", "success:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "Top 问题：① 按凭据；② 按模型；本读点读 client_model:detail,error_kind:base,failure_detail_code:base,success:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/topology_clients_today.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "头部统计客户端组（单一统计轮）：request_logs 直查按 clientKeyOf 同款表达式聚合",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"agent_name:base", "cache_read_tokens:base", "cache_write_tokens:base", "client_profile:detail", "client_protocol:base", "completion_tokens:base", "cost_usd:base", "credits_charged:base", "identity_hash:base", "prompt_tokens:base", "request_status:expr", "success:base", "tenant_id:base", "total_tokens:expr", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "头部统计客户端组（单一统计轮）；本读点读 agent_name:base,cache_read_tokens:base,cache_write_tokens:base,client_profile:detail,client_protocol:base,completion_tokens:base,cost_usd:base,credits_charged:base,identity_hash:base,prompt_tokens:base,request_status:expr,success:base,tenant_id:base,total_tokens:expr,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/topology_window_stats.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "头部统计短窗口口径：① 供应商聚合（provider_id 走 details 腿 + credential_id/request_status 表达式列）；② 模型聚合（client_model 走 details 腿 + canonical_id 底表直映）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"cache_read_tokens:base", "cache_write_tokens:base", "completion_tokens:base", "cost_usd:base", "credential_id:expr", "credits_charged:base", "prompt_tokens:base", "provider_id:detail", "request_status:expr", "success:base", "tenant_id:base", "total_tokens:expr", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "头部统计短窗口口径：① 供应商聚合（provider_id 走 details 腿 + credential_id/request_status 表达式列）；本读点读 cache_read_tokens:base,cache_write_tokens:base,completion_tokens:base,cost_usd:base,credential_id:expr,credits_charged:base,prompt_tokens:base,provider_id:detail,request_status:expr,success:base,tenant_id:base,total_tokens:expr,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"cache_read_tokens:base", "cache_write_tokens:base", "canonical_id:base", "client_model:detail", "completion_tokens:base", "cost_usd:base", "credits_charged:base", "prompt_tokens:base", "request_status:expr", "success:base", "tenant_id:base", "total_tokens:expr", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "头部统计短窗口口径：② 模型聚合（client_model 走 details 腿 + canonical_id 底表直映）；本读点读 cache_read_tokens:base,cache_write_tokens:base,canonical_id:base,client_model:detail,completion_tokens:base,cost_usd:base,credits_charged:base,prompt_tokens:base,request_status:expr,success:base,tenant_id:base,total_tokens:expr,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"admin/usage.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "用量面：① 失败数；② 失败钻取；③ 5 分钟分桶",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"api_key_id:expr", "failure_detail_code:base", "failure_stage:base", "provider_id:detail", "request_status:expr", "success:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "用量面：① 失败数；② 失败钻取；③ 5 分钟分桶；本读点读 api_key_id:expr,failure_detail_code:base,failure_stage:base,provider_id:detail,request_status:expr,success:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"api_key_id:expr", "failure_detail_code:base", "failure_stage:base", "request_status:expr", "success:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "用量面：① 失败数；② 失败钻取；③ 5 分钟分桶；本读点读 api_key_id:expr,failure_detail_code:base,failure_stage:base,request_status:expr,success:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
		},
	},
	"admin/usage_credits.go": {
		Form:  formSwitchLayer,
		Class: classSwitchArm,
		Note:  "requestLogsFromClause(days) 按天数分臂：≤7 天走会话族热腿原生源，>7 天走 canonical 视图 ⇒ 切换层契约",
		Points: []canonicalReadPoint{
			{Kind: "holder", Columns: nil, Holder: "request_logs_with_current_month AS r", Class: classSwitchArm, Exempt: true, ExemptReason: exemptSwitchLayerContract, Note: "requestLogsFromClause(days) 按天数分臂：≤7 天走会话族热腿原生源，>7 天走 canonical 视图 ⇒ 切换层契约；本读点读 request_logs_with_current_month AS r ⇒ 源由切换层决定 ⇒ 调用点改不动"},
		},
	},
	"admin/usage_enhanced.go": {
		Form:  formSwitchLayer,
		Class: classSwitchArm,
		Note:  "planCostTrend() 按 groupBy 在计费宽表与请求表（canonical 视图）之间选基表 ⇒ 切换层契约",
		Points: []canonicalReadPoint{
			{Kind: "holder", Columns: nil, Holder: "request_logs_with_current_month rl", Class: classSwitchArm, Exempt: true, ExemptReason: exemptSwitchLayerContract, Note: "planCostTrend() 按 groupBy 在计费宽表与请求表（canonical 视图）之间选基表 ⇒ 切换层契约；本读点读 request_logs_with_current_month rl ⇒ 源由切换层决定 ⇒ 调用点改不动"},
		},
	},
	"admin/usage_trend_series.go": {
		Form:  formSwitchLayer,
		Class: classSwitchArm,
		Note:  "usageTrendSource() 三臂切换（api_key 过滤 ⇒ canonical 视图明细面；provider 过滤 ⇒ minute 汇总；默认 ⇒ dim 汇总）；另两处是按该返回值 dispatch 的 case 标签 ⇒ 同一处切换层",
		Points: []canonicalReadPoint{
			{Kind: "holder", Columns: nil, Holder: "request_logs_with_current_month", Class: classSwitchArm, Exempt: true, ExemptReason: exemptSwitchLayerContract, Note: "usageTrendSource() 三臂切换（api_key 过滤 ⇒ canonical 视图明细面；provider 过滤 ⇒ minute 汇总；默认 ⇒ dim 汇总）；另两处是按该返回值 dispatch 的 case 标签 ⇒ 同一处切换层；本读点读 request_logs_with_current_month ⇒ 源由切换层决定 ⇒ 调用点改不动"},
			{Kind: "holder", Columns: nil, Holder: "request_logs_with_current_month", Class: classSwitchArm, Exempt: true, ExemptReason: exemptSwitchLayerContract, Note: "usageTrendSource() 三臂切换（api_key 过滤 ⇒ canonical 视图明细面；provider 过滤 ⇒ minute 汇总；默认 ⇒ dim 汇总）；另两处是按该返回值 dispatch 的 case 标签 ⇒ 同一处切换层；本读点读 request_logs_with_current_month ⇒ 源由切换层决定 ⇒ 调用点改不动"},
			{Kind: "holder", Columns: nil, Holder: "request_logs_with_current_month", Class: classSwitchArm, Exempt: true, ExemptReason: exemptSwitchLayerContract, Note: "usageTrendSource() 三臂切换（api_key 过滤 ⇒ canonical 视图明细面；provider 过滤 ⇒ minute 汇总；默认 ⇒ dim 汇总）；另两处是按该返回值 dispatch 的 case 标签 ⇒ 同一处切换层；本读点读 request_logs_with_current_month ⇒ 源由切换层决定 ⇒ 调用点改不动"},
		},
	},
	"admin/user_usage_stats.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "只在注释里记录「放弃 canonical 视图、全 owner GROUP BY」的决策",
	},
	"admin/view_source_columns_contract.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "视图列契约的文档面；只解释契约，不读数据",
	},
	"admin/work_types.go": {
		Form:  formDirectLiteralSQL,
		Class: classNarrowMigratable,
		Note:  "工作类型分布（work_type / is_auto_request / ts / canonical_model）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"is_auto_request:base", "ts:base", "work_type:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "工作类型分布（work_type / is_auto_request / ts / canonical_model）；本读点读 is_auto_request:base,ts:base,work_type:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
			{Kind: "read", Columns: []string{"is_auto_request:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "工作类型分布（work_type / is_auto_request / ts / canonical_model）；本读点读 is_auto_request:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
			{Kind: "read", Columns: []string{"is_auto_request:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "工作类型分布（work_type / is_auto_request / ts / canonical_model）；本读点读 is_auto_request:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
			{Kind: "read", Columns: []string{"canonical_model:base", "client_model:detail", "is_auto_request:base", "outbound_model:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "工作类型分布（work_type / is_auto_request / ts / canonical_model）；本读点读 canonical_model:base,client_model:detail,is_auto_request:base,outbound_model:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"bg/auto_route_settle_worker.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "settle worker 只在注释里写明「不能改成读 710 视图」这条护栏",
	},
	"bg/candidate_failure_monitor.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "只在注释里记录 lastRequest 改读会话族的历史",
	},
	"bg/daily_probe_audit.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsExpressionReplication,
		Note:  "探针每日审计（离线诊断面，不在线上读路径上）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "outbound_model:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "探针每日审计（离线诊断面，不在线上读路径上）；本读点读 client_model:detail,credential_id:expr,outbound_model:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"bg/integrity_fingerprint_drift.go": {
		Form:  formDirectLiteralSQL,
		Class: classNarrowMigratable,
		Note:  "指纹漂移审计（离线诊断面）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"is_auto_request:base", "task_type:base"}, Holder: "", Class: classNarrowMigratable, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "指纹漂移审计（离线诊断面）；本读点读 is_auto_request:base,task_type:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"bg/passive_probe_listener.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "被动探针监听器（探针系统后台面，不在管理台读路径上）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "outbound_model:base", "success:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "被动探针监听器（探针系统后台面，不在管理台读路径上）；本读点读 client_model:detail,credential_id:expr,outbound_model:base,success:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "error_kind:base", "failure_stage:base", "outbound_model:base", "request_id:base", "request_status:expr", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "被动探针监听器（探针系统后台面，不在管理台读路径上）；本读点读 client_model:detail,credential_id:expr,error_kind:base,failure_stage:base,outbound_model:base,request_id:base,request_status:expr,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "outbound_model:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "被动探针监听器（探针系统后台面，不在管理台读路径上）；本读点读 client_model:detail,credential_id:expr,outbound_model:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "outbound_model:base", "success:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "被动探针监听器（探针系统后台面，不在管理台读路径上）；本读点读 client_model:detail,credential_id:expr,outbound_model:base,success:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"bg/probe_policy.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "只在注释里引用过视图名；探针策略本身不读它",
	},
	// ★ R170（2026-10-27）：`bg/shared_pick.go` 的条目**已删除** ——
	//   它的 Priority 1 读点（探针选型「近 7 天最常用 client_model」）
	//   改读会话族原生源（db.SessionFamilyMostUsedClientModelSourceSQL），
	//   **不再提 canonical 视图** ⇒ 它不再是 canonical 视图的读点 ⇒
	//   「登记了但不再提及」= 登记簿腐烂（TestCanonicalReadPointRegistryIsNotRot
	//   会红）⇒ 条目必须删，而不是把 Form 改成什么别的形状。
	"bg/stats_minute_rollup.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "离线分钟汇总（读视图只为把窗口内的行汇进汇总表）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"canonical_id:base", "completion_tokens:base", "cost_usd:base", "credits_charged:base", "latency_ms:base", "prompt_tokens:base", "provider_id:detail", "request_status:expr", "success:base", "tenant_id:base", "total_tokens:expr", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "离线分钟汇总（读视图只为把窗口内的行汇进汇总表）；本读点读 canonical_id:base,completion_tokens:base,cost_usd:base,credits_charged:base,latency_ms:base,prompt_tokens:base,provider_id:detail,request_status:expr,success:base,tenant_id:base,total_tokens:expr,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"completion_tokens:base", "cost_usd:base", "credits_charged:base", "error_kind:base", "prompt_tokens:base", "request_status:expr", "success:base", "tenant_id:base", "total_tokens:expr", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "离线分钟汇总（读视图只为把窗口内的行汇进汇总表）；本读点读 completion_tokens:base,cost_usd:base,credits_charged:base,error_kind:base,prompt_tokens:base,request_status:expr,success:base,tenant_id:base,total_tokens:expr,ts:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
			{Kind: "read", Columns: []string{"client_model:detail", "client_profile:detail", "error_kind:base", "outbound_model:base", "provider_id:detail", "request_status:expr", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "离线分钟汇总（读视图只为把窗口内的行汇进汇总表）；本读点读 client_model:detail,client_profile:detail,error_kind:base,outbound_model:base,provider_id:detail,request_status:expr,tenant_id:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"bg/stats_minute_rollup_retire.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "离线分钟汇总退役（DELETE 的子查询用视图定位要清理的窗口）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"canonical_id:base", "provider_id:detail", "request_status:expr", "success:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "离线分钟汇总退役（DELETE 的子查询用视图定位要清理的窗口）；本读点读 canonical_id:base,provider_id:detail,request_status:expr,success:base,tenant_id:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"error_kind:base", "request_status:expr", "success:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "离线分钟汇总退役（DELETE 的子查询用视图定位要清理的窗口）；本读点读 error_kind:base,request_status:expr,success:base,tenant_id:base,ts:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
			{Kind: "read", Columns: []string{"client_model:detail", "client_profile:detail", "error_kind:base", "outbound_model:base", "provider_id:detail", "request_status:expr", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "离线分钟汇总退役（DELETE 的子查询用视图定位要清理的窗口）；本读点读 client_model:detail,client_profile:detail,error_kind:base,outbound_model:base,provider_id:detail,request_status:expr,tenant_id:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"cmd/gateway/waterfall_by_request.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "管理台 dispatch waterfall 的按请求读面（线上管理台读路径）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"canonical_model:base", "client_model:detail", "credential_id:expr", "error_kind:base", "gw_session_id:expr", "outbound_model:base", "request_id:base", "success:base", "t0_arrived_at:base", "t1_total_enqueued_at:base", "t2_total_dequeued_at:base", "t3_model_enqueued_at:base", "t4_model_dequeued_at:base", "t5_cred_enqueued_at:base", "t6_cred_dequeued_at:base", "t7_forward_start_at:base", "t8_response_start_at:base", "t9_response_end_at:base", "tenant_id:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "管理台 dispatch waterfall 的按请求读面（线上管理台读路径）；本读点读 canonical_model:base,client_model:detail,credential_id:expr,error_kind:base,gw_session_id:expr,outbound_model:base,request_id:base,success:base,t0_arrived_at:base,t1_total_enqueued_at:base,t2_total_dequeued_at:base,t3_model_enqueued_at:base,t4_model_dequeued_at:base,t5_cred_enqueued_at:base,t6_cred_dequeued_at:base,t7_forward_start_at:base,t8_response_start_at:base,t9_response_end_at:base,tenant_id:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"cmd/gateway/waterfall_db.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "管理台 dispatch waterfall 的 DB 回填读面（线上管理台读路径）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"canonical_model:base", "client_model:detail", "credential_id:expr", "error_kind:base", "gw_session_id:expr", "outbound_model:base", "request_id:base", "success:base", "t0_arrived_at:base", "t1_total_enqueued_at:base", "t2_total_dequeued_at:base", "t3_model_enqueued_at:base", "t4_model_dequeued_at:base", "t5_cred_enqueued_at:base", "t6_cred_dequeued_at:base", "t7_forward_start_at:base", "t8_response_start_at:base", "t9_response_end_at:base", "tenant_id:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "管理台 dispatch waterfall 的 DB 回填读面（线上管理台读路径）；本读点读 canonical_model:base,client_model:detail,credential_id:expr,error_kind:base,gw_session_id:expr,outbound_model:base,request_id:base,success:base,t0_arrived_at:base,t1_total_enqueued_at:base,t2_total_dequeued_at:base,t3_model_enqueued_at:base,t4_model_dequeued_at:base,t5_cred_enqueued_at:base,t6_cred_dequeued_at:base,t7_forward_start_at:base,t8_response_start_at:base,t9_response_end_at:base,tenant_id:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"cmd/tools/sql_source_indirection_audit/resolve.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "SQL 间接层审计工具；只在注释里解释视图名与 v1 族的区别",
	},
	"cmd/tools/sql_source_indirection_audit/v1arm.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "SQL 间接层审计工具；只在注释里讨论视图名清单",
	},
	// ★ 2026-10-28（R177 轮新增）：「逐读点实测分区下界」的量具。
	//
	// ★★ **视图名不硬编码在源码里** —— 它由 `--canonical-view` 传进来
	//   ⇒ ⇒ 源码里只在**注释**里提到那个名字 ⇒ ⇒ 因此这两条是
	//   「仅注释/字符串提及、本文件没有读点」形态。
	//
	// ★ 这不是「为了过门而登记」：★ 仓内那道门
	//   （`TestCanonicalReadPointRegistryCoversEveryMentionedFile`）扫**全仓生产 .go**
	//   里对 canonical 视图名的提及 ⇒ ⇒ **工具源码里出现那个名字就会被算成一个
	//   「提及该视图的生产文件」** ⇒ ⇒ 而它**不是**读点 ⇒ ⇒ 必须登记为无读点。
	// ⇒ ⇒ 顺带得到一个**更好的设计**：★ 量具不再绑死某一个视图 ⇒ 对任何 canonical 视图都适用。
	"cmd/tools/query_plan_form/main.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "R179 轮的实测计划形态量具（★ EXPLAIN ⇒ 计划形态分类）；★ 视图名由 --view 传参，★ 源码里只在注释里提到它 ⇒ 不是生产读点",
	},
	"cmd/tools/partition_bound_probe/main.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "分区下界实测量具；视图名由 --canonical-view 传参，源码里只在注释里提到它 ⇒ 不是生产读点",
	},
	"db/db.go": {
		Form:         formViewDefinitionProbe,
		Class:        classViewDefinitionFace,
		Exempt:       true,
		ExemptReason: exemptViewDefinitionFace,
		Note:         "只在**探测视图定义**的语境里提到它（pg_get_viewdef / to_regclass / POSITION）⇒ 读的是视图**元数据**，不是它的数据",
	},
	"db/probe_views_unified.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "探针视图的**生产者**（CREATE OR REPLACE VIEW v_model_health_dashboard）⇒ 它是视图的下游聚合面，不是终端读点",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "credential_id:expr", "outbound_model:base", "quality_flags:detail", "success:base", "task_type:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "探针视图的**生产者**（CREATE OR REPLACE VIEW v_model_health_dashboard）⇒ 它是视图的下游聚合面，不是终端读点；本读点读 client_model:detail,credential_id:expr,outbound_model:base,quality_flags:detail,success:base,task_type:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"db/request_logs_view_schema.go": {
		Form:         formViewDDLDefinition,
		Class:        classViewDefinitionFace,
		Exempt:       true,
		ExemptReason: exemptViewDefinitionFace,
		Note:         "canonical 视图**自身的定义面**（CREATE OR REPLACE VIEW / COMMENT ON VIEW / pg_views 探测）⇒ 它是视图的**生产者**，不是读方",
	},
	"domains/attachments/handler.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "附件正文读取（attachments 列走 details 腿）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"attachments:detail", "request_id:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "附件正文读取（attachments 列走 details 腿）；本读点读 attachments:detail,request_id:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"domains/routeincident/store.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "路由事故 store",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"canonical_id:base", "client_model:detail", "credential_id:expr", "latency_ms:base", "outbound_model:base", "provider_id:detail", "request_status:expr", "success:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "路由事故 store；本读点读 canonical_id:base,client_model:detail,credential_id:expr,latency_ms:base,outbound_model:base,provider_id:detail,request_status:expr,success:base,tenant_id:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"domains/session/v2/turn_writer.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "写入侧只在注释里说明视图如何投影 tool_calls；不读视图",
	},
	"domains/sessionforensics/export.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "★ **已修（R173）**：两个导出读点原读 `rl.id`（canonical 视图的 `id` 是 v1 遗留列、**恒 NULL**）⇒ 导出里的 id 字段永远是 NULL、`ORDER BY rl.ts ASC, rl.id ASC` 的次序键也是死的 ⇒ ★ 现改为读 `rl.request_id`（实测非空、视图内唯一）⇒ 导出消息终于带上轮次身份、次序键稳定；第三个读点是压缩策略聚合",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"attachments:detail", "client_model:detail", "compression_meta:base", "compression_reason:detail", "compression_strategy:base", "gw_session_id:expr", "outbound_model:base", "parent_request_id:base", "request_id:base", "request_mode:base", "tenant_id:base", "ts:base", "work_type:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "★ **已修（R173）**：原读 `rl.id`（恒 NULL）⇒ 现读 `rl.request_id` ⇒ 导出消息带上轮次身份、`ORDER BY rl.ts ASC, rl.request_id ASC` 次序确定；本读点仍引用 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"attachments:detail", "client_model:detail", "compression_meta:base", "compression_reason:detail", "compression_strategy:base", "gw_session_id:expr", "outbound_model:base", "parent_request_id:base", "request_id:base", "request_mode:base", "tenant_id:base", "ts:base", "work_type:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "★ **已修（R173）**：原读 `rl.id`（恒 NULL）⇒ 现读 `rl.request_id` ⇒ 导出消息带上轮次身份、`ORDER BY rl.ts ASC, rl.request_id ASC` 次序确定；本读点仍引用 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"client_model:detail", "completion_tokens:base", "compression_strategy:base", "cost_usd:base", "gw_session_id:expr", "prompt_tokens:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "★ **已修（R173）**：两个导出读点原读 `rl.id`（canonical 视图的 `id` 是 v1 遗留列、**恒 NULL**）⇒ 导出里的 id 字段永远是 NULL、`ORDER BY rl.ts ASC, rl.id ASC` 的次序键也是死的 ⇒ ★ 现改为读 `rl.request_id`（实测非空、视图内唯一）⇒ 导出消息终于带上轮次身份、次序键稳定；第三个读点是压缩策略聚合"},
		},
	},
	"domains/sessionsummary/summarizer.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "★ **已修（R173）**：标题解析读点原 `ORDER BY ts DESC, id DESC`，而视图的 `id` 恒 NULL ⇒ 同 ts 的并列行取哪一行随机 ⇒ ★ 现改为 `ORDER BY ts DESC, request_id DESC`（稳定键）；另两个读点取 request_id / ts / outbound_model",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"gw_session_id:expr", "outbound_model:base", "request_id:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "★ **已修（R173）**：标题解析读点原 `ORDER BY ts DESC, id DESC`，而视图的 `id` 恒 NULL ⇒ 同 ts 的并列行取哪一行随机 ⇒ ★ 现改为 `ORDER BY ts DESC, request_id DESC`（稳定键）；另两个读点取 request_id / ts / outbound_model"},
			{Kind: "read", Columns: []string{"gw_session_id:expr", "outbound_model:base", "request_id:base", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "★ **已修（R173）**：标题解析读点原 `ORDER BY ts DESC, id DESC`，而视图的 `id` 恒 NULL ⇒ 同 ts 的并列行取哪一行随机 ⇒ ★ 现改为 `ORDER BY ts DESC, request_id DESC`（稳定键）；另两个读点取 request_id / ts / outbound_model"},
			{Kind: "read", Columns: []string{"gw_session_id:expr", "gw_task_id:detail", "is_auto_request:base", "request_id:base", "success:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "★ **已修（R173）**：原读 `id`（恒 NULL）⇒ 现改用 `request_id`（非空、唯一）作 tie-break ⇒ 同 ts 的并列行取哪一行不再随机；本读点仍引用 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"domains/sessionsummary/system_prompt_prefix.go": {
		Form:  formDirectLiteralSQL,
		Class: classNarrowMigratable,
		Note:  "系统提示前缀（正文来自 SessionBodiesSourceSQL 的 LEFT JOIN；视图腿只提供连接键）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"gw_session_id:expr", "request_id:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNeedsExpressionReplication, Exempt: false, ExemptReason: "", Note: "系统提示前缀（正文来自 SessionBodiesSourceSQL 的 LEFT JOIN；视图腿只提供连接键）；本读点读 gw_session_id:expr,request_id:base,tenant_id:base,ts:base ⇒ 引用了视图里的 CASE 表达式列 ⇒ 窄投影须复刻那份表达式"},
		},
	},
	"domains/streaming/model_alternatives.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "视图名只出现在一条 SQL 字面量的 `--` 注释里（解释它是 session_turns 与 … 的 UNION）⇒ 不是读点",
	},
	"installer/internal/dbinit/runner.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "安装器只在注释里记录视图的 118 列契约与删臂决策；不读视图",
	},
	"internal/collector/gateway_adapters.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "collector 适配：① 延迟分位；② 模型计数；③ 30 秒计数",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"latency_ms:base", "success:base", "tenant_id:base", "ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "collector 适配：① 延迟分位；② 模型计数；③ 30 秒计数；本读点读 latency_ms:base,success:base,tenant_id:base,ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
			{Kind: "read", Columns: []string{"client_model:detail", "outbound_model:base", "ts:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: false, ExemptReason: "", Note: "collector 适配：① 延迟分位；② 模型计数；③ 30 秒计数；本读点读 client_model:detail,outbound_model:base,ts:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
			{Kind: "read", Columns: []string{"ts:base"}, Holder: "", Class: classNarrowMigratable, Exempt: false, ExemptReason: "", Note: "collector 适配：① 延迟分位；② 模型计数；③ 30 秒计数；本读点读 ts:base ⇒ 全部引用列都是底表直映列 ⇒ 底表两臂窄投影可等价供给"},
		},
	},
	"internal/quality/minute_aggregator.go": {
		Form:  formDirectLiteralSQL,
		Class: classNeedsDetailsLeg,
		Note:  "离线分钟聚合（provider_id 走 details 腿，缺 details 行的轮次被谓词排除）",
		Points: []canonicalReadPoint{
			{Kind: "read", Columns: []string{"client_model:detail", "completion_tokens:base", "cost_usd:base", "error_kind:base", "latency_ms:base", "outbound_model:base", "prompt_tokens:base", "provider_id:detail", "stream_first_chunk_ms:base", "success:base", "ts:base", "upstream_status_code:base"}, Holder: "", Class: classNeedsDetailsLeg, Exempt: true, ExemptReason: exemptAuditOrToolingFace, Note: "离线分钟聚合（provider_id 走 details 腿，缺 details 行的轮次被谓词排除）；本读点读 client_model:detail,completion_tokens:base,cost_usd:base,error_kind:base,latency_ms:base,outbound_model:base,prompt_tokens:base,provider_id:detail,stream_first_chunk_ms:base,success:base,ts:base,upstream_status_code:base ⇒ 引用了 734 details 腿的列 ⇒ 窄投影须带上 details LEFT JOIN"},
		},
	},
	"internal/sessionv2mirror/synthetic_session.go": {
		Form:         formCommentOrStringOnly,
		Class:        classNoRead,
		Exempt:       true,
		ExemptReason: exemptNoReadPoint,
		Note:         "只在注释里讨论视图上的 NULL 语义；本文件是合成器",
	},
	"maas/usage.go": {
		Form:  formHelperConcat,
		Class: classSwitchArm,
		Note:  "requestLogsSource(days) 在 890 之后**恒**返回 canonical 视图（原 days>7 分臂已合并，分流本身已无意义）⇒ 源仍由 helper 决定 ⇒ 归切换层裁决",
		Points: []canonicalReadPoint{
			{Kind: "holder", Columns: nil, Holder: "request_logs_with_current_month AS r", Class: classSwitchArm, Exempt: true, ExemptReason: exemptSwitchLayerContract, Note: "requestLogsSource(days) 在 890 之后**恒**返回 canonical 视图（原 days>7 分臂已合并，分流本身已无意义）⇒ 源仍由 helper 决定 ⇒ 归切换层裁决；本读点读 request_logs_with_current_month AS r ⇒ 源由切换层决定 ⇒ 调用点改不动"},
		},
	},
}

// ─────────────────────── 扫描器 ───────────────────────

// canonicalReadPointSite 是扫描器在一条 SQL 字面量 / 一个 Go 字符串里
// 找到的一个提及。
type canonicalReadPointSite struct {
	// Line 是提及所在行（1 起）。
	Line int
	// Kind 是 site 类别："read" / "holder" / "ddl" / "probe" / "message" /
	// "sqlComment" / "goComment"。
	Kind string
	// Text 是规范化的上下文（holder/message 是整条字符串；其余是所在行）。
	Text string
	// Columns 是 read 站点的列集合（"名:来源" 形式）。
	Columns []string
}

// canonicalFileFacts 是扫描器对**一个生产文件**的读数。
type canonicalFileFacts struct {
	// Sites 是该文件里全部提及。
	Sites []canonicalReadPointSite
	// ReadPoints 是读点（一条 SQL 字面量一个；Go 持有点一个）。
	ReadPoints []canonicalReadPointSite
	// Form 是机械推导出的形态。
	Form string
}

var (
	canonicalScanOnce sync.Once
	canonicalScanData map[string]canonicalFileFacts
)

// scanCanonicalReadPoints 扫**生产** .go 文件里对 canonical 视图的全部提及。
//
// ⚠ 用 go/ast 而不是逐行正则：只有 AST 能区分「注释里的 SQL」与
// 「真的会被执行的 SQL」，也能区分「Go 字符串持有表名」与「Go 消息串」。
// （admin/v1_family_purge_gate_test.go 的 scanV1WriteOperationsInGo 同款理由。）
func scanCanonicalReadPoints(t *testing.T, root string) map[string]canonicalFileFacts {
	t.Helper()
	canonicalScanOnce.Do(func() { canonicalScanData = computeCanonicalReadPoints(t, root) })
	return canonicalScanData
}

func computeCanonicalReadPoints(t *testing.T, root string) map[string]canonicalFileFacts {
	t.Helper()
	out := map[string]canonicalFileFacts{}

	contract := canonicalColumnContract()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if nonSourceDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// 预筛：只有真正提到视图名的文件才需要解析（全仓几千个 .go 文件，
		// 逐个 go/parser 太慢）。⚠ 预筛用**朴素包含**是安全的 —— 它只用来
		// 决定「要不要解析」，命中判定仍然是 AST 级（边界 + 上下文）。
		if !strings.Contains(string(raw), canonicalRequestLogsView) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, raw, parser.ParseComments)
		if err != nil {
			// 解析失败不静默跳过 —— 那会让门在这个文件上恒真空。
			t.Fatalf("parse %s: %v", rel, err)
		}

		var facts canonicalFileFacts
		holderHasBranch := false

		// ① Go 注释里的提及。
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if len(canonicalMentionOffsets(c.Text)) == 0 {
					continue
				}
				facts.Sites = append(facts.Sites, canonicalReadPointSite{
					Line: fset.Position(c.Pos()).Line,
					Kind: "goComment",
					Text: strings.TrimSpace(c.Text),
				})
			}
		}

		// ② 字符串字面量里的提及。
		//
		// ⚠ **必须按「拼接链」而不是按单个字面量**分析：仓里的 SQL 大量是
		// `"SELECT … FROM " + dbpkg.SessionFamilyBodiesSourceSQL() + " rb ON …"`
		// 这种 Go 字符串拼接 —— 视图名与列引用**分处不同字面量**。
		// ⇒ 逐字面量分析会把 `WHERE rl.request_id = $1`（在第三段里）漏掉，
		// 让读点的列集合**不完整** ⇒ 分类失真。
		canonicalInspectTextUnits(f, fset, func(unit canonicalTextUnit) {
			text := unit.text
			if len(canonicalMentionOffsets(text)) == 0 {
				return
			}
			base := unit.startLine
			trimmed := strings.TrimSpace(text)

			switch {
			case canonicalBareHolderRE.MatchString(trimmed):
				// Go 字符串**持有**视图名（表名/别名片段）⇒ 持有点。
				site := canonicalReadPointSite{
					Line: base, Kind: "holder", Text: trimmed,
				}
				facts.Sites = append(facts.Sites, site)
				facts.ReadPoints = append(facts.ReadPoints, site)
				if unit.hasBranch {
					holderHasBranch = true
				}
				return

			case !canonicalSQLKeywordRE.MatchString(text):
				// 非 SQL 的 Go 字符串（错误消息 / 日志串）⇒ 只是提及。
				facts.Sites = append(facts.Sites, canonicalReadPointSite{
					Line: base, Kind: "message", Text: trimmed,
				})
				return
			}

			// SQL 字面量：先剥 SQL 注释（**保持字节偏移**），再判读点。
			clean := canonicalStripSQLComments(text)
			readCols := map[string]bool{}
			hasRead := false
			for _, off := range canonicalMentionOffsets(text) {
				line := base + strings.Count(text[:off], "\n")
				before := canonicalLineBefore(text, off)
				after := canonicalLineAfter(text, off+len(canonicalRequestLogsView))
				shown := strings.TrimSpace(before + canonicalRequestLogsView + after)
				switch {
				case !canonicalMentionSurvives(clean, off):
					facts.Sites = append(facts.Sites, canonicalReadPointSite{
						Line: line, Kind: "sqlComment", Text: shown,
					})
				case canonicalDDLPrefixRE.MatchString(clean[:off]):
					facts.Sites = append(facts.Sites, canonicalReadPointSite{
						Line: line, Kind: "ddl", Text: shown,
					})
				case canonicalReadPrefixRE.MatchString(clean[:off]):
					hasRead = true
					for _, c := range canonicalReferencedColumns(text, clean, off, contract) {
						readCols[c] = true
					}
					facts.Sites = append(facts.Sites, canonicalReadPointSite{
						Line: line, Kind: "read", Text: shown,
					})
				case canonicalProbeContext(text):
					facts.Sites = append(facts.Sites, canonicalReadPointSite{
						Line: line, Kind: "probe", Text: shown,
					})
				default:
					facts.Sites = append(facts.Sites, canonicalReadPointSite{
						Line: line, Kind: "sqlOther", Text: shown,
					})
				}
			}
			// 一条 SQL 字面量 = **一个**读点（哪怕它提到两次视图名）。
			//
			// ⚠ 读点**即使一列都没引用**也算读点（例如「SELECT 1 FROM 视图
			// WHERE …」这种存在性检查，或只拿视图当 JOIN 的一侧）——
			// 「它读了视图」这件事与「它从视图取了哪几列」是两件事。
			if hasRead {
				cols := make([]string, 0, len(readCols))
				for c := range readCols {
					cols = append(cols, c)
				}
				sort.Strings(cols)
				line := 0
				for _, s := range facts.Sites {
					if s.Kind == "read" {
						line = s.Line
						break
					}
				}
				facts.ReadPoints = append(facts.ReadPoints, canonicalReadPointSite{
					Line: line, Kind: "read", Columns: cols,
				})
			}
		})

		if len(facts.Sites) == 0 {
			return nil
		}
		facts.Form = canonicalFormOf(facts, holderHasBranch)
		out[rel] = facts
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	return out
}

// canonicalTextUnit 是「一段待分析的 Go 字符串文本」。
//
// ⚠ 一段 = **一个拼接链**（`"a" + x + "b"` 里的全部字面量）或**一个独立字面量**。
type canonicalTextUnit struct {
	// text 是拼接后的完整文本（非字面量片段用占位符顶替）。
	text string
	// startLine 是这段文本的起始行（1 起）。
	startLine int
	// hasBranch 报告这段文本所在的函数体内有没有条件分支。
	hasBranch bool
}

// canonicalConcatPart 是拼接链里的一段。
type canonicalConcatPart struct {
	lit  *ast.BasicLit // 非 nil ⇒ 这一段是字符串字面量
	text string        // 这一段在链文本里的内容
}

// canonicalConcatHole 是「非字面量片段」的占位符。
//
// ⚠ 长度固定 ⇒ 拼接链里各段的偏移可算；占位符里不含视图名 ⇒ 不会造出
// 假读点。
const canonicalConcatHole = "\x00?\x00"

// canonicalConcatParts 把一个 `+` 表达式拆成有序的段。
func canonicalConcatParts(n ast.Node) []canonicalConcatPart {
	switch v := n.(type) {
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return []canonicalConcatPart{{text: canonicalConcatHole}}
		}
		left := canonicalConcatParts(v.X)
		right := canonicalConcatParts(v.Y)
		return append(left, right...)
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return []canonicalConcatPart{{text: canonicalConcatHole}}
		}
		return []canonicalConcatPart{{lit: v, text: canonicalLiteralText(v)}}
	default:
		return []canonicalConcatPart{{text: canonicalConcatHole}}
	}
}

// canonicalLiteralText 取一个字符串字面量的**内容**（去掉引号/反引号）。
func canonicalLiteralText(lit *ast.BasicLit) string {
	text := lit.Value
	if strings.HasPrefix(text, "`") {
		return text[1 : len(text)-1]
	}
	if u, err := strconv.Unquote(text); err == nil {
		return u
	}
	return text
}

// canonicalInspectTextUnits 遍历一个文件里**所有**待分析的字符串文本单元。
//
// ⚠ 拼接链只分析**一次**（在链根上处理并停止下降）⇒ 不会把同一条 SQL
// 按段重复计数。
func canonicalInspectTextUnits(f *ast.File, fset *token.FileSet, fn func(canonicalTextUnit)) {
	ast.Inspect(f, func(n ast.Node) bool {
		if be, ok := n.(*ast.BinaryExpr); ok && be.Op == token.ADD {
			parts := canonicalConcatParts(be)
			hasLit := false
			for _, p := range parts {
				if p.lit != nil {
					hasLit = true
					break
				}
			}
			if hasLit {
				var sb strings.Builder
				startLine := 0
				first := true
				firstLitPos := token.NoPos
				for _, p := range parts {
					sb.WriteString(p.text)
					if p.lit != nil {
						if first {
							startLine = fset.Position(p.lit.Pos()).Line
							first = false
						}
						if firstLitPos == token.NoPos {
							firstLitPos = p.lit.Pos()
						}
					}
				}
				fn(canonicalTextUnit{
					text:      sb.String(),
					startLine: startLine,
					hasBranch: enclosingFuncHasBranch(f, fset, firstLitPos),
				})
				return false
			}
		}
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			fn(canonicalTextUnit{
				text:      canonicalLiteralText(lit),
				startLine: fset.Position(lit.Pos()).Line,
				hasBranch: enclosingFuncHasBranch(f, fset, lit.Pos()),
			})
			return false
		}
		return true
	})
}

// canonicalFormOf 由扫描读数机械推导**形态**。
//
// ⚠ 优先级是刻意的：**定义面 > 探测面 > 直接读 > 持有点 > 仅提及**。
// 一个文件同时命中多类时，取**最靠近「它是视图自己的面」**的那一类。
func canonicalFormOf(facts canonicalFileFacts, holderHasBranch bool) string {
	hasDDL, hasProbe, hasRead := false, false, false
	for _, s := range facts.Sites {
		switch s.Kind {
		case "ddl":
			hasDDL = true
		case "probe":
			hasProbe = true
		case "read":
			hasRead = true
		}
	}
	switch {
	case hasDDL:
		return formViewDDLDefinition
	case hasProbe:
		return formViewDefinitionProbe
	case hasRead:
		return formDirectLiteralSQL
	case len(facts.ReadPoints) > 0:
		if holderHasBranch {
			return formSwitchLayer
		}
		return formHelperConcat
	default:
		return formCommentOrStringOnly
	}
}

// ─────────────────────── 判据（正则 / 词表） ───────────────────────

var (
	// canonicalBareHolderRE：Go 字符串**恰好**持有视图名（可带 public. 前缀
	// 与一个别名）⇒ 它是「表名/别名片段」，被 Go 代码拼进 SQL。
	canonicalBareHolderRE = regexp.MustCompile(
		`(?i)^(?:public\.)?` + canonicalRequestLogsView + `(?:\s+(?:as\s+)?[a-z_][a-z0-9_]*)?$`)

	// canonicalSQLKeywordRE：字面量里出现 SQL 关键字 ⇒ 它是 SQL。
	canonicalSQLKeywordRE = regexp.MustCompile(
		`(?is)\b(select|from|join|where|insert|update|delete|create|alter|comment\s+on|with)\b`)

	// canonicalReadPrefixRE：视图名处于 FROM/JOIN 读位置。
	canonicalReadPrefixRE = regexp.MustCompile(
		`(?i)\b(?:from|join)\s+(?:only\s+)?(?:public\.)?$`)

	// canonicalDDLPrefixRE：视图名处于 DDL 位置（视图自身的定义面）。
	canonicalDDLPrefixRE = regexp.MustCompile(
		`(?i)\b(?:create\s+(?:or\s+replace\s+)?view|comment\s+on\s+view|alter\s+view|drop\s+view)` +
			`\s+(?:if\s+exists\s+)?(?:only\s+)?(?:public\.)?$`)

	// canonicalIdentRE：标识符。
	canonicalIdentRE = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\b`)

	// canonicalQualRE：限定引用 `alias.column`。
	canonicalQualRE = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\b`)
)

// canonicalSQLKeywords 是不可能是表别名的 SQL 关键字（判别 FROM 之后的别名）。
var canonicalSQLKeywords = map[string]bool{
	"select": true, "from": true, "join": true, "where": true, "group": true,
	"order": true, "limit": true, "having": true, "union": true, "all": true,
	"on": true, "and": true, "or": true, "not": true, "null": true, "as": true,
	"case": true, "when": true, "then": true, "else": true, "end": true,
	"distinct": true, "insert": true, "into": true, "values": true,
	"update": true, "set": true, "delete": true, "with": true, "using": true,
	"asc": true, "desc": true, "left": true, "right": true, "inner": true,
	"outer": true, "cross": true, "lateral": true, "exists": true,
	"between": true, "like": true, "ilike": true, "is": true, "in": true,
	"interval": true, "extract": true, "filter": true, "over": true,
	"partition": true, "true": true, "false": true, "array": true, "row": true,
	"count": true, "sum": true, "coalesce": true, "nullif": true,
	"greatest": true, "least": true, "substring": true, "position": true,
	"now": true, "current_date": true, "current_timestamp": true,
}

// canonicalMentionOffsets 返回视图名在文本里的**独立**出现位置。
//
// ⚠ **标识符边界**：`request_logs_with_current_month_without_customer_id`
// 不是 canonical 视图（那是 890 已 DROP 的 v1 包装视图）⇒ 必须排除。
func canonicalMentionOffsets(s string) []int {
	var out []int
	for i := 0; i+len(canonicalRequestLogsView) <= len(s); i++ {
		if s[i:i+len(canonicalRequestLogsView)] != canonicalRequestLogsView {
			continue
		}
		if i > 0 && canonicalIsWordByte(s[i-1]) {
			continue
		}
		end := i + len(canonicalRequestLogsView)
		if end < len(s) && canonicalIsWordByte(s[end]) {
			continue
		}
		out = append(out, i)
	}
	return out
}

func canonicalIsWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// canonicalStripSQLComments 把 SQL 里的 `--` 行注释与 `/* */` 块注释**涂成
// 空格**（**保持字节偏移**）⇒ 注释里的视图名不会被算成读点，且偏移不变。
func canonicalStripSQLComments(s string) string {
	b := []byte(s)
	blank := func(i, j int) {
		for k := i; k < j && k < len(b); k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
	}
	for i := 0; i < len(b); {
		if b[i] == '-' && i+1 < len(b) && b[i+1] == '-' {
			j := i
			for j < len(b) && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
			continue
		}
		if b[i] == '/' && i+1 < len(b) && b[i+1] == '*' {
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				blank(i, len(b))
				break
			}
			blank(i, i+2+j+2)
			i += 2 + j + 2
			continue
		}
		i++
	}
	return string(b)
}

// canonicalMentionSurvives 报告该位置的视图名是否**躲过了** SQL 注释涂白。
func canonicalMentionSurvives(clean string, off int) bool {
	if off+len(canonicalRequestLogsView) > len(clean) {
		return false
	}
	return clean[off:off+len(canonicalRequestLogsView)] == canonicalRequestLogsView
}

// canonicalProbeContext 报告该 SQL 字面量是不是在**探测视图定义**。
func canonicalProbeContext(text string) bool {
	l := strings.ToLower(text)
	return strings.Contains(l, "pg_get_viewdef") ||
		strings.Contains(l, "pg_views") ||
		strings.Contains(l, "information_schema.views") ||
		strings.Contains(l, "to_regclass")
}

func canonicalLineBefore(text string, off int) string {
	seg := text[:off]
	if i := strings.LastIndex(seg, "\n"); i >= 0 {
		seg = seg[i+1:]
	}
	return seg
}

func canonicalLineAfter(text string, off int) string {
	rest := text[off:]
	if i := strings.Index(rest, "\n"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// enclosingFuncHasBranch 报告 pos 所在函数体内**有没有**条件分支
// （If / Switch / TypeSwitch）⇒ 用来区分「切换层」与「常量持有」。
func enclosingFuncHasBranch(f *ast.File, fset *token.FileSet, pos token.Pos) bool {
	found := false
	var stack []*ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if fd.Pos() <= pos && pos < fd.End() {
			stack = append(stack, fd)
		}
	}
	if len(stack) == 0 {
		return false
	}
	for _, fd := range stack {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch n.(type) {
			case *ast.IfStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt:
				found = true
			}
			return true
		})
	}
	_ = fset
	return found
}

// ─────────────────────── 投影契约（列 → 来源） ───────────────────────

// canonicalViewColumnContract 是 canonical 视图的**列 → 投影来源**映射。
//
// ★ 来源取自 **db 包导出的权威投影文本**
// （db.CanonicalSessionOnlyProjectionForTest()）—— 不是本表自己抄一份列清单。
// ⇒ 投影一改，本表自动跟着变（⇒ 登记簿会红，而不是悄悄过期）。
type canonicalViewColumnContract struct {
	// Order 是列序（契约顺序）。
	Order []string
	// Kind 是列 → 投影来源（base / detail / NULL / expr）。
	Kind map[string]string
}

// canonicalContractOnce 缓存投影契约（解析一次即可）。
var (
	canonicalContractOnce sync.Once
	canonicalContractData canonicalViewColumnContract
)

func canonicalColumnContract() canonicalViewColumnContract {
	canonicalContractOnce.Do(func() {
		c := canonicalViewColumnContract{Kind: map[string]string{}}
		// 投影文本形如 `expr AS name`，各段以 ",\n" 分隔。
		for _, part := range strings.Split(dbpkg.CanonicalSessionOnlyProjectionForTest(), ",\n") {
			part = strings.TrimSpace(part)
			i := strings.LastIndex(part, " AS ")
			if i < 0 {
				continue
			}
			expr := strings.TrimSpace(part[:i])
			name := strings.TrimSpace(part[i+4:])
			if name == "" {
				continue
			}
			c.Order = append(c.Order, name)
			switch {
			case strings.HasPrefix(expr, "NULL::"):
				c.Kind[name] = "NULL"
			case strings.HasPrefix(expr, "t."):
				c.Kind[name] = "base"
			case strings.HasPrefix(expr, "d."):
				c.Kind[name] = "detail"
			default:
				c.Kind[name] = "expr"
			}
		}
		canonicalContractData = c
	})
	return canonicalContractData
}

// canonicalViewAlias 返回绑定到 canonical 视图的别名（没有则空串）。
func canonicalViewAlias(sql string, off int) string {
	rest := sql[off+len(canonicalRequestLogsView):]
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
		i++
	}
	// 可选的 AS
	if len(rest)-i >= 2 && strings.EqualFold(rest[i:i+2], "as") &&
		(i+2 >= len(rest) || !canonicalIsWordByte(rest[i+2])) {
		i += 2
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
			i++
		}
	}
	j := i
	for j < len(rest) && canonicalIsWordByte(rest[j]) {
		j++
	}
	if j == i {
		return ""
	}
	w := strings.ToLower(rest[i:j])
	if canonicalSQLKeywords[w] {
		return ""
	}
	return rest[i:j]
}

// canonicalReferencedColumns 返回该 SQL 字面量**真正读到**的 canonical 视图列，
// 每项写作 "<列名>:<投影来源>"。
//
// ⚠ **别名感知**：只认「用视图自己的别名限定的列」；未加限定的列只在
// 「这条 SQL 里没有第二张表的别名」时才算数。
// ⇒ 否则 `mo.id`（models 表的 id）会被当成视图的 `id` 列 —— 那正是视图里
// **恒 NULL** 的 v1 遗留列 ⇒ 会造出一堆假告警。
func canonicalReferencedColumns(raw, clean string, off int, c canonicalViewColumnContract) []string {
	inSet := map[string]bool{}
	for _, n := range c.Order {
		inSet[n] = true
	}
	alias := canonicalViewAlias(raw, off)

	// ⚠ 三类「看着像视图列、其实不是」的排除项 —— 少了任何一条都会造出
	// **假缺陷告警**（最坏的一类错：把 `mo.id` 报成「视图恒 NULL 的 id」）。
	//
	//	① **列别名**：`SELECT rl.credential_id AS id` ⇒ `id` 是别名，不是列。
	//	② **别的关系的列**：`SELECT id FROM provider_models pm` ⇒ `id` 是
	//	   provider_models 的列，不是视图的。
	//	③ **被别的别名限定**：`mo.id` ⇒ `id` 前面是 '.' ⇒ 不是未限定列。
	aliasNames := canonicalSelectAliasNames(raw)
	foreign := canonicalForeignSelectColumns(raw)

	found := map[string]bool{}
	// ① 用**视图自己的别名**限定的列 —— 一定是视图的列。
	if alias != "" {
		pat := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(alias) + `\.([A-Za-z_][A-Za-z0-9_]*)\b`)
		for _, m := range pat.FindAllStringSubmatch(raw, -1) {
			if inSet[m[1]] {
				found[m[1]] = true
			}
		}
	}
	// ② **未限定**的列 —— 前面不是 '.'、不是列别名、不属于别的关系时才算数。
	for _, m := range canonicalIdentRE.FindAllStringSubmatchIndex(raw, -1) {
		tok := raw[m[2]:m[3]]
		if !inSet[tok] {
			continue
		}
		start := m[0]
		if start > 0 && raw[start-1] == '.' {
			continue // ③ 被别的别名限定
		}
		if aliasNames[tok] {
			continue // ① 列别名
		}
		if foreign[tok] {
			continue // ② 属于别的关系
		}
		found[tok] = true
	}
	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k+":"+c.Kind[k])
	}
	sort.Strings(out)
	return out
}

// canonicalSelectAliasNames 返回这条 SQL 里**被 AS 命名过**的标识符。
func canonicalSelectAliasNames(sql string) map[string]bool {
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?i)\bas\s+([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(sql, -1) {
		out[m[1]] = true
	}
	return out
}

// canonicalForeignSelectColumns 返回「属于**别的关系**」的未限定列名集合。
//
// 判据：某个 `SELECT … FROM <rel>` 片段里，`<rel>` **不是** canonical 视图 ⇒
// 该片段 SELECT 列表里的标识符都属于 `<rel>`。
func canonicalForeignSelectColumns(sql string) map[string]bool {
	out := map[string]bool{}
	re := regexp.MustCompile(`(?is)\bselect\b(.*?)\bfrom\b\s+(?:only\s+)?([a-z_][a-z0-9_.]*)`)
	for _, m := range re.FindAllStringSubmatch(sql, -1) {
		rel := m[2]
		if rel == canonicalRequestLogsView || rel == "public."+canonicalRequestLogsView {
			continue
		}
		for _, id := range canonicalIdentRE.FindAllStringSubmatch(m[1], -1) {
			out[id[1]] = true
		}
	}
	return out
}

// ─────────────────────── 门的自检逻辑 ───────────────────────

// canonicalPointKey 是读点的**身份**（用于逐条对拍）。
//
// ⚠ 用「列集合」而不是「行号」：行号会随无关编辑整体平移 ⇒ 用它做身份会
// 让登记簿在每次插行时集体漂移。列集合是**语义**上的指纹：
// 投影变了 ⇒ 指纹变 ⇒ 登记簿必须重新审定 ⇒ 门红。
func canonicalPointKey(p canonicalReadPoint) string {
	switch p.Kind {
	case "holder":
		return "holder|" + p.Holder
	default:
		return "read|" + strings.Join(p.Columns, ",")
	}
}

// canonicalEntryProblems 返回一条登记的问题列表（空 = 合格）。
//
// ⚠ 抽成函数是为了让**自检**能在合成条目上驱动它（表里全是真实条目时，
// 「自检逻辑有没有牙」没人能证明）。
func canonicalEntryProblems(file string, e canonicalReadPointFile, facts canonicalFileFacts) []string {
	var problems []string
	if !canonicalReadPointForms[e.Form] {
		problems = append(problems, fmt.Sprintf("形态 %q 不在已声明集合里", e.Form))
	}
	if !canonicalReadPointClasses[e.Class] {
		problems = append(problems, fmt.Sprintf("文件级分类 %q 不在已声明集合里", e.Class))
	}
	if strings.TrimSpace(e.Note) == "" {
		problems = append(problems, "文件级 Note 为空 —— 空说明的登记等于没有登记")
	}
	// 文件级豁免只允许出现在「没有读点」的文件上。
	if e.Exempt {
		if !canonicalExemptReasons[e.ExemptReason] {
			problems = append(problems, fmt.Sprintf("文件级豁免理由 %q 不在已声明集合里", e.ExemptReason))
		}
		if len(e.Points) > 0 {
			problems = append(problems, "文件级豁免用在了**有读点**的文件上 —— "+
				"豁免必须逐读点地写在 Points 里，否则会把该文件的每个读点一起藏掉")
		}
	} else if strings.TrimSpace(e.ExemptReason) != "" {
		problems = append(problems, "文件级未豁免却写了豁免理由（两者必须一致）")
	}
	if facts.Sites == nil && len(facts.ReadPoints) == 0 {
		// 该文件在扫描里**不存在** ⇒ 登记已经陈旧（文件被删/改名，
		// 或它其实不再提及视图）。
		problems = append(problems, "该文件在扫描结果里不存在 —— 登记已陈旧")
		return problems
	}
	if e.Form != facts.Form {
		problems = append(problems, fmt.Sprintf("形态登记为 %q，扫描实测 %q", e.Form, facts.Form))
	}
	if len(e.Points) != len(facts.ReadPoints) {
		problems = append(problems, fmt.Sprintf("读点条数登记 %d 条，扫描实测 %d 条", len(e.Points), len(facts.ReadPoints)))
	}
	// 逐条对拍：登记的读点身份多重集必须与扫描结果**逐字相等**。
	declared := map[string]int{}
	for _, p := range e.Points {
		declared[canonicalPointKey(p)]++
	}
	actual := map[string]int{}
	for _, p := range facts.ReadPoints {
		switch p.Kind {
		case "holder":
			actual["holder|"+p.Text]++
		default:
			actual["read|"+strings.Join(p.Columns, ",")]++
		}
	}
	for k, n := range declared {
		if actual[k] != n {
			problems = append(problems, fmt.Sprintf("读点 %q 登记 %d 条，扫描实测 %d 条", k, n, actual[k]))
		}
	}
	for k, n := range actual {
		if declared[k] != n {
			problems = append(problems, fmt.Sprintf("读点 %q 扫描实测 %d 条，登记里没有（或条数不同）", k, n))
		}
	}
	// 逐条自检（分类 / 豁免 / 说明）。
	for i, p := range e.Points {
		problems = append(problems, canonicalPointProblems(file, i, p)...)
	}
	return problems
}

// canonicalPointProblems 返回单个读点登记的问题列表（空 = 合格）。
func canonicalPointProblems(file string, idx int, p canonicalReadPoint) []string {
	var problems []string
	where := fmt.Sprintf("%s 读点#%d", file, idx+1)
	if p.Kind != "read" && p.Kind != "holder" {
		problems = append(problems, where+": Kind 必须是 read 或 holder")
	}
	if p.Kind == "holder" && strings.TrimSpace(p.Holder) == "" {
		problems = append(problems, where+": holder 读点缺 Holder 文本")
	}
	if p.Kind == "read" && p.Columns == nil {
		problems = append(problems, where+": read 读点缺 Columns（机器可核的投影列集合）")
	}
	if !canonicalReadPointClasses[p.Class] {
		problems = append(problems, fmt.Sprintf("%s: 分类 %q 不在已声明集合里", where, p.Class))
	}
	if strings.TrimSpace(p.Note) == "" {
		problems = append(problems, where+": Note 为空 —— 空说明的登记等于没有登记")
	}
	if p.Exempt {
		if !canonicalExemptReasons[p.ExemptReason] {
			problems = append(problems, fmt.Sprintf("%s: 豁免理由 %q 不在已声明集合里", where, p.ExemptReason))
		}
	} else if strings.TrimSpace(p.ExemptReason) != "" {
		problems = append(problems, where+": 未豁免却写了豁免理由（两者必须一致）")
	}
	return problems
}

// ─────────────────────── 门 ───────────────────────

// TestCanonicalReadPointRegistryCoversEveryMentionedFile 守「**不许有没被
// 登记的 canonical 提及**」这条不变式。
//
// # 三个方向
//
//	① 扫描到的每个生产文件（只要它**提及**了 canonical 视图）必须已登记
//	   —— 新增一个 canonical 读点却没登记 ⇒ **红**。
//	② 登记的每个文件必须仍在扫描结果里 —— 文件删了/改名了/不再提及了
//	   ⇒ **红**（登记簿腐烂）。
//	③ 每条登记的自检（形态 / 条数 / 读点身份 / 分类 / 豁免）必须通过。
//
// # 阳性对照（防「扫描器坏了 ⇒ 本门恒绿」）
//
// 本门断言的是「扫描集 == 登记集」。若扫描器压根看不见 canonical 视图
// （模式写坏 / 路径写错），扫描集恒空 ⇒ ① 恒绿 ⇒ 本门变成空气。
// ⇒ 这里用**已知真实读点**钉住扫描器的区分力（见
// TestCanonicalReadPointScannerHasDiscriminatingPower）。
func TestCanonicalReadPointRegistryCoversEveryMentionedFile(t *testing.T) {
	root := repoRootFromCaller(t)
	scanned := scanCanonicalReadPoints(t, root)

	// ① 扫描到但没登记。
	var unregistered []string
	for file := range scanned {
		if _, ok := canonicalReadPointRegistry[file]; !ok {
			unregistered = append(unregistered, file)
		}
	}
	sort.Strings(unregistered)
	if len(unregistered) > 0 {
		t.Errorf("%d 个生产文件提及 canonical 视图（%s），但**不在** canonicalReadPointRegistry 里：\n  %s\n"+
			"⇒ 处置：登记它的形态 / 分类 / 豁免（分类必须**人工**核定，不要让规则替你决定）。",
			len(unregistered), canonicalRequestLogsView, strings.Join(unregistered, "\n  "))
	}

	// ② 登记了但扫描不到（登记簿腐烂）。
	var stale []string
	for file := range canonicalReadPointRegistry {
		if _, ok := scanned[file]; !ok {
			stale = append(stale, file)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("%d 个已登记的文件在扫描结果里**不存在**：\n  %s\n"+
			"⇒ 文件被删/改名，或它已经不再提及 canonical 视图 ⇒ 请把它从登记簿里删掉。",
			len(stale), strings.Join(stale, "\n  "))
	}

	// ③ 逐条自检。
	problems := 0
	for file, e := range canonicalReadPointRegistry {
		facts := scanned[file]
		for _, p := range canonicalEntryProblems(file, e, facts) {
			problems++
			t.Errorf("登记问题 %s: %s", file, p)
		}
	}
	t.Logf("canonical 视图读点登记簿：%d 个生产文件 / %d 个读点（豁免 %d 个）",
		len(canonicalReadPointRegistry), canonicalRegistryPointCount(), canonicalRegistryExemptCount())
	if problems == 0 {
		t.Logf("登记簿自检：全部合格")
	}
}

// TestCanonicalReadPointRegistryIsNotRot 专守「登记了但读点已经不在了」。
//
// ⚠ 为什么单列一道门：上一条的 ① 是「漏登」，本条是「**多登**」——
// 方向相反、失败形态也相反（① 让人以为「读点没人管」，本条让人以为
// 「读点已经改完了」）。两者的处置方向完全相反 ⇒ 必须分开报。
func TestCanonicalReadPointRegistryIsNotRot(t *testing.T) {
	root := repoRootFromCaller(t)
	scanned := scanCanonicalReadPoints(t, root)

	rot := 0
	for file, e := range canonicalReadPointRegistry {
		facts := scanned[file]
		if facts.Sites == nil && len(facts.ReadPoints) == 0 {
			rot++
			t.Errorf("登记簿腐烂：%s 已登记，但它在扫描结果里**不存在**（文件没了 / 不再提及视图）", file)
			continue
		}
		if len(e.Points) != len(facts.ReadPoints) {
			rot++
			t.Errorf("登记簿腐烂：%s 登记 %d 个读点，扫描实测 %d 个 —— "+
				"读点被删了（请改登记）或被加了（请补登记）",
				file, len(e.Points), len(facts.ReadPoints))
			continue
		}
		declared := map[string]int{}
		for _, p := range e.Points {
			declared[canonicalPointKey(p)]++
		}
		for _, p := range facts.ReadPoints {
			key := "read|" + strings.Join(p.Columns, ",")
			if p.Kind == "holder" {
				key = "holder|" + p.Text
			}
			if declared[key] == 0 {
				rot++
				t.Errorf("登记簿腐烂：%s 里有一个扫描到的读点（%s）**不在**登记里", file, key)
				break
			}
			declared[key]--
		}
	}
	if rot == 0 {
		t.Logf("登记簿无腐烂：%d 个文件的读点条数与身份逐条对上", len(canonicalReadPointRegistry))
	}
}

// TestCanonicalReadPointRegistryEntriesAreWellFormed 守「登记了但说不清」。
//
// ⚠ 空表 + 循环 = **恒绿**。⇒ 本门在**真实条目**上跑一遍自检之外，还用
// **合成条目**做阳性/阴性对照，证明自检逻辑本身有牙：
// 缺分类 / 缺说明 / 豁免理由不在枚举里 / 未豁免却写了理由 —— 必须被判出问题；
// 各项都合规的合成条目 —— 必须**不**被判出问题。
func TestCanonicalReadPointRegistryEntriesAreWellFormed(t *testing.T) {
	root := repoRootFromCaller(t)
	scanned := scanCanonicalReadPoints(t, root)

	// ① 真实条目：跑一遍自检（问题已由覆盖门报出，这里只统计）。
	real := 0
	for file, e := range canonicalReadPointRegistry {
		real += len(canonicalEntryProblems(file, e, scanned[file]))
	}
	if real != 0 {
		t.Errorf("登记簿里有 %d 条自检不通过的条目（详见 TestCanonicalReadPointRegistryCoversEveryMentionedFile）", real)
	}

	// ② 合成条目的阴性对照（各项合规 ⇒ 不得被判出问题）。
	okFacts := canonicalFileFacts{
		Form:  formDirectLiteralSQL,
		Sites: []canonicalReadPointSite{{Line: 1, Kind: "read"}},
		ReadPoints: []canonicalReadPointSite{{
			Line: 1, Kind: "read", Columns: []string{"request_id:base"},
		}},
	}
	okEntry := canonicalReadPointFile{
		Form:  formDirectLiteralSQL,
		Class: classNarrowMigratable,
		Points: []canonicalReadPoint{{
			Kind: "read", Columns: []string{"request_id:base"},
			Class: classNarrowMigratable, Note: "合成条目：各项合规",
		}},
		Note: "合成条目：各项合规",
	}
	if problems := canonicalEntryProblems("synthetic/ok.go", okEntry, okFacts); len(problems) != 0 {
		t.Errorf("一个各项都合规的合成条目被判出问题 %v —— 自检逻辑本身坏了", problems)
	}

	// ③ 合成条目的阳性对照（每一条缺陷都必须被抓到）。
	bad := map[string]struct {
		entry canonicalReadPointFile
		facts canonicalFileFacts
	}{
		"形态不在枚举": {
			entry: canonicalReadPointFile{
				Form: "not-a-form",
				Points: []canonicalReadPoint{{
					Kind: "read", Columns: []string{"request_id:base"},
					Class: classNarrowMigratable, Note: "合成条目",
				}},
				Note: "合成条目",
			},
			facts: okFacts,
		},
		"形态与实测不符": {
			entry: canonicalReadPointFile{
				Form: formHelperConcat,
				Points: []canonicalReadPoint{{
					Kind: "read", Columns: []string{"request_id:base"},
					Class: classNarrowMigratable, Note: "合成条目",
				}},
				Note: "合成条目",
			},
			facts: okFacts,
		},
		"读点条数对不上": {
			entry: canonicalReadPointFile{
				Form: formDirectLiteralSQL,
				Points: []canonicalReadPoint{{
					Kind: "read", Columns: []string{"request_id:base"},
					Class: classNarrowMigratable, Note: "合成条目",
				}},
				Note: "合成条目",
			},
			facts: canonicalFileFacts{
				Form:  formDirectLiteralSQL,
				Sites: []canonicalReadPointSite{{Line: 1, Kind: "read"}, {Line: 2, Kind: "read"}},
				ReadPoints: []canonicalReadPointSite{
					{Line: 1, Kind: "read", Columns: []string{"request_id:base"}},
					{Line: 2, Kind: "read", Columns: []string{"ts:base"}},
				},
			},
		},
		"读点身份对不上": {
			entry: canonicalReadPointFile{
				Form: formDirectLiteralSQL,
				Points: []canonicalReadPoint{{
					Kind: "read", Columns: []string{"ts:base"},
					Class: classNarrowMigratable, Note: "合成条目",
				}},
				Note: "合成条目",
			},
			facts: okFacts,
		},
		"分类不在枚举": {
			entry: canonicalReadPointFile{
				Form: formDirectLiteralSQL,
				Points: []canonicalReadPoint{{
					Kind: "read", Columns: []string{"request_id:base"},
					Class: "class-not-declared", Note: "合成条目",
				}},
				Note: "合成条目",
			},
			facts: okFacts,
		},
		"读点缺说明": {
			entry: canonicalReadPointFile{
				Form: formDirectLiteralSQL,
				Points: []canonicalReadPoint{{
					Kind: "read", Columns: []string{"request_id:base"},
					Class: classNarrowMigratable,
				}},
				Note: "合成条目",
			},
			facts: okFacts,
		},
		"文件级缺说明": {
			entry: canonicalReadPointFile{
				Form: formDirectLiteralSQL,
				Points: []canonicalReadPoint{{
					Kind: "read", Columns: []string{"request_id:base"},
					Class: classNarrowMigratable, Note: "合成条目",
				}},
			},
			facts: okFacts,
		},
		"豁免理由不在枚举": {
			entry: canonicalReadPointFile{
				Form: formDirectLiteralSQL,
				Points: []canonicalReadPoint{{
					Kind: "read", Columns: []string{"request_id:base"},
					Class: classNarrowMigratable, Exempt: true,
					ExemptReason: "not-a-reason", Note: "合成条目",
				}},
				Note: "合成条目",
			},
			facts: okFacts,
		},
		"豁免但缺理由": {
			entry: canonicalReadPointFile{
				Form: formDirectLiteralSQL,
				Points: []canonicalReadPoint{{
					Kind: "read", Columns: []string{"request_id:base"},
					Class: classNarrowMigratable, Exempt: true, Note: "合成条目",
				}},
				Note: "合成条目",
			},
			facts: okFacts,
		},
		"未豁免却写了理由": {
			entry: canonicalReadPointFile{
				Form: formDirectLiteralSQL,
				Points: []canonicalReadPoint{{
					Kind: "read", Columns: []string{"request_id:base"},
					Class: classNarrowMigratable, ExemptReason: exemptNoReadPoint, Note: "合成条目",
				}},
				Note: "合成条目",
			},
			facts: okFacts,
		},
		"文件已不存在": {
			entry: canonicalReadPointFile{
				Form:   formDirectLiteralSQL,
				Points: []canonicalReadPoint{},
				Note:   "合成条目",
			},
			facts: canonicalFileFacts{},
		},
	}
	for name, c := range bad {
		if problems := canonicalEntryProblems("synthetic/"+name+".go", c.entry, c.facts); len(problems) == 0 {
			t.Errorf("合成条目「%s」没有被自检逻辑判出问题 —— 自检逻辑没有牙", name)
		}
	}
}

// TestCanonicalReadPointScannerHasDiscriminatingPower 是扫描器的**阳性/阴性对照**。
//
// ⚠ 没有这一条，上面两道门可能因为「扫描器什么都没看见」而恒绿 ——
// 那是最坏的失败形态：门是绿的，而生产照样在读那张 118 列的宽视图。
func TestCanonicalReadPointScannerHasDiscriminatingPower(t *testing.T) {
	// ① 阳性对照：真 canonical 读点必须被识别为读点。
	for _, s := range []string{
		"SELECT request_id FROM request_logs_with_current_month WHERE tenant_id = $1",
		"SELECT rl.request_id FROM request_logs_with_current_month rl",
		"JOIN request_logs_with_current_month rl ON rl.request_id = x.request_id",
		"FROM ONLY public.request_logs_with_current_month rl",
	} {
		off := canonicalMentionOffsets(s)
		if len(off) == 0 {
			t.Errorf("扫描器在 %q 上找不到 canonical 视图名", s)
			continue
		}
		if !canonicalReadPrefixRE.MatchString(s[:off[0]]) {
			t.Errorf("扫描器没把 %q 认成读点（FROM/JOIN 读位置没命中）", s)
		}
	}

	// ② 阴性对照：**已退役**的 v1 包装视图不是 canonical 视图读点。
	for _, s := range []string{
		"SELECT * FROM request_logs_with_current_month_without_customer_id",
		"SELECT * FROM request_logs_with_current_month_without_request_class_due_at",
		"SELECT * FROM request_logs_bodies_with_current_month",
	} {
		if off := canonicalMentionOffsets(s); len(off) != 0 {
			t.Errorf("扫描器把 %q 认成 canonical 视图读点 —— 标识符边界判定坏了", s)
		}
	}

	// ③ 阴性对照：会话族底表与 v1 族都不是 canonical 视图。
	for _, s := range []string{
		"SELECT * FROM public.session_turns_hot t",
		"SELECT * FROM public.session_turns t",
		"SELECT * FROM _request_logs_hot rl",
	} {
		if off := canonicalMentionOffsets(s); len(off) != 0 {
			t.Errorf("扫描器把 %q 认成 canonical 视图读点", s)
		}
	}

	// ④ 阴性对照：SQL 注释里的视图名不是读点。
	clean := canonicalStripSQLComments("SELECT 1 -- FROM request_logs_with_current_month\n")
	if canonicalMentionSurvives(clean, 10) {
		t.Error("SQL 注释里的视图名被算成了读点 —— 注释不是读点")
	}

	// ⑤ 阳性对照：扫描器在**真实仓库**里必须看得见已知的 canonical 读点，
	// 且必须**看不见**只在注释里提及的文件。
	root := repoRootFromCaller(t)
	scanned := scanCanonicalReadPoints(t, root)

	knownReaders := []string{
		"admin/logs_summary.go",
		"admin/work_types.go",
		"bg/passive_probe_listener.go",
		"domains/sessionsummary/summarizer.go",
	}
	for _, f := range knownReaders {
		facts, ok := scanned[f]
		if !ok {
			t.Errorf("扫描器在真实仓库里找不到 %s ⇒ 扫描路径/模式坏了（上面两道门会恒绿）", f)
			continue
		}
		if facts.Form != formDirectLiteralSQL {
			t.Errorf("%s 的形态实测为 %q，期望 %q", f, facts.Form, formDirectLiteralSQL)
		}
		if len(facts.ReadPoints) == 0 {
			t.Errorf("%s 实测 0 个读点 ⇒ 扫描器坏了", f)
		}
	}

	// ⑥ 阴性对照：只把视图名写在注释里的文件**不得**被算成读点文件。
	for _, f := range []string{
		"admin/logs.go",
		"admin/session_list.go",
		"admin/logs_search_predicate.go",
	} {
		facts, ok := scanned[f]
		if !ok {
			continue // 该文件已经不再提及视图 ⇒ 不在扫描集里，也算通过
		}
		if len(facts.ReadPoints) != 0 {
			t.Errorf("%s 只在注释里提及 canonical 视图，却实测出 %d 个读点", f, len(facts.ReadPoints))
		}
		if facts.Form != formCommentOrStringOnly {
			t.Errorf("%s 的形态实测为 %q，期望 %q（只在注释/字符串里提及）",
				f, facts.Form, formCommentOrStringOnly)
		}
	}
}

// ─────────────────────── 登记簿读数 ───────────────────────

func canonicalRegistryPointCount() int {
	n := 0
	for _, e := range canonicalReadPointRegistry {
		n += len(e.Points)
	}
	return n
}

func canonicalRegistryExemptCount() int {
	n := 0
	for _, e := range canonicalReadPointRegistry {
		for _, p := range e.Points {
			if p.Exempt {
				n++
			}
		}
	}
	return n
}

// ── R173 守卫：canonical 视图的「恒 NULL 列」这一族必须保持为空 ──────────
//
// ★ 背景（事实源）：canonical 视图 `request_logs_with_current_month` 的第一列
//   是 `NULL::bigint AS id` —— v1 遗留列，**视图给不出真值**（实测 2758 行
//   全为 NULL）。⇒ 读它的读点「读到的 id 永远是 NULL」⇒
//   ★ 按登记簿自己的分类口径：★ 这不是「迁移候选」，这是**缺陷**。
//
// ★ R173 的动作：把这一族（3 个读点：`export.go` 的两份导出 SQL ×2 +
//   `summarizer.go` 的标题解析 ×1）的 `id` 依赖去掉，并把**死次序键**
//   （`ORDER BY …, id` ⇒ 次序键恒 NULL ⇒ 并列时顺序随机）换成稳定键
//   `request_id`（实测非空、视图内唯一）。
//
// ★★ 本门是**棘轮**：这一族一旦重新出现（无论是登记簿里还是源码里）⇒ 立刻红。
//
// ★★ ★★ **判据抽成纯函数**（`nullPaddedReadPointHits`）★★ ★★
// ⇒ 好让「阳性对照」能用**合成数据**证明判据本身有牙
// ⇒ ★ 否则「0 命中」有两种读法（扫到了没发现 / 根本没扫到那一段）。

// nullPaddedReadPointHits 返回「读到了 canonical 视图恒 NULL 列」的读点清单。
//
// ★ 入参是扫描器实测的结果（不是登记簿）⇒ ⇒ **登记簿写错也不会掩盖源码事实**。
func nullPaddedReadPointHits(scanned map[string]canonicalFileFacts) []string {
	var hits []string
	for file, facts := range scanned {
		for _, p := range facts.ReadPoints {
			for _, c := range p.Columns {
				// ★ 扫描器给列名标了来源；`:NULL` = 视图里**恒 NULL** 的填充列。
				if strings.HasSuffix(c, ":NULL") {
					hits = append(hits, fmt.Sprintf("%s:%d 读 %s（列 %s 是视图里恒 NULL 的填充列）",
						file, p.Line, c, c))
					break
				}
			}
		}
	}
	sort.Strings(hits)
	return hits
}

func TestNoCanonicalReadPointReadsNullPaddedColumn(t *testing.T) {
	root := repoRootFromCaller(t)
	scanned := scanCanonicalReadPoints(t, root)

	// ① 实测侧：源码里不得再有读点读 canonical 的恒 NULL 列。
	//    ★ 这条**不看登记簿** ⇒ 登记簿写漏了也照样抓得到。
	if hits := nullPaddedReadPointHits(scanned); len(hits) != 0 {
		for _, h := range hits {
			t.Errorf("有读点又读到了 canonical 视图里**恒 NULL** 的列：%s\n"+
				"  ⇒ 视图给不出真值 ⇒ 该读点读到的永远是 NULL ⇒ ★ 这是缺陷不是迁移候选\n"+
				"  ⇒ 请改读 request_id（实测非空、视图内唯一）并同步登记簿",
				h)
		}
	}

	// ② 登记簿侧：这一族不许重新出现（文件级 + 读点级）。
	for file, e := range canonicalReadPointRegistry {
		if e.Class == classReadsNullPaddedColumn {
			t.Errorf("登记簿里 %s 的文件级分类又是 %q ⇒ 有读点回到了「读恒 NULL 列」这一族",
				file, classReadsNullPaddedColumn)
		}
		for _, p := range e.Points {
			if p.Class == classReadsNullPaddedColumn {
				t.Errorf("登记簿里 %s 的读点 %v 又是 %q ⇒ 同上", file, p.Columns, classReadsNullPaddedColumn)
			}
		}
	}

	// ③ 源码形态：两个读点文件不得再依赖 canonical 的 `id` 列。
	//    ★ SQL 片段用【拼接】写出来 ⇒ 扫描器不会把本门自己当成一个读点。
	expFile := filepath.Join(root, "domains", "sessionforensics", "export.go")
	sumFile := filepath.Join(root, "domains", "sessionsummary", "summarizer.go")
	expSrc, err := os.ReadFile(expFile)
	if err != nil {
		t.Fatalf("read %s: %v", expFile, err)
	}
	sumSrc, err := os.ReadFile(sumFile)
	if err != nil {
		t.Fatalf("read %s: %v", sumFile, err)
	}
	expText := string(expSrc)
	sumText := string(sumSrc)

	// ★ 判据 ③a：`rl.id` / 裸 `id` 出现在 ORDER BY 里 ⇒ 死次序键回来了。
	badOrderExp := "ORDER BY rl.ts ASC, rl." + "id"
	if strings.Contains(expText, badOrderExp) {
		t.Errorf("%s 里又有 `%s` ⇒ 视图的 id 恒 NULL ⇒ 次序键是死的 ⇒ 并列时顺序随机",
			expFile, badOrderExp)
	}
	badOrderSum := "ORDER BY ts DESC, " + "id"
	if strings.Contains(sumText, badOrderSum) {
		t.Errorf("%s 里又有 `%s` ⇒ 视图的 id 恒 NULL ⇒ 同 ts 并列取哪一行随机",
			sumFile, badOrderSum)
	}
	// ★ 判据 ③b：读出来的 request_id 必须**真的落进导出消息**
	//    ⇒ 否则就是「读出来又丢掉」（R173 修掉的原症状）。
	//    ★★ 用【逐字正则】而不是「含 RequestID: 且含 requestID」——
	//    ★★ 后者会让 `RequestID: strings.TrimSpace(requestID)` 这类
	//    「把真值加工掉」的写法逃过门（变异臂 M4 就是这种形态）。
	//    ★★★ 而【逐字正则】本身也不够强：★★★ 「整文件里至少有一处匹配」
	//    ★★★ 会被「**两处只坏一处**」逃过（export.go 有两份 SQL 变体、两个构造点）
	//    ★★★ ⇒ ⇒ **判据的粒度必须与被测对象的粒度一致**：
	//    ★★★ **每一个 `msg := ExportMessage{` 构造都必须带 `RequestID: requestID,`。**
	//    ★★★ （★ 这是 `TestForensicsExportSQLVariantsStayInSync` 的同族：
	//    ★★★ 两份变体必须同步，★ 判据也必须**逐变体**地查。）
	nWanted := strings.Count(expText, "msg := ExportMessage{")
	nGot := len(regexp.MustCompile(`RequestID:\s+requestID,`).FindAllString(expText, -1))
	if nWanted == 0 {
		t.Errorf("%s 里找不到 `msg := ExportMessage{` 构造点 ⇒ 判据本身失效（源码形态变了？）", expFile)
	} else if nGot != nWanted {
		t.Errorf("%s 里 %d 个 ExportMessage 构造中只有 %d 个把 request_id **原样**落进去"+
			"（期望 `RequestID: requestID,` 各一处）⇒ 导出消息仍然没有轮次身份、或者落进去的是被加工过的值",
			expFile, nWanted, nGot)
	}

	// ④ 阳性对照：★ 判据本身必须对**合成的**坏数据判红 ⇒ 否则「0 命中」是恒绿。
	synthetic := map[string]canonicalFileFacts{
		"synthetic/bad.go": {ReadPoints: []canonicalReadPointSite{{
			Line: 1, Kind: "read", Columns: []string{"id:NULL", "request_id:base"},
		}}},
	}
	if hits := nullPaddedReadPointHits(synthetic); len(hits) != 1 {
		t.Errorf("合成的一条「读了恒 NULL 列」的读点被判出 %d 条（应 1 条）⇒ 判据没牙",
			len(hits))
	}
}

// ─────────────────────── R174 · canonical 视图「无法表达分区裁剪」 ───────────────────────
//
// ★ 这道门守的是一条**结构性缺陷**，不是「某个读点写得不好」：
//
// ★ canonical 视图（`request_logs_with_current_month`）的**投影列里没有
// `partition_date`** ⇒ ⇒ 任何经它做的读点都**无法表达分区下界**
// ⇒ ⇒ PG 无法做**分区裁剪** ⇒ ⇒ 父表分支要把**全部分区**扫一遍。
//
// ★ 252 生产规模实测（只读；`ts >= 2026-01-01` + `tenant_id='default'`）：
//
//	形态                                          Execution(热)   planBuf
//	A canonical 视图                                 1535 / 1306 ms     125
//	B 两臂窄投影（无分区下界）                        1008 / 1073 ms     246
//	C 两臂 + `partition_date >= '2026-01-01'`（下界太宽 ⇒ 裁不到）  2760 ms
//	F 两臂 + `partition_date >= '2026-10-01'`（裁到 1 个分区）       406 ms
//	H3 只父表 + **无**分区下界                                     845 ms
//
// ⇒ ★★ **最优形态比 canonical 视图快 3.2×（1306 → 406 ms）** ⇒ ★★
// ⇒ ★★ **★ 但 H3（无下界 845 ms）比 H2（加了宽下界 1763 ms）更快** ⇒ ★★
// ⇒ ★★ **★ ⇒ 「随便加个分区下界」可能【更慢】（planner 换了更差的计划）★ ★★**
// ⇒ ★★ ⇒ **★ 分区下界必须【逐读点实测】，不能一刀切。★ ★★**
//
// ★ ★★ **为什么这道门是「双向有牙」的 ★★**
// ★ 判据① 断言「事实源的投影列里【没有】`partition_date`」⇒ ⇒
// ★   · 现在：读点无法分区裁剪 ⇒ ⇒ 这个分类成立；
// ★   · 将来：**一旦有人给视图补上 `partition_date` 投影** ⇒ ⇒ 读点就能分区裁剪
// ★     ⇒ ⇒ **门会红** ⇒ ⇒ 【届时这个分类就该退役】⇒ ⇒ ★★
// ★   ⇒ ★★ **★ 「现在不能」与「将来能」都被同一道门盯着 ⇒ ★★
//
// ★ ★★ **为什么判据③ 要【对拍】而不是【只查登记簿】★★**
// ★ 只查登记簿 ⇒ 登记漏了就是假绿；只查实测 ⇒ 没人知道该改哪里 ⇒ ⇒ **两侧都要**。
// ★ 实测口径：**文件级**（★ 不是一个读点一个读点）—— ★
// ★   · 该文件在登记簿里的 Form 不是「只在注释里提及」/「视图定义探针」/「holder」
// ★     ⇒ ⇒ 它**有**真实的生产读点（★ `formSwitchLayer` 的文件也算：★ 它们的
// ★       SQL 字面量就在本文件里，只是 FROM 走切换层 helper）；
// ★   · 且该**整份文件**的文本里出现 ts 的**范围**谓词（★ 整文件匹配 ⇒ 跨行安全
// ★     ⇒ 不受「谓词在下一行」影响）。
// ★ ★★ **为什么不用「逐读点」粒度**：★ 扫描器给的 read site 的 `Text` 只是
// ★ **所在行** ⇒ ⇒ 跨行的 ts 谓词会漏 ⇒ ⇒ 逐读点实测会【假阴性】⇒ ⇒
// ★ ★★ **粒度必须与【量具能真正看到的东西】对齐**（★ 这是 R173「判据粒度必须与
// ★ 被测对象粒度一致」的同族）。

// canonicalViewFactsSource 是 canonical 视图定义的**仓内单一事实源**。
//
// ★ 它**就是**部署形态（★ 890 迁移把它从「v1 回退体」改成了「会话族两臂体」，
// ★ 与 composer 装到库里的形态逐字相同）⇒ ⇒ ★★ **门可以静态断言它，不必连库。★★**
const canonicalViewFactsSource = "sql/objects/views/request_logs_with_current_month.sql"

// canonicalTSWindowRe 认 ts 的**范围**谓词（★ 不是等值）。
//
// ★ 词边界必须精确：★ `ts` 是独立词 ⇒ 不命中 `timestamps` / `start_ts` 之类。
var canonicalTSWindowRe = regexp.MustCompile(`(?i)\bts\b\s*(>=|<>|<|>|<=)|(?i)\bBETWEEN\b[^;()]*\bts\b`)

// canonicalViewProjectionColumnSet 从视图定义 SQL 里抽出**投影列集合**。
//
// ★ 只看「投影」，不看 JOIN 条件 ⇒ ⇒ 视图定义里的
// `ON d.partition_date = t.partition_date` **不算**投影列（★ 它是 JOIN 键）。
// ★ 判据要问的是「读点能不能 SELECT 到这一列」，不是「视图内部用不用这一列」。
func canonicalViewProjectionColumnSet(sqlText string) map[string]bool {
	cols := map[string]bool{}
	for _, line := range strings.Split(sqlText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		// 投影形状：`<expr> AS <col>,` 或 `<expr> AS <col>`
		i := strings.LastIndex(strings.ToUpper(line), " AS ")
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[i+4:])
		name = strings.TrimSuffix(name, ",")
		name = strings.TrimSpace(name)
		// 只认「裸标识符」形态的列名（★ `NULL::bigint AS id` ⇒ `id`）。
		if name == "" {
			continue
		}
		for _, r := range name {
			if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				name = ""
				break
			}
		}
		if name == "" {
			continue
		}
		cols[name] = true
	}
	return cols
}

// canonicalTimeWindowedReadPointFiles 实测出「带 ts 时间窗口且有真实生产读点」的文件集合。
//
// ★ 口径与上面头注释里的「为什么判据③ 是文件级」一致。
func canonicalTimeWindowedReadPointFiles(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	scanned := scanCanonicalReadPoints(t, root)
	for path, facts := range scanned {
		// ① 该文件必须**有真实生产读点**。
		//
		// ★★ **★ 第一版口径写成「只要 read 形态」⇒ ⇒ 把【holder 形态】排除了 ⇒ ⇒
		// ★★ 【门红】并指出「admin/usage_credits.go 登记为受影响但实测不满足」⇒ ⇒
		// ★★ ⇒ ★★ **★ 那是【登记方】错了，不是【实测方】错了** ⇒ ⇒ ★★★
		// ★★ ★★ 因为 holder 形态**也是真实读点**：★ `formSwitchLayer` 的文件
		// ★★ （`admin/usage_credits.go` / `admin/usage_enhanced.go` /
		// ★★  `admin/usage_trend_series.go`）的读点是 Go 字符串持有视图名，
		// ★★ **而它们的 ts 谓词就在本文件里**（FROM 子句是本文件拼的）⇒ ⇒
		// ★★ ★★ **★ 而且它们恰恰是本缺陷最刺眼的活标本**：★
		// ★★ 「≤7 天走会话族热腿原生源，>7 天走 canonical 视图」⇒ ⇒
		// ★★ ★★ **★ 「查 30 天」这种最常见的运营查询，恰好落在【>7 天 ⇒ 走
		// ★★ ★★ canonical 视图 ⇒ ⇒ 无法分区裁剪 ⇒ ⇒ 秒级】的那一支上。★★ ★★**
		//
		// ★★ ⇒ 口径修正为：**有 read 或 holder 读点** ⇒ ★★ 且 ★★ 用【扫描器实测的
		// ★★ Form】做白名单（★ 不依赖登记簿 ⇒ ★★ 避免循环依赖 ⇒ ★★ 而且登记簿的
		// ★★ Form 已被另一道门 `TestCanonicalReadPointRegistryIsNotRot` 钉住）★★
		hasRealRead := false
		for _, p := range facts.ReadPoints {
			if p.Kind == "read" || p.Kind == "holder" {
				hasRealRead = true
				break
			}
		}
		if !hasRealRead {
			continue
		}
		switch facts.Form {
		case formDirectLiteralSQL, formHelperConcat, formSwitchLayer:
			// ★ 这三种形态的文件里，读点是真的、ts 谓词也在本文件里 ⇒ 纳入。
		default:
			// ★ 「只在注释里提及」/「视图定义探针」/「视图 DDL 定义」⇒
			// ★ 它们的 ts 谓词与「按时间窗口读」无关（★ 例如 `db/db.go` 的探针
			// ★ 是「查视图里有哪些列」）⇒ ⇒ 不纳入。
			continue
		}
		// ② 该文件整份文本里出现 ts 的范围谓词（★ 跨行安全）。
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			continue
		}
		if canonicalTSWindowRe.Match(b) {
			out[path] = true
		}
	}
	return out
}

// canonicalTimeWindowedReadPoints 登记「【带 ts 时间窗口】且【直接读 canonical 视图】」的生产文件，
// 以及**人工核定**的每一条理由（★ 空说明的登记等于没有登记）。
//
// ★ ★★ **这张清单是【可排序的缺陷台账】**：★
// ★   · 它列出所有「经 canonical 视图按时间窗口读」的读点面 ⇒ ⇒
// ★     **这正是「迁到两臂直查 + 带 partition_date 下界」的候选集** ⇒ ⇒
// ★   · 而迁移的收益**已被实测钉住**：★ 3.2×（1306 → 406 ms）⇒ ★
// ★   · ★★ **但下界必须逐读点实测**（★ H3 845 ms vs H2 1763 ms ⇒ 宽下界反而更慢）★★
//
// ★ ★★ **登记与实测必须【逐文件相等】**：★ 漏登记 ⇒ 门红；多登记 ⇒ 门红。
// ★ ★★ 登记时若认为某文件「虽然有 ts 谓词，但那个谓词与 canonical 读点无关」⇒
// ★   ⇒ 那是**登记错误**（★ 口径是文件级）⇒ ⇒ 要么改口径、要么把它移出清单 ⇒
// ★   ⇒ ★★ **不许用「例外」把它藏起来**：★ 藏起来就等于门看不见它。★★
var canonicalTimeWindowedReadPoints = map[string]string{
	"admin/compression_stats.go":             "压缩带统计（token_band / success / ts）；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/credential_monitor.go":            "凭据监控：① live P95 回退；② 按凭据统计行数；③ 滑窗明细；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/credential_monitor_heatmap.go":    "凭据 × 模型热力图；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/credential_success_rate.go":       "凭据成功率；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/data_lifecycle.go":                "数据生命周期：① 7 天趋势；② 清理预览的受影响行数（不取列，只要 COUNT(*)）；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/diagnostics_credential.go":        "凭据诊断明细；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/live_stream_sse.go":               "SSE 实时流：① 全量事件行；② 按 request_id 数组取状态；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/memora_handlers.go":               "memora 上下文：① 任务/会话头；② 最近一条模型；③ 计数；④ 轮次明细；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/model_routing_diagnostic.go":      "模型路由诊断的 24h 统计；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/model_status.go":                  "模型状态统计；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/no_topic_session.go":              "无主题会话面：① 轮次明细；② 会话日志摘要；③ 按 request_id 取 api_key_id/tenant_id；④ 取 preview/work_type/request_mode；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/probe_history.go":                 "探针历史；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/provider_models.go":               "供应商模型面：① COUNT(*) 统计（不取列）；② 明细；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/route_incidents.go":               "路由事故面；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/session_analytics_breakdown.go":   "会话分析维度下钻：① 按模型；② 按供应商；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/session_analytics_timeseries.go":  "会话分析时间序列：① 会话/请求/成本；② 成本拆分；③ 延迟分位；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/swim_lane_init.go":                "泳道初始化；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/tenants.go":                       "租户统计（tenant_id / tokens / cost / credits_charged）；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/top_problems.go":                  "Top 问题：① 按凭据；② 按模型；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/topology_clients_today.go":        "头部统计客户端组（单一统计轮）：request_logs 直查按客户端聚合；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/topology_window_stats.go":         "头部统计短窗口口径（?window=5m|1h）：① 供应商聚合；② 模型聚合；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/usage.go":                         "用量面：① 失败数；② 失败钻取；③ 5 分钟分桶；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/usage_credits.go":                 "requestLogsFromClause(days) 按天数分臂：≤7 天走会话族热腿原生源，>7 天走 canonical 视图 ⇒ 切换层契约；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/usage_enhanced.go":                "planCostTrend() 按 groupBy 在计费宽表与请求表（canonical 视图）之间选基表 ⇒ 切换层契约；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/usage_trend_series.go":            "usageTrendSource() 三臂切换（api_key 过滤 ⇒ canonical 视图明细面；provider 过滤 ⇒ minute 汇总；默认 ⇒ dim 汇总）；另两处是按该返回值 dispatch 的 case 标签 ⇒ 同一处切换层；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"admin/work_types.go":                    "工作类型分布（work_type / is_auto_request / ts / canonical_model）；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"bg/daily_probe_audit.go":                "探针每日审计（离线诊断面，不在线上读路径上）；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"bg/integrity_fingerprint_drift.go":      "指纹漂移审计（离线诊断面）；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"bg/passive_probe_listener.go":           "被动探针监听器（探针系统后台面，不在管理台读路径上）；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"bg/stats_minute_rollup.go":              "离线分钟汇总（读视图只为把窗口内的行汇进汇总表）；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"bg/stats_minute_rollup_retire.go":       "离线分钟汇总退役（DELETE 的子查询用视图定位要清理的窗口）；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"db/probe_views_unified.go":              "探针视图的**生产者**（CREATE OR REPLACE VIEW v_model_health_dashboard）⇒ 它是视图的下游聚合面，不是终端读点；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"domains/routeincident/store.go":         "路由事故 store；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"domains/sessionsummary/summarizer.go":   "★ **已修（R173）**：标题解析读点原 `ORDER BY ts DESC, id DESC`，而视图的 `id` 恒 NULL ⇒ 同 ts 的并列行取哪一行随机 ⇒ ★ 现改为 `ORDER BY ts DESC, request_id DESC`（稳定键）；另两个读点取 reque…；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"internal/collector/gateway_adapters.go": "collector 适配：① 延迟分位；② 模型计数；③ 30 秒计数；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"internal/quality/minute_aggregator.go":  "离线分钟聚合（provider_id 走 details 腿，缺 details 行的轮次被谓词排除）；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
	"maas/usage.go":                          "requestLogsSource(days) 在 890 之后**恒**返回 canonical 视图（原 days>7 分臂已合并，分流本身已无意义）⇒ 源仍由 helper 决定 ⇒ 归切换层裁决；★ 该文件有 ts 范围谓词且【直接读 canonical 视图】⇒ ⇒ 经该视图读时【无法】表达分区下界 ⇒ ⇒ 受本缺陷影响",
}

// TestCanonicalViewCannotExpressPartitionPruning 是【行为判据】。
//
// ★ 四道判据：
// ① 事实源（仓内 SQL）里 canonical 视图的**投影列没有 `partition_date`**
//
//	⇒ ⇒ 读点无法表达分区下界 ⇒ ⇒ 【这就是缺陷的根因】；
//
// ② 登记清单非空（★ 防「登记 0 条 ⇒ 门变瞎」）；
// ③ 登记清单与实测集合**逐文件相等**（★ 漏登记 / 多登记都红）；
// ④ 阳性对照：★ 合成「视图定义带 `partition_date` 投影」⇒ 必须判红。
func TestCanonicalViewCannotExpressPartitionPruning(t *testing.T) {
	root := repoRootFromCaller(t)

	// ── 判据①：事实源的投影列里没有 partition_date ──
	sqlPath := filepath.Join(root, canonicalViewFactsSource)
	b, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatalf("读事实源失败：%v", err)
	}
	cols := canonicalViewProjectionColumnSet(string(b))
	if len(cols) == 0 {
		t.Fatalf("★ 从事实源 %s 没抽到任何投影列 ⇒ ⇒ 提取器坏了 ⇒ ⇒ 本门判红（★ 而不是判绿）",
			canonicalViewFactsSource)
	}
	if cols["partition_date"] {
		t.Fatalf("★ 事实源 %s 的投影列里【已经有】`partition_date` ⇒ ⇒ 读点【可以】做分区裁剪了 ⇒ ⇒ "+
			"★ 【「无法表达分区裁剪」这个分类就该退役】⇒ ⇒ 请把相关登记从 canonicalTimeWindowedReadPoints 里移除，"+
			"★ 并把本门改成断言「读点必须带 partition_date 下界」。★★", canonicalViewFactsSource)
	}

	// ── 判据②：登记清单非空 + 每条理由非空 ──
	if len(canonicalTimeWindowedReadPoints) == 0 {
		t.Fatalf("★ canonicalTimeWindowedReadPoints 是空的 ⇒ ⇒ 这道门【看不见任何读点】⇒ ⇒ 门变瞎了 ⇒ ⇒ " +
			"★ 请把它填上（★ 判据③ 会逐文件对拍实测集合）")
	}
	for path, why := range canonicalTimeWindowedReadPoints {
		if strings.TrimSpace(why) == "" {
			t.Fatalf("★ 登记 %s 的理由是空的 ⇒ ⇒ 空说明的登记等于没有登记 ⇒ ⇒ 请写清「为什么这个文件受影响」", path)
		}
	}

	// ── 判据③：登记集合 == 实测集合（逐文件） ──
	measured := canonicalTimeWindowedReadPointFiles(t, root)
	for path := range measured {
		if _, ok := canonicalTimeWindowedReadPoints[path]; !ok {
			t.Fatalf("★ %s 【实测】带 ts 范围谓词且有真实生产读点 ⇒ ⇒ 它受「canonical 视图无法分区裁剪」影响 ⇒ ⇒ "+
				"★ 但它【没在】canonicalTimeWindowedReadPoints 里登记 ⇒ ⇒ 门红（★ 漏登记 = 缺陷被藏起来）", path)
		}
	}
	for path := range canonicalTimeWindowedReadPoints {
		if !measured[path] {
			t.Fatalf("★ %s 【登记】为受影响，但【实测】不满足（★ 要么它没有真实生产读点，要么它整份文件里没有 ts 范围谓词）⇒ ⇒ "+
				"★ 登记与事实不符 ⇒ ⇒ 门红（★ 多登记 = 把不相关的东西塞进台账，★ 会稀释台账）", path)
		}
	}

	// ── 判据④：阳性对照（★ 合成坏数据必须判红） ──
	// ★ 合成一个「投影里带 partition_date」的视图定义 ⇒ 提取器必须【认得出】。
	synthetic := "SELECT NULL::bigint AS id,\n\t\tt.partition_date AS partition_date,\n\t\tt.ts AS ts\n\tFROM public.session_turns t"
	if !canonicalViewProjectionColumnSet(synthetic)["partition_date"] {
		t.Fatalf("★★ 阳性对照失败：★ 合成视图定义里的 `partition_date` 投影没被提取器认出来 ⇒ ⇒ " +
			"★ 提取器【对 partition_date 失明】⇒ ⇒ ⇒ **判据① 就是恒绿的 ⇒ ⇒ 本门没有牙齿** ⇒ ⇒ " +
			"★ 请修提取器（★ 注意：★ JOIN 条件里的 `d.partition_date = t.partition_date` 【不算】投影列）★★")
	}
}
