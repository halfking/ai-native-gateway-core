package main

// mobile_mount_contract_test.go — 守住「前端构建 base」与「后端挂载前缀」这条
// **跨产物契约**。
//
// 为什么要有这道门：/m-assets/ 同时出现在两棵树里——
//   · cmd/gateway/mobile_static.go 的挂载前缀（本文件用常量锁住）
//   · web-mobile/vite.config.ts 的 production `base`
// 改任何一边**都不会让任何 Go 测试变红**，但线上结果是移动端首页拿到 HTML、
// 每一个 JS/CSS 都 404 ⇒ 白屏。
//
// 现状（实测，2026-10-06，127.0.0.1:8782 实跑）：
//   GET /m                                → 200 text/html
//   GET /m-assets/assets/index-*.js        → 200 text/javascript
//   构建产物里的 API 调用形态               → 相对路径（`/api/auth/me`），
//                                           绝对 URL 命中 0，`credentials:'same-origin'`
// ⇒ **同源托管成立**，这正是 UI规范 04 §7.2 ④ 此刻**不必要**的前提。
// 这道门的作用就是把那个前提钉住：它一旦破了，④ 才突然变成必需品。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// viteBuildBaseRe 抽取 vite.config.ts 里的 production `base` 值。
//
// ★ **必须行锚定（^）**。web-mobile/vite.config.ts 里紧挨着 `base:` 的上方注释
//
//	本身就写着 `/m-assets/*`，下一行注释还写着 `base '/'`——不锚定的话，
//	有人把真正的 `base:` 改坏、注释照旧，这道门仍然是绿的。
//	参见 TestExtractViteBuildBaseIgnoresComments。
var viteBuildBaseRe = regexp.MustCompile(`(?m)^\s*base:[^\n]*?'(/[A-Za-z0-9._-]+/)'`)

// extractViteBuildBase 返回抽取到的 base；抽不到返回空串。
//
// 为什么 `/`（ternary 的另一个分支）抽不到：**模式前后各有一个字面 `/`**，
// 所以匹配至少需要两个斜杠，单个 `/` 天生不满足。
// ⚠️ 这个性质来自**两个斜杠**，不是来自字符类里的 `+`。把 `+` 改成 `*` 观察不到
// 任何差别（实测：两个输入上输出完全相同 ⇒ 等价变异），所以它不是这道门的牙；
// 真正的牙是「去掉结构约束」，见 TestExtractViteBuildBaseRejectsSingleSlash。
func extractViteBuildBase(src string) string {
	m := viteBuildBaseRe.FindStringSubmatch(src)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func repoFile(t *testing.T, parts ...string) string {
	t.Helper()
	// 该包在 cmd/gateway/ 下，仓库根是上两级。
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

// ---------------------------------------------------------------- 抽取器自身

// 判据的自检：注释里出现 /m-assets/ **不得**让抽取器有输出。
// 没有这一条，上面的行锚定就只是「看起来严谨」，没有任何东西证明它起作用。
func TestExtractViteBuildBaseIgnoresComments(t *testing.T) {
	const commentOnly = `  // 生产（build）：资产走 /m-assets/*（网关 /m 挂载，镜像 maintain 先例）；
  // 开发（serve）：base '/'，应用在根路径跑。
  serve: { base: '/m-assets/' }
`
	if got := extractViteBuildBase(commentOnly); got != "" {
		t.Fatalf("抽取器把非 `base:` 行也算进去了：%q —— 这道门会变成恒真判据", got)
	}
}

func TestExtractViteBuildBaseReadsRealAssignment(t *testing.T) {
	if got := extractViteBuildBase("  base: command === 'build' ? '/m-assets/' : '/',\n"); got != "/m-assets/" {
		t.Fatalf("base = %q, want /m-assets/", got)
	}
}

// 单斜杠必须抽不到——这是「别把 ternary 的另一个分支当答案」这条契约本身。
// 变异台账 M4 把模式换成 `'(\/[^']*)'`（去掉双斜杠结构约束）时，就是这条转红。
func TestExtractViteBuildBaseRejectsSingleSlash(t *testing.T) {
	if got := extractViteBuildBase("  base: command === 'build' ? '/',\n"); got != "" {
		t.Fatalf("base='/' 不该被当成答案（那会把 dev 分支读成 production base）：%q", got)
	}
}

// ---------------------------------------------------------------- 契约本体

// 契约一：vite.config.ts 的 production base 必须等于后端挂载前缀常量。
func TestViteBuildBaseMatchesMobileAssetMount(t *testing.T) {
	path := repoFile(t, "web-mobile", "vite.config.ts")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到 %s（release bundle?）：%v", path, err)
	}
	got := extractViteBuildBase(string(src))
	if got == "" {
		t.Fatalf("%s 里抽不到行首的 `base:` —— 抽取器或该文件形态变了，需人工确认", path)
	}
	if got != mobileAssetsMountPath {
		t.Fatalf("跨产物契约破了：vite production base = %q，后端挂载前缀 = %q。\n"+
			"改了一边没改另一边 ⇒ 线上每个 JS/CSS 都 404，移动端白屏，且没有任何 Go 测试会红。",
			got, mobileAssetsMountPath)
	}
}

// 契约二（需要构建产物）：built index.html 里引用的**每一个** /m-assets/* 资源，
// 都必须能真的穿过真实 handler 取到 200。
func TestBuiltIndexAssetsResolveThroughHandler(t *testing.T) {
	dist := repoFile(t, "web-mobile", "dist")
	indexPath := filepath.Join(dist, "index.html")
	indexHTML, err := os.ReadFile(indexPath)
	if err != nil {
		t.Skipf("没有构建产物 %s（先 pnpm build）", indexPath)
	}

	static := NewMobileStaticHandler(dist)
	if static == nil {
		t.Fatalf("dist 存在但 NewMobileStaticHandler 返回 nil：%s", dist)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := newMobileGatewayHandler(next, static)

	// 抓出 index.html 里所有以挂载前缀开头的绝对引用。
	re := regexp.MustCompile(`(?:src|href)="` + regexp.QuoteMeta(mobileAssetsMountPath) + `[^"]*"`)
	refs := re.FindAllString(string(indexHTML), -1)
	if len(refs) == 0 {
		t.Fatalf("%s 里没有任何 %s* 引用 —— 构建产物形态变了，需人工确认", indexPath, mobileAssetsMountPath)
	}

	for _, ref := range refs {
		p := strings.TrimPrefix(strings.TrimSuffix(ref[strings.Index(ref, `"`)+1:], `"`), `"`)
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			body, _ := io.ReadAll(rec.Result().Body)
			t.Fatalf("built index.html 引用的 %s 取不到：HTTP %d %s\n"+
				"⇒ 构建 base 与后端挂载前缀已经对不上（同源形态失效，④ 才变成必需品）",
				p, rec.Code, strings.TrimSpace(string(body)))
		}
	}
	t.Logf("built index.html 的 %d 个资产引用全部 200（%s）", len(refs), mobileAssetsMountPath)
}
