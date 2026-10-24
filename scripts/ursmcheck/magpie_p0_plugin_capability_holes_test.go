package ursm_test

// magpie_p0_plugin_capability_holes_test.go — P0-2 的门：
// **插件能力表与运行时实现同源**。
//
// # 这道门守住哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §3.6 / §6-P0-2）审计出 plugin-runtime 有两处**硬缺口**：
//
//	① **数据面能力已声明但被自我否决**：能力词表里写好了
//	   `request.mutate` / `request.block` / `response.mutate` / `response.block` /
//	   `tool.mutate` / `tool.block` / `tool.execute`（7 个），
//	   而 `plugin-runtime/sandbox.go` 自陈"data-plane write capability
//	   (mutate/block/tool.execute) is refused until a real sandbox is available"，
//	   且 `DefaultSandboxPolicy().Available = false`。
//	② **5 条 capability 路由只挂了 2 条**：`bodies` / `interception-policies` /
//	   `events` 三条**在全仓找不到任何 handler**。
//
// ⇒ 处置取计划书 §6-P0-2 的**第二个选项**（明确判死）：能力表标注
//   `Implemented: false` + **manifest 声明即拒** + 门禁逐条对拍现实。
//
// # 判据清单
//
//	G1 每条路由能力的"是否实现"与**真实路由注册**一致（扫 cmd/gateway）
//	G2 manifest 声明未实现的能力 ⇒ **被拒**（且该能力永远不被授予）
//	G3 沙箱的"谁负责置 true"是**可达但不可被业务代码误置**的
//	G4 路由能力词表与绑定能力词表**不相交**且各自单一事实源
//
// # 变异灵敏度
//
//	M1 给 /bodies 加上一个真实 handler，但表里仍标 Implemented=false
//	    ⇒ G1 红（表说有实现、现实说有 ⇒ 两种错都抓）
//	M2 把 body.fetch 的 Implemented 改成 true，但没有 handler
//	    ⇒ G1 红
//	M3 让 RegisterManifest 不再拒绝未实现能力
//	    ⇒ G2 红
//	M4 让生产代码构造一个 Available=true 的沙箱策略
//	    ⇒ G3 红（业务代码不得决定沙箱可用性）

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// magpieP0PluginRepoRoot 返回仓根目录。
func magpieP0PluginRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// magpieP0PluginRouteHandlers 扫 cmd/gateway 下**真实的**插件路由注册。
//
// 返回 map[pathPrefix]bool：该前缀是否被注册过。
func magpieP0PluginRouteHandlers(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	dir := filepath.Join(root, "cmd", "gateway")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 cmd/gateway 失败: %v", err)
	}
	// mux.Handle("GET /_gateway/plugin/v1/sessions", …) 这一类。
	re := regexp.MustCompile(`mux\.Handle\("[A-Z]+\s+([^"]+)"`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", e.Name(), err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			out[m[1]] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("没扫到任何插件路由注册 ⇒ 判据失效（cmd/gateway 下 %d 个文件）", len(entries))
	}
	return out
}

// magpieP0PluginRoutePrefixes 读路由表（单一事实源）。
func magpieP0PluginRoutePrefixes() []struct {
	Prefix      string
	Capability  string
	Implemented bool
} {
	var out []struct {
		Prefix      string
		Capability  string
		Implemented bool
	}
	for _, r := range pluginruntime.CapabilityRoutes() {
		out = append(out, struct {
			Prefix      string
			Capability  string
			Implemented bool
		}{r.PathPrefix, r.Capability, r.Implemented})
	}
	return out
}

// TestMagpieP0CapabilityTableMatchesRuntime G1：
// 每条路由能力的「是否实现」与**真实路由注册**一致。
//
// ★ 两个方向都抓：
//
//	① 表说 implemented=true ⇒ **必须**有 handler（否则表在骗人）；
//	② 表说 implemented=false ⇒ **不许**有 handler（否则实现被藏起来了）。
func TestMagpieP0CapabilityTableMatchesRuntime(t *testing.T) {
	root := magpieP0PluginRepoRoot(t)
	handlers := magpieP0PluginRouteHandlers(t, root)

	checked := 0
	for _, r := range magpieP0PluginRoutePrefixes() {
		// 路由注册用 Go 1.22 的模式串（可含 {seg}），前缀比对着看。
		registered := false
		for h := range handlers {
			if h == r.Prefix || strings.HasPrefix(r.Prefix, h) || strings.HasPrefix(h, r.Prefix) {
				registered = true
				break
			}
		}
		if r.Implemented && !registered {
			t.Fatalf("能力 %q 标 Implemented=true，但 cmd/gateway 里没有 %q 的路由注册 ⇒ "+
				"能力表与运行时不同源", r.Capability, r.Prefix)
		}
		if !r.Implemented && registered {
			t.Fatalf("能力 %q 标 Implemented=false（未实现），但 cmd/gateway 里**有** %q 的路由注册 ⇒ "+
				"要么把表改成 implemented，要么把实现撤掉（不能两处不一致）", r.Capability, r.Prefix)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("路由表为空 ⇒ 判据失效")
	}
}

// TestMagpieP0ManifestDeclaringUnimplementedCapabilityIsRejected G2：
// manifest 声明未实现的能力 ⇒ **被拒**，且该能力**永远不被授予**。
func TestMagpieP0ManifestDeclaringUnimplementedCapabilityIsRejected(t *testing.T) {
	unimplemented := pluginruntime.UnimplementedCapabilities()
	if len(unimplemented) == 0 {
		t.Fatal("没有未实现的能力 ⇒ 判据失效（P0-2 的前提是存在数据面空洞）")
	}
	for _, c := range unimplemented {
		if pluginruntime.IsImplementedCapability(c) {
			t.Fatalf("能力 %q 同时出现在已实现与未实现两表里", c)
		}
	}

	reg := pluginruntime.NewCapabilityRegistry()
	m := &pluginruntime.Manifest{
		PluginID: "demo",
		Capabilities: []string{
			pluginruntime.CapabilitySessionRead,
			unimplemented[0],
		},
	}
	err := reg.RegisterManifest(m)
	if err == nil {
		t.Fatalf("manifest 声明未实现能力 %q 竟被接受 —— 声明即拒的判据失效", unimplemented[0])
	}
	// ★ 关键：被拒的能力**永远不被授予**（失败关闭）。
	if reg.Allows("demo", unimplemented[0]) {
		t.Fatalf("被拒的能力 %q 仍被授予 ⇒ 会让插件真的走到没有 handler 的路由上", unimplemented[0])
	}
	// ★ 其余能力照常登记（不该因为多声明一条就把能用的也丢了）。
	if !reg.Allows("demo", pluginruntime.CapabilitySessionRead) {
		t.Fatalf("未实现能力的声明不应牵连已实现能力 %q", pluginruntime.CapabilitySessionRead)
	}
}

// TestMagpieP0SandboxAvailabilityIsNotBusinessControlled G3：
// 沙箱的可用性**不得**由业务代码决定。
//
// ★ 判据：生产代码（非测试）里**不得**出现 `Available: true` 的沙箱策略构造；
//
//	且 `DefaultSandboxPolicy().Available` 必须是 false；
//	且 `ValidateSandboxPolicy` 对"自称可用但缺 OS 用户/内存上限"的策略**必须报错**。
func TestMagpieP0SandboxAvailabilityIsNotBusinessControlled(t *testing.T) {
	if pluginruntime.DefaultSandboxPolicy().Available {
		t.Fatal("默认沙箱策略必须是不可用的（deny-by-default）")
	}
	// 自称可用但不合规 ⇒ 报错。
	bad := pluginruntime.SandboxPolicy{Available: true}
	if err := pluginruntime.ValidateSandboxPolicy(bad); err == nil {
		t.Fatal("Available=true 却缺 os_user / 内存上限，必须报错（否则就是个假沙箱）")
	}

	root := magpieP0PluginRepoRoot(t)
	dir := filepath.Join(root, "plugin-runtime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 plugin-runtime 失败: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", e.Name(), err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "Available:") {
				continue
			}
			if !strings.Contains(line, "true") {
				continue
			}
			t.Fatalf("%s:%s 生产代码里出现了 Available: true 的沙箱策略 —— "+
				"沙箱可用性只能由配置装载处决定，业务代码只读不写",
				e.Name(), strconv.Itoa(i+1))
		}
	}
}

// TestMagpieP0RouteAndBindingVocabulariesAreDisjoint G4：
// 路由能力词表与绑定能力词表**不相交**，且各自单一事实源。
//
// ★ 为什么这条重要：两个词表的名字长得很像（session.read vs session.observe），
//
//	一旦哪天混用，"插件声明了 X ⇒ 被允许做 Y"这种越权就会静悄悄发生。
func TestMagpieP0RouteAndBindingVocabulariesAreDisjoint(t *testing.T) {
	route := map[string]bool{}
	for _, c := range pluginruntime.ImplementedCapabilities() {
		route[c] = true
	}
	for _, c := range pluginruntime.UnimplementedCapabilities() {
		route[c] = true
	}
	for _, c := range pluginruntime.BindingCapabilities() {
		if route[c] {
			t.Fatalf("能力 %q 同时出现在路由词表与绑定词表里 —— 两个词表必须不相交", c)
		}
	}
	if len(route) == 0 {
		t.Fatal("路由能力词表为空 ⇒ 判据失效")
	}
}
