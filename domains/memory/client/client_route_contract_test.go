package client

// client_route_contract_test.go —— G-1 的**跨仓契约清单**门。
//
// # 这道门在补哪个缺口
//
// 审计方案 §3.3 的 G-1 判据原文是：
// 「契约测试断言客户端使用的路径在 memora 路由表内（**跨仓契约清单，双向核对**）」。
//
// 而「双向核对」此前是**人工**做的：网关侧 `client.go:40-48` **手抄**了一份
// memora 的路由表（注释形式），门里又**硬编码**了一份期望值
// ⇒ ★ **memora 改路由时，两边都不会红** —— 契约靠注释维持。
//
// # 本门钉三件事
//
//  1. 客户端的**默认路径**（add / search / health）都在 memora 路由清单里，
//     且 **method** 也对得上（清单是 method+path，不只是 path ——
//     path 对、method 错 ⇒ 405，同样是断链）；
//  2. MemOS 旧路径（`/product/*`）**不在**清单里 —— 它们是「显式 opt-in 的回退」，
//     memora 从来不提供 ⇒ 清单里出现它们，说明清单或注册被改错了；
//  3. **vendored 的副本与 memora 仓的原副本逐字一致** —— 这一条才是真正的
//     「双向核对」（两侧各有一道门，见下）。
//
// # 两个仓、两侧的门
//
//   · memora 侧：`internal/memory/route_manifest_test.go`
//     （`contracts/routes.json` == 真实路由表 ⇒ memora 改路由就红）
//   · 网关侧（本文件）：客户端路径 ⊆ 清单 + 两份副本一致
//     （⇒ 网关改客户端路径就红；memora 改了路由但没同步副本也会红）
//
// ⇒ 「双向」= 两个仓各有一道门 + 同一份契约产物。
//
// # 怎么找到 memora 的 checkout
//
// ① 环境变量 `MEMORA_REPO_PATH`；② 仓库根往上两级的 `memora/` 兄弟目录
// （本机布局：`ai-native-tools/memora` 与 `ai-native-tools/llm-gateway/<本仓>`）。
// 两者都找不到 ⇒ **显式 skip 并说清原因**（不是「安静地通过」）；
// 此时本门的前两条断言仍然照跑（它们只依赖 vendored 副本）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memoraRouteManifest 是 vendored 的路由清单（与 memora 仓的产物同形）。
type memoraRouteManifest struct {
	Schema int    `json:"schema"`
	Source string `json:"source"`
	Routes []struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	} `json:"routes"`
}

// gatewayRepoRoot 从当前工作目录往上找 go.mod 找到仓库根。
func gatewayRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败：%v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("从工作目录往上找不到 go.mod ⇒ 定位不到仓库根")
	return ""
}

// loadMemoraManifest 读 vendored 的路由清单。
func loadMemoraManifest(t *testing.T) memoraRouteManifest {
	t.Helper()
	path := filepath.Join(gatewayRepoRoot(t), "contracts", "memora-routes.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 vendored 路由清单失败 %s：%v（它由 memora 仓的 "+
			"`go run ./cmd/routes -out contracts/routes.json` 产出）", path, err)
	}
	var m memoraRouteManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("解析 vendored 路由清单失败：%v", err)
	}
	if len(m.Routes) == 0 {
		t.Fatalf("前提不成立：vendored 路由清单是空的 ⇒ 「路径在清单内」会恒真")
	}
	return m
}

// manifestHas 清单里是否有这条（method + path）。
func (m memoraRouteManifest) manifestHas(method, path string) bool {
	for _, r := range m.Routes {
		if strings.EqualFold(r.Method, method) && r.Path == path {
			return true
		}
	}
	return false
}

// TestClientDefaultRoutesAreInMemoraRouteManifest 是 G-1 判据的正题：
// 客户端默认使用的路径必须在 memora 的路由清单里，method 也要对。
//
// ★ 判据的**强度**说明：它比「路径字符串相等」强 ——
//
//	memora 的 `/api/add` 与 `/api/memory/add` 是**两条不同的路由**，
//	客户端打错其中一条 ⇒ 404；而 method 错 ⇒ 405。
//	⇒ 门比「路径对」多钉一层「method 对」。
func TestClientDefaultRoutesAreInMemoraRouteManifest(t *testing.T) {
	m := loadMemoraManifest(t)
	c := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1"})

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"add", "POST", c.AddPath()},
		{"search", "POST", c.SearchPath()},
		// health 路径没有导出访问器（客户端只在 Ping 里用它）⇒ 直接取常量。
		{"health", "GET", DefaultHealthPath},
	}
	for _, tc := range cases {
		if !m.manifestHas(tc.method, tc.path) {
			t.Errorf("客户端默认的 %s 路径 %s %s **不在** memora 路由清单里 ⇒ "+
				"这条调用会 404/405（清单来源：%s）",
				tc.name, tc.method, tc.path, m.Source)
		}
	}
}

// TestMemOSLegacyPathsAreNotInMemoraRouteManifest 钉住「回退路径不是 memora 的路径」。
//
// MemOS 旧路径只允许作为**显式 opt-in** 的回退存在（见 LegacyMemOSPathsEnv）。
// 若它们出现在 memora 的路由清单里，说明清单或注册被改成了另一套东西
// ⇒ 客户端的「旧路径是回退」这个前提被打破。
func TestMemOSLegacyPathsAreNotInMemoraRouteManifest(t *testing.T) {
	m := loadMemoraManifest(t)
	for _, p := range []string{LegacyMemOSAddPath, LegacyMemOSSearchPath} {
		for _, r := range m.Routes {
			if r.Path == p {
				t.Errorf("memora 路由清单里出现了 MemOS 旧路径 %s（%s）⇒ "+
					"「旧路径只是 opt-in 回退」的前提被打破", p, r.Method)
			}
		}
	}
}

// TestMemoraRouteManifestCopyIsInSyncWithMemoraCheckout 是**真正的那一半**：
// vendored 副本必须与 memora 仓里的原副本**逐字一致**。
//
// 没有这一条，vendored 副本就是「又一份手抄」⇒ memora 改路由后、
// 没人同步副本，这道门照样绿 ⇒ 契约又回到「靠注释维持」。
//
// ⚠ 需要能看到 memora 的 checkout（`MEMORA_REPO_PATH` 或兄弟目录）。
//
//	找不到 ⇒ **显式 skip 并说明**（不冒充「已核对」）。
func TestMemoraRouteManifestCopyIsInSyncWithMemoraCheckout(t *testing.T) {
	root := gatewayRepoRoot(t)
	vendored := filepath.Join(root, "contracts", "memora-routes.json")

	var upstream string
	if env := strings.TrimSpace(os.Getenv("MEMORA_REPO_PATH")); env != "" {
		upstream = filepath.Join(env, "contracts", "routes.json")
	} else {
		upstream = filepath.Join(root, "..", "..", "memora", "contracts", "routes.json")
	}
	upstreamData, err := os.ReadFile(upstream)
	if err != nil {
		t.Skipf("看不到 memora 的 checkout（%s：%v）⇒ 本条「双向核对」**未执行**；"+
			"设 MEMORA_REPO_PATH 可让它真跑。前两条断言（客户端路径 ⊆ 清单）不受影响。",
			upstream, err)
	}
	vendoredData, err := os.ReadFile(vendored)
	if err != nil {
		t.Fatalf("读 vendored 副本失败：%v", err)
	}
	if string(upstreamData) != string(vendoredData) {
		t.Fatalf("vendored 副本与 memora 仓的原副本**不一致** ⇒ 契约已经漂移：\n"+
			"  vendored：%s\n  memora  ：%s\n"+
			"  ⇒ 修法：在 memora 仓重新生成（go run ./cmd/routes -out contracts/routes.json）"+
			"并把新副本拷到本仓的 contracts/memora-routes.json",
			vendored, upstream)
	}
}
