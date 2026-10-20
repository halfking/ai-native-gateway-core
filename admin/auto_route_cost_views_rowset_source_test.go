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
// 变异验证：scripts/audit/verify-l11b-cost-view-rowset.sh（6 条全红）。

import (
	"os"
	"path/filepath"
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
			"deployed:883":      costViewFrozenRowsetTable,
			"baseline:customer": costViewFrozenRowsetTable,
		},
		note: "行集来自 2026-06-19 冻结的 api_key_model_cost（写方随迁移 890 删除）⇒ " +
			"生产上该视图只有 5 行（api_key_id 1/10/11/17/29），" +
			"而近 30 天真正在跑 auto 的是 3/6/7/89/105/114/199 ⇒ 零交集；" +
			"active_concurrent 恒 0、cost_usd_24h 恒 0、last_request_at 停在 6 月。",
	},
	"model_cost_per_task_view": {
		shapes: map[string]string{
			"deployed:883":   costViewFrozenRowsetTable,
			"baseline:model": costViewFrozenRowsetTable,
		},
		note: "同上（同一张冻结表）；额外叠一层「7 天窗」谓词 ⇒ " +
			"生产上该视图 0 行（bucket 全在 6 月，WHERE bucket >= now()-7d 全被滤掉）" +
			"⇒ 模型成本表在生产上恒空。",
	},
}

// costViewRowsetShapes 是本门要看的**形态来源**文件。
//
// ⚠ 两种形态都要看，因为它们**同时存在**：
//   - 部署形态 = 迁移 883 的 up 体（存量升级 / 新装跑完启动序列后的最终形态）；
//   - 基线形态 = sql/objects/views/*.sql（基线 schema 逐语句应用时的形态，
//     两条路径跑完启动序列后形态一致 —— 见 §19.10 跨会话订正）。
//
// 只看一种形态 ⇒ 另一种形态悄悄改掉时门看不见。
var costViewRowsetShapes = []struct {
	name string
	rel  string
}{
	{"deployed:883", filepath.Join("sql", "migrations", "startup", "883_session_family_cost_view_metrics.sql")},
	{"baseline:customer", filepath.Join("sql", "objects", "views", "customer_cost_view.sql")},
	{"baseline:model", filepath.Join("sql", "objects", "views", "model_cost_per_task_view.sql")},
}

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
