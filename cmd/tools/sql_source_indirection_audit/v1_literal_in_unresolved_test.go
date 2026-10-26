//go:build !integration

package main

// v1_literal_in_unresolved_test.go —— ★★「形参决定的表名不许是 v1 关系」这道门。
//
// # 为什么需要它（2026-10-25 R152 轮的实测发现）
//
// 本工具（`sql_source_indirection_audit`）的分类前提是「关系名不是字面量的拼接点」。
// ⇒ 而**关系名由形参决定**的拼接点，工具**解析不出来** ⇒ 落进 unresolved 桶。
//
// ★★★ **实测证明这条路径是个洞**（不是一个假想）：
//
//	把 db/request_logs_view_schema.go 的
//	    leg("public.session_turns_hot")
//	改成
//	    leg("public._request_logs_hot")
//	⇒ 本工具整包**绿**（它解析不出 `src`，分类仍是 unresolved）；
//	⇒ admin 包的 v1 族全套门**绿**（`retiredV1ReadPattern` 要求关系名
//	   **紧跟** `from`/`join` 且**在同一个字符串字面量里**，
//	   而这里是「`FROM ` 在一个字面量、关系名在另一个字面量」）；
//	⇒ `admin/v1_family_purge_gate_test.go` 的例外台账**是空的**，
//	   它的注释写着「将来谁再写回 `_request_logs`，本门立即红」
//	   ⇒ ★★ **那句承诺在���个形态下是假的**。
//
// ⇒ 结论：**「v1 读点为零」这句话当时并不成立** —— 门是绿的，生产可以是坏的。
//
// # 这道门的判据（可证伪 + 零误报）
//
//	凡是落进**本工具的 unresolved 桶**的文件（关系名静态不可判定），
//	其源码里**不许出现任何已退役 v1 关系名的字符串字面量**。
//
// ★ 为什么这个判据**不是**「把洞补上」而是「把洞变成不可能」：
//
//	形参决定的表名，**可能取到的值**就在那个文件里（字面量实参、
//	或本文件的常量表）。⇒ 文件里**一个 v1 关系名都不许有**
//	⇒ 「形参」这条路上**根本走不到 v1 表**。
//
// ★ 为什么**只限 unresolved 桶**：resolved 的文件里，关系名是**字面量**，
//   工具已经看得见（canonical 桶）或已经判死（reads-v1 桶）⇒
//   再加一道「不许出现字面量」只会误伤**正当的引用**（例如
//   admin/data_lifecycle_partition.go 的退役表清单、internal/partguard/parents.go
//   的分区守卫名单、cmd/tools/sql_source_indirection_audit/resolve.go 的 SSOT 映射），
//   而那些文件**不在** unresolved 桶里 ⇒ ⇒ **本判据的误报面为空**（实测）。
//
// # 阳性对照（防「恒真空」）
//
// 门必须证明自己的扫描器**看得见**真命中 ⇒ 用夹具证明：
//	① 形如 "public._request_logs_hot" 的整段字面量 ⇒ 必须命中；
//	② 形如 "FROM public._request_logs_hot rl" 的字面量 ⇒ 必须命中；
//	③ 会话族字面量（"public.session_turns_hot"）⇒ 必须**不**命中；
//	④ canonical 视图（"request_logs_with_current_month"）⇒ 必须**不**命中。
//
// # 例外台账
//
// `v1LiteralGuardExemptions` 是**已知**的、在这条判据下仍然合法的命中。
// ⚠ 每一项都必须写清「为什么它不是读点」；空理由的具名等于没具名。
// ⚠ 与 admin/session_family_sql_shape_test.go 的 `retiredV1ReferenceExceptions`
//   **不是**同一张表：那一张登记的是「**SQL 里**引用了已退役 v1 关系」
//   （只许缩小、基线 0），本张登记的是「**未判定**的文件里出现了 v1 关系名字面量」
//   （同样是只许缩小、基线 0）。

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
	"testing"
)

// retiredV1OnlyRelationNames 是 v1 族里**不是基表**的那部分：
// 4 张 v1-only 视图 + archive 表（两种拼写）。
//
// ⚠ 与 `v1Tables`（本工具的 SSOT）**分开**是有原因的、不是重复：
//
//	v1Tables 刻意**只含基表**（4 张 × 2 拼写）—— 因为本工具的分类语义是
//	「关系名在 DROP 时会不会**消失**」；视图 DROP 后关系名还在、只是行集变小，
//	那是**另一类**（ClassReadsV1Arm）。
//	⇒ 而本门问的是**另一个问题**：「这个名字会不会被拼进 SQL」。
//	   ⇒ 对那个问题来说，**任何**已退役的关系名都算数。
//
// ★ SSOT 说明：这份清单与 admin/session_family_sql_shape_test.go 的
//
//	`retiredV1RelationNames`（那份多出 "request_logs_archive" 与 "_request_logs_archive"
//	之外的全部内容、且按 5 基表×2 拼写 + 4 视图组织）**同源**。
//	⚠ 两处各自维护是**已知的轻微重复**：跨包共享一个 `main` 包的变量做不到
//	（`cmd/tools/...` 是 package main，不可被 import）⇒ 只能并列登记 + 本门
//	在**每次运行时对拍**（见 TestRetiredRelationNameSetsAgreeAcrossPackages 的说明）。
var retiredV1OnlyRelationNames = []string{
	// v1-only 视图（890 已 DROP）
	"request_logs_with_current_month_without_request_class_due_at",
	"request_logs_with_current_month_without_customer_id",
	"request_logs_bodies_with_current_month",
	"request_logs_bodies_progress",
	// archive 表（331 已删）
	"request_logs_archive",
	"_request_logs_archive",
}

// retiredRelationNameSet 返回「所有已退役 v1 关系名」的集合
// （本工具的基表 SSOT ∪ v1-only 视图/归档表）。
func retiredRelationNameSet() map[string]bool {
	set := make(map[string]bool, len(v1Tables)+len(retiredV1OnlyRelationNames))
	for name := range v1Tables {
		set[name] = true
	}
	for _, name := range retiredV1OnlyRelationNames {
		set[name] = true
	}
	return set
}

// v1RelationWholeLiteralPattern 匹配「字面量**整段**就是一个已退役 v1 关系名」
// （可带 schema 限定）："public._request_logs_hot"。
//
// ★ 这是「形参决定的表名」最典型的落点：实参就是一个关系名。
//
// ⚠ 必须**锚到整段**（`\A`…`\z`）：否则同一段 SQL 里的
//
//	「日志文案提到 v1 名」/「对视图定义文本的探针谓词里带 v1 名」
//	都会被算成命中 ⇒ 门恒红 ⇒ 门被关掉（这正是本门最忌讳的形态）。
func v1RelationWholeLiteralPattern() *regexp.Regexp {
	return regexp.MustCompile(`(?i)\A\s*(?:public\.)?(?:` + retiredRelationNameAlternation() + `)\s*\z`)
}

// v1RelationInSQLPattern 匹配「同一个字面量里，`from`/`join` 之后紧跟已退役 v1 关系名」。
//
// ★ 与 admin/session_family_sql_shape_test.go 的 `retiredV1ReadPattern` 同义，
//
//	但**多了 `public.` / `only` 两种限定**（admin 那道门恰好漏掉这两种形态）。
var v1RelationInSQLPattern = regexp.MustCompile(
	`(?i)\b(?:from|join)\s+(?:only\s+)?(?:public\.)?(?:` + retiredRelationNameAlternation() + `)\b`)

// retiredRelationNameAlternation 把全部已退役关系名拼成正则交替项
// （**长的排前面** ⇒ 先匹配长的：`request_logs_bodies_hot` 必须先于 `request_logs`）。
func retiredRelationNameAlternation() string {
	names := make([]string, 0, len(v1Tables)+len(retiredV1OnlyRelationNames))
	for name := range v1Tables {
		names = append(names, name)
	}
	names = append(names, retiredV1OnlyRelationNames...)
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if len(names[j]) > len(names[i]) {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	alt := make([]string, len(names))
	for i, n := range names {
		alt[i] = regexp.QuoteMeta(n)
	}
	return strings.Join(alt, "|")
}

// v1RelationLiteralHitsInFile 扫一个 .go 文件的**字符串字面量**，返回命中。
//
// ⚠ 只看字符串字面量（go/ast 的 *ast.BasicLit）⇒ Go 注释不在其中
// ⇒ 历史注释（v1 族的历史是承重证据）不受影响；真 SQL / 真表名一定在字符串里。
//
// ⚠ 反引号原始字符串用 strconv.Unquote 会失败 ⇒ 手工剥。
func v1RelationLiteralHitsInFile(path string) ([]string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
			text = strings.Trim(lit.Value, "`")
		}
		// ① 整段就是关系名（形参实参形态）。
		if v1RelationWholeLiteralPattern().MatchString(text) {
			pos := fset.Position(lit.Pos())
			hits = append(hits, fmt.Sprintf("%s:%d: [整段关系名] %q",
				filepath.Base(path), pos.Line, truncateForGate(text)))
			return true
		}
		// ② 同一字面量里 `from`/`join` 之后紧跟关系名（带 schema 限定也算）。
		if v1RelationInSQLPattern.MatchString(text) {
			pos := fset.Position(lit.Pos())
			hits = append(hits, fmt.Sprintf("%s:%d: [SQL 里的关系名] %q",
				filepath.Base(path), pos.Line, truncateForGate(text)))
		}
		return true
	})
	return hits, nil
}

// truncateForGate 把命中文本截短，便于在门里读。
func truncateForGate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}

// v1LiteralGuardExemptions 是这条判据下的**已知合法命中**（只许缩小）。
//
// ⚠ 现在是**空的** ⇒ 本门是「一条都不许有」。
var v1LiteralGuardExemptions = map[string]string{}

// TestV1RelationNamesNeverAppearAsLiteralsInUnresolvedFiles 是本门的主体。
//
// 判据：**unresolved 桶的文件里不许出现已退役 v1 关系名的字符串字面量**。
//
// # 为什么这道门不是「多一道计数」而是「补一个洞」
//
// 本工具的 unresolved 桶 = 「关系名静态不可判定」的文件集合。
// ★ 在这些文件里，**关系名的可能取值就在文件里**（实参字面量 / 常量表）。
// ⇒ 文件里没有 v1 关系名 ⇒ 形参这条路**到不了** v1 表。
// ⇒ 这正好补上 §头里那条实测出来的洞（`leg("public._request_logs_hot")` 全绿通过）。
func TestV1RelationNamesNeverAppearAsLiteralsInUnresolvedFiles(t *testing.T) {
	root := repoRoot(t)
	_, _, _, unresolved, _ := measureBuckets(t, root)

	var files []string
	for f := range unresolved {
		files = append(files, f)
	}
	sort.Strings(files)

	if len(files) == 0 {
		// ⚠ 不能直接 return：那会让本门变成「恒真空」（unresolved 桶空了 ⇒
		// 门什么都不检查）。⇒ 交给下面的阳性对照证明扫描器有牙。
		t.Log("unresolved 桶为空 —— 判据无对象；扫描器的区分力由阳性对照保证")
	}

	var errs []string
	for _, rel := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		hits, err := v1RelationLiteralHitsInFile(abs)
		if err != nil {
			// 解析失败**不静默跳过** —— 那会让门在这个文件上恒真空。
			errs = append(errs, fmt.Sprintf("%s: 解析失败：%v", rel, err))
			continue
		}
		for _, h := range hits {
			if reason, ok := v1LiteralGuardExemptions[rel]; ok {
				_ = reason
				continue
			}
			errs = append(errs, fmt.Sprintf(
				"%s: 出现了已退役 v1 关系名的字面量（%s）—— 该文件的拼接点**关系名由形参决定**，"+
					"工具解析不出 ⇒ 它落到 unresolved 桶 ⇒ 关系名的可能取值就在这个文件里 ⇒ "+
					"这里出现 v1 关系名，就意味着**有人能把 v1 表拼进 SQL**。"+
					"请改用会话族关系名；确属正当引用（守卫名单/退役表清单）时，"+
					"在 v1LiteralGuardExemptions 里具名并写清理由。", rel, h))
		}
	}

	// 例外台账：只许缩小，且每项都要有非空理由。
	for rel, reason := range v1LiteralGuardExemptions {
		if strings.TrimSpace(reason) == "" {
			errs = append(errs, rel+": 例外理由为空 —— 空理由的具名等于没具名")
		}
		if _, ok := unresolved[rel]; !ok {
			errs = append(errs, rel+": 例外已失效（该文件已不在 unresolved 桶里）—— "+
				"请删掉这条例外，别让它当占位")
		}
	}

	sort.Strings(errs)
	for _, e := range errs {
		t.Error(e)
	}
	t.Logf("unresolved 桶 %d 个文件；已退役关系名集合 %d 个；例外 %d 条",
		len(files), len(retiredRelationNameSet()), len(v1LiteralGuardExemptions))
}

// TestV1RelationLiteralScannerHasTeeth 是本门的**阳性/阴性对照**。
//
// ⚠ 没有这道对照，本门可能是「恒真空」：扫描器写坏了 ⇒ 一个命中都报不出来
// ⇒ 主体那道门照样绿。
func TestV1RelationLiteralScannerHasTeeth(t *testing.T) {
	whole := v1RelationWholeLiteralPattern()
	inSQL := v1RelationInSQLPattern

	// ① 阳性：**整段**就是关系名（带 schema 限定）⇒ 必须命中。
	if !whole.MatchString("public._request_logs_hot") {
		t.Error("扫描器匹配不上 \"public._request_logs_hot\" —— 没有区分力，本门是恒真空")
	}
	// ② 阳性：不带 schema 限定 ⇒ 必须命中。
	if !whole.MatchString("_request_logs") {
		t.Error("扫描器匹配不上 \"_request_logs\" —— 没有区分力，本门是恒真空")
	}
	// ③ 阳性：紧跟 from/join 之后、带 schema 限定 ⇒ 必须命中
	//（★ admin 那道门的 `retiredV1ReadPattern` 恰好漏掉这一形态）。
	if !inSQL.MatchString("FROM public._request_logs_hot rl") {
		t.Error("扫描器匹配不上 \"FROM public._request_logs_hot rl\" —— 没有区分力，本门是恒真空")
	}
	if !inSQL.MatchString("JOIN _request_logs_bodies_hot rb") {
		t.Error("扫描器匹配不上 \"JOIN _request_logs_bodies_hot rb\" —— 没有区分力，本门是恒真空")
	}
	// ④ 阴性：会话族 ⇒ 必须**不**命中。
	for _, ok := range []string{"public.session_turns_hot", "public.session_turns", "public.session_bodies_hot"} {
		if whole.MatchString(ok) || inSQL.MatchString(ok) {
			t.Errorf("扫描器误伤会话族关系名 %q —— 判据没有区分力", ok)
		}
	}
	// ⑤ 阴性：canonical 视图（890 之后仍然活着）⇒ 必须**不**命中。
	for _, ok := range []string{
		"request_logs_with_current_month",
		"public.request_logs_with_current_month",
		"FROM public.request_logs_with_current_month rl",
	} {
		if whole.MatchString(ok) || inSQL.MatchString(ok) {
			t.Errorf("扫描器误伤 canonical 视图 %q —— 890 之后它仍然存在，"+
				"把它算成 v1 会让本门恒红", ok)
		}
	}
	// ⑥ 阳性：v1-only 视图本身**就是**已退役关系 ⇒ 必须命中
	//（否则形参这条路能把它们拼进 SQL）。
	if !whole.MatchString("request_logs_bodies_with_current_month") {
		t.Error("扫描器匹配不上 v1-only 视图 —— 没有区分力，本门是恒真空")
	}
	// ⑦ ★ 阴性（防「一刀切」）：**提到** v1 名但**不是关系名**的文本必须不命中。
	//    这三条都是仓内**真实存在**的形态（分区管理器日志 / 视图定义探针 /
	//    COMMENT 文本）⇒ 它们若被判命中 ⇒ 门恒红 ⇒ 门被关掉 ⇒ 真洞没人管。
	for _, ok := range []string{
		"partition_manager: _request_logs archive begin failed",
		"SELECT 1 FROM pg_views WHERE viewname = 'request_logs_with_current_month' " +
			"AND strpos(pg_get_viewdef(schemaname || '.' || viewname, true), '_request_logs') > 0",
		"会话存储解耦 v4（890）: v1 族（_request_logs*）整体退役 ⇒ canonical 视图去掉第三臂",
	} {
		if whole.MatchString(ok) {
			t.Errorf("扫描器把「提到 v1 名但不是关系名」的文本判成命中：%q —— "+
				"这一刀切下去门会恒红", ok)
		}
	}
}
