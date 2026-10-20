package admin

import "strings"

// ★★★★★ 这个文件**故意不带 build tag**。
//
// # 为什么
//
// `stripGoCommentsKeepingStrings` 原来住在
// `admin/request_logs_retirement_tiering_test.go` 里，而那个文件带
// `//go:build !integration` ⇒ **在 `-tags=integration` 的树里它不存在**。
//
// 而 §143.36 那道门（`admin/section_14336_board_single_scan_test.go`）
// 在 **两种** build 里都要用它 ⇒ 于是：
//
//	go vet -tags=integration ./...
//	vet: admin/section_14336_board_single_scan_test.go:69:9: undefined: stripGoCommentsKeepingStrings
//
// ⇒ ★ **整棵 integration 树编译不过** ⇒ 连带把
// `sql/schema` 的 `TestIntegrationTaggedTreeCompiles`（这道门**本来就是**为了
// 抓这种问题而存在的）拉红 ⇒ ⇒ 而且它**挡住所有 `-tags=integration` 的跑法**
// （本仓有好几道真库门带 integration tag）。
//
// # 通用式
//
// ★ **共享 helper 的定义文件不能带任何 build tag** ——
// 只要**任何一个**使用方在另一种 build 里，它就必须两边都在。
// ⇒ 判别方法：`grep -rn "func <helper>"` 看定义文件的头两行有没有 `//go:build`。
//
// ⚠ 反过来也成立：**只被一种 build 用的 helper**，不该放在这个文件里
// （那样它会在另一种 build 里变成死代码）。
//
// # 为什么不用正则
//
// ⚠ 刻意不直接用 `session_view_dependency_risk_test.go` 里的 `stripGoComments`：
// 那是一条裸正则（`//[^\n]*`），**不看字符串** ⇒ 会把 SQL 里的
// `http://` 与原始字符串里的内容一起当注释删掉。本 helper 要的是
// 「剥 Go 注释、但 SQL 字符串原样留下，好让第二遍去剥 SQL 注释」。

// stripGoCommentsKeepingStrings 剥 Go 的 `//` 与 `/* */`，**保留**字符串字面量内容。
func stripGoCommentsKeepingStrings(src string) string {
	var b strings.Builder
	rs := []rune(src)
	n := len(rs)
	for i := 0; i < n; {
		c := rs[i]
		switch {
		case c == '"' || c == '`':
			q := c
			b.WriteRune(c)
			i++
			for i < n {
				if rs[i] == '\\' && q != '`' && i+1 < n {
					b.WriteRune(rs[i])
					b.WriteRune(rs[i+1])
					i += 2
					continue
				}
				b.WriteRune(rs[i])
				if rs[i] == q {
					i++
					break
				}
				i++
			}
		case c == '/' && i+1 < n && rs[i+1] == '/':
			for i < n && rs[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && rs[i+1] == '*':
			j := i + 2
			for j+1 < n && !(rs[j] == '*' && rs[j+1] == '/') {
				j++
			}
			if j+1 < n {
				i = j + 2
			} else {
				i = n
			}
		default:
			b.WriteRune(c)
			i++
		}
	}
	return b.String()
}
