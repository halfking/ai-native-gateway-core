package startup

// session_turns_tools_population_test.go —— 迁移 889 的静态契约门（审计 G-6）。
//
// # 缺陷本体
//
// `public.session_turns.tools`（456 建列）是**死列**：写方的 INSERT 列清单里
// 没有它 ⇒ 每行都停在默认值 '[]'::jsonb ⇒ 视图
// request_logs_with_current_month 投影出的 tool_calls **恒为空数组**。
//
// # 这道门钉什么
//
// ★ **决策必须留下理由，不能只留结论。** 889 选的是「填充」而不是「删列」，
//   而删列在本仓**不可行**（视图链 710/816/817/824 都写着
//   `, t.tools AS tool_calls`；Go 侧 db/request_logs_view_schema.go 的
//   sessionFamilyProjection 第 475 行是 "t.tools"；还有三份基线快照与
//   SELECT * 读者）。⇒ 门断言迁移头部**点名这些依赖**——
//   没有它们，下一个人会以为「DROP 一下不就完了」而真的去 DROP。
//
// ★ **「工具行从未带 turn_no」这条事实必须被钉住。** 它是 889 的列注释
//   里写的风险（按 (session_id, turn_no) join 会丢掉全部行），也是
//   「tools 只能靠 (session_id, request_id) 关联」的原因。
//   只在注释里写一句「turn_no 恒为 NULL」是不够的：写方哪天补上这一列，
//   注释就变成假的。⇒ 门直接读写方的 INSERT 列清单，断言 turn_no 不在里面。
//
// ★ **写路径与迁移必须对得上**：迁移说「由 turnToolsJSONBExpr 从
//   session_tools_hot 聚合」，门就断言 turn_writer.go 里真的有这个表达式、
//   且连接键是 $1/$4（不是 $2/turn_no —— 那个错法会让子查询恒空）。
//
// ★ **不删列**：889 与其 .down.sql 都不得出现 DROP COLUMN tools。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	migration889     = "889_session_turns_tools_population.sql"
	migration889Down = "889_session_turns_tools_population.down.sql"
)

// TestMigration889CommentsToolsAndTurnNo 钉住 889 订正了哪 4 条列注释。
func TestMigration889CommentsToolsAndTurnNo(t *testing.T) {
	body := readRepoFile(t, migration889)

	for _, col := range []string{
		"public.session_turns.tools",
		"public.session_turns_hot.tools",
		"public.session_tools.turn_no",
		"public.session_tools_hot.turn_no",
	} {
		if !strings.Contains(body, "COMMENT ON COLUMN "+col+" IS") {
			t.Errorf("%s 里没有 COMMENT ON COLUMN %s —— 该列的注释没被订正", migration889, col)
		}
	}

	// turn_no 的注释必须**写明「从未被写」**（这是本迁移记录的风险本体）。
	if !strings.Contains(body, "从未被写") {
		t.Errorf("%s 里没有「从未被写」这句 —— session_tools.turn_no 恒为 NULL 这条风险没有被记进列注释", migration889)
	}
	// tools 的注释必须写明填充来源（否则读注释的人不知道值从哪来）。
	if !strings.Contains(body, "session_tools_hot") {
		t.Errorf("%s 的 tools 列注释里没有点明填充来源 session_tools_hot", migration889)
	}
}

// TestMigration889KeepsTheToolsColumn 钉住「不删列」。
func TestMigration889KeepsTheToolsColumn(t *testing.T) {
	for _, f := range []string{migration889, migration889Down} {
		body := stripSQLComments(readRepoFile(t, f))
		if strings.Contains(strings.ToUpper(body), "DROP COLUMN") {
			t.Errorf("%s 里出现了 DROP COLUMN —— 本轮裁决是「填充」，不是删列；"+
				"删列会撞上 710/816/817/824 的视图投影与 db/request_logs_view_schema.go 的 sessionFamilyProjection", f)
		}
	}
}

// TestMigration889RecordsWhyDropIsBlocked 钉住决策的理由（点名依赖对象）。
func TestMigration889RecordsWhyDropIsBlocked(t *testing.T) {
	body := readRepoFile(t, migration889)
	// 视图链（710/816/817/824）与 Go 侧投影，两类依赖都必须被点名。
	for _, dep := range []string{
		"710_request_logs_view_session_family_v2.sql",
		"816_request_logs_view_client_ip_projection.sql",
		"817_request_logs_view_client_ip_semantic_guard.sql",
		"824_request_status_rate_limited_projection.sql",
		"db/request_logs_view_schema.go",
	} {
		if !strings.Contains(body, dep) {
			t.Errorf("%s 的头部没有点名依赖对象 %s —— "+
				"没有依赖清单，读者会以为「DROP 一下不就完了」", migration889, dep)
		}
	}
}

// TestMigration889DownRestoresOriginalComments 钉住 down 的回滚语义。
func TestMigration889DownRestoresOriginalComments(t *testing.T) {
	body := readRepoFile(t, migration889Down)
	if !strings.Contains(body, "Tools invoked during this turn (JSON array of tool_use records).") {
		t.Errorf("%s 没有把 456 的原文还原回来 —— 回滚语义是「回到迁移前的库状态」", migration889Down)
	}
	// turn_no 在 706 建列时**没有**列注释 ⇒ 还原 = 无注释。
	if !strings.Contains(body, "public.session_tools.turn_no IS NULL") {
		t.Errorf("%s 没有把 session_tools.turn_no 的注释删掉 —— 706 建列时它本来就没有列注释", migration889Down)
	}
}

// TestTurnWriterPopulatesToolsFromSessionToolsHot 是**跨文件契约**：
// 迁移 889 说值来自 session_tools_hot，写方就得真的这么写。
func TestTurnWriterPopulatesToolsFromSessionToolsHot(t *testing.T) {
	src := readRepoFile(t, "../../../domains/session/v2/turn_writer.go")

	// ① 聚合表达式存在，且指向 hot 表。
	if !strings.Contains(src, "public.session_tools_hot") {
		t.Errorf("domains/session/v2/turn_writer.go 里没有引用 public.session_tools_hot —— " +
			"tools 列没有被真正填上（迁移 889 的注释会变成假的）")
	}
	// ② 连接键必须是 $1/$4，且带显式转型（§9.100 纪律）。
	for _, want := range []string{"t.session_id = $1::text", "t.request_id = $4::text"} {
		if !strings.Contains(src, want) {
			t.Errorf("turn_writer.go 的聚合表达式里没有 %q —— "+
				"连接键写错（$2 是 turn_no ⇒ 子查询恒空）或缺显式转型（依赖算子解析偏好）", want)
		}
	}
	// ③ INSERT 的列清单里必须有 tools。
	cols := insertColumnList(t, src, "INSERT INTO public.session_turns_hot (")
	if !containsWord(cols, "tools") {
		t.Errorf("turn_writer.go 的 INSERT 列清单里没有 tools ⇒ 这一列仍停在默认值 '[]'::jsonb")
	}
}

// TestToolStoreNeverWritesTurnNo 钉住「turn_no 恒为 NULL」这条事实。
//
// 889 的列注释写着 session_tools.turn_no **从未被写**。若写方哪天补上了
// 这一列，注释就成了假的 ⇒ 这里直接读写方的 INSERT 列清单。
func TestToolStoreNeverWritesTurnNo(t *testing.T) {
	src := readRepoFile(t, "../../../domains/toolexecution/postgres_store.go")
	cols := insertColumnList(t, src, "INSERT INTO public.session_tools_hot (")
	if len(cols) == 0 {
		t.Fatal("没解析到 domains/toolexecution/postgres_store.go 里对 session_tools_hot 的 INSERT 列清单 —— " +
			"判据的尺子坏了（写方写法漂移？）")
	}
	if containsWord(cols, "turn_no") {
		t.Errorf("写方现在写 turn_no 了 ⇒ 迁移 889 的列注释「本列从未被写」已过期，需要同步订正。列清单：%v", cols)
	}
	// 连接键必须在：session_id 与 request_id 都 NOT NULL。
	for _, c := range []string{"session_id", "request_id"} {
		if !containsWord(cols, c) {
			t.Errorf("写方的列清单里没有 %s —— tools 列的连接键依赖它", c)
		}
	}
}

// ── 本门的小工具 ──────────────────────────────────────────────────────

// insertColumnList 解析 `INSERT INTO <table> (<cols>)` 里的列名清单。
func insertColumnList(t *testing.T, src, marker string) []string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		return nil
	}
	rest := src[i+len(marker):]
	j := strings.Index(rest, ")")
	if j < 0 {
		t.Fatalf("INSERT 列清单没有闭合括号")
	}
	var out []string
	for _, raw := range strings.Split(rest[:j], ",") {
		c := strings.TrimSpace(raw)
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// containsWord 判断一个逗号分隔的列名清单里是否有某个列名（精确匹配）。
func containsWord(cols []string, want string) bool {
	for _, c := range cols {
		if c == want {
			return true
		}
	}
	return false
}

// 确认本包能读到仓库根下的文件（路径写错时给出可读的错误而不是空断言）。
func TestRepoFilePathResolves(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "..", "..", "go.mod")); err != nil {
		t.Fatalf("判据定位不到仓库根（../../../go.mod 不存在）: %v", err)
	}
}
