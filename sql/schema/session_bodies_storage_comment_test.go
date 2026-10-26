package schema

// session_bodies_storage_comment_test.go —— **基线存储引擎注释的订正门**
// （2026-10-20 会话族审计 G-6 复核轮 / 迁移 886）。
//
// # 这道门防什么
//
// 430_sessions_v2_schema.sql:237-240 写下的表注释是：
//
//	COMMENT ON TABLE public.session_bodies IS
//	  'V2正文存储表：…… 使用columnar存储格式，配合zstd压缩，预计可节省60-80%磁盘空间。'
//
// 这句话与实现**相反**：session_bodies 族是 heap + TOAST(lz4)，而且
// **不可能**是 columnar（唯一约束 / ON CONFLICT / UNION ALL 视图族三重互斥，
// 论证见 765_bodies_columnar_storage.sql:13-21 与
// 829_bodies_columnar_rollback.sql）。
//
// 2026-10-20 的 D3 轮（分支 G-3）已经订正了**正典目录**
// （sql/objects/tables/session_bodies.sql），但**三份基线快照**
// （sql/schema/01-schema.sql、deploy/sql/schemas/baseline/01-schema.sql、
// installer/cmd/llm-gw-installer/embeddata/01-schema.sql）里仍然是**旧的不实
// 注释** —— 而新装实际用的就是第三份 ⇒ 一次全新安装出来的库，
// \d session_bodies 依然会显示「使用columnar存储格式」。
//
// 迁移 886 用 COMMENT ON TABLE 订正**存量库**，三份基线同步订正**新装库**。
// 本门钉住「三条路径（正典 / 基线 / 迁移）说的是同一句话」。
//
// # 为什么用「文本逐字相等」而不是「包含 heap 字样」
//
// 判据纪律：只断言「没坏」的判据，在「干脆没做」面前永远是绿的。
// 包含 "heap" 三个字母的文件可以一边说 heap 一边说 columnar。
// 这里比的是**整段注释的逐字相等** —— 正典、基线、迁移三者必须说同一句话，
// 且该句话里**不得**再出现那句不实的「使用columnar存储格式」。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// baselineCopies 是基线 schema 的三份副本。
//
// ★ 三份的分工：前两份必须**逐字节相同**（deploy/sql/verify-migration.sh 会
// diff），第三份是新装实际使用的快照。本门同时钉住「三份彼此相同」与
// 「三份与正典/迁移说同一句话」。
var baselineCopies = []string{
	"../../sql/schema/01-schema.sql",
	"../../deploy/sql/schemas/baseline/01-schema.sql",
	"../../installer/cmd/llm-gw-installer/embeddata/01-schema.sql",
}

const (
	canonicalSessionBodiesFile = "../../sql/objects/tables/session_bodies.sql"
	migration886File           = "../../sql/migrations/startup/886_session_bodies_storage_comment.sql"
)

// falseColumnarClaim 是那句不实的声明（430 的原文）。
//
// 它是**逐字**取自 430 的注释，不是概括 —— 概括出来的字符串会漏掉变体
// （例如"使用columnar存储格式" vs "使用 columnar 存储格式"），
// 而漏掉的变体正好是判据要抓的东西。
const falseColumnarClaim = "使用columnar存储格式"

// sessionBodiesCommentRe 抓 `COMMENT ON TABLE public.session_bodies IS '...'` 整段。
var sessionBodiesCommentRe = regexp.MustCompile(`(?is)COMMENT\s+ON\s+TABLE\s+public\.session_bodies\s+IS\s+('(?:[^']|'')*')\s*;`)

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// extractSessionBodiesComment 返回文件里 session_bodies 的表注释整段
// （含 `COMMENT ON TABLE ... IS '...';` 的完整文本）。
func extractSessionBodiesComment(t *testing.T, path string) string {
	t.Helper()
	body := readRepoFile(t, path)
	m := sessionBodiesCommentRe.FindString(body)
	if m == "" {
		t.Fatalf("%s 里找不到 `COMMENT ON TABLE public.session_bodies IS '...'` —— "+
			"要么表注释被删了，要么写法漂移（判据的尺子要跟着改）", path)
	}
	return m
}

// TestSessionBodiesBaselineCommentStatesTheRealStorageEngine 是正题：
// 三份基线副本的 session_bodies 注释必须与正典目录**逐字相同**，
// 且不得再含那句不实的「使用columnar存储格式」。
func TestSessionBodiesBaselineCommentStatesTheRealStorageEngine(t *testing.T) {
	canonical := extractSessionBodiesComment(t, canonicalSessionBodiesFile)

	if strings.Contains(canonical, falseColumnarClaim) {
		t.Fatalf("正典 DDL 仍含不实的存储引擎声明 %q：\n%s", falseColumnarClaim, canonical)
	}

	for _, path := range baselineCopies {
		got := extractSessionBodiesComment(t, path)
		if strings.Contains(got, falseColumnarClaim) {
			t.Errorf("%s 仍含不实的存储引擎声明 %q：\n%s\n"+
				"真相是 heap + lz4 TOAST（765_bodies_columnar_storage.sql:13-21）；"+
				"新装实际用的是 installer 那份副本 ⇒ 不改它等于新装出来的库继续撒谎",
				path, falseColumnarClaim, got)
			continue
		}
		if got != canonical {
			t.Errorf("%s 的 session_bodies 注释与正典目录**不逐字相同**：\n--- 正典 ---\n%s\n--- 本文件 ---\n%s\n"+
				"三条路径（新装基线 / 存量库迁移 / 正典目录）必须说同一句话，"+
				"否则同一个表在不同环境里对外契约不同",
				path, canonical, got)
		}
	}
}

// TestSessionBodiesBaselineCopiesAreByteIdentical 钉住三份副本彼此相同。
//
// deploy/sql/verify-migration.sh 只 diff 前两份；第三份（新装实际用的那份）
// 没有字节门 ⇒ 它会独自漂移。本门补上。
func TestSessionBodiesBaselineCopiesAreByteIdentical(t *testing.T) {
	ref := readRepoFile(t, baselineCopies[0])
	for _, path := range baselineCopies[1:] {
		if got := readRepoFile(t, path); got != ref {
			t.Errorf("%s 与 %s 不再逐字节相同 —— 基线副本分叉了"+
				"（新装用的是 %s；deploy/sql/verify-migration.sh 只 diff 前两份）",
				path, baselineCopies[0], baselineCopies[2])
		}
	}
}

// TestMigration886CarriesTheSameSessionBodiesComment 钉住迁移与基线同文。
//
// 迁移 886 是**存量库**的订正路径，基线是**新装库**的订正路径。
// 两条路径必须落成同一句话，否则同一张表在升级环境与新装环境里
// 对外契约不同（这正是「基线自洽」原则的第三条腿）。
func TestMigration886CarriesTheSameSessionBodiesComment(t *testing.T) {
	canonical := extractSessionBodiesComment(t, canonicalSessionBodiesFile)
	mig := readRepoFile(t, migration886File)

	if !strings.Contains(mig, "COMMENT ON TABLE public.session_bodies") {
		t.Fatalf("%s 里没有 `COMMENT ON TABLE public.session_bodies` —— 订正迁移没做它该做的事", migration886File)
	}
	// 迁移里那句注释是包在 DO 块里的，缩进与基线不同 ⇒ 比**引号内的字面量**。
	literal := sessionBodiesCommentRe.FindStringSubmatch(canonical)
	if literal == nil {
		t.Fatalf("正典注释块解析不出字面量（判据的尺子坏了）")
	}
	inner := literal[1]
	if !strings.Contains(mig, inner) {
		t.Errorf("%s 里的表注释与正典目录**不逐字相同**：\n--- 正典字面量 ---\n%s\n"+
			"迁移是存量库的订正路径，基线是新装库的订正路径，两条路径必须落成同一句话",
			migration886File, inner)
	}
	// 那句不实声明在迁移文件里**只允许**出现在注释行（`--` 开头）里 ——
	// 也就是「引用 430 的原文」这一种用法。
	//
	// ★ 为什么钉「行级注释」而不是「文件里出现过就报错」：
	//   迁移的头部**必须**引用 430 的原话（那是本迁移的立项证据），
	//   所以「文件里出现这句话」本身不是缺陷；缺陷是它出现在**可执行语句**里
	//   （那就等于把不实声明又写回库了）。行级断言正好钉住这个区别。
	for i, line := range strings.Split(mig, "\n") {
		if !strings.Contains(line, falseColumnarClaim) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "--") {
			t.Errorf("%s:%d 不实声明 %q 出现在**可执行语句**里（不是注释行）：\n%s\n"+
				"它只能作为「被订正的旧文本」被引用，不能被再次写进库",
				migration886File, i+1, falseColumnarClaim, line)
		}
	}
}
