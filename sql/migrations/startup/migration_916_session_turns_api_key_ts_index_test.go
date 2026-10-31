package startup

// migration_916_session_turns_api_key_ts_index_test.go —— 迁移 916 的静态契约门。
//
// # 这道门钉什么
//
// ★ 迁移 916 给会话族建**表达式索引**（键 = 与 canonical 视图投影逐字一致的
//   CASE 表达式），让「按 api_key 过滤会话」的读点能用上【索引 seek】。
//
// ★★ 而这条索引的**全部价值**都系在一个字面量上：★★
//   索引键的表达式必须与视图投影的表达式**逐字一致** ⇒ planner 才认得它
//   ⇒ 若两者分叉（例如视图改成 `::text` 形态、而索引写成裸形态），
//   索引会**静默地不被使用** ⇒ ⇒ **读数上完全看不出异常，只是又变慢了。**
//   ⇒ ⇒ 所以这道门的第一断言不是「索引在不在」，★ 而是**「表达式与唯一真值源
//   逐字一致」**（唯一真值源 = db.SessionFamilyAPIKeyIDExpr）。
//
// ★ 第二类断言：**索引必须有承重查询**（888 立的规矩：★ 一条没有查询承重的
//   索引是缺陷 —— 本仓自己的先例是 877 删掉了一条没人用的部分索引）。
//   ⇒ 这里断言「按 api_key 过滤会话的读点」在生产代码里确实存在，且它的
//   谓词形态（api_key_id 等值 + ts 窗口）正是这条索引能承的形态。
//
// ★ 第三类断言（★ 反向自证）：★ **906 那条部分索引承不住 CASE 形态**
//   （它的**键**是 `(session_id, turn_no DESC)`，`api_key_id` 只在**谓词**里）
//   ⇒ ⇒ 本迁移不是在重复建设 ⇒ ⇒ 若哪天 906 被改成以表达式为键，
//   本门必须立刻报红（避免两条索引重复吃写入放大）。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

const (
	mig916File     = "916_session_turns_api_key_ts_index.sql"
	mig916DownFile = "916_session_turns_api_key_ts_index.down.sql"
	mig906File     = "906_turns_api_key_numeric_partial_index.sql"
)

// 读仓库内相对本包（sql/migrations/startup）的文件。
func readRepo916(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// normalize916Expr 把索引/常量里的表达式归一化成可比形式：
//
//	① format() 形态里的 '' 转义还原成 '；
//	② 去掉视图侧的表别名 `t.`（索引表达式里没有别名）；
//	③ 去掉所有空白；
//	④ 剥掉最外层的成对括号。
func normalize916Expr(s string) string {
	s = strings.ReplaceAll(s, "''", "'")
	s = strings.ReplaceAll(s, "t.", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\t", "")
	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		inner := s[1 : len(s)-1]
		depth := 0
		balanced := true
		for i, c := range inner {
			switch c {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 && i != len(inner)-1 {
					balanced = false
				}
			}
			if !balanced {
				break
			}
		}
		if !balanced {
			break
		}
		s = inner
	}
	return s
}

// 抓迁移里的表达式索引定义：两种形态都要覆盖。
//
//	① 内联：`CREATE INDEX ... ON [ONLY] public.<table> USING btree ((CASE ... END), ts DESC)`
//	② 生成器：`format('CREATE INDEX CONCURRENTLY ... %I ON public.%I USING btree ((CASE ... END), ts DESC)', ...)`
var (
	mig916InlineRe = regexp.MustCompile(`(?is)CREATE\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_]+)\s+ON\s+(?:ONLY\s+)?public\.([a-z0-9_]+)\s+(?:USING\s+btree\s+)?\(\((.*?)\)\s*,\s*ts\s+DESC\)`)
	mig916FormatRe = regexp.MustCompile(`(?is)format\(\s*'CREATE\s+INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\s+%I\s+ON\s+public\.%I\s+(?:USING\s+btree\s+)?\(\((.*?)\)\s*,\s*ts\s+DESC\)'`)
)

// TestMigration916IndexExpressionMatchesViewProjection 是本门的核心断言：
// 索引键的表达式必须与 db.SessionFamilyAPIKeyIDExpr **逐字一致**（去别名后）。
func TestMigration916IndexExpressionMatchesViewProjection(t *testing.T) {
	body := stripSQLComments(readRepo916(t, mig916File))
	want := normalize916Expr(db.SessionFamilyAPIKeyIDExpr)
	if want == "" {
		t.Fatal("db.SessionFamilyAPIKeyIDExpr 为空 —— 唯一真值源没了")
	}

	got := map[string]bool{}
	for _, m := range mig916InlineRe.FindAllStringSubmatch(body, -1) {
		got[normalize916Expr(m[3])] = true
	}
	for _, m := range mig916FormatRe.FindAllStringSubmatch(body, -1) {
		got[normalize916Expr(m[1])] = true
	}
	if len(got) == 0 {
		t.Fatalf("%s 里没解析到任何一条 api_key 表达式索引 —— 判据的解析器失效了（迁移形态漂移？）", mig916File)
	}
	for expr := range got {
		if expr != want {
			t.Errorf("索引表达式与 canonical 视图投影不一致 ⇒ ★ planner 认不出它 ⇒ 索引会静默不被使用 ★\n  索引里的：%s\n  唯一真值源（db.SessionFamilyAPIKeyIDExpr）：%s", expr, want)
		}
	}
}

// TestMigration916HasBearingQuery 断言这条索引**有承重查询**，且谓词形态正是
// 它能承的形态（api_key_id 等值 + ts 窗口）。
func TestMigration916HasBearingQuery(t *testing.T) {
	usage := readRepo916(t, "../../../admin/usage.go")
	// ★ 承重读点：canonical 视图 + api_key_id 等值 + ts 上下界。
	idx := strings.Index(usage, "FROM request_logs_with_current_month")
	if idx < 0 {
		t.Fatal("admin/usage.go 里找不到 `FROM request_logs_with_current_month` —— 读点被改了 ⇒ 本索引可能失去承重查询")
	}
	seg := usage[idx:]
	if len(seg) > 1500 {
		seg = seg[:1500]
	}
	if !strings.Contains(seg, "api_key_id = $") {
		t.Errorf("承重读点的谓词里没有 `api_key_id = $n` ⇒ 索引形态对不上：\n%s", seg)
	}
	if !strings.Contains(seg, "ts >= $") || !strings.Contains(seg, "ts < $") {
		t.Errorf("承重读点的谓词里没有 ts 上下界 ⇒ 索引第二列（ts）失去意义：\n%s", seg)
	}
}

// TestMigration906PartialIndexCannotServeCaseForm 是反向自证：
// 906 那条部分索引的**键**里没有 CASE 表达式 ⇒ 它承不住 CASE 形态的过滤
// ⇒ ⇒ 本迁移不是重复建设。
// ★ 若哪天 906 被改成以表达式为键，本门必须报红（避免两条索引重复吃写入放大）。
func TestMigration906PartialIndexCannotServeCaseForm(t *testing.T) {
	body906 := readRepo916(t, mig906File)
	expr := normalize916Expr(db.SessionFamilyAPIKeyIDExpr)
	// 906 的键列必须**不含** CASE 表达式（只含裸列 + 谓词里出现正则）。
	for _, line := range strings.Split(body906, "\n") {
		up := strings.ToUpper(strings.TrimSpace(line))
		if !strings.HasPrefix(up, "CREATE INDEX") && !strings.HasPrefix(up, "ON PUBLIC.") {
			continue
		}
		norm := normalize916Expr(line)
		if strings.Contains(norm, expr) && !strings.Contains(norm, "WHERE") {
			t.Errorf("906 的索引键里出现了 CASE 表达式 ⇒ ★ 它现在能承 CASE 形态了 ⇒ ★ 本迁移 916 就是重复建设 ⇒ 请重新裁决：\n%s", line)
		}
	}
	// ★ 并且 906 的谓词必须是**裸正则**（与 SessionFamilyAPIKeyIDNumericPredExpr 同源），
	//   否则它与 916 的关系判断需要重写。
	if !strings.Contains(body906, "WHERE api_key_id ~ '^[0-9]+$'") {
		t.Error("906 的部分索引谓词不再是裸正则形态 ⇒ 本门对「谁承什么」的判断需要重写")
	}
}

// TestMigration916Structure 断言迁移的**形态**（不是内容）：
// 首行标记 + 会话级锁守卫 + 三段式 + 同名 .down.sql。
func TestMigration916Structure(t *testing.T) {
	raw := readRepo916(t, mig916File)
	lines := strings.Split(strings.TrimLeft(raw, "\n"), "\n")
	// ★ 结构检查只看【真语句】⇒ 注释里出现的 "SET LOCAL" 只是论证文字，不能算数。
	body := stripSQLComments(raw)
	if !strings.Contains(lines[0], "dbinit:no-transaction") {
		t.Errorf("首行必须有 dbinit:no-transaction 标记（★ 否则 installer 的 --single-transaction 会让 CONCURRENTLY 直接失败）：\n%s", lines[0])
	}
	if !strings.Contains(body, "SET lock_timeout = '2s'") {
		t.Error("必须设**会话级** lock_timeout（★ 本通道不在事务块内 ⇒ SET LOCAL 只打 WARNING 后完全失效 ⇒ 914 文件头实测）")
	}
	if strings.Contains(body, "SET LOCAL lock_timeout") {
		t.Error("★ 不得用 SET LOCAL lock_timeout（★ 本通道不在事务块内 ⇒ 它完全失效 ⇒ 等于没有守卫）")
	}
	if !strings.Contains(body, "SET statement_timeout") {
		t.Error("必须设 statement_timeout")
	}
	// 三段式：逐分区生成器 + 父壳 + ATTACH。
	if !strings.Contains(body, "ON ONLY public.session_turns") {
		t.Error("必须有父表 ONLY 索引壳（★ 否则未来月分区不会继承）")
	}
	if !strings.Contains(body, "ATTACH PARTITION") {
		t.Error("必须有 ATTACH PARTITION 步骤（★ PG 不支持在父表上 CONCURRENTLY ⇒ 子索引要逐个挂上）")
	}
	if !strings.Contains(body, "session_turns_hot") {
		t.Error("必须给 hot 表单独建一条（★ hot 表非分区 ⇒ 父表索引不级联）")
	}

	down := readRepo916(t, mig916DownFile)
	if !strings.Contains(down, "DROP INDEX IF EXISTS public.idx_session_turns_api_key_ts") ||
		!strings.Contains(down, "DROP INDEX IF EXISTS public.idx_session_turns_hot_api_key_ts") {
		t.Error("同名 .down.sql 必须把两条索引都删掉（★ 父壳连带子索引一并消失）")
	}
}

// TestMigration916RegisteredEverywhere 断言登记面齐全：
// installer 的 StartupFiles + 部署通道脚本 + db-changelog.md（含正确 sha256）。
func TestMigration916RegisteredEverywhere(t *testing.T) {
	runner := readRepo916(t, "../../../installer/internal/dbinit/runner.go")
	if !strings.Contains(runner, mig916File) {
		t.Errorf("★ %s 必须登记进 installer 的 StartupFiles（★ 否则**全新安装**出来的库没有这条索引 ⇒ 多环境不满足「同样的标准」）", mig916File)
	}
	seq := readRepo916(t, "../../../scripts/apply-db-revision-sequence.sh")
	if !strings.Contains(seq, mig916File) {
		t.Errorf("★ %s 必须登记进 scripts/apply-db-revision-sequence.sh（★ 否则 211/252 存量库部署不到）", mig916File)
	}

	raw, err := os.ReadFile(mig916File)
	if err != nil {
		t.Fatalf("read %s: %v", mig916File, err)
	}
	sum := sha256.Sum256(raw)
	want := hex.EncodeToString(sum[:])

	changelog := readRepo916(t, "../../../docs/db-changelog.md")
	if !strings.Contains(changelog, want) {
		t.Errorf("★ docs/db-changelog.md 里没有本文件当前的 sha256（%s）⇒ 登记册与磁盘不一致", want)
	}
}
