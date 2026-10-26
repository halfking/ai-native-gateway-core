package middleware

// mobile_index_asset_path_test.go —— 移动端 index.html 的**资产引用路径**门。
//
// 背景（2026-10-19 分支裁决轮）：`origin/feat/app-icon-standard` 把
// web-mobile/index.html 的图标引用写成根绝对路径 `/favicon.svg`，与该文件
// 里既有的、写着「根绝对路径在两侧必有一侧错」的契约注释**直接冲突**。
//
// 为什么是缺陷而不是风格问题：
//   · 生产下移动端 index.html 由 /m/ 提供（cmd/gateway/mobile_static.go 的
//     ServeSPA，从 web-mobile/dist 根取文件），`/favicon.svg` 解析到**桌面**
//     web 根 ⇒ 移动端自己的 public 资产永远不会被请求；
//   · dev serve 下应用在根路径，`/favicon.svg` 又恰好能取到（vite public 目录）
//     ⇒ **开发期看不出问题**，只在生产露出来；
//   · 两侧图标一旦分叉（桌面/移动本来是两个 public 目录），移动端会静默显示
//     桌面图标，没有任何报错。
//
// 桌面侧有 web_public_bypass_contract_test.go 盯着「引用的 public 根资产必须
// 匿名可达」，但**移动侧没有对等的一道门**——引用的路径形态没人管。本文件补上。
//
// 两条腿都要有牙：
//   · 正向：web-mobile/index.html 引用的每个 public 根资产，其**生产解析后**
//     的 URL 必须匿名可达（/m/ 前缀豁免），否则移动端图标破图；
//   · 反向：这些引用**不得是根绝对路径**——必须是相对路径，解析后落在 /m/ 下。
//
// 口径说明：`/src/main.ts` 这类 Vite 源模块引用不在本门范围——它不来自
// web-mobile/public/（构建后成为 /m-assets/ 下的哈希产物）。

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

// mobileMountPath 是移动端 SPA 的挂载前缀（与 cmd/gateway/mobile_static.go 的
// mobileSPAMountPath 一致）。这里重述为常量而不是从 cmd/gateway 导入——
// cmd/gateway 是 main 包，middleware 不能依赖它；两者各写一份字面量时，
// 任何一侧改前缀而另一侧没改，本门会变红，这正是本门存在的意义。
const mobileMountPath = "/m/"

// htmlRefRe 抽取 src=/href= 引用。
var htmlRefRe = regexp.MustCompile(`(?:src|href)="([^"]+)"`)

// mobilePublicRootFiles 列出 web-mobile/public 下的**根级文件**。
func mobilePublicRootFiles(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "web-mobile", "public"))
	if err != nil {
		t.Fatalf("read web-mobile/public: %v", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out[e.Name()] = true
	}
	return out
}

// mobilePublicAssetRefs 取出 web-mobile/index.html 里指向 web-mobile/public
// 根级文件的引用（去重、排序）。返回 (引用原文, 是否根绝对路径)。
func mobilePublicAssetRefs(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "web-mobile", "index.html"))
	if err != nil {
		t.Fatalf("read web-mobile/index.html: %v", err)
	}
	publicFiles := mobilePublicRootFiles(t)

	seen := map[string]bool{}
	var refs []string
	for _, m := range htmlRefRe.FindAllStringSubmatch(string(raw), -1) {
		ref := m[1]
		if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "//") {
			continue
		}
		if strings.HasPrefix(ref, "#") {
			continue
		}
		// 只认 public **根级**文件：带目录的引用（/src/main.ts、assets/…）
		// 不是 public 根资产。
		name := strings.TrimPrefix(ref, "/")
		if strings.Contains(name, "/") {
			continue
		}
		if !publicFiles[name] {
			continue
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

// TestMobileIndexPublicAssetsUseRelativePaths —— 反向腿：移动端 public 根资产
// 的引用不得是根绝对路径。
func TestMobileIndexPublicAssetsUseRelativePaths(t *testing.T) {
	refs := mobilePublicAssetRefs(t)
	if len(refs) == 0 {
		t.Fatal("web-mobile/index.html 一个 public 根资产都没引用到 —— 量具失效（正则或路径形态漂移？）")
	}
	for _, ref := range refs {
		if strings.HasPrefix(ref, "/") {
			t.Errorf("web-mobile/index.html 用根绝对路径引用 public 资产 %q —— "+
				"生产下 index.html 由 %s 提供，根绝对路径会解析到桌面 web 根，"+
				"移动端自己的 public 资产永远取不到（dev serve 在根路径反而「正常」，"+
				"所以这错只在生产露出来）。请改成相对路径（不带前导 /）。",
				ref, mobileMountPath)
		}
	}
	t.Logf("反向腿核对 %d 条移动端 public 根资产引用", len(refs))
}

// TestMobileIndexPublicAssetsAreAnonymouslyReachable —— 正向腿：按相对路径
// 解析出来的生产 URL 必须匿名可达。
func TestMobileIndexPublicAssetsAreAnonymouslyReachable(t *testing.T) {
	refs := mobilePublicAssetRefs(t)
	if len(refs) == 0 {
		t.Fatal("web-mobile/index.html 一个 public 根资产都没引用到 —— 量具失效（正则或路径形态漂移？）")
	}
	for _, ref := range refs {
		// 生产解析：相对引用挂在 /m/ 下；根绝对引用挂在站点根下（这里只用于
		// 暴露「解析到桌面去了」这一事实，正向腿按正确形态断言）。
		resolved := mobileMountPath + strings.TrimPrefix(ref, "/")

		mw := NewAuthMiddleware("secret-key")
		reached := false
		handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, resolved, nil)
		req.Header.Set("Accept", "*/*")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if !reached || rr.Code == http.StatusUnauthorized {
			t.Errorf("%s（由 %q 解析而来）未匿名可达（status=%d）—— "+
				"移动端图标会破图。/m/ 前缀豁免必须覆盖它。", resolved, ref, rr.Code)
		}
	}
	t.Logf("正向腿核对 %d 条移动端 public 根资产", len(refs))
}
