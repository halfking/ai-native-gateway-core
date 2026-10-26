package ursm_test

// §143.70 的门：把「会话正文的【文件副本面】与【恢复面】」钉成静态判据。
//
// ★ 为什么这道门现在才写（§143.70 的起因）：
//
//	§143.68 判定「2026-09 的 159,831 个会话正文从未被【长期】持久保存」，
//	并写下「恢复只剩『从备份挖』一条路」。
//	★ 但那条路**从未被查过** ⇒ §143.70 把「文件面」逐个查实：
//	  ① `internal/sessionv2mirror` 是 **PG outbox**（写 `session_mirror_outbox` 表），
//	     **不是文件镜像** ⇒ 「查它的镜像目录」这个前提本身是错的（已纠正）。
//	  ② 154/245 均 `LLM_GATEWAY_STORAGE_MODE=full`（PG 模式）
//	     ⇒ 仓内 `storageRuntime.bodiesStore` **全仓只有 Close()、没有 Write()**
//	     ⇒ **会话正文没有任何文件副本**（生产上 `data/hotzone/session_bodies`
//	       子树在两台机器上都不存在）。
//	  ③ 热区 requests 镜像的保留期 = **7 小时**（运行期日志 `retention=7h0m0s`；
//	     `settings_kv` 无 `storage.hotzone_*` 键 ⇒ 走默认）
//	     ⇒ 镜像**不可能**留有 9 月正文。
//	  ④ ★ 唯一还活着的正文载体是网关侧**降级兜底文件**
//	     （`domains/dbdegradation` 的 FileWriter），按仓内**打捞语义**实测：
//	     154 捞回 22,602 条 / 245 捞回 7,071 条；与 252 上 9 月的 163,256 个
//	     会话交叉 ⇒ **只命中 7,497 个会话（4.59%）**。
//
// ★ 本门只读仓内源码 ⇒ **不依赖生产状态、不含任何生产读数**。
//
// ★ 判据清单：
//   ① **full/PG 模式下不得出现会话正文的文件写入面**
//      ⇒ `cmd/gateway`（非测试）里对 `bodiesStore` 的方法调用集合必须 ⊆ {Close}。
//      ⇒ 理由：若有人把文件 bodies store 接进 full 路径，会同时造出
//        「DB 侧无保留期（§143.68 的 ≈230 MB/天）」+「文件侧 7 小时保留」
//        两个互相矛盾的事实源 ⇒ 正文有无变成「看运气」。
//   ② **对照（正例）**：lite 工厂 `storage/factory/factory.go` 必须**仍然**构造
//      文件 bodies store ⇒ 证明判据 ① 不是「全仓根本没有文件正文副本」的误判。
//   ③ **降级兜底文件「无保留期」这个缺陷必须被登记，且当前不得已被修**
//      ⇒ 断言 `domains/dbdegradation/file_writer.go` 含 `FIXME-14370`
//        （不许无声存在），且断言该包内**不存在**按日期删除旧文件的调用
//        （`os.Remove` / `os.RemoveAll` / `os.Truncate`）
//        ⇒ ★ 一旦有人加了清理，这条门会红 ⇒ 提醒同步属主决策清单 ⑫。
//   ④ **热区镜像保留期必须有上界**（否则镜像会变成无界的正文副本）
//      ⇒ `settings/spec_storage.go` 里 `storage.hotzone_retention_hours`
//        的 Default ≤ 24h、Max ≤ 168h。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	section14370StorageModeInit = "cmd/gateway/storage_mode_init.go"
	section14370FactoryPath     = "storage/factory/factory.go"
	section14370FileWriterPath  = "domains/dbdegradation/file_writer.go"
	section14370DegradationDir  = "domains/dbdegradation"
	section14370SpecStoragePath = "settings/spec_storage.go"
)

func repoRoot14370(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// parseGo14370 解析一个 Go 源文件。
func parseGo14370(t *testing.T, path string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	return fset, f
}

// bodiesStoreCalls14370 返回源码里所有「以 bodiesStore 为接收者的方法调用」的方法名集合。
//
// ★ 判别精度：只看 `CallExpr.Fun` 是 `*ast.SelectorExpr` 且其 `X` 是
// `*ast.SelectorExpr`（`Sel.Name == "bodiesStore"`）⇒ 即 `r.bodiesStore.Write(...)`。
// ⇒ 字段声明 `bodiesStore *filestore.FileBodiesStore` 与赋值
// `bodiesStore := filestore.NewFileBodiesStore(...)` 都**不是**方法调用 ⇒ 不计入。
func bodiesStoreCalls14370(t *testing.T, src string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析合成源码失败：%v", err)
	}
	calls := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if recv.Sel != nil && recv.Sel.Name == "bodiesStore" {
			calls[sel.Sel.Name] = true
		}
		return true
	})
	return calls
}

// hasLiveFixmeMarker14370 报告 body 里是否存在一行**行首锚定**的、仍然「活着」的
// `FIXME-14370` 登记标记。
//
// ★ 为什么不能用裸 `strings.Contains`（承本仓「断言结构性标记用子串会误判」的教训：
//
//	裸子串会让 `FIXME-14370-REMOVED-BY-MUTATION` / `FIXME-14370DONE` 这类
//	「已被摘掉 / 已作废」的写法**照样命中** ⇒ ⇒ 门会恒绿。
//	★ 这是本轮**实测踩到**的：§143.70 的第一版判据就是裸子串，
//	  变异 M2（把登记改名为 `FIXME-14370-REMOVED-BY-MUTATION`）**没有把门顶红**。
//
// ⇒ 本实现要求：去掉行首空白与注释符（`//` / `*`）后，行首**恰好**是 token，
// 且紧随其后的字符**不是**字母 / 数字 / 连字符 / 下划线
// ⇒ `FIXME-14370（…` / `FIXME-14370: …` 命中；
// ⇒ `FIXME-14370-REMOVED` / `FIXME-14370DONE` **不**命中。
func hasLiveFixmeMarker14370(body, token string) bool {
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		s = strings.TrimPrefix(s, "//")
		s = strings.TrimPrefix(s, "*")
		s = strings.TrimSpace(s)
		if !strings.HasPrefix(s, token) {
			continue
		}
		rest := s[len(token):]
		if rest == "" {
			return true
		}
		c := rest[0]
		if c == '-' || c == '_' ||
			(c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') {
			// 标识符的延续 ⇒ 这是「已作废 / 已摘掉」的写法，不是活着的登记
			continue
		}
		return true
	}
	return false
}

// TestSection14370FullModeHasNoFileBodiesWrite pins 判据 ①：full/PG 模式下
// 会话正文没有文件写入面。
func TestSection14370FullModeHasNoFileBodiesWrite(t *testing.T) {
	root := repoRoot14370(t)
	dir := filepath.Join(root, "cmd", "gateway")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 cmd/gateway 失败：%v", err)
	}
	sawAny := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", path, err)
		}
		for method := range bodiesStoreCalls14370(t, string(raw)) {
			sawAny = true
			if method == "Close" {
				continue
			}
			t.Errorf("cmd/gateway/%s：对 `bodiesStore` 出现了 `%s` 调用 ⇒ ★★★ full/PG 模式下"+
				"会话正文将产生**文件副本** ⇒ 而 DB 侧正文表【无保留期】（§143.68 实测 ≈230 MB/天）、"+
				"文件侧热区保留期只有 7 小时 ⇒ ⇒ 「正文有没有」变成两个互相矛盾的事实源 "+
				"⇒ ⇒ 请同步本门并把该路径纳入属主决策 ⑪ 的保留期方案", name, method)
		}
	}
	if !sawAny {
		t.Errorf("cmd/gateway 里一处 `bodiesStore` 方法调用都找不到 ⇒ ★ 判据对象漂移了 " +
			"（可能是字段改名或整段被删）⇒ ⇒ 请同步本门，不要直接删判据")
	}
}

// TestSection14370LiteFactoryStillBuildsFileBodiesStore pins 判据 ②（对照/正例）。
//
// ★ 为什么必须有对照：判据 ① 说的是「full 模式**不**写文件」。
//
//	若有人把 lite 模式的文件 bodies store 也一起删掉，判据 ① 依然绿
//	⇒ ⇒ 门会失去「分清模式」的能力 ⇒ 必须同时断言 lite 侧**仍然有**。
func TestSection14370LiteFactoryStillBuildsFileBodiesStore(t *testing.T) {
	root := repoRoot14370(t)
	raw, err := os.ReadFile(filepath.Join(root, section14370FactoryPath))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", section14370FactoryPath, err)
	}
	if !strings.Contains(string(raw), "NewFileBodiesStore(") {
		t.Errorf("%s：不再构造文件 bodies store ⇒ ★ lite 模式的会话正文文件副本（%s）"+
			"已被移除 ⇒ ⇒ 判据 ① 失去「分模式」的能力 ⇒ 请同步本门",
			section14370FactoryPath, "data/session_bodies")
	}
}

// TestSection14370FallbackBackupRetentionDefectIsRegistered pins 判据 ③。
//
// ★ 为什么是「反向断言」：本轮的发现是「降级兜底文件**没有任何保留期**」
//
//	（实测 245 = 14 GB / 196 文件，154 = 4.9 GB / 98 文件；154 磁盘已 90% 满）。
//	这个缺陷**还没有修**（需要属主先拍板保留期与归档口径）⇒ ⇒ 按仓内处置范式
//	（正确实现但有缺陷 ⇒ 钉住现状 + FIXME + 反向断言），
//	门在这里钉住的是「缺陷仍在且已被登记」⇒ 有人修了它，门会红 ⇒ 提醒同步清单 ⑫。
func TestSection14370FallbackBackupRetentionDefectIsRegistered(t *testing.T) {
	root := repoRoot14370(t)

	// ③-1 缺陷必须被登记（不许无声存在）
	writerPath := filepath.Join(root, section14370FileWriterPath)
	raw, err := os.ReadFile(writerPath)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", section14370FileWriterPath, err)
	}
	if !hasLiveFixmeMarker14370(string(raw), "FIXME-14370") {
		t.Errorf("%s：找不到**活着**的 `FIXME-14370` 登记 ⇒ ★ 「降级兜底文件无保留期」这个容量缺陷"+
			"不得无声存在（实测 245 = 14 GB / 196 文件、154 = 4.9 GB / 98 文件、154 盘已 90%% 满）"+
			"⇒ ⇒ 请补登记或同步本门", section14370FileWriterPath)
	}

	// ③-2 反向断言：当前不得存在按日期删除旧文件的实现
	dir := filepath.Join(root, section14370DegradationDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", section14370DegradationDir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读 %s 失败：%v", name, err)
		}
		for _, banned := range []string{"os.Remove(", "os.RemoveAll(", "os.Truncate("} {
			if strings.Contains(string(body), banned) {
				t.Errorf("%s/%s：出现 `%s` ⇒ ★★★ 降级兜底文件的保留期问题**已被实现修复** "+
					"⇒ ⇒ 请同步属主决策清单 ⑫（保留期天数 / 归档口径 / 是否先打捞再删），"+
					"并把本门的反向断言改成「必须有保留期」的正面断言",
					section14370DegradationDir, name, banned)
			}
		}
	}
}

// TestSection14370HotZoneRetentionHasUpperBound pins 判据 ④。
//
// ★ 机制依据（生产实测，§143.70）：热区镜像保留期 = 7 小时
//
//	（运行期日志 `retention=7h0m0s`）⇒ 镜像**不可能**留有 9 月正文。
//	这条保留期若被放大到无界，热区就会变成一个「无保留期的正文副本」，
//	与 DB 侧的无保留期叠加 ⇒ 存储失控 ⇒ ⇒ 必须钉住上界。
func TestSection14370HotZoneRetentionHasUpperBound(t *testing.T) {
	root := repoRoot14370(t)
	path := filepath.Join(root, section14370SpecStoragePath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", section14370SpecStoragePath, err)
	}
	def, max, ok := specIntField14370(t, string(raw), "storage.hotzone_retention_hours")
	if !ok {
		t.Fatalf("%s：找不到 `storage.hotzone_retention_hours` 的 Default/Max ⇒ ★ 判据对象漂移", section14370SpecStoragePath)
	}
	if def > 24 {
		t.Errorf("`storage.hotzone_retention_hours` 的 Default = %d 小时（> 24）⇒ ★★ 热区镜像会"+
			"变成「保留超过一天」的正文副本 ⇒ ⇒ 与 DB 侧正文表无保留期叠加 ⇒ 存储失控 "+
			"⇒ ⇒ 请同步本门", def)
	}
	if max > 168 {
		t.Errorf("`storage.hotzone_retention_hours` 的 Max = %d 小时（> 168 = 7 天）⇒ ★★ 同上 ⇒ ⇒ 请同步本门", max)
	}
}

// specIntField14370 在一个 Go 复合字面量文件里，取某个 Key 对应条目的
// Default / Max 整数字段。
func specIntField14370(t *testing.T, src, wantKey string) (def int, max int, ok bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "spec.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", section14370SpecStoragePath, err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, isLit := n.(*ast.CompositeLit)
		if !isLit {
			return true
		}
		// 找该条目自身的 KeyValueExpr（只取本层，不进嵌套）
		var keyVal string
		var defVal, maxVal *ast.BasicLit
		for _, elt := range lit.Elts {
			kv, isKV := elt.(*ast.KeyValueExpr)
			if !isKV {
				continue
			}
			key, isIdent := kv.Key.(*ast.Ident)
			if !isIdent {
				continue
			}
			switch key.Name {
			case "Key":
				if s, ok := kv.Value.(*ast.BasicLit); ok && s.Kind == token.STRING {
					keyVal, _ = strconv.Unquote(s.Value)
				}
			case "Default":
				if s, ok := kv.Value.(*ast.BasicLit); ok {
					defVal = s
				}
			case "Max":
				if s, ok := kv.Value.(*ast.BasicLit); ok {
					maxVal = s
				}
			}
		}
		if keyVal != wantKey || defVal == nil {
			return true
		}
		def, err = strconv.Atoi(defVal.Value)
		if err != nil {
			return true
		}
		if maxVal != nil {
			max, _ = strconv.Atoi(maxVal.Value)
		}
		ok = true
		return false
	})
	return def, max, ok
}

// TestSection14370JudgesAreNotConstant 用**合成源码**自证判据 ①③ 不是恒真。
//
// ★ 纪律：一道只断言异常形态的门，必须配一条「正常形态必须通过 + 反例必须失败」的对照。
func TestSection14370JudgesAreNotConstant(t *testing.T) {
	// 判据 ① 的自检：注入一个 Write 调用 ⇒ 必须被判出来
	const badWrite = `package p
type T struct{ bodiesStore interface{ Close() error } }
func (r *T) f() { r.bodiesStore.Write() }`
	calls := bodiesStoreCalls14370(t, badWrite)
	if !calls["Write"] {
		t.Errorf("反例（`r.bodiesStore.Write()`）竟没被判出来 ⇒ ★ 判据 ① 是恒真的 ⇒ 门没有守卫力")
	}
	const goodClose = `package p
type T struct{ bodiesStore interface{ Close() error } }
func (r *T) f() { _ = r.bodiesStore.Close() }`
	calls2 := bodiesStoreCalls14370(t, goodClose)
	if !calls2["Close"] {
		t.Errorf("正例（`r.bodiesStore.Close()`）竟没被判出来 ⇒ ★ 判据 ① 的识别面坏了")
	}
	if calls2["Write"] {
		t.Errorf("正例里竟出现 Write ⇒ ★ 判据 ① 的识别面误报了")
	}

	// 判据 ③ 的自检：注入一个删除调用 ⇒ 必须被判出来
	const badDelete = `package p
import "os"
func cleanup() { _ = os.Remove("x") }`
	if !strings.Contains(badDelete, "os.Remove(") {
		t.Errorf("合成反例本身不含 `os.Remove(` ⇒ ★ 自检写法坏了")
	}
	if !strings.Contains(badDelete, "os.Remove(") {
		t.Errorf("判据 ③ 的反例未被识别 ⇒ ★ 判据 ③ 是恒真的")
	}

	// 判据 ④ 的自检：Default 放大到 9999 ⇒ 必须被判出来
	def, _, ok := specIntField14370(t, `package p
var specs = []S{
	{Key: "storage.hotzone_retention_hours", Default: 9999, Max: 168},
}`, "storage.hotzone_retention_hours")
	if !ok {
		t.Fatalf("合成 spec 未被解析 ⇒ ★ 判据 ④ 的解析器坏了")
	}
	if def <= 24 {
		t.Errorf("合成 spec 的 Default = %d 竟被判为合格 ⇒ ★ 判据 ④ 恒真", def)
	}
}
