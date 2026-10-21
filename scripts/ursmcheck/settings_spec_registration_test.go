package ursm_test

// settings **spec 注册面**的守卫（2026-10-21，R72f 轮新增；属主决策项 #10 的前半）
//
// # 背景：一条「配置了也不生效」的静默链
//
// settings 的取值链（settings/spec.go:284 `EffectiveValue`）：
//
//	① DB（settings_kv）⇒ 按 **`spec.Scope`** 查 `r.backends[scope]`
//	② env ⇒ **仅当 `scope == ScopePlatform`**
//	③ default
//
// 而 `Registry.RegisterSpec`（spec.go:239）**不归一化 Scope** ⇒ 零值 `""`
// 既不是 `ScopePlatform`（"platform"）也不是 `ScopeTenant`
// ⇒ ⇒ ① 用 `""` 查后端 ⇒ **查不到 ⇒ DB 的值永远读不到**
// ⇒ ⇒ ② 又因为 `"" != ScopePlatform` ⇒ **env 那条路也走不到**
// ⇒ ⇒ ★★ **⇒ 凡是省略 `Scope` 的 spec，都是「彻底不可配」**：
//     写 settings_kv 无效、设 env 也无效，代码只会一直走调用方的 fallback。
//
// ★ 2026-10-21 实测：211/252 的 settings_kv 里**早就写着**
//   `provider_profile.enabled = true`，而它**一直是被忽略的**
//   （代码实际取 fallback=false）⇒ ⇒ ★★ 补上 Scope 之后，那个值会
//   **第一次真正生效** ⇒ ⇒ ⇒ **这是一处「生产行为变化」，上线前必须知情。**
//
// 另一族同源缺陷：**键从未 Register** ⇒ `Global.Spec(key)` 返回 nil
// ⇒ `getPlatformBool`（settings/helpers.go:83）直接回退 fallback
// ⇒ ⇒ **写 settings_kv 完全无效**（连 spec 都没有）。
//
// # 本文件的三道门
//
//	① TestSettingsReadKeysAreRegistered
//	   —— 运行期读的每个 settings 键**必须已 Register 成 spec**（用 `go/ast`
//	      扫读方 + 解析常量引用；★ 不用正则：多行 raw string 与常量引用都会
//	      把正则口径骗过去 —— 本轮已被这两件事各骗过一次）。
//	② TestRegisteredSettingsSpecsDeclareScope
//	   —— 每个已注册的 spec **必须显式声明 Scope**（★ 零值 = 彻底不可配）。
//	③ TestSettingsReadKeyTypeMatchesSpecType
//	   —— 读方期望的类型必须与 spec 的 `Type` 一致（★ 类型不符 ⇒
//	      `json.Unmarshal` 失败 ⇒ 静默回退 fallback ⇒ 「写了也白写」）。
//
// ★ 「已注册集」的取法：用**运行期**的 `settings.PlatformSpecs()`
//   （cmd/gateway/main.go:1545 就是拿它注册进 Global 的）⇒ ⇒ 比解析源码
//   更权威，且**新增 spec 忘了加进 PlatformSpecs() 也会被抓到**。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// platformReaders 是「运行期读 settings 的取值器」清单。
//
// ★ 名字到「期望类型」的映射：★ `GetPlatformDuration` 走的是
//
//	`getPlatformInt`（helpers.go:60）⇒ **它读的是整数秒**，不是 Go duration
//	⇒ ⇒ 所以它对应的 spec `Type` 必须是 **TypeInt**。
var platformReaders = map[string]string{
	"GetPlatformBool":     "bool",
	"GetPlatformFloat":    "float64",
	"GetPlatformDuration": "int",
	"GetPlatformInt":      "int",
	"GetPlatformInt64":    "int64",
	"GetPlatformString":   "string",
}

// settingsReaderCalls 是仓内实际出现的读法（含 settings 包内 helper 的
// 非导出别名 `settingsGetPlatformInt`）。
var settingsReaderCalls = map[string]bool{
	"GetPlatformBool":        true,
	"GetPlatformFloat":       true,
	"GetPlatformDuration":    true,
	"GetPlatformInt":         true,
	"GetPlatformInt64":       true,
	"GetPlatformString":      true,
	"settingsGetPlatformInt": true,
}

// constLiterals 全仓的「常量名 → 字符串字面量」表。
//
// ★ 为什么要解析常量：仓内大量 spec 键写成常量（例：
//
//	`Key: KeySanitizeRulesRefreshIntervalSeconds`）⇒ ⇒ 只认字面量的扫描器
//	会把它们**误判成未注册** ⇒ ⇒ **本轮已被这个坑骗过一次**
//	（v1 扫描器报了 19 个孤儿键，修正后只剩 15 个 —— 4 个是假阳性）。
var constLiterals = map[string]string{}

// constCollisions 名字相同但字面量不同的常量（跨包同名）⇒ 解析时不猜。
var constCollisions = map[string]bool{}

// readSites 读方：键 → 期望类型
var readSites = map[string]string{}

// readSitesAt 读方：键 → 读它的位置（给人看的）
var readSitesAt = map[string]string{}

// registeredKeys 运行期已注册的 spec 键。
func registeredKeys() map[string]*settings.Spec {
	out := map[string]*settings.Spec{}
	for _, sp := range settings.PlatformSpecs() {
		if sp != nil {
			out[sp.Key] = sp
		}
	}
	return out
}

func loadRepo(t *testing.T) {
	t.Helper()
	root := "../.."
	fset := token.NewFileSet()

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if strings.HasPrefix(top, ".") || top == "node_modules" || top == "vendor" || top == "dist" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		collectConsts(f)
		collectReaders(f, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("扫仓失败：%v", err)
	}
	if len(constLiterals) == 0 || len(readSites) == 0 {
		t.Fatalf("★ 扫描口径失效：解析到 %d 个常量、%d 个读方 ⇒ "+
			"⇒ 本门会「因为扫不到东西而恒绿」⇒ 请更新扫描口径。",
			len(constLiterals), len(readSites))
	}
}

// collectConsts 收集本文件里的字符串常量。
func collectConsts(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Values) != 1 || len(vs.Names) != 1 {
			return true
		}
		lit, ok := vs.Values[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		name := vs.Names[0].Name
		if old, exists := constLiterals[name]; exists && old != s {
			constCollisions[name] = true
			return true
		}
		constLiterals[name] = s
		return true
	})
}

// collectReaders 收集 settings 读方调用。
func collectReaders(f *ast.File, rel string) {
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var fnName string
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			fnName = fn.Sel.Name
		case *ast.Ident:
			fnName = fn.Name
		default:
			return true
		}
		if !settingsReaderCalls[fnName] {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		key, ok := resolveKey(call.Args[0])
		if !ok {
			return true
		}
		// ★ 键必须是「点分层级」的完整键（前缀拼接的动态键不在此列）。
		if !strings.Contains(key, ".") || strings.HasSuffix(key, ".") {
			return true
		}
		if _, seen := readSites[key]; !seen {
			readSites[key] = platformReaders[strings.TrimPrefix(fnName, "settings")]
			readSitesAt[key] = rel
		}
		return true
	})
}

// resolveKey 把实参解析成 settings 键（字面量或常量）。
func resolveKey(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.Ident:
		if constCollisions[e.Name] {
			return "", false
		}
		s, ok := constLiterals[e.Name]
		return s, ok
	}
	return "", false
}

// TestSettingsReadKeysAreRegistered 运行期读的每个键都必须已注册成 spec。
//
// ★ 判据方向：**红 = 读得到、但没注册** ⇒ 那个键「写 settings_kv 完全无效」。
func TestSettingsReadKeysAreRegistered(t *testing.T) {
	loadRepo(t)
	reg := registeredKeys()

	var orphans []string
	for key := range readSites {
		if _, ok := reg[key]; !ok {
			orphans = append(orphans, key)
		}
	}
	sort.Strings(orphans)

	for _, key := range orphans {
		t.Logf("  ★ 孤儿键 %-58s 读于 %s", key, readSitesAt[key])
	}
	if len(orphans) > 0 {
		t.Fatalf("★ 有 %d 个 settings 键「运行期在读、但从未 Register 成 spec」：\n    %s\n"+
			"   ⇒ ⇒ `settings.Global.Spec(key)` 返回 nil ⇒ `GetPlatformX` 直接回退 fallback\n"+
			"   ⇒ ⇒ **往 settings_kv 里写这些键完全无效**（静默无效，不报错）\n"+
			"   ⇒ ⇒ 修法：在 settings/spec_*.go 里补一个 Spec（★ Type 要与读方一致、\n"+
			"   ⇒ ⇒ ★ Scope 必须是 ScopePlatform、★ Default 建议与读方 fallback 对齐）\n"+
			"   ⇒ ⇒ 见审计档 docs/audit/2026-10-21-R72f-…md §3.11 与 LESSONS §167",
			len(orphans), strings.Join(orphans, "\n    "))
	}
}

// TestRegisteredSettingsSpecsDeclareScope 每个已注册的 spec 必须显式声明 Scope。
//
// ★ 为什么这道门独立存在：★ `Scope` 的零值是 `""`，**不是** `ScopePlatform`
// ⇒ ⇒ `EffectiveValue` 用 `""` 查后端查不到 ⇒ 且 env 分支要求
// `scope == ScopePlatform` ⇒ ⇒ **省略 Scope = 彻底不可配**（DB 与 env 两条路全断）。
// ⇒ ⇒ 这是**静默**的：spec 在、UI 上看得到、写了不生效。
func TestRegisteredSettingsSpecsDeclareScope(t *testing.T) {
	reg := registeredKeys()

	var missing []string
	for key, sp := range reg {
		if sp.Scope == "" {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)

	for _, key := range missing {
		t.Logf("  ★ %s 省略了 Scope ⇒ EffectiveValue 用 \"\" 查后端查不到 ⇒ env 分支也不走 ⇒ 彻底不可配", key)
	}
	if len(missing) > 0 {
		t.Fatalf("★ 有 %d 个已注册的 spec **省略了 Scope**：\n    %s\n"+
			"   ⇒ ⇒ Scope 零值是 \"\"（≠ ScopePlatform）⇒ `EffectiveValue` 查 `r.backends[\"\"]` 查不到\n"+
			"   ⇒ ⇒ env 分支又要求 `scope == ScopePlatform` ⇒ ⇒ **DB 与 env 两条路全断**\n"+
			"   ⇒ ⇒ ⇒ 后果：**写 settings_kv 无效、设 env 也无效**，代码只会走调用方 fallback\n"+
			"   ⇒ ⇒ ⇒ ★ 而这是**静默**的：spec 在、UI 上看得到、运营以为配了\n"+
			"   ⇒ ⇒ 修法：补 `Scope: ScopePlatform`（★ 注意：这会让**已经被忽略的**\n"+
			"   ⇒ ⇒ ★ 生产 settings_kv 值**第一次生效** ⇒ 上线前必须知情）",
			len(missing), strings.Join(missing, "\n    "))
	}
}

// TestSettingsReadKeyTypeMatchesSpecType 读方期望的类型必须与 spec 的 Type 相容。
//
// ★ 类型不符的后果：`getPlatformBool` 等用 `json.Unmarshal(raw, &v)` 解一个
// **bool**，而 raw 的 JSON 形态与 v 不匹配 ⇒ 解不出来 ⇒ **静默回退 fallback**
// ⇒ ⇒ 「写进 settings_kv 了，但读出来还是默认值」——和「键没注册」是同一种
// 症状，但修法完全不同 ⇒ ⇒ 所以这道门独立存在。
//
// ★ 判据用**相容集**而不是一对一：★ `TypeEnum` / `TypeURL` 存的都是
//
//	**JSON 字符串** ⇒ ⇒ `GetPlatformString` 能正常解开 ⇒ ⇒ 它们与
//	`TypeString` **相容**。（★ 第一版把 enum 判成不匹配 ⇒ 那是本门的
//	**假阳性**，已修正为相容集。）
var readerCompatibleTypes = map[string][]settings.ValueType{
	"bool":    {settings.TypeBool},
	"float64": {settings.TypeFloat},
	"int":     {settings.TypeInt, settings.TypeDuration},
	"int64":   {settings.TypeInt, settings.TypeDuration},
	"string":  {settings.TypeString, settings.TypeEnum, settings.TypeURL},
}

func TestSettingsReadKeyTypeMatchesSpecType(t *testing.T) {
	loadRepo(t)
	reg := registeredKeys()

	type mismatch struct {
		key  string
		want string
		got  settings.ValueType
		loc  string
	}
	var bad []mismatch

	for key, want := range readSites {
		sp, ok := reg[key]
		if !ok {
			continue // 已由 TestSettingsReadKeysAreRegistered 覆盖
		}
		compat, known := readerCompatibleTypes[want]
		if !known {
			continue
		}
		okType := false
		for _, c := range compat {
			if sp.Type == c {
				okType = true
				break
			}
		}
		if !okType {
			bad = append(bad, mismatch{key: key, want: want, got: sp.Type, loc: readSitesAt[key]})
		}
	}
	sort.Slice(bad, func(i, j int) bool { return bad[i].key < bad[j].key })

	for _, m := range bad {
		t.Logf("  ★ %s：读方要 %s（读于 %s），但 spec 的 Type 是 %s", m.key, m.want, m.loc, m.got)
	}
	if len(bad) > 0 {
		var lines []string
		for _, m := range bad {
			lines = append(lines, m.key+" 读方要="+m.want+" spec.Type="+string(m.got))
		}
		t.Fatalf("★ 有 %d 个 settings 键的「读方类型」与「spec.Type」不相容：\n    %s\n"+
			"   ⇒ ⇒ `json.Unmarshal` 解不出来 ⇒ **静默回退 fallback**\n"+
			"   ⇒ ⇒ 症状与「键没注册」一样（写了不生效），但修法不同：改 spec 的 Type",
			len(bad), strings.Join(lines, "\n    "))
	}
}
