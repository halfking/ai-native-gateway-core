package admin

// session_detail_sql_gate_test.go — 会话详情页 SQL 的列限定回归门
//
// 2026-10-03。这个页面此前**从未被自动化扫到**：ui-sweep.mjs 对带 `:` 的动态
// 路由一刀切跳过，于是 `/admin/sessions/:id` 的 500 一直没被发现。
//
// 真因：querySessionDetailV2 的 SELECT 用裸列名，而 LEFT JOIN LATERAL
// session_analysis_metadata 暴露了 status / updated_at 两列，与 public.sessions
// 同名 ⇒ PostgreSQL SQLSTATE 42702 `column reference "updated_at" is ambiguous`，
// 整个详情页 500。
//
// 为什么静态扫而不是起真库跑：起真库的门在 CI 上跑不动（同 wiring_gate_test.go
// 的理由）。这里盯的是**字面量级的形状**——「SELECT 列表里有没有裸列名」，
// 而这正是当初写错的那一处。改回裸列名，本门必须红。
//
// ⚠ 本门只覆盖 querySessionDetailV2 这一条查询。同族查询（session_turns_v2.go
// 的快照查询）本来就写的是 s.xxx —— 已人工核对，见本文件末尾的兜底断言。
import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// LATERAL 暴露的列（sessionAnalysisSelectCols）。这六个名字一旦与 sessions
// 重名，裸写就会二义。
var lateralExposedCols = []string{
	"status", "schema_version", "input_hash", "source_task_id", "updated_at", "payload",
}

// sessions 与 session_analysis_metadata 真实共有的列（information_schema 实测）：
// created_at / status / tenant_id / updated_at。其中只有 status 与 updated_at
// 出现在 LATERAL 的输出里，所以只有这两个会真的二义。
var sharedWithSessions = []string{"status", "updated_at"}

// 抽出 querySessionDetailV2 里那条 query 的 SELECT 列表（`` ` `` 之间的第一段）。
func sessionDetailSelectList(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("session_detail_v2.go"))
	if err != nil {
		t.Fatalf("读 session_detail_v2.go 失败: %v", err)
	}
	// query := ` ... `  里、以 SELECT 开头的部分。
	// 2026-10-03 R38：原先取全文件**第一个** `query :=`——session_detail_v2.go
	// 里现有两处（querySession / queryTurns），谁排在前面取决于函数书写顺序；
	// 日后在文件更上方新增带 `query :=` 的函数，门会静默改测别人的 SQL
	//（queryTurns 本就全限定 ⇒ 恒绿假安全）。现在锚定 querySession 函数体。
	funcIdx := strings.Index(string(src), "func (api *SessionDetailV2API) querySession(")
	if funcIdx < 0 {
		t.Fatal("找不到 querySession 函数——函数改名或移走时本门失效，请先修门再动产品代码")
	}
	scope := string(src)[funcIdx:]
	if funcEnd := strings.Index(scope, "\nfunc "); funcEnd >= 0 {
		scope = scope[:funcEnd]
	}
	re := regexp.MustCompile("(?s)query := `(.*?)`")
	m := re.FindStringSubmatch(scope)
	if m == nil {
		t.Fatal("querySession 里没找到 query := `...`，本门失去判别力（先修门再改产品代码）")
	}
	body := m[1]
	selStart := strings.Index(body, "SELECT")
	if selStart < 0 {
		t.Fatal("query 里没有 SELECT，本门失去判别力")
	}
	selEnd := strings.Index(body[selStart:], "FROM")
	if selEnd < 0 {
		t.Fatal("query 里没有 FROM，无法截取 SELECT 列表")
	}
	return body[selStart : selStart+selEnd]
}

// TestSessionDetailSelectIsFullyQualified：SELECT 列表里不得出现裸的
// status / updated_at —— 这两个列名 sessions 与 LATERAL 同时提供。
func TestSessionDetailSelectIsFullyQualified(t *testing.T) {
	sel := sessionDetailSelectList(t)

	// 逐个逗号切出列表达式，忽略换行与缩进
	cols := strings.Split(sel, ",")
	bare := []string{}
	for _, raw := range cols {
		c := strings.TrimSpace(raw)
		if c == "" || strings.HasPrefix(strings.ToUpper(c), "SELECT") {
			continue
		}
		for _, bad := range sharedWithSessions {
			// 合法的形态：`sam.status`（带限定）或 `s.status`。
			// 二义的形态：正好就是 `status`。
			if c == bad {
				bare = append(bare, bad)
			}
		}
	}
	if len(bare) > 0 {
		t.Errorf(
			"querySessionDetailV2 的 SELECT 里有未加表限定的列 %v：\n%s\n"+
				"LATERAL（sessionAnalysisSelectCols）暴露了 %v，与 public.sessions 同名时 PostgreSQL 报 "+
				"SQLSTATE 42702 column reference is ambiguous，会话详情页直接 500。\n"+
				"修法：全部写成 s.<col>（LATERAL 那几列保持 sam. 前缀）。",
			bare, sel, lateralExposedCols,
		)
	}
}

// TestSessionDetailSelectQualifiesSessionsColumns：更强的形状约束 ——
// SELECT 里凡是 sessions 自己的列，都必须带 s. 前缀。防止下次新增一列时
// 又写成裸名（那次的 updated_at 就是这么来的）。
func TestSessionDetailSelectQualifiesSessionsColumns(t *testing.T) {
	sel := sessionDetailSelectList(t)
	// 抽取所有列表达式的首段标识符
	identRe := regexp.MustCompile(`^[a-z_][a-z0-9_]*`)
	for _, raw := range strings.Split(sel, ",") {
		c := strings.TrimSpace(raw)
		if c == "" || strings.HasPrefix(strings.ToUpper(c), "SELECT") {
			continue
		}
		first := identRe.FindString(c)
		if first == "" {
			continue
		}
		// 带表限定的（s. / sam.）不检查
		if strings.HasPrefix(c, "s.") || strings.HasPrefix(c, "sam.") {
			continue
		}
		t.Errorf(
			"SELECT 列 %q 没有表限定。LATERAL 与 sessions 同名列会让 PostgreSQL 报二义（42702），"+
				"详情页 500 —— 本门就是为了挡住这个。", c,
		)
	}
}
