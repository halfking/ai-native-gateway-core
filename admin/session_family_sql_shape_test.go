package admin

// ⚠ 本文件**不带任何 build tag**：它的消费方 `request_logs_read_inventory_test.go`
// 是无标签的普通测试文件 ⇒ 若本文件带 `!integration`，在 integration 构建里
// 它会被排除 ⇒ 消费方引用不到它的符号 ⇒ `go vet -tags=integration ./...`
// 直接编译失败（实测：`undefined: retiredV1ReadPattern`）。
// ⇒ **共享判据文件必须与它的消费方同标签**（此处＝都不带标签）。

// 会话族读面 SQL 形状的**共享判据**（迁移 890，v1 族真删之后）。
//
// # 为什么共享
//
// v1 族退役后，读面统一到会话族；「这条 SQL 读的是不是会话族」这个断言在
// 十几个测试文件里都要判一次。各写各的正则 ⇒ 生产侧改一次形状，十几处同时红，
// 而**真正要问的问题只有一个**（读的是哪一族、还剩几条腿）。
//
// ⇒ 形状判据集中在这里，各测试文件只引用。
//
// # 判据打在什么上
//
// 打在**关系名 + 两腿结构**上，不打在列投影上：投影会随需求变（118 列），
// 关系名与腿数不会。`db.SessionFamilyTurnsSourceSQL()` 的形状是
// `hot ∪ parent` 两腿（加 734 details 层），`db.SessionFamilyBodiesSourceSQL()`
// 同构。
//
// # 负向判据必须按标识符边界
//
// `public.request_logs` 是 `public.request_logs_with_current_month` 的**前缀**，
// 而后者是 canonical 视图（890 之后仍然存在，只是内容换成会话族两臂）。
// ⇒ 朴素包含会把「必须在场的 canonical 视图」判成「必须不在场的 v1 族」。
// 本文件的 `sqlMentionsRelation` 用**标识符边界**判定：命中位置的前后字符
// 都不能是 [A-Za-z0-9_]。
//
// ⚠ 同理，判据里**不用** `LIKE '%…_request_logs…%'`：SQL 的 LIKE 里 `_` 是
// 单字符通配符（审计 §10.8 的坏尺子）。

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// retiredV1RelationNames 是迁移 890 之后**不再存在**的 v1 族关系名（闭合集合，
// 不是通配）。
//
// 来源：sql/migrations/startup/890_drop_request_logs_v1_family.sql
//   - 5 张底表 × 两种拼写（`request_logs` / `_request_logs`，迁移 860 加的
//     `_` 前缀形态一并列出）
//   - 4 张 v1-only 视图
//
// ⚠ **canonical 视图 `request_logs_with_current_month` 不在此列**：890 之后它
// 仍然存在（内容换成会话族两臂体），把它列为「不得出现」会把一条活的读面
// 判成死代码。
var retiredV1RelationNames = []string{
	// v1 底表（两种拼写）
	"request_logs", "_request_logs",
	"request_logs_hot", "_request_logs_hot",
	"request_logs_bodies", "_request_logs_bodies",
	"request_logs_bodies_hot", "_request_logs_bodies_hot",
	"request_logs_archive", "_request_logs_archive",
	// v1-only 视图
	"request_logs_with_current_month_without_request_class_due_at",
	"request_logs_with_current_month_without_customer_id",
	"request_logs_bodies_with_current_month",
	"request_logs_bodies_progress",
}

// sessionFamilyTurnsSourceRE 匹配 `db.SessionFamilyTurnsSourceSQL()`：
// hot ∪ parent 两腿。
//
// ⚠ 两条腿的名字必须**分别**出现：`public.session_turns_hot t` 与
// `public.session_turns t` 是两个不同的关系，只出现一条 = 漏读一侧表
// （判据纪律第 7 条：同构腿用「两条都在」而不是 Contains）。
//
// ⚠ 末尾**不**锚 `\)`：投影里含 `(CASE WHEN … END)`，用括号平衡判「整个
// FROM 源被括起来」会把正则变成一个必须靠回溯才能满足的模式。
func sessionFamilyTurnsSourceRE() string {
	return `(?s)FROM \(SELECT .*FROM public\.session_turns_hot t.*UNION ALL SELECT .*FROM public\.session_turns t`
}

// sessionFamilyBodiesSourceRE 匹配 `db.SessionFamilyBodiesSourceSQL()`：
// `session_bodies_hot` ∪ `session_bodies` 两腿。
func sessionFamilyBodiesSourceRE() string {
	return `(?s)FROM \(SELECT .*FROM public\.session_bodies_hot sb.*UNION ALL SELECT .*FROM public\.session_bodies sb`
}

// sessionFamilyBodiesSourceLooseRE 匹配**内联**写两腿的会话族正文源
// （admin/session_bodies_batch.go 的 sessionBodiesByRequestIDAndTSSQL
// 因为要投影 ts，不能复用 db 侧 helper，只能自己写两腿 —— 且它写的是
// 不带 `public.` 前缀的形式）。
func sessionFamilyBodiesSourceLooseRE() string {
	return `(?s)FROM \(\s*SELECT sb\.request_id.*FROM session_bodies_hot sb.*UNION ALL.*FROM session_bodies sb`
}

// sessionFamilyTurnsLookupRE 匹配「元数据腿」的 request_id 形态：
// 会话族 turns 源 + `WHERE rl.request_id = $1` [+ 租户谓词] + `LIMIT 1`。
//
// scoped=true 时要求 `AND tenant_id = $2` 在场（生产侧不带 `rl.` 前缀，
// 与 unified_detail.go 的 tenantClause 逐字相同）—— 租户隔离那道门就是靠
// 这一条把「越权读」挡在 SQL 里（而不是只在 HTTP 层事后 404）。
func sessionFamilyTurnsLookupRE(scoped bool) string {
	tenant := ""
	if scoped {
		tenant = ` AND tenant_id = \$2`
	}
	return `(?s)SELECT rl\.request_id, COALESCE\(rl\.tenant_id, ''\).*` +
		sessionFamilyTurnsSourceRE() + `.*\) rl\s+WHERE rl\.request_id = \$1` + tenant + `\s+LIMIT 1`
}

// sessionFamilyTurnsByClientRequestIDRE 匹配「元数据腿」的 client_request_id
// 形态（request_id 未命中时的第二腿）：会话族 turns 源 +
// `WHERE rl.client_request_id = $1` [+ 租户谓词] + `ORDER BY rl.ts DESC` +
// `LIMIT 1`。
func sessionFamilyTurnsByClientRequestIDRE(scoped bool) string {
	tenant := ""
	if scoped {
		tenant = ` AND tenant_id = \$2`
	}
	return `(?s)SELECT rl\.request_id, COALESCE\(rl\.tenant_id, ''\).*` +
		sessionFamilyTurnsSourceRE() + `.*\) rl\s+WHERE rl\.client_request_id = \$1` + tenant +
		`\s+ORDER BY rl\.ts DESC\s+LIMIT 1`
}

// sessionFamilyBodiesLookupRE 匹配「正文腿」：会话族正文源 +
// `WHERE request_id = $1` + `LIMIT 1`。
func sessionFamilyBodiesLookupRE() string {
	return `(?s)SELECT outbound_body::text\s+` + sessionFamilyBodiesSourceRE() +
		`.*\)\s+WHERE request_id = \$1\s+LIMIT 1`
}

// sqlMentionsRelation 报告 SQL 文本里是否出现了**独立**的标识符 ident。
//
// 「独立」= 命中位置的前后字符都不是 [A-Za-z0-9_]。
//
// 为什么必须按边界：`public.request_logs` 是
// `public.request_logs_with_current_month` 的前缀 ⇒ 朴素包含会把 canonical
// 视图（必须在场）判成 v1 族（必须不在场）。
func sqlMentionsRelation(sql, ident string) bool {
	isWord := func(b byte) bool {
		return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
	}
	for i := 0; i+len(ident) <= len(sql); i++ {
		if sql[i:i+len(ident)] != ident {
			continue
		}
		if i > 0 && isWord(sql[i-1]) {
			continue
		}
		end := i + len(ident)
		if end < len(sql) && isWord(sql[end]) {
			continue
		}
		return true
	}
	return false
}

// retiredV1RelationsInSQL 返回 SQL 文本里出现的**已退役 v1 族关系名**
// （按标识符边界判定）。
func retiredV1RelationsInSQL(sql string) []string {
	var hits []string
	for _, name := range retiredV1RelationNames {
		if sqlMentionsRelation(sql, name) {
			hits = append(hits, name)
		}
	}
	return hits
}

// TestSessionFamilySourceShapeMatchersHaveTeeth 是上面那些判据的**自证**。
//
// 为什么需要：判据本身是「正则」，而正则最典型的失效模式是**恒不匹配**
// （写错一个转义 ⇒ 全部判据假绿、或者全部假红）。⇒ 这里给每一对判据各配
// 一条**阳性对照**（真形态必须命中）与一条**阴性对照**（v1 形态必须不命中）。
//
// ⚠ 阴性对照不是装饰：如果有人把 `sessionFamilyTurnsSourceRE()` 改成
// `FROM public\.session_turns`（去掉两腿要求），它会开始命中 v1 形态 ⇒ 本门
// 转红。这正是「判据有牙」的证明。
func TestSessionFamilySourceShapeMatchersHaveTeeth(t *testing.T) {
	turnsRE := regexp.MustCompile(sessionFamilyTurnsSourceRE())
	bodiesRE := regexp.MustCompile(sessionFamilyBodiesSourceRE())
	bodiesLooseRE := regexp.MustCompile(sessionFamilyBodiesSourceLooseRE())

	// ① 阳性对照：生产 helper 的真实输出必须被各自的判据命中。
	//
	// ⚠ helper 返回的是**被括号包住的 FROM 源**，本身**不带** "FROM " 前缀
	//（"FROM" 由调用点写）。⇒ 判据打的是调用点的形态 ⇒ 这里要补上前缀，
	// 否则「判据匹配不上真实输出」是**自证的假阴性**。
	if !turnsRE.MatchString("FROM " + dbpkg.SessionFamilyTurnsSourceSQL()) {
		t.Errorf("sessionFamilyTurnsSourceRE() 匹配不上 db.SessionFamilyTurnsSourceSQL() 的真实输出 —— "+
			"判据写坏了：\n%s", dbpkg.SessionFamilyTurnsSourceSQL())
	}
	if !bodiesRE.MatchString("FROM " + dbpkg.SessionFamilyBodiesSourceSQL()) {
		t.Errorf("sessionFamilyBodiesSourceRE() 匹配不上 db.SessionFamilyBodiesSourceSQL() 的真实输出 —— "+
			"判据写坏了：\n%s", dbpkg.SessionFamilyBodiesSourceSQL())
	}

	// ② 阴性对照：v1 形态**不得**被命中（否则判据分不出族）。
	v1Turns := `(SELECT * FROM _request_logs_hot UNION ALL SELECT * FROM _request_logs)`
	if turnsRE.MatchString(v1Turns) {
		t.Error("sessionFamilyTurnsSourceRE() 命中了 v1 形态的源 —— 判据没有区分力")
	}
	v1Bodies := `(SELECT * FROM _request_logs_bodies_hot UNION ALL SELECT * FROM _request_logs_bodies)`
	if bodiesRE.MatchString(v1Bodies) {
		t.Error("sessionFamilyBodiesSourceRE() 命中了 v1 形态的源 —— 判据没有区分力")
	}

	// ③ 单腿形态（只读 hot、漏了母表）**不得**被命中：
	//    「少一条腿 = 漏读一侧表」是这一族最贵的静默缺陷。
	if turnsRE.MatchString(`(SELECT * FROM public.session_turns_hot t)`) {
		t.Error("sessionFamilyTurnsSourceRE() 命中了只有 hot 一条腿的源 —— " +
			"少一条腿 = 漏读母表，判据必须能分辨")
	}
	if bodiesRE.MatchString(`(SELECT * FROM public.session_bodies_hot sb)`) {
		t.Error("sessionFamilyBodiesSourceRE() 命中了只有 hot 一条腿的源 —— " +
			"少一条腿 = 漏读母表，判据必须能分辨")
	}

	// ④ 内联两腿形态（不带 public. 前缀）只被 loose 判据接受。
	inline := `FROM ( SELECT sb.request_id, sb.ts FROM session_bodies_hot sb UNION ALL SELECT sb.request_id FROM session_bodies sb ) rb`
	if !bodiesLooseRE.MatchString(inline) {
		t.Error("sessionFamilyBodiesSourceLooseRE() 匹配不上内联两腿形态 —— 判据写坏了")
	}
	if bodiesLooseRE.MatchString("FROM " + v1Bodies) {
		t.Error("sessionFamilyBodiesSourceLooseRE() 命中了 v1 形态 —— 判据没有区分力")
	}

	// ⑤ 标识符边界：canonical 视图**不得**被判成 v1 族，而 v1 族**必须**被判出来。
	if sqlMentionsRelation("FROM public.request_logs_with_current_month rl", "_request_logs") {
		t.Error("sqlMentionsRelation 把 canonical 视图判成了 v1 族 —— 边界判定坏了")
	}
	if sqlMentionsRelation("FROM public.request_logs_with_current_month rl", "request_logs") {
		t.Error("sqlMentionsRelation 把 canonical 视图判成了 v1 族 —— 边界判定坏了")
	}
	if !sqlMentionsRelation("FROM _request_logs_hot rl", "_request_logs_hot") {
		t.Error("sqlMentionsRelation 没认出 v1 热表 —— 边界判定坏了")
	}
	if !sqlMentionsRelation("FROM request_logs_bodies_with_current_month rb", "request_logs_bodies_with_current_month") {
		t.Error("sqlMentionsRelation 没认出 v1 正文视图 —— 边界判定坏了")
	}
	if sqlMentionsRelation("FROM request_logs_bodies_progress", "request_logs_bodies_progress") == false {
		t.Error("sqlMentionsRelation 没认出 v1-only 视图 request_logs_bodies_progress")
	}

	// ⑥ retiredV1RelationsInSQL 与边界判定同源，且 canonical 视图不算命中。
	hits := retiredV1RelationsInSQL("FROM public.request_logs_with_current_month rl JOIN public.session_turns_hot t ON true")
	if len(hits) != 0 {
		t.Errorf("retiredV1RelationsInSQL 在只有 canonical 视图的 SQL 上报出 %v —— 判据过宽", hits)
	}
	hits = retiredV1RelationsInSQL("FROM _request_logs_hot rl")
	if len(hits) != 1 || hits[0] != "_request_logs_hot" {
		t.Errorf("retiredV1RelationsInSQL 在 v1 热表上应恰好报 1 条，实际 %v", hits)
	}

	// ⑦ **调用点形态**的阳性对照 —— 这才是各测试文件真正拿去用的形态。
	//
	// ⚠ 为什么必须单独钉：helper 返回的源**不带** "FROM " 前缀、且母表腿后面
	// 还跟着 details JOIN ⇒ 「源本身的形态」与「调用点的形态」不是同一个
	// 字符串。第一版判据只按源本身写 ⇒ 在调用点形态上恒不匹配（判据假红）。
	callerTurns := "SELECT rl.request_id, COALESCE(rl.tenant_id, '') FROM " +
		dbpkg.SessionFamilyTurnsSourceSQL() + " rl WHERE rl.request_id = $1 LIMIT 1"
	if !regexp.MustCompile(sessionFamilyTurnsLookupRE(false)).MatchString(callerTurns) {
		t.Errorf("sessionFamilyTurnsLookupRE(false) 匹配不上调用点形态 —— 判据写坏了：\n%s", callerTurns)
	}
	callerTurnsScoped := "SELECT rl.request_id, COALESCE(rl.tenant_id, '') FROM " +
		dbpkg.SessionFamilyTurnsSourceSQL() + " rl WHERE rl.request_id = $1 AND tenant_id = $2 LIMIT 1"
	if !regexp.MustCompile(sessionFamilyTurnsLookupRE(true)).MatchString(callerTurnsScoped) {
		t.Errorf("sessionFamilyTurnsLookupRE(true) 匹配不上调用点形态 —— 判据写坏了：\n%s", callerTurnsScoped)
	}
	callerByClientID := "SELECT rl.request_id, COALESCE(rl.tenant_id, '') FROM " +
		dbpkg.SessionFamilyTurnsSourceSQL() + " rl WHERE rl.client_request_id = $1 ORDER BY rl.ts DESC LIMIT 1"
	if !regexp.MustCompile(sessionFamilyTurnsByClientRequestIDRE(false)).MatchString(callerByClientID) {
		t.Errorf("sessionFamilyTurnsByClientRequestIDRE(false) 匹配不上调用点形态 —— 判据写坏了：\n%s", callerByClientID)
	}
	callerBodies := "SELECT outbound_body::text FROM " +
		dbpkg.SessionFamilyBodiesSourceSQL() + "\n WHERE request_id = $1\n LIMIT 1"
	if !regexp.MustCompile(sessionFamilyBodiesLookupRE()).MatchString(callerBodies) {
		t.Errorf("sessionFamilyBodiesLookupRE() 匹配不上调用点形态 —— 判据写坏了：\n%s", callerBodies)
	}

	// ⑧ 租户形态**不得**被未加租户的那条判据接受（否则「越权读」判据失效）。
	if regexp.MustCompile(sessionFamilyTurnsLookupRE(false)).MatchString(callerTurnsScoped) {
		t.Error("sessionFamilyTurnsLookupRE(false) 接受了带租户谓词的形态 —— " +
			"未加租户的那条判据必须拒绝它，否则租户隔离那道门恒绿")
	}
}

// retiredV1RelationAlternation 是已退役 v1 族关系名的正则交替串（**最长在前**）。
//
// ⚠ 最长在前是必须的：Go 的 alternation 是 leftmost-first，**不是**
// longest-match。写成 (request_logs|request_logs_bodies_with_current_month)
// 时它会先匹配前缀 `request_logs`，而下一个字符是 `_`（word 字符）⇒
// `\b` 不成立 ⇒ 整支不匹配 ⇒ **别名被丢掉**（那不是降级测量，是根本没有测量）。
func retiredV1RelationAlternation() string {
	alt := make([]string, len(retiredV1RelationNames))
	for i, n := range retiredV1RelationNames {
		alt[i] = regexp.QuoteMeta(n)
	}
	for i := 0; i < len(alt); i++ {
		for j := i + 1; j < len(alt); j++ {
			if len(alt[j]) > len(alt[i]) {
				alt[i], alt[j] = alt[j], alt[i]
			}
		}
	}
	return strings.Join(alt, "|")
}

// retiredV1RelationPattern 是「SQL 里出现已退役 v1 族关系名」的正则版本。
//
// ⚠ 它按**边界**锚定（`(?i)\b(?:…)\b`）—— 但 Go 的 `\b` 只认 [A-Za-z0-9_]
// 之间的边界，而 `_` 本身是 word 字符 ⇒ `\brequest_logs\b` **不会**命中
// `request_logs_with_current_month`（`_` 后面紧跟着 word 字符）。这一点是
// 本判据能同时表达「canonical 视图必须在场」与「v1 族必须不在场」的前提。
var retiredV1RelationPattern = regexp.MustCompile(
	`(?i)\b(?:` + retiredV1RelationAlternation() + `)\b`)

// retiredV1ReadPattern 是「FROM/JOIN 一个已退役 v1 族关系」的正则版本。
//
// 它是**读点**口径（不是「提到过」口径）⇒ 注释里的 `FROM _request_logs`
// 不会被它算成读点。
//
// ★ 2026-10-25（R152 轮）：**补上 `public.` 与 `only` 两种限定**。
//
//	原形态是 `\b(?:from|join)\s+(关系名)\b` ⇒ 它要求关系名**紧跟**动词。
//	⇒ 而**带 schema 限定的写法一律看不见**：
//	    FROM public._request_logs_hot rl      ← 不命中（原形态）
//	    FROM ONLY _request_logs_hot rl        ← 不命中（原形态）
//	⇒ ★★ 而 `public.` 恰恰是**本仓自己的主流写法**（会话族源一律写成
//	   `public.session_turns_hot`）⇒ ⇒ 这不是假想缺口，是「按本仓惯例写出来的
//	   v1 读点」**必然**逃过这道门。
//
//	实测（2026-10-25）：把 db/request_logs_view_schema.go 的
//	`leg("public.session_turns_hot")` 改成 `leg("public._request_logs_hot")`
//	⇒ 这道门**绿**、例外台账**空**（它的注释写着「将来谁再写回 `_request_logs`，
//	本门立即红」）⇒ ⇒ 那句承诺在这个形态下是**假的**。
//	⇒ 本轮补上限定 + 补上阳性对照；另在
//	cmd/tools/sql_source_indirection_audit/v1_literal_in_unresolved_test.go
//	补上「形参决定的表名」那一条路（那道门管不了跨文件的形参取值）。
//
// ⚠ `public.` 是**可选**的 ⇒ 不带限定的写法仍然命中。
var retiredV1ReadPattern = regexp.MustCompile(
	`(?i)\b(?:from|join)\s+(?:only\s+)?(?:public\.)?(?:` + retiredV1RelationAlternation() + `)\b`)

// retiredV1ReferenceExceptions 是**已知仍引用已退役 v1 族关系**的生产位置。
//
// # 为什么需要这张表
//
// 迁移 890 把 v1 族**物理删除**了（10 张底表 + 4 张 v1-only 视图 + 序列/索引/RLS）。
// ⇒ 任何仍引用它的生产代码在运行时会 **42P01**（relation does not exist）。
//
// 2026-10-20 实测（`admin/` 收口轮）：**4 个 admin 生产文件**仍在读 890 已 DROP
// 的两张包装视图：
//
//	admin/usage_trend_series.go     :407 / :588  FROM …_without_customer_id r
//	admin/auto_route.go              :158        FROM …_without_customer_id
//	admin/attempt_quality_api.go     :139        FROM …_without_request_class_due_at
//	admin/auto_route_correlations.go :129/150/171/193/246  FROM …_without_request_class_due_at
//
// 生产侧的修复不在本轮范围（生产文件由父会话收口），但**「v1 读方总体为空」这句话
// 必须逐个具名才成立** —— 否则门是绿的、而生产是坏的。
//
// # 这张表**只许缩小**
//
// 每一项对应一个已知的生产缺陷；缺陷被修掉后这一项就该删掉。
// ⇒ TestRetiredV1ReferencesAreAllAccountedFor 断言三条：
// ① 扫描命中 ⊆ 本表（**新面孔 ⇒ 红**）；② 每项都有非空理由；
// ③ 本表条数 ≤ 基线（**只许缩小**，不许新增）。
//
// ⚠ 与 `storage/sqlite/request_log_store.go` 无关：那是 SQLite 存储面**自己的**
// `request_logs` 表（列名/语义都与 PG 的 v1 族不同），扫描器已按路径排除它
// （见 request_logs_read_inventory_test.go 的 sqliteStorageFace）。
var retiredV1ReferenceExceptions = map[string]string{
	// ★ 2026-10-20：**已清空**（基线 4 → 0）。
	//
	// 上一轮（admin 收口轮）登记的 4 条例外——它们读的是 890 已 DROP 的两张包装视图
	// `request_logs_with_current_month_without_customer_id` /
	// `request_logs_with_current_month_without_request_class_due_at`：
	//
	//	admin/usage_trend_series.go     :407 / :588
	//	admin/auto_route.go              :158
	//	admin/attempt_quality_api.go     :139
	//	admin/auto_route_correlations.go :129/150/171/193/246
	//
	// ⇒ 父会话已把它们**改指会话族原生源** `db.SessionFamilyTurnsSourceSQL()`
	//（含 details 特征层的 LEFT JOIN ⇒ `model_chosen` / `strategy_used` /
	// `task_type_chosen` / `provider_id` / `total_tokens` 等列**都有值**）。
	//
	// ⚠ 顺带一条**能力恢复**的事实（不是「修崩溃」那么简单）：那两张包装视图
	// **直读 v1 族**（实测真库：`request_logs_with_current_month_without_customer_id`
	// 的定义体第一行就是 `FROM _request_logs_hot`），而 v1 族自 2026-10-13 停写后
	// **恒 0 行** ⇒ 这 4 个端点在 890 之前**就已经是空的**（返回 0 行/空结果，
	// 不报错）；890 之后它们会从「空」变成 **42P01** ⇒ 改指会话族既是**止崩**，
	// 也是**把它们从「恒空」救回「有数据」**。
	//
	// ⇒ 例外表清空 ⇒ 本门变成**最锐**的那条：「admin 生产代码里**一个** v1 引用都不许有」。
	// 将来谁再写回 `_request_logs` / 那两张包装视图，本门立即红。
}

// retiredV1ReferenceExceptionsBaseline 是例外台账的**规模基线**（只许缩小）。
//
// ⚠ 为什么要有它：`scan ⊆ exceptions` 这条检查只挡「新面孔」，
// 挡不住「有人把一条已知例外复制成两条」——集合会变大而扫描集没变。
// ⇒ 规模单独设一个基线，与集合检查同源但不同向。
const retiredV1ReferenceExceptionsBaseline = 0

// TestRetiredV1ReferencesAreAllAccountedFor 是「已退役 v1 关系引用」的总闸门。
//
// 三条方向：
//
//	① 扫描命中 ⊆ 例外台账 —— **新面孔即红**（不许有没被具名的 v1 引用）。
//	② 例外台账每一项都要有非空理由 —— 空理由的具名等于没具名。
//	③ 例外台账条数 ≤ 基线 —— 只许缩小，不许新增。
//
// # 阳性对照（防「恒真空」）
//
// 本门断言的是「扫描命中集 ⊆ 例外集」。若扫描器压根看不见 v1 引用
// （模式写坏 / 路径写错），命中集恒空 ⇒ ① 恒绿 ⇒ 本门变成空气。
// ⇒ 这里用**模式级**对照钉住扫描器的区分力：
// 真 v1 读点必须命中、canonical 视图必须不命中。
func TestRetiredV1ReferencesAreAllAccountedFor(t *testing.T) {
	root := repoRootFromCaller(t)
	hits := scanRequestLogsReaders(t, root)

	var unknown []string
	for file := range hits {
		if _, ok := retiredV1ReferenceExceptions[file]; !ok {
			unknown = append(unknown, file)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("%d 个生产文件读已退役的 v1 族关系，但**不在** retiredV1ReferenceExceptions 里 —— "+
			"890 已经把它们从库里删了，这些代码运行时会 42P01：\n  %s\n"+
			"要么生产侧改指（会话族 / canonical 视图），要么把它具名登记进例外台账并写清理由。",
			len(unknown), strings.Join(unknown, "\n  "))
	}

	for file, why := range retiredV1ReferenceExceptions {
		if strings.TrimSpace(why) == "" {
			t.Errorf("retiredV1ReferenceExceptions[%q] 的理由为空 —— 空理由的具名等于没具名", file)
		}
		if _, ok := hits[file]; !ok {
			t.Logf("例外 %s 已不再被扫描命中（生产侧可能已修）——确认后请把它从例外台账里删掉", file)
		}
	}
	if len(retiredV1ReferenceExceptions) > retiredV1ReferenceExceptionsBaseline {
		t.Errorf("retiredV1ReferenceExceptions 有 %d 条，超过基线 %d —— 这张表只许缩小（每一条都是"+
			"一个待修的生产缺陷），新增必须先在文档里说明为什么不是「扫描器把新读点漏掉了」。",
			len(retiredV1ReferenceExceptions), retiredV1ReferenceExceptionsBaseline)
	}

	// 阳性对照：扫描器的模式必须能看见真 v1 读点、且不误伤 canonical 视图。
	if !retiredV1ReadPattern.MatchString("FROM _request_logs_hot rl") {
		t.Error("retiredV1ReadPattern 匹配不上真 v1 读点 —— 扫描器没有区分力，本门是恒真空")
	}
	if !retiredV1ReadPattern.MatchString("LEFT JOIN request_logs_bodies_with_current_month rb") {
		t.Error("retiredV1ReadPattern 匹配不上 v1 正文视图 —— 扫描器没有区分力，本门是恒真空")
	}
	if retiredV1ReadPattern.MatchString("FROM public.request_logs_with_current_month rl") {
		t.Error("retiredV1ReadPattern 命中了 canonical 视图 —— 890 之后它仍然存在（会话族两臂体），" +
			"把它算成 v1 引用会让本门恒红")
	}
	// ★ 2026-10-25（R152 轮）补的阳性对照：**带 schema 限定**与 **ONLY** 的形态。
	//
	// 这两条是本轮实测发现的缺口：`public.` 是本仓自己的主流写法
	// （会话族源一律 `public.session_turns_hot`）⇒ 原形态对这种写法**全盲**。
	// ⇒ 没有这两条对照，将来有人把限定去掉，门照样绿 ⇒ 缺口会重新长回来。
	if !retiredV1ReadPattern.MatchString("FROM public._request_logs_hot rl") {
		t.Error("retiredV1ReadPattern 匹配不上 \"FROM public._request_logs_hot rl\" —— " +
			"带 schema 限定的 v1 读点逃过了本门（本仓主流写法就是带 public. 的）")
	}
	if !retiredV1ReadPattern.MatchString("FROM ONLY public._request_logs_hot rl") {
		t.Error("retiredV1ReadPattern 匹配不上 \"FROM ONLY public._request_logs_hot rl\" —— " +
			"ONLY 限定的 v1 读点逃过了本门")
	}
	if !retiredV1ReadPattern.MatchString("JOIN public.request_logs_bodies_with_current_month rb") {
		t.Error("retiredV1ReadPattern 匹配不上带 public. 的 v1 正文视图 —— 扫描器没有区分力")
	}
	// ★ 阴性对照：补上限定**不能**把 canonical 视图带进来。
	if retiredV1ReadPattern.MatchString("FROM public.request_logs_with_current_month rl") {
		t.Error("retiredV1ReadPattern 命中了 canonical 视图（带 public. 形态）—— " +
			"限定放宽必须只放宽「已退役」那批")
	}
	if retiredV1ReadPattern.MatchString("JOIN public.session_turns_hot t") {
		t.Error("retiredV1ReadPattern 命中了会话族热表 —— 限定放宽必须只放宽「已退役」那批")
	}
}
