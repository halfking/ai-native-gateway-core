// Package jsonbguard 钉住「json.Marshal 的 []byte 产物不得裸传 pgx 写参数」。
//
// R46 轮（2026-10-05）B 路审计的全仓普查发现：8ab8a4fd6（R24）修掉的
// modality 证据列 []byte→hex 静默写入全灭，不是孤例而是**家族**——同款
// 裸传在 bg / domains / admin / routingopt / center / vibecoding 里还有
// 十余处。每处的死法都相同：
//
//	网关连接池全局 SimpleProtocol（db/db.go:72）⇒ pgx 把 []byte 参数
//	内联为 `'\x...'::bytea` 字面量 ⇒ 目标 jsonb 列解析必炸（或被
//	静默拒绝）⇒ 写入 0 行、只剩一条 Warn 日志。
//
// 本门把这个家族从「逐点修补」升级为「结构断言」：
//
//   - 收集每个函数内 `x, _ := json.Marshal(...)` 形态的变量名；
//   - 断言这些变量**不会**以裸 Ident 形态出现在 Exec/Query/QueryRow 的
//     参数列表里（`string(x)` 包裹后是 CallExpr，天然不命中）；
//   - 读方向（Scan 目标）是合法的 []byte 用法，不在本门管辖。
//
// 豁免必须逐条写理由（exempt 表），并同样以 文件:变量 定位 —— 与
// healthstateguard 的登记表纪律一致：没做必须能区分「有意」与「遗漏」。
//
// 已知边界（如实登记，不假装闭合）：
//   - 只追踪「变量名直传」；`append(nil, x...)`、二次赋值等 launder 路径
//     逃逸。 launder 在本仓无先例，若出现请先修点再考虑收紧本门。
//   - 只看单函数作用域；跨函数 launder（marshal 后传走再传回）同理逃逸。
package jsonbguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// scanRoots 是生产代码根（vendor / 测试 / 独立模块与前端不在其列）。
var scanRoots = []string{
	"../../admin", "../../bg", "../../domains", "../../routingopt",
	"../../center", "../../vibecoding", "../../apihub", "../../db",
	"../../cmd/gateway", "../../cmd/tools",
}

// exempt 登记已知豁免：相对路径 → 变量名 → 理由。空表 = 无豁免。
// 新增豁免必须写清楚为什么该点的裸传是安全的（或为什么暂不修）。
var exempt = map[string]map[string]string{
	// 示例形态（当前为空；出现豁免时按此登记）：
	// "../../cmd/tools/validate_sessions_v2/repair.go": {
	//     "metadataJSON": "离线工具直连库，非网关 SimpleProtocol 池",
	// },
}

// offenders 是一处违规：文件、行号、变量名。
type offender struct {
	file string
	line int
	var_ string
}

func (o offender) String() string {
	return filepath.ToSlash(o.file) + ":" + itoa(o.line) + " " + o.var_
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// isDBWriteCall 报告 call 是否 pgx 池/事务的写参数通道。
// Scan（读方向）刻意不在列：jsonb → []byte 的读是合法用法。
func isDBWriteCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Exec", "Query", "QueryRow":
		return true
	}
	return false
}

// isJSONMarshalCall 报告 expr 是否 `json.Marshal(...)`（含别名包路径尾巴）。
func isJSONMarshalCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Marshal" {
		return false
	}
	if id, ok := sel.X.(*ast.Ident); ok {
		return id.Name == "json"
	}
	return false
}

// scanFile 对单个 Go 文件做 per-function 追踪，返回违规清单。
func scanFile(fset *token.FileSet, path string) []offender {
	src, err := os.ReadFile(path)
	if err != nil {
		return []offender{{file: path, line: 0, var_: "READ_ERROR"}}
	}
	// 独立模块 / 已知非 SimpleProtocol 的文件级豁免走同一张表：
	// 变量名 "*" 代表整文件豁免。
	fileExemptAll := false
	if vars, ok := exempt[path]; ok {
		if _, all := vars["*"]; all {
			fileExemptAll = true
		}
	}
	if fileExemptAll {
		return nil
	}
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return []offender{{file: path, line: 0, var_: "PARSE_ERROR"}}
	}

	var found []offender
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		marshalVars := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) == 0 || len(assign.Rhs) != 1 {
				return true
			}
			if !isJSONMarshalCall(assign.Rhs[0]) {
				return true
			}
			for _, lhs := range assign.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					marshalVars[id.Name] = true
				}
			}
			return true
		})
		if len(marshalVars) == 0 {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isDBWriteCall(call) || len(call.Args) == 0 {
				return true
			}
			for _, arg := range call.Args {
				id, ok := arg.(*ast.Ident)
				if !ok || !marshalVars[id.Name] {
					continue
				}
				if vars, ok := exempt[path]; ok {
					if _, ok := vars[id.Name]; ok {
						continue
					}
				}
				found = append(found, offender{
					file: path,
					line: fset.Position(id.Pos()).Line,
					var_: id.Name,
				})
			}
			return true
		})
	}
	return found
}

// TestNoBareMarshalBytesIntoDBWriteParams 是主门。
func TestNoBareMarshalBytesIntoDBWriteParams(t *testing.T) {
	fset := token.NewFileSet()
	var all []offender
	for _, root := range scanRoots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			all = append(all, scanFile(fset, path)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].file != all[j].file {
			return all[i].file < all[j].file
		}
		return all[i].line < all[j].line
	})

	if len(all) > 0 {
		var b strings.Builder
		for _, o := range all {
			b.WriteString("\n  " + o.String())
		}
		t.Fatalf("json.Marshal 的 []byte 产物裸传 DB 写参数（SimpleProtocol 下会被内联成 "+
			"bytea hex 字面量，jsonb 列解析必炸）：%s\n修法：传参处包 string(x)，SQL 位点加 "+
			"::jsonb cast（先例：bg/capability_backfill.go capabilityEvidenceParam、"+
			"domains/streaming/integrity_harvester.go）。若该点确认安全，请在 exempt 表登记并写理由。",
			b.String())
	}
}

// TestExemptEntriesStillExist 防腐：豁免表指向的文件必须存在，且豁免的
// 变量名在文件里必须真的出现 —— 指向已删除代码的豁免是死登记，会让
// 「表是全的」变成假象。
func TestExemptEntriesStillExist(t *testing.T) {
	for path, vars := range exempt {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("豁免指向不存在的文件 %s（登记过期）", path)
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("读豁免文件 %s: %v", path, err)
			continue
		}
		for name, reason := range vars {
			if name == "*" {
				continue
			}
			if !strings.Contains(string(src), name) {
				t.Errorf("豁免 %s:%s 指向的变量在文件里不存在（登记过期）；理由=%q", path, name, reason)
			}
			if strings.TrimSpace(reason) == "" {
				t.Errorf("豁免 %s:%s 没写理由 —— 「没做」必须能区分「有意」与「遗漏」", path, name)
			}
		}
	}
}
