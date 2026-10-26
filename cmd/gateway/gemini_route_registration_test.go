package main

// 判据：Gemini 原生路由的**挂载面**必须与凭据收口面一致。
//
// # 背景（2026-10-26，Gemini 原生认证修复轮）
//
// 原生 Gemini 客户端按厂方契约用 `x-goog-api-key` 头或 `?key=` query 传凭据
// （docs/vendor-formats/gemini.md §HTTP 形态、docs/供应商协议优化.md §8.3、
// docs/QUICK_REFERENCE.md §Gemini协议）。本仓此前**一张都没实现** ⇒ 原生
// 客户端一律 401 missing_key，且死在 mux 之前。
//
// 修复里，`?key=` 这一形态被**收口到 Gemini 原生路由**（`key` 是个极通用的
// query 参数名，全局认它当凭据源会劫持未来任何把 `?key=` 当业务参数用的
// 端点）。收口面由 internal/auth.IsGeminiNativePath 判定，它的锚点就是
// **这里挂载 Gemini handler 的两个 mux 前缀**。
//
// ⇒ 两侧一旦漂移（挂载面变了 / 收口面没跟着变），就会出现两种坏结果：
//   - 挂载面变宽而收口面没变 ⇒ 新路由上的 Gemini 客户端无法认证（401）；
//   - 收口面变宽而挂载面没变 ⇒ 非 Gemini 路由上 `?key=` 变成了凭据源。
//
// ⇒ 本文件从**源码**读出挂载面（不是从常量读，避免自证），与
//   domains/streaming 侧的收口面契约测试（TestGeminiNativePathMatchesHandlerParser）
//   一起把两侧钉死。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// geminiHandlerMountRe 匹配 Gemini handler 的挂载语句。
//
// ★ 判据用**挂载语句**而不是常量：常量与挂载点可以一起改而测试仍然全绿
// （自证），挂载语句是实际生效的那一行。
var geminiHandlerMountRe = regexp.MustCompile(`mux\.Handle\("(/v1beta/models/|/v1/models/)",\s*geminiHandler\)`)

// geminiModelListMountRe matches the Gemini-NATIVE model list mount.
//
// ★ It is a DIFFERENT shape from the generateContent mount (HandleFunc vs
// Handle, and a models handler vs the Gemini handler) ⇒ a separate
// pattern, not a widened geminiHandlerMountRe. Widening the existing
// pattern would silently change what "the mount set" means.
//
// The path is EXACTLY "/v1beta/models": it is a distinct route, not a
// prefix. "/v1beta/models/" belongs to the Gemini handler subtree above.
var geminiModelListMountRe = regexp.MustCompile(`mux\.HandleFunc\("(/v1beta/models)",\s*modelsHandler\.ServeGeminiModels\)`)

// TestGeminiHandlerIsMountedOnBothVendorPrefixes asserts the Gemini handler
// is reachable on BOTH vendor prefixes.
//
// The vendor spec (docs/vendor-formats/gemini.md §HTTP 形态) documents
// `/v1beta/...` as the canonical path and `/v1/...` as the newer one; a
// gateway that serves only one of them rejects half of the native clients.
func TestGeminiHandlerIsMountedOnBothVendorPrefixes(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(b)

	mounted := map[string]bool{}
	for _, m := range geminiHandlerMountRe.FindAllStringSubmatch(src, -1) {
		mounted[m[1]] = true
	}

	for _, prefix := range []string{"/v1beta/models/", "/v1/models/"} {
		if !mounted[prefix] {
			t.Errorf("Gemini handler is not mounted on %q (mounted: %v)", prefix, mounted)
		}
	}

	// The credential scope (internal/auth.IsGeminiNativePath) is anchored on
	// exactly these two prefixes. If a third prefix ever appears, the scope
	// must be extended in the same commit -- this test is the alarm.
	if len(mounted) != 2 {
		t.Errorf("Gemini handler mount set changed (%v) — extend internal/auth.IsGeminiNativePath in the same change", mounted)
	}
}

// TestGeminiModelListIsMounted pins the Gemini-NATIVE model list mount.
//
// The native model list is a served Gemini-native route (its own mux entry,
// not a subtree), so the credential scope (internal/auth.IsGeminiNativePath)
// covers it too. Two sides drift independently:
//
//   - the mount disappears -> a native client gets a 404 on the list while
//     generateContent still works;
//   - the scope covers a path nothing serves -> "?key=" silently becomes a
//     credential source on an unmounted route.
//
// ⇒ The mount is asserted against main.go SOURCE (not against a constant),
//
//	so changing the mount without changing this file is what makes it red.
func TestGeminiModelListIsMounted(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(b)

	matches := geminiModelListMountRe.FindAllStringSubmatch(src, -1)
	if len(matches) != 1 {
		t.Fatalf("Gemini-native model list mount count = %d, want exactly 1 (mounts: %v)", len(matches), matches)
	}
	if matches[0][1] != "/v1beta/models" {
		t.Errorf("Gemini-native model list mounted at %q, want %q", matches[0][1], "/v1beta/models")
	}

	// Reverse nail: the OpenAI-shaped list keeps its own contract. If
	// "/v1/models" ever gets the Gemini shape, the credential scope
	// (which deliberately excludes "/v1/models") and the wire shape would
	// disagree -- the scope must be extended in the SAME change.
	if strings.Contains(src, `mux.HandleFunc("/v1/models"`) {
		t.Errorf("the OpenAI-shaped model list was replaced by the Gemini-native one -- extend internal/auth.IsGeminiNativePath in the same change")
	}
}
