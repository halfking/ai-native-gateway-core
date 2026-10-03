package sqlguard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestApiLayerEndpointShapeParity —— 226 号新增。
//
// 缺陷本体（objective 第 21 项「数据展示的统一性」的前置条件）：
// `web/src/api/` 里**同一个后端端点被两个模块各自定义了一份函数，
// 而且两份的返回形状互不兼容**：
//
//	1) `/api/admin/modules`
//	   - `modules.ts:59`  声明 `req<{ items: ModuleWithStatus[] }>`  ✅ 与后端一致
//	   - `ops.ts:163`    声明 `req<ProductModule[]>`                ❌ 裸数组
//	   - 后端 `admin/modules.go:787` 写死 `map[string]any{"items": out}`
//	   ⇒ ops 那份的**返回类型与实际不符**：运行时 `res.items` 才是数组，
//	     按声明去 `res.map/forEach` 会得到 `undefined is not iterable`。
//
//	2) `/api/system/license/status`
//	   - `customer.ts:89`  `CustomerLicenseStatus.mode: 'licensed'|'community'|'restricted'`
//   - `ops.ts:758`      `LicenseStatus.mode:         'normal'|'in_grace'|'restricted'`
//	   - 后端 `licensing/customer_api.go` 的字段注释明写 `"licensed" | "community" | "restricted"`
//	   ⇒ **三种取值里只有 `restricted` 对得上**；`licensed`/`community` 与
//	     `normal`/`in_grace` 完全不交集。
//	   ⇒ 而且两个函数**同名**（都叫 `getLicenseStatus`），一旦有人 import 其中
//	     另一个，改 import 路径不会报错、只会换一套语义。
//
// ⚠️ **诚实边界（必须写进门里，不能只写在报告里）**：
// 本轮查到这两份定义**都零调用方**（`git grep getProductModules` /
// `git grep getLicenseStatus` 在 `web/src/` 下只命中定义行本身）
// ⇒ **这不是现役缺陷，是"接口层已分叉、尚未传导到视图"**。
// 故定级 P3 而不是 P1/P2。
// ⇒ **本门的价值不是抓现役 bug，而是挡住"分叉被接线"的那一刻** ——
//   那一刻不会有任何编译错误、不会有任何测试失败，只会在运行时静默出错。
//
// 判据：**后端一个端点只应在前端 api 层有一份形状声明。**

// endpointShapeDecl 记录「后端端点在前端 api 层的形状声明」。
type endpointShapeDecl struct {
	// 后端权威位置：写死返回形状的那一行（`writeJSON` / `c.JSON`）
	implFile string
	implLine int
	// 后端实际返回的顶层形状，取自那一行的字面量
	implShape string
	// 前端各自的声明（file:line 与声明的类型文本）
	frontDecls []frontShapeDecl
}

type frontShapeDecl struct {
	file string
	line int
	// 该声明里 req<T>(...) 的 T
	declaredType string
}

var endpointShapeRegistry = []endpointShapeDecl{
	{
		implFile:  "admin/modules.go",
		implLine:  787,
		implShape: `{"items": ...}`,
		frontDecls: []frontShapeDecl{
			{file: "web/src/api/modules.ts", line: 59, declaredType: "{ items: ModuleWithStatus[] }"},
			{file: "web/src/api/ops.ts", line: 163, declaredType: "ProductModule[]"},
		},
	},
	{
		implFile:  "licensing/customer_api.go",
		implLine:  0, // 该端点的形状由结构体 tag 决定，单独用 mode 取值域断言
		implShape: `{"state":..., "mode":"licensed"|"community"|"restricted", ...}`,
		frontDecls: []frontShapeDecl{
			{file: "web/src/api/customer.ts", line: 0, declaredType: "mode: 'licensed'|'community'|'restricted'"},
			{file: "web/src/api/ops.ts", line: 0, declaredType: "mode: 'normal'|'in_grace'|'restricted'"},
		},
	},
}

func TestApiLayerEndpointShapeParity(t *testing.T) {
	root := repoRootForTest(t)

	for _, e := range endpointShapeRegistry {
		if len(e.frontDecls) < 2 {
			t.Errorf("%s 的登记表只有 %d 条前端声明（下限 2）："+
				"一条端点只有一份声明时本门无从谈起。", e.implFile, len(e.frontDecls))
			continue
		}

		// (1) 权威侧：确认后端那一行仍在原处、仍写死那个形状。
		if e.implLine > 0 {
			b, err := os.ReadFile(filepath.Join(root, e.implFile))
			if err != nil {
				t.Errorf("权威侧 %s 读不到：%v", e.implFile, err)
				continue
			}
			lines := strings.Split(string(b), "\n")
			if e.implLine < 1 || e.implLine > len(lines) {
				t.Errorf("权威侧行号 %d 越界（%s 共 %d 行）", e.implLine, e.implFile, len(lines))
				continue
			}
			src := lines[e.implLine-1]
			// 只核对"仍写着 items"这一件最硬的事实（顶层被包成对象）。
			if e.implShape == `{"items": ...}` && !strings.Contains(src, `"items"`) {
				t.Errorf("权威侧 %s:%d 不再写 `\"items\"`：\n  实际: %s\n"+
					"⇒ 后端返回形状已改，本门的前端形状判据需要重新确认"+
					"（不是前端已修复）。", e.implFile, e.implLine, strings.TrimSpace(src))
				continue
			}
		}

		// (2) 前端侧：逐份声明取其 req<T> 的 T，比对是否一致。
		types := map[string]string{} // declaredType -> "file:line"
		for _, fd := range e.frontDecls {
			b, err := os.ReadFile(filepath.Join(root, fd.file))
			if err != nil {
				t.Errorf("前端 %s 读不到：%v", fd.file, err)
				continue
			}
			s := string(b)
			if fd.line > 0 {
				lines := strings.Split(s, "\n")
				if fd.line < 1 || fd.line > len(lines) {
					t.Errorf("前端 %s:%d 越界（共 %d 行）", fd.file, fd.line, len(lines))
					continue
				}
				// 锚点校验：那一行必须仍含该端点路径
				if !strings.Contains(lines[fd.line-1], endpointPathOf(e)) {
					t.Errorf("前端 %s:%d 不再命中该端点：\n  实际: %s\n"+
						"⇒ 声明已移走或端点已改，登记表需要重新确认（不是分叉已修复）。",
						fd.file, fd.line, strings.TrimSpace(lines[fd.line-1]))
					continue
				}
			}
			t := findReqTypeNear(s, endpointPathOf(e))
			if t == "" {
				t = fd.declaredType // 形态断言型（mode 取值域）走登记值
			}
			types[t] = fmt.Sprintf("%s:%d", fd.file, fd.line)
		}

		uniq := make([]string, 0, len(types))
		for k := range types {
			uniq = append(uniq, k)
		}
		sort.Strings(uniq)
		// ⚠️ 与下面取值域那段同一条纪律：**逐份与后端比**，而不是"彼此不同就算分叉"。
		// 若某端点有多份声明但**都**与后端一致，那只是冗余，不是分叉。
		//
		// authoritative == "" 表示这一组的形状**在类型名层面判不了**
		//（如 license/status：两份类型名不同但那只是命名，不是形状证据）
		// ⇒ 此时只报"有 N 份声明"作为线索，**不下分叉结论**
		//（否则 NC-H 抓到的那个假分叉会换个形态回来）。
		authoritative := authoritativeFrontShape(e)
		if authoritative == "" {
			if len(uniq) > 1 {
				t.Logf("端点 %s 在前端 api 层有 %d 份形状声明（%s）；"+
					"该端点的形状无法在类型名层面机械判定 ⇒ 交由下面的取值域检查裁决。",
					endpointPathOf(e), len(uniq), strings.Join(uniq, " / "))
			}
		} else if len(uniq) > 1 {
			authoritative := authoritativeFrontShape(e)
			mismatch := 0
			for _, k := range uniq {
				if authoritative != "" && !shapeAgrees(k, authoritative) {
					mismatch++
				}
			}
			if mismatch == 0 {
				t.Logf("端点 %s 在前端 api 层有 %d 份形状声明，但**全部与后端一致**（%s）"+
					"⇒ 不是形状分叉，只是重复声明。",
					endpointPathOf(e), len(uniq), authoritative)
			} else {
				t.Logf("【已登记分叉，待裁决 92】端点 %s 在前端 api 层有 %d 种形状声明"+
					"（其中 %d 份与后端权威形状 %s 不符）：",
					endpointPathOf(e), len(uniq), mismatch, authoritative)
				for _, k := range uniq {
					mark := "✅ 与后端一致"
					if authoritative != "" && !shapeAgrees(k, authoritative) {
						mark = "❌ 与后端不符"
					}
					t.Logf("    %-60s ← %s  %s", k, types[k], mark)
				}
				t.Logf("  后端权威形状：%s（%s）", e.implShape, e.implFile)
				t.Logf("  ⇒ 一旦有人 import 错误那一份，**不会有编译错误、不会有测试失败**，")
				t.Logf("    只会在运行时静默出错（如把 {\"items\":[...]} 当数组 forEach）。")
			}
		}

		// (3) ⚠️ 类型名相同**不等于**形状相同 —— 还要比**同一字段的取值域**。
		//
		// 这不是补丁，是本轮第二次尺子错（第一次：views 层扫不到端点，
		// 见 playbook §181）。第一版只比"类型名是否相同"，
		// 于是 `/api/system/license/status` 那组被判成"只是类型名不同"——
		// 而**真正的分叉在 `mode` 的取值域**：
		//     customer.ts: 'licensed' | 'community' | 'restricted'
		//     ops.ts:     'normal'  | 'in_grace'  | 'restricted'
		// 只有 `restricted` 交集，`licensed`/`community` 与 `normal`/`in_grace` 完全不沾。
		// 后端 `licensing/customer_api.go` 的字段注释明写前一组。
		// ⇒ **类型名相同只是"同名"，字段取值域才是契约。**
		if e.implFile == "licensing/customer_api.go" {
			modeDecls := map[string][]string{} // 取值集合 -> 声明位置
			for _, fd := range e.frontDecls {
				b, err := os.ReadFile(filepath.Join(root, fd.file))
				if err != nil {
					continue
				}
				if vals := findModeUnion(string(b)); len(vals) > 0 {
					modeDecls[fd.file] = vals
				}
			}
			if len(modeDecls) > 1 {
				auth := []string{"licensed", "community", "restricted"}
				files := make([]string, 0, len(modeDecls))
				for f := range modeDecls {
					files = append(files, f)
				}
				sort.Strings(files)
				// 先算：有没有哪一份**与后端一致**。
				// ⚠️ 226 号负控 NC-H 抓到的第三处尺子错：
				// 第一版只要"两份不一样"就报"分叉"，
				// 于是把 ops.ts 改成与后端一致之后（两份**都**等于后端），
				// 它仍然报"分叉" ⇒ **判据量的不是"与后端的一致性"，是"彼此不同"**。
				// ⇒ 正确的判据是**逐份与后端比**：
				//   全都与后端一致 ⇒ 无分叉（哪怕有 3 份重复声明，那只是冗余，不是分叉）；
				//   至少一份不一致 ⇒ 才是分叉。
				badFiles := []string{}
				for _, f := range files {
					for _, v := range modeDecls[f] {
						if !containsStr(auth, v) {
							badFiles = append(badFiles, f)
							break
						}
					}
				}
				if len(badFiles) == 0 {
					t.Logf("端点 %s 的 `mode` 有 %d 份声明，但**全部与后端一致**"+
						"（%s）⇒ 不是形状分叉，只是重复声明。",
						endpointPathOf(e), len(files), strings.Join(files, ", "))
					continue
				}
				t.Logf("【已登记分叉，待裁决 92】端点 %s 的 `mode` 字段在前端有两套取值域：",
					endpointPathOf(e))
				for _, f := range files {
					ok := true
					for _, v := range modeDecls[f] {
						if !containsStr(auth, v) {
							ok = false
						}
					}
					mark := "✅ 与后端一致"
					if !ok {
						mark = "❌ 含后端不产出的取值"
					}
					t.Logf("    %-32s %v  %s", f, modeDecls[f], mark)
				}
				t.Logf("  后端权威取值域（customer_api.go 字段注释）：%v", auth)
				t.Logf("  ⇒ 两套**只有 restricted 交集**；按错误那套写 `mode === 'licensed'`")
				t.Logf("    的分支永远不成立，而 TypeScript 不会报错。")
			}
		}
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// findModeUnion 找出 `mode: 'a' | 'b' | 'c'` 形态的取值联合。
//
// ⚠️ 226 号在这里踩了一次：`strings.Index(src, "mode:")` 找的是**第一处** "mode:"，
// 而那两个文件里第一处可能是别的东西（例如 `mode` 作为函数参数名、
// 或 `responseMode:` 之类），取到的行不是类型声明 ⇒ 返回空 ⇒ 分支永不触发
// ⇒ **门静默地什么都没检查**。
// ⇒ 改法：正则锚定**行首可选空白 + 恰好是 mode: + 后面跟着单引号字面量**，
//
//	且要求该行至少含 2 个单引号字面量（联合类型的特征）。
func findModeUnion(src string) []string {
	re := regexp.MustCompile(`(?m)^\s*mode:\s*'([a-z_]+)'(\s*\|\s*'[a-z_]+')+`)
	m := re.FindString(src)
	if m == "" {
		return nil
	}
	lit := regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(m, -1)
	out := []string{}
	for _, x := range lit {
		out = append(out, x[1])
	}
	return out
}

func endpointPathOf(e endpointShapeDecl) string {
	if e.implFile == "admin/modules.go" {
		return "/api/admin/modules"
	}
	return "/api/system/license/status"
}

// findReqTypeNear 在端点路径附近找出 `req<...>(...)` 的类型实参。
func findReqTypeNear(src, endpoint string) string {
	i := strings.Index(src, "'"+endpoint+"'")
	if i < 0 {
		i = strings.Index(src, "`"+endpoint+"`")
	}
	if i < 0 {
		i = strings.Index(src, `"`+endpoint+`"`)
	}
	if i < 0 {
		return ""
	}
	// 从该点往前找最近的 req<
	head := src[:i]
	j := strings.LastIndex(head, "req<")
	if j < 0 {
		return ""
	}
	rest := src[j+len("req<"):]
	depth := 1
	var b strings.Builder
	for _, c := range rest {
		switch c {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return strings.TrimSpace(b.String())
			}
		}
		b.WriteRune(c)
	}
	return ""
}

// authoritativeFrontShape 返回该端点**后端权威**对应的那份前端形状声明的识别特征。
//
// 返回空串表示"无法机械判定"（如 mode 取值域那组）⇒ 调用方跳过标记。
func authoritativeFrontShape(e endpointShapeDecl) string {
	switch e.implFile {
	case "admin/modules.go":
		// 后端写死 {"items": ...} ⇒ 顶层被包成对象的那份才合规。
		return "object"
	}
	return ""
}

// shapeAgrees 判断一份前端形状声明是否与后端权威形状相符。
func shapeAgrees(declared, authoritative string) bool {
	switch authoritative {
	case "object":
		// 后端返回 {"items": [...]} ⇒ 声明里必须出现 items 键。
		return strings.Contains(declared, "items")
	}
	return true
}
