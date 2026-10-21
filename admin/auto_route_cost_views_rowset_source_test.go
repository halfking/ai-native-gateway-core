package admin

// auto_route_cost_views_rowset_source_test.go —— L11-b 登记门：
// 「auto-route 成本仪表盘两个视图的**行集来源**仍是 v1 冻结聚合表」
// 这条**已知缺陷**的登记册 + 形状钉。
//
// ─────────────────────────────────────────────────────────────────────────────
// 缺陷（2026-10-21 实测：生产 252 只读 + 本地真库只读）
//
// 迁移 883 把两个「活指标」的**取值来源**从 v1 冻结族换到了会话族：
//
//	· customer_cost_view.active_concurrent      （5 分钟窗实时并发）
//	· model_cost_per_task_view.avg_latency_ms   （7 天窗平均延迟）
//
// 但视图的**行集**仍由 `public.api_key_model_cost` 决定 —— 而这张表的
// **写方（v1 语句级触发器 update_api_key_model_cost）已随迁移 890 删除**。
// ⇒ 两个视图 = 「行集来自一张冻结表」+「取值来自会话族」⇒
//
//	· 现在真正在跑 auto 流量的 API key ⇒ 视图里**没有它的行**；
//	· 视图里有的行 ⇒ 是冻结那一刻的快照。
//
// ⇒ 活指标在一个**活着的特性**上恒为 0/空，且**没有任何错误信号**。
//
// 实测读数（生产 252，只读）：
//
//	· api_key_model_cost：352 行，bucket 2026-06-14 23:40 ~ 2026-06-19 03:30
//	  （+08）⇒ 冻结约 3.5 个月（写方随 890 删除）；
//	· customer_cost_view：5 行，api_key_id = 1,10,11,17,29；
//	  cost_usd_24h 全 0、active_concurrent 全 0、last_request_at 停在 6 月；
//	· model_cost_per_task_view：0 行（7 天窗 vs 6 月的 bucket）；
//	· 会话族的 auto 流量**活着**：session_turns 里 is_auto_request=true
//	  共 463,932 行（2026-09-15 ~ 2026-10-17）；
//	· 近 30 天真正在跑 auto 的 api_key_id = 3,6,7,89,105,114,199
//	  ⇒ 与视图行集 {1,10,11,17,29} **零交集**。
//
// ⚠ 与既有门 `auto_route_cost_views_session_source_test.go` 的关系：
// 那道门**造夹具**（它自己往 api_key_model_cost 里插外层行）⇒ 它证明的是
// 「读源已切、匹配键与谓词正确」，**证明不了**「行集是活的」——
// 它的注释其实写明了「外层聚合表零行时子查询根本不会执行」。
// ⇒ 两道门互补，**不是重复**：那道守"取值来源"，本门守"行集来源"。
//
// ─────────────────────────────────────────────────────────────────────────────
// 本门守什么（为什么是「登记」而不是「断言应该修好」）
//
// 修法有三个候选，**每个都改产品语义** ⇒ 属主拍板：
//
//	(a) 在会话族上复活写侧触发器（写热路径 + 写放大 ⇒ 需独立一轮）；
//	(b) 让视图直接聚合会话族（但 score_smart / pressure_ratio /
//	    concurrency_limit 这些**决策列**只存在于那张聚合表 ⇒ 要么丢、
//	    要么改读 credential_model_index ⇒ 那是另一套语义）；
//	(c) 明确废弃这两个端点（前端 RoutingDashboardView.vue:364-365 在消费）。
//
// ⇒ 本门**不替属主选**。它只保证两件事：
//
//	① 仓内「以冻结表作为行集来源」的视图集合 **== 登记册**（双向、且**按形态**）：
//	   多一个视图 ⇒ 红（缺陷在静悄悄扩散）；登记与形状不符 ⇒ 红。
//	② 形态一旦被改（行集来源换了）⇒ 登记册与形状不符 ⇒ 红
//	   ⇒ 逼出一次**显式重判**（真修好了就把登记改成「已修」，别让它悄悄过期）。
//
// ★★ **「按形态」这三个字不是讲究，是被变异逼出来的**：本门第一版只登记
// 「这个视图的行集来源是冻结表」这一个集合 ⇒ **M1/M2 两条变异都是绿的**
// （改掉 883 那份，基线那份还在 ⇒ 集合没变）⇒ 门会放过形态被改。
// ⇒ ★ **变异的价值就是把「文档里写的意图」与「代码实际断言的东西」对齐**。
//
// ★★ **「形态源穷举」这四个字是被第二次变异逼出来的**（2026-10-21 续做轮）：
// 门自己的注释写着「真正被应用的是**三份** 01-schema.sql」，而代码只钉了**两份**
// —— `deploy/sql/schemas/baseline/01-schema.sql`（部署基线，**随包分发**）
// 里同样定义着这两个视图，却不在形态源清单里 ⇒ ★ **实测：改掉它的顶层 FROM，
// 门照样绿** ⇒ 门是瞎的而外观完全正常 ⇒ ⇒ 现在形态源 9 个、判定**穷举全仓 .sql**。
//
// 变异验证：scripts/audit/verify-l11b-cost-view-rowset.sh（15 条全红）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// costViewFrozenRowsetTable 是「行集来源」为冻结 v1 聚合表时的关系名。
//
// ⚠ 这里**必须**带 schema（`public.`）：视图定义体里写的是
// `FROM (public.api_key_model_cost akmc …)` 与 `FROM public.api_key_model_cost mcp`
// ⇒ 解析出来的就是带 schema 的形式 ⇒ 两侧字面量必须一致，否则门恒红。
const costViewFrozenRowsetTable = "public.api_key_model_cost"

// costViewRowsetEntry 是登记册的一行：**各形态的期望行集来源** + 后果说明。
type costViewRowsetEntry struct {
	// shapes 是「形态名 → 该形态下的期望行集来源」。
	//
	// ⚠⚠ **必须按形态登记**，不能只登记「这张视图的行集来源是冻结表」这一个事实：
	// 只登记集合的话，**改掉其中一个形态**（部署形态 或 基线形态）在**另一个形态**里
	// 仍然能找到它 ⇒ 门照样绿 ⇒ 「形态被改」这件事根本不会逼出重判。
	// ★ 这不是推测：本轮 **M1/M2 两条变异在第一版门下都是绿的** ——
	// 第一版只断言「该视图在**某个**形态里读冻结表」⇒ 改掉 883 那份、基线那份还在
	// ⇒ 绿。⇒ ★ **变异的价值就是把「文档里写的意图」与「代码实际断言的东西」对齐**；
	// 没有变异，这道门会以"绿"的姿态放过形态被改。
	shapes map[string]string
	// note 是这条缺陷的**后果说明**，不许留空
	// （空说明 = 没人知道它为什么在这儿 ⇒ 登记就失去意义）。
	note string
}

// costViewRowsetRegistry 登记册：视图名 → 各形态的行集来源 + 后果。
//
// ⚠ **改这张表 = 一次显式重判**：要么缺陷被修（把 note 改成"已修 + 修法"，
// 并把对应形态的 rowsetSource 改成新的真实来源），要么范围变了（增删条目）。
// 无论哪种，**必须同时改仓里的形态**，否则本门会红（这是故意的）。
var costViewRowsetRegistry = map[string]costViewRowsetEntry{
	"customer_cost_view": {
		shapes: map[string]string{
			"deployed:883":                costViewFrozenRowsetTable,
			"deployed:883-down":           costViewFrozenRowsetTable,
			"deployed:883-embeddata":      costViewFrozenRowsetTable,
			"deployed:883-down-embeddata": costViewFrozenRowsetTable,
			"baseline:schema":             costViewFrozenRowsetTable,
			"baseline:embeddata":          costViewFrozenRowsetTable,
			"baseline:deploy":             costViewFrozenRowsetTable,
			"baseline:objects-customer":   costViewFrozenRowsetTable,
		},
		note: "行集来自 2026-06-19 冻结的 api_key_model_cost（写方随迁移 890 删除）⇒ " +
			"生产上该视图只有 5 行（api_key_id 1/10/11/17/29），" +
			"而近 30 天有 auto 流量的 api_key 是 3/6/7/89/105/114/199（近 7 天仍在跑的是 89/114/199）" +
			"⇒ 零交集；active_concurrent 恒 0、cost_usd_24h 恒 0、last_request_at 停在 6 月。",
	},
	"model_cost_per_task_view": {
		shapes: map[string]string{
			"deployed:883":                costViewFrozenRowsetTable,
			"deployed:883-down":           costViewFrozenRowsetTable,
			"deployed:883-embeddata":      costViewFrozenRowsetTable,
			"deployed:883-down-embeddata": costViewFrozenRowsetTable,
			"baseline:schema":             costViewFrozenRowsetTable,
			"baseline:embeddata":          costViewFrozenRowsetTable,
			"baseline:deploy":             costViewFrozenRowsetTable,
			"baseline:objects-model":      costViewFrozenRowsetTable,
		},
		note: "同上（同一张冻结表）；额外叠一层「7 天窗」谓词 ⇒ " +
			"生产上该视图 0 行（bucket 全在 6 月，WHERE bucket >= now()-7d 全被滤掉）" +
			"⇒ 模型成本表在生产上恒空。",
	},
}

// costViewRowsetShapes 是本门要看的**形态来源**文件（= 会被真正执行到的定义体）。
//
// ⚠⚠ **这里踩过两次坑（2026-10-21 审计轮 + 同日续做轮）**：
//
//	(a) 第一版只把 `sql/objects/views/*.sql` 当「基线形态」的来源 ⇒
//	    **但那目录根本没有任何 apply 通道**（`sql/objects/` 对部署**惰性**）
//	    ⇒ 真正被应用的是三份 `01-schema.sql` ⇒ 只钉 objects 副本 ⇒
//	    **被 apply 的那份改了也不会红** ⇒ 门是瞎的。
//
//	(b) ★★ **续做轮实测抓到的第二例**：R81 修完 (a) 后，门**自己的注释**写着
//	    「真正被应用的是**三份** 01-schema.sql（正典 / 部署基线 / 安装器内嵌）」，
//	    而**代码只钉了其中两份** —— `deploy/sql/schemas/baseline/01-schema.sql`
//	    （**部署基线**，随离线包与一键安装分发到 `sql/baseline/`）里同样
//	    `CREATE VIEW` 了这两个视图，却**不在形态源清单里**
//	    ⇒ ★ **实测复现：改掉它的顶层 FROM ⇒ 门照样绿**（门是瞎的，
//	      而外观上完全正常）。
//	    ⇒ ⇒ **同一个病的第二例**：上一轮问的是「我钉的是不是**被 apply**的那一份」，
//	      这一轮必须问「我钉的是不是**全部**形态源」。
//
// ⚠ **「三份基线一起钉」是仓内既有约定**，别的门早就这么做了
//
//	（`sql/migrations/startup/baseline_ensure_functions_contract_test.go`：
//	 canonical + deploy-baseline + installer-embed 三份并列）
//	⇒ 本门上一版偏离了这个约定 ⇒ 现在改回。
//
// ⚠ **四种形态都要看，因为它们同时存在**：
//   - 部署形态 = 迁移 883 的 up 体（存量升级 / 新装跑完启动序列后的最终形态）；
//   - ★ **回滚形态 = 迁移 883 的 down 体**（回滚会把视图换回 v1 形状）——
//     上一版只把 down 体**列进白名单**（「不是它」），**没钉它的形状**
//     ⇒ 回滚之后的行集来源无人看管 ⇒ 现在一起钉；
//   - 基线形态 = 01-schema.sql（基线 schema 逐语句应用时的形态，
//     两条路径跑完启动序列后形态一致 —— 见 §19.10 跨会话订正）；
//   - 溯源形态 = sql/objects/views/*.sql（源文件，非 apply 通道，
//     但 883 的门要求它们与基线逐字同步 ⇒ 钉住更严）。
var costViewRowsetShapes = []struct {
	name string
	rel  string
}{
	{"deployed:883", filepath.Join("sql", "migrations", "startup", "883_session_family_cost_view_metrics.sql")},
	{"deployed:883-down", filepath.Join("sql", "migrations", "startup", "883_session_family_cost_view_metrics.down.sql")},
	{"deployed:883-embeddata", filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", "883_session_family_cost_view_metrics.sql")},
	{"deployed:883-down-embeddata", filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", "883_session_family_cost_view_metrics.down.sql")},
	{"baseline:schema", filepath.Join("sql", "schema", "01-schema.sql")},
	{"baseline:embeddata", filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "01-schema.sql")},
	{"baseline:deploy", filepath.Join("deploy", "sql", "schemas", "baseline", "01-schema.sql")},
	{"baseline:objects-customer", filepath.Join("sql", "objects", "views", "customer_cost_view.sql")},
	{"baseline:objects-model", filepath.Join("sql", "objects", "views", "model_cost_per_task_view.sql")},
}

// costViewDefinerRole 标注一个「允许含定义体」的文件**扮演什么角色**。
type costViewDefinerRole int

const (
	// costViewRoleShape = 这个文件是**形态源**：它的形状会被本门逐形态钉住
	// ⇒ 它必须同时出现在 costViewRowsetShapes 里（否则「钉了形状却没钉来源」）。
	costViewRoleShape costViewDefinerRole = iota
	// costViewRoleArchived = 归档/文档副本：**从不执行、也不随包分发**
	// ⇒ 不钉形状，但必须登记 —— 否则下面的穷举扫描会把它当成「新定义体」。
	costViewRoleArchived
)

// costViewAllowedDefiners 是**允许**含这两个视图定义体的文件（相对仓根）→ 角色。
//
// ⚠⚠ **这是一份「穷举」清单**，不是「几个已知目录」。
//
//	上一版用的是「扫描根清单」（sql/migrations + installer 内嵌 startup）：
//	⇒ **新开一个目录**（比如 deploy/sql/schemas/baseline/）放定义体 ⇒ 扫不到
//	⇒ **这正是上一轮补 `baseline:deploy` 时踩到的坑**（它就在第三个目录里）。
//	⇒ 现在改成：**全仓 .sql 逐个扫**，命中集必须**恰好**等于本清单。
//	⇒ ⇒ **「以后再开新目录」不再是一个需要人记得的活事实**。
//
// ⚠ 清单里每一条都必须**真的**含定义体（见判据的「不许有陈旧条目」）：
//
//	否则删了定义体却留着条目 ⇒ 清单在自说自话 ⇒ 判据失去判别力。
var costViewAllowedDefiners = map[string]costViewDefinerRole{
	// —— 形态源（9 个，与 costViewRowsetShapes 一一对应）——
	filepath.Join("sql", "migrations", "startup", "883_session_family_cost_view_metrics.sql"):                                      costViewRoleShape,
	filepath.Join("sql", "migrations", "startup", "883_session_family_cost_view_metrics.down.sql"):                                 costViewRoleShape,
	filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", "883_session_family_cost_view_metrics.sql"):      costViewRoleShape,
	filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", "883_session_family_cost_view_metrics.down.sql"): costViewRoleShape,
	filepath.Join("sql", "schema", "01-schema.sql"):                                                                                costViewRoleShape,
	filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "01-schema.sql"):                                            costViewRoleShape,
	filepath.Join("deploy", "sql", "schemas", "baseline", "01-schema.sql"):                                                         costViewRoleShape,
	filepath.Join("sql", "objects", "views", "customer_cost_view.sql"):                                                             costViewRoleShape,
	filepath.Join("sql", "objects", "views", "model_cost_per_task_view.sql"):                                                       costViewRoleShape,

	// —— 归档副本：v1 形态的**历史文档**（不执行、不分发）——
	// ⚠ 它们用的是**无 schema 限定**的 `CREATE VIEW customer_cost_view`，
	//   行集来源也是无限定名的 `api_key_model_cost` ⇒ 与形态源**不是同一形状**
	//   ⇒ 登记为 archived，不钉形状。
	filepath.Join("deploy", "sql", "docs", "features", "2026-06-15-auto-route-mode-cost-table.sql"):            costViewRoleArchived,
	filepath.Join("docs", "archive", "process", "schema-history", "2026-06-15-auto-route-mode-cost-table.sql"): costViewRoleArchived,
}

// costViewScanSkipDirs 是穷举扫描时**跳过**的目录。
//
// ⚠ 只跳过与 SQL 源无关的东西（版本库对象 / 依赖树）——
//
//	**绝不能按「看起来像文档」跳过**：上一轮正是因为「这个目录不会被执行」
//	才漏掉了 deploy/sql/schemas/baseline/ ⇒ 而它恰恰**会被分发**。
var costViewScanSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
}

// costViewCreateViewForThese 匹配「用 CREATE 定义这两个视图」的 DDL。
//
// ⚠ 口径必须是 **CREATE … VIEW**，不能是 "VIEW public.x" ——
// 否则 `COMMENT ON VIEW public.customer_cost_view` 也会命中（883 的 up/down 里就有）
// ⇒ 假红。
//
// ⚠ **schema 限定必须可选**（`public.` 可有可无）：仓内两份**归档副本**用的就是
//
//	无限定写法 `CREATE VIEW customer_cost_view AS`（2026-06-15 那版）。
//	⇒ 若只认 `public.`，这两份文件对判据**完全隐形** ⇒
//	  「以后有人写一份无 schema 限定的定义体」也是隐形的 ⇒
//	⇒ 现在认两种写法（★ 变异 M14 专门打这条）。
var costViewCreateViewForThese = regexp.MustCompile(`(?i)CREATE\s+(OR\s+REPLACE\s+)?VIEW\s+(public\.)?(customer_cost_view|model_cost_per_task_view)\b`)

// ─────────────────────────────────────────────────────────────────────────────
// 工具
// ─────────────────────────────────────────────────────────────────────────────

func costViewReadRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// costViewStripSQL 把注释与字符串字面量替换成**等长空白**（换行保留）。
//
// 为什么必须等长：调用方要在**被处理后的文本**上按位置切片取视图体 ⇒
// 长度变了位置就错了。
//
// 为什么必须做：视图文件里有大段说明性注释（如基线文件的 v1 fallback 头），
// 注释里出现 "CREATE VIEW" / "FROM ..." ⇒ 不剥注释就会把注释当可执行 SQL，
// 解析出**假的行集来源**。
//
// 为什么字符串字面量也要处理：字面量里的括号会打乱括号深度
// （如 `key_alias <> ')'`）⇒ 会让"顶层 FROM"判定错位。
func costViewStripSQL(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	const (
		stateCode = iota
		stateLine
		stateBlock
		stateString
	)
	state := stateCode
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch state {
		case stateLine:
			if c == '\n' {
				state = stateCode
				b.WriteByte(c)
			} else {
				b.WriteByte(' ')
			}
		case stateBlock:
			if c == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				state = stateCode
				b.WriteByte(' ')
				b.WriteByte(' ')
				i++
			} else if c == '\n' {
				b.WriteByte(c)
			} else {
				b.WriteByte(' ')
			}
		case stateString:
			if c == '\'' {
				// SQL 里的 '' 是转义的单引号，不结束字符串。
				if i+1 < len(sql) && sql[i+1] == '\'' {
					b.WriteByte(' ')
					b.WriteByte(' ')
					i++
					continue
				}
				state = stateCode
				b.WriteByte(' ')
			} else if c == '\n' {
				b.WriteByte(c)
			} else {
				b.WriteByte(' ')
			}
		default:
			switch {
			case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
				state = stateLine
				b.WriteByte(' ')
				b.WriteByte(' ')
				i++
			case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
				state = stateBlock
				b.WriteByte(' ')
				b.WriteByte(' ')
				i++
			case c == '\'':
				state = stateString
				b.WriteByte(' ')
			default:
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// costViewIsIdentByte 报告 c 是否可以出现在 SQL 标识符里。
func costViewIsIdentByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// costViewIsFromKeywordAt 报告 s[i:] 是否正好是独立单词 FROM。
func costViewIsFromKeywordAt(s string, i int) bool {
	if i+4 > len(s) {
		return false
	}
	if !strings.EqualFold(s[i:i+4], "from") {
		return false
	}
	if i > 0 && costViewIsIdentByte(s[i-1]) {
		return false
	}
	if i+4 < len(s) && costViewIsIdentByte(s[i+4]) {
		return false
	}
	return true
}

// costViewTopLevelFromSource 返回**顶层** FROM 指向的关系名（可能带 schema）。
//
// 「顶层」= 括号深度 0。视图体里的相关子查询
// （`customer_cost_view` 的 active_concurrent、`model_cost_per_task_view` 的
// avg_latency_ms）都在 SELECT 列表的括号里 ⇒ 深度 ≥ 1 ⇒ 被正确跳过。
//
// ★ **这条函数的"顶层"判定就是本门的全部判别力** ⇒ 自检见
// TestCostViewRowsetParserSelfCheck（变异 M4/M5 证明它不是恒返回某个值）。
func costViewTopLevelFromSource(sql string) string {
	s := costViewStripSQL(sql)
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 || !costViewIsFromKeywordAt(s, i) {
			continue
		}
		j := i + 4
		for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
			j++
		}
		// FROM 后可能直接跟 '('（`FROM (public.x a JOIN …)`）。
		if j < len(s) && s[j] == '(' {
			j++
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
		}
		start := j
		for j < len(s) && costViewIsIdentByte(s[j]) {
			j++
		}
		name := s[start:j]
		if name == "" {
			return ""
		}
		// schema-qualified：public.api_key_model_cost
		if j < len(s) && s[j] == '.' {
			j++
			start = j
			for j < len(s) && costViewIsIdentByte(s[j]) {
				j++
			}
			if part := s[start:j]; part != "" {
				name = name + "." + part
			}
		}
		return name
	}
	return ""
}

// costViewSkipSpace 返回 s[i:] 里第一个非空白字符的位置。
func costViewSkipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// costViewReadQualifiedName 读一个（可带 schema 的）关系名，返回
// **最后一段**（`public.customer_cost_view` ⇒ `customer_cost_view`）与结束位置。
//
// ⚠ 必须取最后一段：`CREATE VIEW public.x AS` 里的 schema 段是**限定符**，
// 视图名是后面那段 ⇒ 取第一段会把视图名读成 `public`。
//
// ★ 第一版就是在这里错的：视图名被读成 `public` ⇒ 门红了，而红的其实是
// 解析器不是形状 ⇒ **「门红了先读门在守什么」**的又一个实例。
func costViewReadQualifiedName(s string, i int) (string, int) {
	name := ""
	for {
		start := i
		for i < len(s) && costViewIsIdentByte(s[i]) {
			i++
		}
		if i == start {
			return "", i
		}
		name = s[start:i]
		if i < len(s) && s[i] == '.' {
			i++
			continue
		}
		return name, i
	}
}

// costViewFindAs 报告从 i 起第一次出现独立单词 AS 的位置（-1 = 没有）。
func costViewFindAs(s string, i int) int {
	for k := i; k+2 <= len(s); k++ {
		if !strings.EqualFold(s[k:k+2], "AS") {
			continue
		}
		if k > 0 && costViewIsIdentByte(s[k-1]) {
			continue
		}
		if k+2 < len(s) && costViewIsIdentByte(s[k+2]) {
			continue
		}
		return k
	}
	return -1
}

// costViewCreateViewPattern 匹配 `CREATE [OR REPLACE] VIEW`（大小写不敏感）。
//
// 用途：**解析完整性自检** —— 文件里有几条 CREATE VIEW，解析器就该认出几个；
// 少认 ⇒ 说明解析器漏了某种形态 ⇒ 下面的「反向」检查（缺陷有没有扩散）**不可信**。
var costViewCreateViewPattern = regexp.MustCompile(`(?i)\bCREATE\s+(OR\s+REPLACE\s+)?VIEW\b`)

// costViewCountCreateView 数出剥过注释的文本里 `CREATE [OR REPLACE] VIEW` 的条数。
func costViewCountCreateView(stripped string) int {
	return len(costViewCreateViewPattern.FindAllString(stripped, -1))
}

// costViewBodies 抽出 content 里所有 `CREATE [OR REPLACE] VIEW public.<name> AS`
// 的定义体，返回 视图名 → 定义体。
//
// ⚠ 匹配跑在**剥过注释**的文本上 ⇒ 说明性注释里的 "CREATE VIEW" 不会被误认。
func costViewBodies(content string) map[string]string {
	stripped := costViewStripSQL(content)
	out := map[string]string{}
	for i := 0; i < len(stripped); {
		idx := strings.Index(stripped[i:], "VIEW")
		if idx < 0 {
			break
		}
		idx += i
		i = idx + 4
		// 必须处在 CREATE 之后（允许 CREATE 与 VIEW 之间夹 OR REPLACE）。
		prefix := stripped[:idx]
		createAt := strings.LastIndex(prefix, "CREATE")
		if createAt < 0 {
			continue
		}
		if between := prefix[createAt+len("CREATE"):]; strings.TrimSpace(between) != "" &&
			strings.TrimSpace(between) != "OR REPLACE" {
			continue
		}
		name, after := costViewReadQualifiedName(stripped, costViewSkipSpace(stripped, idx+4))
		if name == "" {
			continue
		}
		// 定义体：从 AS 之后到**深度 0** 的第一个 ';'。
		//
		// ⚠ 不能用 strings.Index(bodyRest, " AS ")：`AS` 后面未必是空格
		//（真实文本是 "AS\n"）⇒ 那样会**找不到 AS** ⇒ 整段定义体丢失。
		asIdx := costViewFindAs(stripped, after)
		if asIdx < 0 {
			continue
		}
		body := stripped[asIdx+2:]
		depth := 0
		end := len(body)
		for k := 0; k < len(body); k++ {
			switch body[k] {
			case '(':
				depth++
			case ')':
				depth--
			case ';':
				if depth == 0 {
					end = k
					k = len(body)
				}
			}
		}
		out[name] = body[:end]
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// 判据
// ─────────────────────────────────────────────────────────────────────────────

// TestAutoRouteCostViewsRowsetSourceIsRegistered 钉 ①②：
// 仓内「以冻结表作为行集来源」的视图集合 **== 登记册**（双向、且**按形态**）。
func TestAutoRouteCostViewsRowsetSourceIsRegistered(t *testing.T) {
	root := repoRoot(t)

	// 形态名 → (视图名 → 该形态下的行集来源)
	parsed := map[string]map[string]string{}
	for _, shape := range costViewRowsetShapes {
		content := costViewReadRepoFile(t, root, shape.rel)
		bodies := costViewBodies(content)
		if len(bodies) == 0 {
			t.Fatalf("%s 里没有解析出任何视图定义体 —— 解析器或文件形态变了（形状不明 ⇒ 门失去判别力）", shape.rel)
		}
		// ★ 解析完整性：文件里有几条 CREATE VIEW，就必须认出几个。
		//   少认 ⇒ 解析器漏了某种形态 ⇒ ②「反向」检查不可信 ⇒ 必须红。
		if want := costViewCountCreateView(costViewStripSQL(content)); want != len(bodies) {
			t.Errorf("%s 里 `CREATE [OR REPLACE] VIEW` 出现 %d 次，但解析器只认出 %d 个视图 ⇒ "+
				"**解析不完整** ⇒ 下面的「反向」检查（缺陷有没有扩散）不可信 ⇒ 必须先修解析器",
				shape.rel, want, len(bodies))
		}
		m := map[string]string{}
		for name, body := range bodies {
			m[name] = costViewTopLevelFromSource(body)
		}
		parsed[shape.name] = m
	}

	// ① 登记册的每个 (视图, 形态) 组合：形态里必须真的有这个视图，
	//    且它的行集来源必须与登记**逐字一致**。
	//    ⚠ 这里必须**按形态**比：只比"这个视图在某个形态里读冻结表"的话，
	//    改掉其中一个形态，另一个形态仍然满足 ⇒ 门放行（★ 本轮 M1/M2 就是这样绿的）。
	for view, entry := range costViewRowsetRegistry {
		if len(entry.shapes) == 0 {
			t.Errorf("登记册条目 %s 一个形态都没登记 —— 这不是一条有效登记", view)
			continue
		}
		for shape, want := range entry.shapes {
			got, ok := parsed[shape][view]
			if !ok {
				t.Errorf("登记册条目 %s 声明形态 %q 里存在它，但该形态里找不到这个视图 ⇒ "+
					"登记与形状不符（形态被改 ⇒ 必须显式重判这条缺陷）", view, shape)
				continue
			}
			if got != want {
				t.Errorf("视图 %s 在形态 %q 的行集来源是 %q，登记册写的是 %q ⇒ 形状变了 ⇒ "+
					"必须显式重判这条缺陷（真修好了就同步改登记 + note，别让它悄悄过期）",
					view, shape, got, want)
			}
		}
	}

	// ② 反向：任何一个形态里「行集来源 == 冻结表」的视图，都必须在登记册里
	//    **登记了这个形态**（缺陷不能静悄悄扩散到第三个视图，也不能只登一半）。
	for shape, views := range parsed {
		for view, src := range views {
			if src != costViewFrozenRowsetTable {
				continue
			}
			entry, ok := costViewRowsetRegistry[view]
			if !ok {
				t.Errorf("视图 %s 在形态 %q 的行集来源也是冻结表 %s，却没登记 ⇒ "+
					"这条已知缺陷在扩散：要么登记，要么修掉", view, shape, src)
				continue
			}
			if _, ok := entry.shapes[shape]; !ok {
				t.Errorf("视图 %s 在形态 %q 的行集来源也是冻结表 %s，但登记册没登记这个形态 ⇒ "+
					"这条已知缺陷在扩散：要么登记，要么修掉", view, shape, src)
			}
		}
	}

	// ③ 每条登记都必须写明后果（空说明 = 没人知道它为什么在这儿）。
	for view, entry := range costViewRowsetRegistry {
		if strings.TrimSpace(entry.note) == "" {
			t.Errorf("登记册条目 %s 的后果说明是空的 —— 登记必须写清「不修会怎样」", view)
		}
	}
}

// TestCostViewRowsetNoOtherMigrationRedefinesTheViews 守住「**形态源穷举**」：
// 仓内**任何** `.sql` 文件都不得在允许清单之外定义这两个视图。
//
// ★ 为什么必须是「穷举」而不是「扫几个已知目录」（2026-10-21 续做轮实测）：
//
//	上一版扫的是「迁移目录 + 安装器内嵌 startup」**两个根** ⇒
//	**第三个目录 `deploy/sql/schemas/baseline/`（部署基线，随离线包与一键安装
//	分发到 `sql/baseline/`）根本不在眼里** ⇒ 而它恰恰定义着这两个视图 ⇒
//	⇒ ★ **实测复现：改掉它的顶层 FROM ⇒ 门照样绿**（门是瞎的，外观完全正常）。
//	⇒ ⇒ 「以后再开一个目录放定义体」也一样静默 ⇒ **扫描根清单是活事实**。
//	⇒ 现在改成：**全仓 .sql 逐个扫** ⇒ 命中集必须**恰好**等于允许清单
//	⇒ ⇒ 「新开目录」不再需要人记得 ⇒ ★ 这也正是「部署链按 ^[0-9]{3}_ 挑迁移」
//	  那条规则**缺门**的同族问题（手挑规则 ⇒ 需要门守着）。
//
// ★ 本判据的三条断言（缺一条，下面那条就不可信）：
//
//	① 命中集 ⊆ 允许清单：多出来的 = 有人在**别处**定义了这两个视图 ⇒ 形态源漂移；
//	② 允许清单 ⊆ 命中集：清单里有条目却没命中 = **陈旧条目** = 清单在自说自话；
//	③ 角色为「形态源」的条目 ↔ costViewRowsetShapes **一一对应**：
//	   登记了角色却没钉形状 / 钉了形状却没登记角色 ⇒ 两份清单漂移。
func TestCostViewRowsetNoOtherMigrationRedefinesTheViews(t *testing.T) {
	root := repoRoot(t)

	// ── 穷举扫描：全仓 .sql，找出所有含这两个视图定义体的文件 ──
	hits := map[string][]string{} // rel → 命中的视图名
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path != root && costViewScanSkipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".sql") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range costViewCreateViewForThese.FindAllStringSubmatch(string(content), -1) {
			hits[rel] = append(hits[rel], m[3])
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo: %v", walkErr)
	}
	if len(hits) == 0 {
		t.Fatalf("全仓扫下来一个定义体都没有 ⇒ 判据前提失效（正则/文件形态变了？）")
	}

	// ① 命中集 ⊆ 允许清单。
	extra := []string{}
	for rel, views := range hits {
		if _, ok := costViewAllowedDefiners[rel]; !ok {
			extra = append(extra, rel+" → "+strings.Join(views, ","))
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Errorf("全仓有 %d 个文件在允许清单之外用 `CREATE … VIEW` 定义这两个视图：%v ⇒ "+
			"**真正被执行的定义体已经换了**（或者只是换了地方）⇒ "+
			"本门钉的形态源过期 ⇒ 必须把它加进允许清单；"+
			"若它**会被执行**，还必须加进 costViewRowsetShapes 并**重判**登记册",
			len(extra), extra)
	}

	// ② 允许清单 ⊆ 命中集（不许有陈旧条目）。
	stale := []string{}
	for rel := range costViewAllowedDefiners {
		if _, ok := hits[rel]; !ok {
			stale = append(stale, rel)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("允许清单里有 %d 个条目**已经不含**这两个视图的定义体：%v ⇒ "+
			"陈旧条目 = 清单在自说自话 ⇒ 必须删掉它（否则清单会越来越假）",
			len(stale), stale)
	}

	// ③ 角色为「形态源」的条目 ↔ costViewRowsetShapes 一一对应。
	shapeRels := map[string]bool{}
	for _, shape := range costViewRowsetShapes {
		shapeRels[shape.rel] = true
	}
	for rel, role := range costViewAllowedDefiners {
		if role != costViewRoleShape {
			continue
		}
		if !shapeRels[rel] {
			t.Errorf("允许清单把 %s 标成「形态源」，但它不在 costViewRowsetShapes 里 ⇒ "+
				"它的形状**没被钉住** ⇒ 必须加进形态源清单（并登记册里补这个形态）", rel)
		}
	}
	for _, shape := range costViewRowsetShapes {
		if role, ok := costViewAllowedDefiners[shape.rel]; !ok {
			t.Errorf("形态源 %s 不在允许清单里 ⇒ 两份清单漂移 ⇒ 必须登记它", shape.rel)
		} else if role != costViewRoleShape {
			t.Errorf("形态源 %s 在允许清单里的角色不是「形态源」⇒ 两份清单漂移", shape.rel)
		}
	}
}

// TestCostViewRowsetParserSelfCheck 证明解析器**不是恒返回某个值**。
//
// ★ 没有这条，上面那条判据就是「恒绿门」：解析器若永远返回
// `public.api_key_model_cost`，登记册怎么写都绿。
//
// 三种「会骗人」的写法各来一份：
//   - 注释里的 FROM（必须被忽略）；
//   - 字符串字面量里的 ')' （必须不打乱括号深度）；
//   - 相关子查询里的 FROM（必须被跳过）。
func TestCostViewRowsetParserSelfCheck(t *testing.T) {
	sql := "-- 历史注释：FROM public.comment_only_table 只是说明，不是行集来源\n" +
		"SELECT akmc.api_key_id,\n" +
		"       ')' AS odd_literal,\n" +
		"       ( SELECT count(*) FROM public.session_turns_hot st\n" +
		"          WHERE st.api_key_id = akmc.api_key_id::text ) AS active_concurrent\n" +
		"  FROM public.api_key_model_cost akmc\n" +
		"  JOIN public.api_keys ak ON (ak.id = akmc.api_key_id)\n" +
		" WHERE ak.key_alias <> 'FROM public.string_literal_table'"

	got := costViewTopLevelFromSource(sql)
	if got != costViewFrozenRowsetTable {
		t.Errorf("解析器自检失败：期望 %q（注释/字面量/子查询里的 FROM 都必须被忽略），实得 %q",
			costViewFrozenRowsetTable, got)
	}

	// 顶层 FROM 换了 ⇒ 必须跟着换（否则解析器就是写死返回值）。
	other := costViewTopLevelFromSource("SELECT a FROM public.session_turns_hot st")
	if other != "public.session_turns_hot" {
		t.Errorf("解析器自检失败：顶层 FROM 换成 session_turns_hot 时应解析出它，实得 %q", other)
	}

	// 没有 FROM ⇒ 空串（不是 nil、也不是猜一个）。
	if src := costViewTopLevelFromSource("SELECT 1"); src != "" {
		t.Errorf("解析器自检失败：没有 FROM 的 SQL 应解析出空串，实得 %q", src)
	}

	// ★ 完整性计数器自证：注释里的 CREATE VIEW 不算数；
	//   CREATE / CREATE OR REPLACE 两种形态都要数到。
	counter := costViewCountCreateView(costViewStripSQL(
		"-- CREATE VIEW public.commented_out AS SELECT 1;\n" +
			"CREATE VIEW public.a AS SELECT 1;\n" +
			"CREATE OR REPLACE VIEW public.b AS SELECT 1;\n"))
	if counter != 2 {
		t.Errorf("完整性计数器自检失败：期望 2（注释里那条不算），实得 %d", counter)
	}
}

// TestCostViewRowsetRegistryIsBackedByRepoEvidence 把登记册里的**事实主张**
// 绑到仓内可查的证据上（免得证据没了、登记还在自说自话）。
func TestCostViewRowsetRegistryIsBackedByRepoEvidence(t *testing.T) {
	root := repoRoot(t)

	// ① 「写方随迁移 890 删除」—— 仓内证据。
	m890 := costViewReadRepoFile(t, root,
		filepath.Join("sql", "migrations", "startup", "890_drop_request_logs_v1_family.sql"))
	if !strings.Contains(m890, "DROP FUNCTION IF EXISTS public.update_api_key_model_cost();") {
		t.Errorf("登记册声称「api_key_model_cost 的写方随迁移 890 删除」，但 890 里没有这条 DROP ⇒ " +
			"证据没了（或 890 被改过）⇒ 必须重判这条登记")
	}

	// ② 「两个视图有生产读者」—— 仓内证据（端点注册 + 读的就是这两个视图）。
	autoRoute := costViewReadRepoFile(t, root, filepath.Join("admin", "auto_route.go"))
	for _, want := range []string{
		"/api/admin/auto-route/cost/customer",
		"/api/admin/auto-route/cost/model",
		"FROM customer_cost_view",
		"FROM model_cost_per_task_view",
	} {
		if !strings.Contains(autoRoute, want) {
			t.Errorf("登记册声称「auto-route 成本仪表盘的两个端点读这两个视图」，但 admin/auto_route.go 里没有 %q ⇒ "+
				"证据没了 ⇒ 必须重判这条登记", want)
		}
	}
}
