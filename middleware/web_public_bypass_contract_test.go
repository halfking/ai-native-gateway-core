package middleware

// web_public_bypass_contract_test.go —— 把 `web/public` 根级资产的匿名豁免
// 从「注释里的维护契约」升级成真判据（r1030）。
//
// 背景：12e94a240（2547 轮 GUI 验收）给全局 API-key 门补了 7 个 public 根
// 资产 + /index.html，提交信息里写下了维护契约——
//
//	今后往 web/public 根新增文件，必须同步登记此处，或改放 /assets/
//
// 但**当时只是一句注释**：没有任何门盯着目录与清单的一致性。契约靠人记，
// 而 12e94a240 修的正是这个缺陷本身（public 根文件从未被豁免、logo 破图、
// theme-init 失效……从 2026-07-10 起持续了三个月）。三个月没人记，是因为
// 没有东西会在漏登记时变红。
//
// 本文件补上这个门，两条腿都要有牙：
//   · 正向：被 index.html 以 src=/href= 引用（即浏览器运行期真会匿名请求）
//     的 public 根文件，必须出现在 auth_mw.go 的 ExactPaths 里；
//   · 反向：public 根目录里**没被引用的**文件（构建期产物），必须逐一登记在
//     buildTimeOnlyPublicAssets 里并说明理由——「没人引用」本身是断言，不是
//     默认豁免。往 public 根丢一个新文件（无论是否被引用）都会变红，逼人做出
//     「登记进 ExactPaths」或「登记为构建期产物」的显式决定。
//
// 口径说明：`/src/main.ts` 这类 Vite 源模块引用不在本门范围（构建后成为
// /assets/ 下的哈希产物，已由 PathPrefixes 前缀豁免覆盖）。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// buildTimeOnlyPublicAssets：public 根里**不被 index.html 运行期引用**的文件。
// 每条都要写理由——新增条目时请顺带复核理由是否仍成立。
var buildTimeOnlyPublicAssets = map[string]string{
	"menu-config.json": "构建期产物：scripts/export-menu-config.mjs 从 src/config/appNav.ts 生成，" +
		"只被 i18n parity 门与构建链读取，浏览器运行期不请求（无需匿名可达）",
	"version.json": "构建期产物：vite.config.ts 的 llmgwDeploySeq 插件与 deploy-local.sh 的 " +
		"build_frontend 读它取 build_seq；浏览器运行期不请求",
}

var publicRootRefRe = regexp.MustCompile(`(?:src|href)="/([^"]+)"`)

// rootPublicFiles 列出 web/public 下的**根级文件**（不进子目录，public 子目录
// 不在豁免范围，见 TestAuthMiddleware_RootAssetBypassDoesNotLeakNeighbors）。
//
// ★ 点开头的文件（`.DS_Store` 等 **OS/编辑器产物**）**不在本门范围**：
//
//	① 它们被 `.gitignore` 排除（实测 `git check-ignore web/public/.DS_Store`
//	   ⇒ 命中 `.gitignore:122`）⇒ **永远进不了仓** ⇒ 「显式登记」这个要求
//	   对它们**不适用**（登记了也没用，它不会被提交）；
//	② 它们**不是构建期产物**（`buildTimeOnlyPublicAssets` 的语义是
//	   「由构建链生成、浏览器运行期不请求」），登记进去是**语义错配**；
//	③ ★ 本门存在的目的（2026-07-10 起）是要逼人对「**会被匿名可达**的
//	   公开资产」做显式决定 ⇒ 而点文件**不会被匿名可达**
//	   （不在 `ExactPaths` ⇒ 鉴权中间件直接拒）。
//
// ⇒ ★ 2026-10-24 之前这里没排除点文件 ⇒ ⇒ **每个 macOS 检出上这道门都是红的**
// ⇒ ⇒ 一道天天红的门 ⇒ 慢慢就没人信它了（与 LESSONS §357「门没人跑」同族）。
func rootPublicFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "web", "public"))
	if err != nil {
		t.Fatalf("read web/public: %v", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue // ★ OS/编辑器产物（见上方说明）
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	return files
}

// TestWebPublicRootRuntimeAssetsAreAnonymouslyReachable —— 正向腿。
func TestWebPublicRootRuntimeAssetsAreAnonymouslyReachable(t *testing.T) {
	indexHTML, err := os.ReadFile(filepath.Join("..", "web", "index.html"))
	if err != nil {
		t.Fatalf("read web/index.html: %v", err)
	}

	rootFiles := map[string]bool{}
	for _, f := range rootPublicFiles(t) {
		rootFiles[f] = true
	}

	// 只看指向 public 根的引用（/src/... 是 Vite 源模块，构建后落在 /assets/）。
	seen := 0
	for _, m := range publicRootRefRe.FindAllStringSubmatch(string(indexHTML), -1) {
		ref := m[1]
		if strings.Contains(ref, "/") { // /src/main.ts 之类的带目录引用
			continue
		}
		if !rootFiles[ref] {
			continue // index.html 根目录本身不在 public/ 下
		}
		seen++

		mw := NewAuthMiddleware("secret-key")
		reached := false
		handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, "/"+ref, nil)
		// 浏览器 <img>/<script> 的典型 Accept：不是 text/html 导航，
		// SPA 导航豁免救不了它们（这正是 12e94a240 的现场形态）。
		req.Header.Set("Accept", "*/*")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if !reached || rr.Code == 401 {
			t.Errorf("/%s 被 index.html 运行期引用却未匿名可达（status=%d）——"+
				"要么登记进 auth_mw.go 的 ExactPaths，要么改放 /assets/", ref, rr.Code)
		}
	}

	if seen == 0 {
		t.Fatal("index.html 一个 public 根资产都没引用到 —— 量具失效（正则或路径形态漂移？）")
	}
	t.Logf("正向腿核对 %d 条运行期引用的 public 根资产", seen)
}

// TestWebPublicRootFilesAreAllAccountedFor —— 反向腿，也是真正防复发的那条：
// public 根里的每个文件都必须**有名字地**被处置过（登记豁免，或登记为构建期
// 产物）。新丢一个文件进来而没人表态，就在这里变红。
func TestWebPublicRootFilesAreAllAccountedFor(t *testing.T) {
	bypass := bypassExactPathsForTest()

	var unaccounted []string
	for _, f := range rootPublicFiles(t) {
		if bypass[f] {
			continue
		}
		if _, ok := buildTimeOnlyPublicAssets[f]; ok {
			continue
		}
		unaccounted = append(unaccounted, f)
	}

	if len(unaccounted) > 0 {
		t.Errorf("web/public 根有 %d 个文件既未登记匿名豁免、也未登记为构建期产物：%v\n"+
			"请二选一处置：① 确需浏览器匿名可达 → 加进 auth_mw.go 的 ExactPaths；"+
			"② 纯构建期产物 → 加进本文件的 buildTimeOnlyPublicAssets 并写明理由。"+
			"（12e94a240 的维护契约此前只有注释，无门；2026-07-10 起三个月无人记）",
			len(unaccounted), unaccounted)
	}
}

// TestBuildTimeOnlyPublicAssetsStillUnreferenced —— 防 buildTimeOnlyPublicAssets
// 这张表变成万能豁免：登记为「构建期产物」的文件一旦真的被 index.html 引用
// （即它其实需要匿名可达），必须从这里挪进 ExactPaths。
func TestBuildTimeOnlyPublicAssetsStillUnreferenced(t *testing.T) {
	indexHTML, err := os.ReadFile(filepath.Join("..", "web", "index.html"))
	if err != nil {
		t.Fatalf("read web/index.html: %v", err)
	}

	var stale []string
	for name := range buildTimeOnlyPublicAssets {
		if strings.Contains(string(indexHTML), `"/`+name+`"`) {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)

	if len(stale) > 0 {
		t.Errorf("这些文件已登记为「构建期产物、浏览器不请求」，但 index.html 真的引用了它们：%v\n"+
			"它们需要匿名可达，请从 buildTimeOnlyPublicAssets 挪进 auth_mw.go 的 ExactPaths。", stale)
	}
}

// bypassExactPathsForTest 从 auth_mw.go 的构造里取出 ExactPaths，避免测试
// 硬编码一份清单（两份清单漂移起来，这道门就成了新的假绿来源）。
func bypassExactPathsForTest() map[string]bool {
	out := map[string]bool{}
	for _, p := range NewAuthMiddleware("test-key").bypass.ExactPaths {
		out[strings.TrimPrefix(p, "/")] = true
	}
	return out
}
