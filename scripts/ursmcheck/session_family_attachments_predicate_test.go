package ursm_test

// 会话族「有附件」谓词的守卫（2026-10-21 新增）
//
// # 背景：一次**由退役改造引入**的读端回归
//
// 2026-10-20（迁移 890 真删 v1 族）把附件元数据列的落点从 v1 宽表的
// `attachments` 换成会话族特征层的 `session_turn_details.attachments`
// （734 特征层，镜像桥仍在写）。**读端的谓词也跟着换了列**，而新列的
// 空值形态与 v1 时代**不一样**：
//
//   · v1 宽表：没有附件 ⇒ SQL NULL。
//   · 会话族特征层：`attachments` 是 jsonb，**空附件曾被写成 JSON `null`
//     标量**（2026-09-06～09-20 那一段历史分区），2026-09-21 起才改成
//     SQL NULL。
//
// ⇒ `WHERE attachments IS NOT NULL` 在新列上**语义反了**：
// JSON `null` 也是「NOT NULL」⇒ 生产实测（252 共享库）它命中
// **454,963** 行（其中 446,891 行是 JSON null ⇒ 根本没有附件），
// 而「真的有附件（非空数组）」只有 **8,072** 行。
//
// ⇒★ 而且这个谓词落在 LEFT JOIN 的**右表**（details 侧）⇒ 规划器无法用
// 会话族的任何索引去满足 `ORDER BY ts DESC LIMIT 50` ⇒ 只能全扫
// session_turns(1,067,701 行) + session_turn_details(1,055,459 行) 再
// hash join + top-N sort。
//
// # 生产实测（252 共享库，2026-10-21，EXPLAIN ANALYZE）
//
//	旧谓词 `rl.attachments IS NOT NULL` ⇒ Execution Time **17,582 ms**
//	  （255k buffers + 17k temp 溢写）⇒ 撞 10s ctx deadline ⇒ HTTP 500
//	  （154 生产日志实证：status=500, duration_ms=10001,
//	    WARN attachments: list query failed "timeout: context deadline exceeded"）
//	新谓词 `rl.attachment_count > 0`  ⇒ Execution Time **1,317 ms**（13.4×）
//	  ⇒ 谓词被**下推进两臂**，用上分区部分索引
//	    `session_turns_2026_09_heap_attachment_count_idx` /
//	    `session_turns_2026_10_attachment_count_idx`（索引谓词逐字相同）。
//
// # 这道门钉的是什么
//
//	① 谓词常量**逐字**必须是 `rl.attachment_count > 0`（AST 解析，**不看注释**）；
//	② 该常量必须被**至少 3 处**引用（列表 / 统计 / 清理预览 三个会话族读端
//	   都得用它 ⇒ 漏一处就有一处退回全扫）；
//	③ 会话族读端的 SQL 字面量里**不得**再出现 `rl.attachments IS NOT NULL`
//	   （手写回退会被抓住）。
//
// ⚠ 本门**只做静态判定**（读源码 + AST），不连库 ⇒ 不产生任何生产写入。
//
// ★ 读法纪律（沿用 R65 的教训）：**断言 SQL 文本前必须剥注释** ——
// 本门的常量值用 AST 取 `BasicLit` 的真实值，字符串字面量里的 `--`/`/* */`
// 注释先剥掉再比对 ⇒ 注释里写「正确谓词」骗不过门。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

const attachmentsHandlerGoRel = "admin/data_lifecycle_attachments.go"

// attachmentsPresencePredicateConstName 是生产代码里那个谓词常量的名字。
const attachmentsPresencePredicateConstName = "attachmentsPresencePredicate"

// attachmentsPresencePredicateWant 是它**必须**逐字等于的谓词。
//
// ★ 为什么不能用 `rl.has_attachments IS TRUE`：那一列是
// `CASE WHEN attachment_count IS NULL THEN NULL WHEN attachment_count > 0
//  THEN true ELSE false END`，规划器**推不出**它蕴含索引谓词
// `attachment_count > 0`（NULL 分支）⇒ 部分索引用不上 ⇒ 退回全扫。
const attachmentsPresencePredicateWant = "rl.attachment_count > 0"

// stripSQLComments 去掉 SQL 文本里的 `--` 行注释与 `/* */` 块注释。
// 断言 SQL 文本前必须剥注释：否则注释里出现「正确谓词」就能骗过门。
func stripSQLComments(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "--"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				i = len(s)
				continue
			}
			out.WriteByte('\n')
			i += j + 1
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				i = len(s)
				continue
			}
			out.WriteByte(' ')
			i += 2 + j + 2
		default:
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

// attachmentsHandlerLiterals 返回目标文件里所有字符串字面量（已剥 SQL 注释）。
func attachmentsHandlerLiterals(t *testing.T, root string) []string {
	t.Helper()
	path := root + "/" + attachmentsHandlerGoRel
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		out = append(out, stripSQLComments(v))
		return true
	})
	return out
}

// TestAttachmentsPresencePredicateIsTheIndexableOne 钉住谓词常量的逐字取值。
func TestAttachmentsPresencePredicateIsTheIndexableOne(t *testing.T) {
	root := repoRootFromUrsm(t)
	path := root + "/" + attachmentsHandlerGoRel
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}

	var got string
	var found bool
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name != attachmentsPresencePredicateConstName {
					continue
				}
				if i >= len(vs.Values) {
					t.Fatalf("常量 %s 没有值", attachmentsPresencePredicateConstName)
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("常量 %s 的值不是字符串字面量", attachmentsPresencePredicateConstName)
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("常量 %s 的值无法解析: %v", attachmentsPresencePredicateConstName, err)
				}
				got, found = v, true
			}
		}
	}
	if !found {
		t.Fatalf("%s 里找不到常量 %s", attachmentsHandlerGoRel, attachmentsPresencePredicateConstName)
	}
	if got != attachmentsPresencePredicateWant {
		t.Fatalf("谓词常量取值不对:\n  期望 %q\n  实得 %q\n"+
			"  （`rl.attachments IS NOT NULL` 在会话族特征层上语义反了：JSON null 也算 NOT NULL，"+
			"且落在 LEFT JOIN 右表侧 ⇒ 全扫；详见本文件头注释）",
			attachmentsPresencePredicateWant, got)
	}
}

// TestAttachmentsPresencePredicateIsUsedByEverySessionFamilyReader 钉住
// 「三个会话族读端都用了这个常量」——漏一处就有一处退回全扫。
func TestAttachmentsPresencePredicateIsUsedByEverySessionFamilyReader(t *testing.T) {
	root := repoRootFromUrsm(t)
	path := root + "/" + attachmentsHandlerGoRel
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}

	// 引用数 = 文件里该标识符出现次数 − 声明处 1 次。
	refs := 0
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == attachmentsPresencePredicateConstName {
			refs++
		}
		return true
	})
	uses := refs - 1 // 减去 const 声明本身的名字
	const wantUses = 3 // 列表 / 统计 / 清理预览
	if uses < wantUses {
		t.Fatalf("常量 %s 只被引用 %d 处，至少要 %d 处（列表 / 统计 / 清理预览）：\n"+
			"  漏掉的读端会退回 `rl.attachments IS NOT NULL` ⇒ 生产实测 17,582ms ⇒ HTTP 500",
			attachmentsPresencePredicateConstName, uses, wantUses)
	}
}

// TestNoSessionFamilyReaderFiltersOnJoinRightSide 钉住「会话族读端不得
// 再用 `rl.attachments IS NOT NULL`」——手写回退会被抓住。
//
// ⚠ 清理**写**路径（`session_turn_details_hot` 直清）允许保留
// `r.attachments IS NOT NULL`：它的语义是「把 hot 上任何非空 attachments
// 置 NULL」，与读端的「挑出有附件的请求」不是一回事，且 hot 表只有几千行
// ⇒ 不受全扫影响。本门只在**会话族别名 `rl.`** 上设限。
func TestNoSessionFamilyReaderFiltersOnJoinRightSide(t *testing.T) {
	root := repoRootFromUrsm(t)
	for i, lit := range attachmentsHandlerLiterals(t, root) {
		if !strings.Contains(lit, "rl.attachments IS NOT NULL") {
			continue
		}
		t.Fatalf("%s 第 %d 个字符串字面量里仍有 `rl.attachments IS NOT NULL`：\n%s\n"+
			"  ⇒ 该谓词落在 LEFT JOIN 右表侧 ⇒ 规划器用不上分区部分索引，"+
			"生产实测 17,582ms ⇒ HTTP 500；应改用 `%s`",
			attachmentsHandlerGoRel, i, strings.TrimSpace(lit), attachmentsPresencePredicateWant)
	}
}

// TestAttachmentsPredicateTargetsThePartitionPartialIndex 钉住
// 「谓词与分区部分索引的谓词逐字同形」——这是下推能发生、计划能走索引的
// **唯一**前提（谓词写成 `has_attachments IS TRUE` 或
// `jsonb_typeof(...) = 'array'` 都推不出 ⇒ 退回全扫）。
func TestAttachmentsPredicateTargetsThePartitionPartialIndex(t *testing.T) {
	// 分区上的部分索引谓词（2026-10-21 生产实测）：
	//   CREATE INDEX session_turns_2026_09_heap_attachment_count_idx
	//     ON public.session_turns_2026_09 USING btree (attachment_count)
	//     WHERE (attachment_count > 0)
	//   CREATE INDEX session_turns_2026_10_attachment_count_idx
	//     ON public.session_turns_2026_10 USING btree (attachment_count)
	//     WHERE (attachment_count > 0)
	const indexPredicate = "attachment_count > 0"
	if !strings.Contains(attachmentsPresencePredicateWant, indexPredicate) {
		t.Fatalf("谓词常量 %q 不含分区部分索引的谓词 %q ⇒ 规划器推不出索引可用 ⇒ 退回全扫",
			attachmentsPresencePredicateWant, indexPredicate)
	}
	// 反向自查：谓词里不得出现会让索引谓词**推不出来**的包装。
	for _, bad := range []string{"has_attachments", "jsonb_typeof", "COALESCE", "IS TRUE"} {
		if strings.Contains(attachmentsPresencePredicateWant, bad) {
			t.Fatalf("谓词常量 %q 含 %q：这一层包装会让规划器**推不出**索引谓词 "+
				"`attachment_count > 0` ⇒ 部分索引用不上 ⇒ 生产实测退回 17,582ms",
				attachmentsPresencePredicateWant, bad)
		}
	}
}
