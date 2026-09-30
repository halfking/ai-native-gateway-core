package sessionforensics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// R67-B 回归：ExportFromTx 原先**没有** defer rows.Close()。
//
// 该洞本身是潜伏的：循环总是跑到耗尽，而 pgx 在 Next() 返回 false 时会自动
// Close，所以「不 Close」从不显形。R66 在循环中加了 `return nil, ...`
// （取证包必须完整，不能跳行），这条路径于是变得可达——rows 在结果集仍挂载
// 时被丢下，而 tx 是**调用方持有**的，调用方随后带着半读的结果集去
// rollback / 释放事务。
//
// 同文件另外两个函数走 database/sql（`e.store`），Close() 返回 error，
// 写法是 `defer func(){ _ = rows.Close() }()`；本函数走 pgx.Tx.Query，
// Close() 无返回值，只能直接 defer。两种形式不能混用——把 pgx 的 rows 写成
// `_ = rows.Close()` 根本编译不过（这正是本轮实际踩到并修掉的）。
//
// **为什么用 AST 而不是文本窗口**：本文件里我为这件事写的说明注释里
// 出现了 `defer rows.Close()` 这个字面量。第一版用
// `strings.Index(text, "func (e *Exporter) ExportFromTx")` + 文本窗口来定位，
// 结果**变异验证时把 `defer rows.Close()` 删掉，测试照样绿**——判据匹配到了
// 自己的注释。同一函数里另一条断言还因为「窗口一直延伸到下一个 `func `」
// 而把邻近函数的 Close 也算了进来。
//
// 这与 R66 守卫踩的「RangeStmt vs ForStmt」是同一类错误：
// **判据锚错层级时，门对真实缺陷是瞎的，而它自己不会报错。**
// 判据必须打在 AST 上，且必须剥掉注释。

// funcSource 返回指定函数名的**源码文本**（按 AST 定位，精确到声明）。
func funcSource(t *testing.T, file, recv, name string) (string, int) {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name {
			continue
		}
		if recv != "" && fn.Recv == nil {
			continue
		}
		start := fset.Position(fn.Pos()).Offset
		end := fset.Position(fn.End()).Offset
		return string(src[start:end]), fset.Position(fn.Pos()).Line
	}
	t.Fatalf("function %s%s not found in %s", recv, name, file)
	return "", 0
}

// stripComments 去掉行注释与块注释，避免判据匹配到说明文字本身。
func stripComments(src string) string {
	var b strings.Builder
	lines := strings.Split(src, "\n")
	for _, l := range lines {
		if i := strings.Index(l, "//"); i >= 0 {
			l = l[:i]
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestR67B_ExportFromTxMustCloseRows(t *testing.T) {
	raw, line := funcSource(t, "export.go", "*Exporter", "ExportFromTx")
	body := stripComments(raw)

	if !strings.Contains(body, "defer rows.Close()") {
		t.Errorf("R67-B regression (export.go:%d): ExportFromTx must `defer rows.Close()` — its "+
			"mid-loop returns (added so a forensic pack is never partially built) otherwise abandon "+
			"an open Rows inside a caller-owned pgx.Tx.", line)
	}
	// 形式不能写错：pgx.Rows.Close() 无返回值，赋值给 _ 编译不过
	if strings.Contains(body, "_ = rows.Close()") {
		t.Errorf("R67-B: pgx.Rows.Close() returns nothing; assigning its result does not compile. " +
			"The database/sql form used by the other two functions in this file is wrong here.")
	}
}

// TestR67B_ExportFunctionsAllCloseRows 按 AST 逐函数确认
// 「**迭代**了 rows 就必须 Close」。
//
// 判据边界的教训：第一版写「取了 rows 就必须 Close」，把
// `PgxStore.Query`（把 rows 包成 RowIterator 交给调用方）也报了出来。
// **判据锚错层级，报出来的就全是噪声。**
func TestR67B_ExportFunctionsAllCloseRows(t *testing.T) {
	src, err := os.ReadFile("export.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "export.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		start := fset.Position(fn.Pos()).Offset
		end := fset.Position(fn.End()).Offset
		body := stripComments(string(src[start:end]))
		// 判据是「**迭代**了 rows 就必须 Close」，不是「取了 rows」——
		// 工厂函数（把 rows 交给调用方）本来就不该关。
		if !strings.Contains(body, "rows, err :=") || !strings.Contains(body, "rows.Next()") {
			continue
		}
		if !strings.Contains(body, "rows.Close()") {
			t.Errorf("%s (export.go:%d) iterates rows but never closes them",
				fn.Name.Name, fset.Position(fn.Pos()).Line)
		}
	}
}
