package sqlguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 本文件是「绑定健康四元组」的**全仓不变式门**。
//
// # 背景（2026-11-17 F3 残留地平线根修轮）
//
//	`credential_model_bindings` 的健康状态是四元组，四列必须同时成立：
//
//	  (available, unavailable_reason, unavailable_at, unavailable_recover_at)
//	    可用   ⇒ reason IS NULL ∧ unavailable_at IS NULL ∧ recover_at IS NULL
//	    不可用 ⇒ reason 非空 ∧ unavailable_at IS NULL/非空（recover_at 可空，见下）
//
//	★ 此前只查「不可用」的半边，于是「**已恢复但恢复标记没被清掉**」的
//	  矛盾态（available=TRUE ∧ recover_at≠NULL）长期无人管 ⇒ F3 残留地平线
//	  在 37~231 行之间反复反弹（2026-11-17 观测 19 轮，从未归零）。
//
//	★ 本门钉住的是**写入侧的不变式**：任何把 available 置为 TRUE 的语句，
//	  都必须同时处理 unavailable_recover_at —— 否则它就是新的残留产生路径。
//	  ★ 迁移 972 修的是「经 model_offers 视图」那条路（触发器的 SET 清单）；
//	    直写 cmb 的路径**各有各的 SET 清单** ⇒ 触发器管不到 ⇒ 必须逐条自查。
//
// # 为什么用 go/parser 而不是正则扫文本
//
//	★ Go 源码里**结构体标签用的是反引号**（`json:"x"`）⇒ 用正则按反引号
//	  配对切 SQL 字符串会**错位**（实测：按反引号配对扫全仓，把 4 条
//	  `available = FALSE` 的语句误判成违规）⇒ 必须走 AST。
//
// # 判据（为什么是这条，不是别的）
//
//	① 只看**直写 cmb** 的语句：
//	   `UPDATE credential_model_bindings …` 与
//	   `INSERT INTO credential_model_bindings … ON CONFLICT … DO UPDATE SET …`
//	   （★ 后者是目录批量刷新的形态，实测是残留地平线的主要产生源）。
//	   经 `model_offers` 视图的写入由 INSTEAD OF UPDATE 触发器统一处理
//	   （迁移 972 已把 unavailable_recover_at 补进触发器的 SET 清单）⇒
//	   视图侧不要求调用方自己写这一列。
//	② 只看**恢复方向**（SET 子句把 available 置 TRUE / CASE）：
//	  标记不可用的方向（available = FALSE）不要求带 recover_at ——
//	  cmb 侧恢复语句用 COALESCE(unavailable_recover_at,
//	  unavailable_at + INTERVAL '30 seconds') 兜底，且 manual% 手工保持
//	  本来就不该有重试时刻。
//	③ 命中即红：语句把 available 置 TRUE 却没提 unavailable_recover_at
//	  ⇒ 它会留下一个陈旧的恢复标记 ⇒ 残留地平线重新开始反弹。
//
// # 例外表（有意不守四元组的语句，必须写明理由）
var cmbHealthTupleExemptions = map[string]string{}

// cmbHealthTuplePathExemptions ＝ 按**路径**豁免的目录（必须在表里写明理由）。
//
// ★ 例外表必须写理由：没有理由的例外＝缺口改名，门会失效。
var cmbHealthTuplePathExemptions = map[string]string{
	"storage/sqlite": "SQLite 形态的**独立存储**（不是 211 生产库所用的引擎）：它的 " +
		"credential_model_bindings 只有 (credential_id, model_id, available, created_at) " +
		"四列（storage/sqlite/schema.go:190）⇒ 根本没有 unavailable_reason / " +
		"unavailable_at / unavailable_recover_at ⇒ PG 侧的四元组不变式对它不适用。",
}

// cmbSetAssignTrueRe ＝ SET 子句里「把 available 置 TRUE」的两种写法。
//
// ★ 必须限定在 SET 子句里：`bg/model_probe.go` 的 broken_confirmed 语句
//
//	的 WHERE 子句里有 `AND cmb.available = TRUE`（那是**过滤条件**，不是赋值）
//	⇒ 不限定就会把它误判成「置可用」。
//
// ★ 第三种写法：`available = EXCLUDED.available`（手工加入路径，插入值决定）
//
//	⇒ 它也可能把绑定置为可用 ⇒ 同样要求处理 recover_at。
var cmbSetAssignTrueRe = regexp.MustCompile(`(?i)(?:^|[,\s])available\s*=\s*(TRUE|CASE|EXCLUDED\.available)`)

// cmbWriteRe ＝ 直写 cmb 的两种语句形态。
var cmbWriteRe = regexp.MustCompile(`(?i)(UPDATE\s+credential_model_bindings|INSERT\s+INTO\s+credential_model_bindings)`)

// ★ 关键词定位一律用**词边界正则**，不用 " SET " 这种带空格的字面量：
//
//	SQL 在 Go raw string 里通常是 **tab 缩进** ⇒ " SET " 匹配不到
//	（实测：credential_monitor.go 那条语句因此被漏扫 ⇒ 门失效）。
var (
	cmbDoUpdateSetRe = regexp.MustCompile(`(?i)\bDO\s+UPDATE\s+SET\b`)
	cmbSetKeywordRe  = regexp.MustCompile(`(?i)\bSET\b`)
	cmbWhereRe       = regexp.MustCompile(`(?i)\bWHERE\b`)
	cmbFromRe        = regexp.MustCompile(`(?i)\bFROM\b`)
)

// cmbSetClauses ＝ 返回一条 SQL 字面量里**属于 cmb 的每一次写入**的 SET 子句。
//
// ★ 两种形态的 SET 起点不同：
//
//	① `UPDATE … SET …`                        ⇒ SET 之后
//	② `INSERT … ON CONFLICT … DO UPDATE SET …` ⇒ **DO UPDATE SET** 之后
//	★ 同一个字面量里可能有多次 cmb 写入（modelcatalog/upsert.go 就是：
//	  一条字面量里先 upsert provider_models、再 upsert cmb）
//	  ⇒ 必须**按 cmb 写入的位置**切，不能取字面量里的第一个 SET。
func cmbSetClauses(sql string) []string {
	var out []string
	matches := cmbWriteRe.FindAllStringIndex(sql, -1)
	for k, m := range matches {
		end := len(sql)
		if k+1 < len(matches) {
			end = matches[k+1][0]
		}
		seg := sql[m[0]:end]
		if i := cmbDoUpdateSetRe.FindStringIndex(seg); i != nil {
			s := seg[i[1]:]
			if w := cmbWhereRe.FindStringIndex(s); w != nil {
				s = s[:w[0]]
			}
			out = append(out, s)
			continue
		}
		if i := cmbSetKeywordRe.FindStringIndex(seg); i != nil {
			s := seg[i[1]:]
			if w := cmbWhereRe.FindStringIndex(s); w != nil {
				s = s[:w[0]]
			}
			if f := cmbFromRe.FindStringIndex(s); f != nil {
				s = s[:f[0]]
			}
			out = append(out, s)
		}
	}
	return out
}

// cmbSetAssignsAvailableTrue ＝ 判断一条 SET 子句是否把 available 置 TRUE。
func cmbSetAssignsAvailableTrue(setClause string) bool {
	return cmbSetAssignTrueRe.MatchString(setClause)
}

// scanGoSQLStrings 返回仓库里所有 Go 文件里、写到 cmb 的 SQL 字符串字面量。
//
// ★ 只取字符串字面量；结构体标签也是反引号，但内容不含 cmb 写入 ⇒ 天然过滤。
func scanGoSQLStrings(t *testing.T, roots []string) []cmbSQLStmt {
	t.Helper()
	var out []cmbSQLStmt
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				base := info.Name()
				if base == ".git" || base == "node_modules" || base == "vendor" ||
					base == "dist" || base == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return nil // 语法错误的文件交给编译期门，不在这里噪声化
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, derr := strconv.Unquote(lit.Value)
				if derr != nil {
					return true
				}
				if !cmbWriteRe.MatchString(val) {
					return true
				}
				out = append(out, cmbSQLStmt{
					file: path,
					line: fset.Position(lit.Pos()).Line,
					sql:  val,
				})
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("扫 %s：%v", root, err)
		}
	}
	return out
}

// cmbSQLStmt ＝ 一条被扫到的 SQL 字面量。
type cmbSQLStmt struct {
	file string
	line int
	sql  string
}

// TestCredentialBindingHealthTupleWritersAreConsistent —— 全仓写入侧不变式门。
func TestCredentialBindingHealthTupleWritersAreConsistent(t *testing.T) {
	// ★ 扫**全仓**（不是手挑的包清单）⇒ 新增的包自动被覆盖；
	//   手挑清单会漏掉后来新建的包 ⇒ 漏扫 = 门失效。
	roots := []string{"../.."}

	var violations []string
	for _, st := range scanGoSQLStrings(t, roots) {
		for _, setClause := range cmbSetClauses(st.sql) {
			if !cmbSetAssignsAvailableTrue(setClause) {
				continue // 非恢复方向
			}
			if strings.Contains(setClause, "unavailable_recover_at") {
				continue // 已守四元组
			}
			if exempt, ok := cmbHealthTuplePathExemptions[cmbExemptPathKey(st.file)]; ok {
				if strings.TrimSpace(exempt) == "" {
					violations = append(violations, st.file+"：在路径例外表里但理由是空的")
				}
				continue
			}
			key := st.file + ":" + strconv.Itoa(st.line)
			if why, ok := cmbHealthTupleExemptions[key]; ok {
				if strings.TrimSpace(why) == "" {
					violations = append(violations, key+"：在例外表里但理由是空的")
				}
				continue
			}
			violations = append(violations, key)
		}
	}

	for _, v := range violations {
		t.Errorf("%s 是一条「把 available 置为 TRUE 却没有处理 unavailable_recover_at」的\n"+
			"  credential_model_bindings 写入语句 ⇒ 它会把恢复标记留在行上 ⇒\n"+
			"  该行落进矛盾态 available=TRUE ∧ recover_at≠NULL ⇒ **F3 残留地平线重新开始反弹**\n"+
			"  （2026-11-17 实测：37~231 行锯齿，19 轮从未归零）。\n"+
			"  ★ 迁移 972 只修了「经 model_offers 视图」那条路（触发器的 SET 清单）；\n"+
			"    **直写 cmb 的路径各有各的 SET 清单** ⇒ 触发器管不到 ⇒ 必须自己写这一列。\n"+
			"  修法：在同一条语句的 SET 清单里加上\n"+
			"    unavailable_recover_at = NULL,\n"+
			"  （置可用 ⇒ 四列全清，这是 cmb 侧直写路径的既有约定，见\n"+
			"   bg/credential_probe_v2.go、bg/node_probe_write_through.go 等）。\n"+
			"  若本条确实该例外，请登记进 cmbHealthTupleExemptions 并写明理由。", v)
	}
}

// cmbExemptPathKey ＝ 取文件路径里可用于路径豁免匹配的那一段
// （仓库根的相对路径，形如 "storage/sqlite/xxx.go"）。
func cmbExemptPathKey(path string) string {
	norm := filepath.ToSlash(path)
	if i := strings.Index(norm, "/storage/sqlite/"); i >= 0 {
		return "storage/sqlite"
	}
	return ""
}
