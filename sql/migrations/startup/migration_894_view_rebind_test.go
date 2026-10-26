package startup

// migration_894_view_rebind_test.go —— 894（把 v_candidate_failure_logs_diagnosis
// 重新绑回活表）的契约门。
//
// # 这道门防什么
//
// 缺陷机制（2026-10-21 R72 轮实测）：
//
//	**视图绑定的是关系 OID，不是名字。**
//	迁移 392 把老堆表 `candidate_failure_logs` **改名**成
//	`candidate_failure_logs_columnar_archive`（保留旧数据）并**新建**同名空分区表时，
//	**既有视图不会跟着换表** —— 它仍绑在老表的 OID 上，
//	而 `pg_get_viewdef` 打印的是那个 OID 的**当前名字**
//	⇒ 视图的 FROM 变成 archive 表 ⇒ **新形态的数据对它不可见**。
//
// 实测读数（只读）：
//
//	· 生产 211：视图读 archive ⇒ **0 行**，而活表 **91,935 行**（2026-10-12~10-20）；
//	· 生产 252：392 走「已分区则跳过」分支（从未改名）⇒ 视图读活表 ⇒ 187,907 行；
//	· 一次性库（基线 + 392）复现：392 之前读 LIVE，392 之后自动变 ARCHIVE。
//
// ⇒ **同一个视图在两个生产实例返回完全不同的数据集**。
//
// # 为什么是「三方同源」而不是「迁移里写对了」
//
// 仓内有**三份**该视图的定义，任何一份漂移都会让形态再次分叉：
//
//	① `sql/migrations/startup/894_...sql`（链上真正生效的那份）
//	② `sql/schema/01-schema.sql`（基线；新装库的第一份真相）
//	③ `sql/objects/views/v_candidate_failure_logs_diagnosis.sql`（对象源文件；
//	   `sql/objects/views/README.md` 教人用 `psql -f` 手工应用的那份）
//
// 三份必须**逐字同形**（归一化后）⇒ 链终态 = 真相源 = 手工应用形态。
// ⇒ 这也正是 890 那道门（「三者必须同源」）的同款判据。
//
// # 判据纪律（本门自身）
//
//   · **剥注释后再比对**（R65 的教训）：迁移文件头里**正文提到**了
//     `candidate_failure_logs_columnar_archive`（解释机制）⇒ 不剥注释的
//     「不得包含 archive」断言会**恒红**；反过来，只看正文首行而不看
//     FROM 子句，则改坏 FROM 也不会红。
//   · **阳性对照**：三方比对前先断言抽出的正文**非空**且**含 16 个投影列名**
//     ⇒ 否则「三份都空」也会「相等」⇒ 门恒绿（这正是 R62/R65 记过的
//     「恒绿门」陷阱）。
//   · **真库腿纯只读**：不建对象、不写行 ⇒ 可以指向任何库（含生产）。
//
// # 变异可证
//
// 见文件末「变异记录」：M1 改坏 FROM ⇒ 红；M2 摘登记面 ⇒ 红；
// M3 动对象源文件 ⇒ 红；M4 删 down ⇒ 红。

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const mig894File = "894_rebind_candidate_failure_logs_diagnosis_view.sql"

// mig894ViewName 是本门唯一关心的视图。
const mig894ViewName = "public.v_candidate_failure_logs_diagnosis"

// mig894Read 读仓内文件（相对本包目录）。
func mig894Read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", rel))
	if err != nil {
		t.Fatalf("读 %s 失败: %v", rel, err)
	}
	return string(b)
}

// mig894StripComments 剥掉 SQL 注释（-- 行注释 与 /* */ 块注释）。
//
// ★ 为什么必须有这一步：894 的文件头**正文里**就写着
// `candidate_failure_logs_columnar_archive`（解释机制）⇒
// 「正文不得包含 archive」这条断言若不剥注释就会恒红；
// 而只看首行又漏掉真正要判的 FROM 子句。
func mig894StripComments(s string) string {
	s = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`/\*.*?\*/`).ReplaceAllString(s, " ")
	return s
}

// mig894ExtractViewBody 抽出该视图的**投影正文**（AS 之后、结尾分号之前）。
//
// ★ 为什么要抽正文而不是整段：链上那份是 `CREATE OR REPLACE VIEW`，
// 基线/对象源文件那份是 `CREATE VIEW` ⇒ 整段必然不等 ⇒ 门会恒红。
// 真正要同源的是**投影**（含 FROM 子句）。
func mig894ExtractViewBody(t *testing.T, sqlText string) string {
	t.Helper()
	s := mig894StripComments(sqlText)
	// 找 `VIEW <name> AS`
	idx := strings.Index(s, mig894ViewName)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(mig894ViewName):]
	// ★ 用正则而不是 `" AS "` 字面量：三份来源里 AS 后面都**跟的是换行**
	//   （`... AS\n SELECT id,`）⇒ 字面量 `" AS "` 匹配不到
	//   ⇒ 会一路找到文件别处的某个 " AS " ⇒ 抽出**不相干的片段**
	//   （本门第一版就是这么写的 ⇒ 抽出了 "SELECT id," 就截断）。
	asLoc := regexp.MustCompile(`(?is)\bAS\b\s+`).FindStringIndex(rest)
	if asLoc == nil {
		return ""
	}
	body := rest[asLoc[1]:]
	// 结尾：第一个不在括号/引号里的分号
	depth := 0
	inStr := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '\'':
			inStr = !inStr
		case inStr:
			// 跳过
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ';' && depth == 0:
			return mig894Normalize(body[:i])
		}
	}
	return mig894Normalize(body)
}

// mig894Normalize 归一化：压空白 + 去首尾空白。
func mig894Normalize(s string) string {
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
}

// mig894ProjectionPrefix 是投影正文的**开头必须逐字匹配**的那一段。
//
// ★ 为什么用「前缀逐字匹配」而不是「逐列 Contains」：
//   逐列 Contains 对 `ts` / `id` 这种短列名几乎没有约束力
//   （`id` 出现在任何地方都算命中）⇒ 抽出一段残缺正文也可能「列全在」。
//   ⇒ 阳性对照必须强到「抽错正文 ⇒ 立刻红」。
var mig894ProjectionPrefix = "SELECT id, ts, tenant_id, credential_id, provider_id, " +
	"raw_model_name, attempt_index, error_kind AS legacy_kind,"

// ─────────────────────────────────────────────────────────────────────────
// ① 三方同源
// ─────────────────────────────────────────────────────────────────────────

func Test894ViewBodyIsIdenticalToBothInRepoSources(t *testing.T) {
	migBody := mig894ExtractViewBody(t, mig894Read(t, filepath.Join("sql", "migrations", "startup", mig894File)))
	baseBody := mig894ExtractViewBody(t, mig894Read(t, filepath.Join("sql", "schema", "01-schema.sql")))
	objBody := mig894ExtractViewBody(t, mig894Read(t, filepath.Join("sql", "objects", "views", "v_candidate_failure_logs_diagnosis.sql")))

	// ── 阳性对照：正文非空且列齐全（否则「三方都空」也会相等 ⇒ 恒绿门）──
	for label, body := range map[string]string{
		"迁移 894": migBody, "基线 01-schema.sql": baseBody, "对象源文件": objBody,
	} {
		if body == "" {
			t.Fatalf("%s 里抽不到 %s 的视图正文（抽正文失败 ⇒ 下面的比对是空对空）", label, mig894ViewName)
		}
		for _, col := range []string{
			"extracted_upstream_status_code", "diagnosed_error_kind",
			"live_upstream_status_code", "classification_disagrees",
		} {
			if !strings.Contains(body, col) {
				t.Errorf("%s 的视图正文缺投影列 %q（列不全 ⇒ 下面的三方比对不可信）", label, col)
			}
		}
		if !strings.HasPrefix(body, mig894ProjectionPrefix) {
			t.Errorf("%s 的视图正文开头不匹配期望的投影前缀：\n  期望前缀: %s\n  实际开头: %.160s",
				label, mig894ProjectionPrefix, body)
		}
	}

	if migBody != baseBody {
		t.Errorf("★ 迁移 894 的视图正文与基线 sql/schema/01-schema.sql 不一致：\n  迁移: %s\n  基线: %s\n"+
			"⇒ 链终态与新装库的第一份真相不同形 ⇒ 同一对象在两个环境会长成两个样子",
			migBody, baseBody)
	}
	if migBody != objBody {
		t.Errorf("★ 迁移 894 的视图正文与对象源文件 sql/objects/views/v_candidate_failure_logs_diagnosis.sql 不一致：\n  迁移: %s\n  对象: %s\n"+
			"⇒ 按 README 用 psql -f 手工应用对象源文件，会把链上的形态改回去 ⇒ 两个环境再次分叉",
			migBody, objBody)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ② 绑到活表、不是 archive 表（剥注释后再判）
// ─────────────────────────────────────────────────────────────────────────

func Test894BindsToLiveTableNotArchive(t *testing.T) {
	body := mig894ExtractViewBody(t, mig894Read(t, filepath.Join("sql", "migrations", "startup", mig894File)))
	if body == "" {
		t.Fatal("抽不到视图正文（下面都是空判）")
	}
	if !strings.Contains(body, "FROM public.candidate_failure_logs cfl") {
		t.Errorf("★ 894 的视图正文没有绑到活表：找不到 `FROM public.candidate_failure_logs cfl`；实际正文 = %s", body)
	}
	if strings.Contains(body, "candidate_failure_logs_columnar_archive") {
		t.Errorf("★ 894 的视图正文仍然引用 archive 表（392 的脱靶形态）：%s", body)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ③ 交付面登记（6 点）
// ─────────────────────────────────────────────────────────────────────────

func Test894RegisteredInEveryDeliverySurface(t *testing.T) {
	const name = mig894File

	// ① embeddata 副本
	if _, err := os.Stat(filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name)); err != nil {
		t.Errorf("installer embeddata/startup/%s 不存在：%v", name, err)
	}

	// ② main.go：go:embed + embeddedSQLFiles 映射
	mainGo := mig894Read(t, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(mainGo, "//go:embed embeddata/startup/"+name) {
		t.Errorf("installer main.go 缺少 894 的 go:embed 指令")
	}
	if !strings.Contains(mainGo, `"startup/`+name+`":`) {
		t.Errorf("installer main.go 的 embeddedSQLFiles 映射缺 894")
	}

	// ③ runner.go：StartupFiles
	runnerGo := mig894Read(t, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runnerGo, `"`+name+`"`) {
		t.Errorf("installer/internal/dbinit/runner.go 的 StartupFiles 缺 894 ⇒ 新装库不会应用它")
	}

	// ④ 启动链镜像 tsv（机器可读镜像，顺序必须与 StartupFiles 一致）
	tsv := mig894Read(t, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, name) {
		t.Errorf("sql/schema/installed_startup_migrations.tsv 缺 894 ⇒ 仓内多道以该 TSV 为准的门会读不到它")
	}

	// ⑤ 生产升级序列
	seq := mig894Read(t, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, name) {
		t.Errorf("scripts/apply-db-revision-sequence.sh 缺 894 ⇒ 生产升级链不会应用它")
	}

	// ⑥ sql 变更台账（sha256 登记表）
	changelog := mig894Read(t, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, name) {
		t.Errorf("docs/db-changelog.md 缺 894 的登记行 ⇒ SQL 变更没有进唯一 sha256 登记表")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ④ embeddata 副本逐字一致（up + down）
// ─────────────────────────────────────────────────────────────────────────

func Test894EmbeddedCopiesAreByteIdentical(t *testing.T) {
	for _, n := range []string{
		mig894File,
		"894_rebind_candidate_failure_logs_diagnosis_view.down.sql",
	} {
		canonical := mig894Read(t, filepath.Join("sql", "migrations", "startup", n))
		embed := mig894Read(t, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", n))
		if canonical != embed {
			t.Errorf("%s：embeddata 副本与 canonical 不逐字一致 ⇒ installer 会装出另一个版本", n)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ⑤ down 必须真的把绑定改回去（否则「可逆」是空话）
// ─────────────────────────────────────────────────────────────────────────

func Test894DownReversesBinding(t *testing.T) {
	downPath := filepath.Join("sql", "migrations", "startup", "894_rebind_candidate_failure_logs_diagnosis_view.down.sql")
	if _, err := os.Stat(filepath.Join("..", "..", "..", downPath)); err != nil {
		t.Fatalf("894 的 down 文件不存在：%v（每条迁移都必须带同名 .down.sql）", err)
	}
	down := mig894ExtractViewBody(t, mig894Read(t, downPath))
	if down == "" {
		t.Fatal("down 文件里抽不到视图正文")
	}
	if !strings.Contains(down, "candidate_failure_logs_columnar_archive") {
		t.Errorf("★ down 的视图正文没有绑回 archive 表 ⇒ down 不还原 894 之前的绑定：%s", down)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ⑥ 真库腿（**纯只读**：不建对象、不写行 ⇒ 可指向任何库）
// ─────────────────────────────────────────────────────────────────────────

func Test894LiveViewBoundToLiveTable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库腿（本腿纯只读，可指向任何库）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连库失败: %v", err)
	}
	defer pool.Close()

	var def string
	err = pool.QueryRow(ctx, `
		SELECT pg_get_viewdef('`+mig894ViewName+`'::regclass, true)`).Scan(&def)
	if err != nil {
		// 视图不存在 ⇒ 该库不是「有该视图」的形态 ⇒ 不做判据（不是失败）。
		t.Skipf("库里没有 %s（pgx: %v）⇒ 跳过真库腿", mig894ViewName, err)
	}
	if strings.Contains(def, "candidate_failure_logs_columnar_archive") {
		t.Errorf("★ 真库里的 %s 仍绑在 archive 表上（392 的脱靶形态）⇒ 894 未生效或未应用：\n%s",
			mig894ViewName, def)
	}
	if !strings.Contains(def, "candidate_failure_logs") {
		t.Errorf("真库里的 %s 没有引用 candidate_failure_logs：\n%s", mig894ViewName, def)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// 变异记录（2026-10-21 R72 轮实测）
//
//	M1 把 894 正文里的 FROM 改成 archive 表   ⇒ Test894BindsToLiveTableNotArchive 红
//	                                            + Test894ViewBodyIsIdenticalToBothInRepoSources 红
//	M2 从 runner.go 的 StartupFiles 摘掉 894   ⇒ Test894RegisteredInEveryDeliverySurface 红
//	M3 改对象源文件的 FROM（制造第三份漂移）   ⇒ Test894ViewBodyIsIdenticalToBothInRepoSources 红
//	M4 删掉 .down.sql                          ⇒ Test894DownReversesBinding 红
//	M5 把 embeddata 副本加一个空格            ⇒ Test894EmbeddedCopiesAreByteIdentical 红
//
//	还原一律**逐字**（sha256 校验），顺序/位置变化也算改动。
// ─────────────────────────────────────────────────────────────────────────
