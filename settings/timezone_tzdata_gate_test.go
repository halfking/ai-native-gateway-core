package settings

import (
	"os"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// tzdata 内嵌判据（不依赖环境）：治理「unknown time zone Asia/Shanghai」。
// 背景：Alpine 运行时镜像无 tzdata 包，宿主机未挂载 /usr/share/zoneinfo 时
// time.LoadLocation 失败 ⇒ SystemTimezoneLocation 静默回退 UTC ⇒ 租户看板/
// 统计面时间聚合漂移（211 实录，docs/operations/2026-11-23-211配置优化执行报告.md:39）。
// 修法＝cmd/gateway 内嵌 _ "time/tzdata"；部署脚本挂载宿主 zoneinfo 只是缓解。
// team_budget_gate 教训：治本手段丢了 ⇒ 缓解失效的环境原样复发，且没有任何门红。
// ─────────────────────────────────────────────────────────────

// TestGatewayBinaryEmbedsTzdata：网关二进制入口必须 import _ "time/tzdata"。
// 源码锚判据——导入被摘除 ⇒ 本判据当场红（无系统 tzdata 的环境即复发）。
func TestGatewayBinaryEmbedsTzdata(t *testing.T) {
	src, err := os.ReadFile("../cmd/gateway/main.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/main.go: %v", err)
	}
	if !strings.Contains(string(src), `"time/tzdata"`) {
		t.Error("cmd/gateway/main.go must embed tzdata via _ \"time/tzdata\" import " +
			"(removed ⇒ LoadLocation fails on tzdata-less runtimes ⇒ dashboard aggregates silently drift to UTC)")
	}
}

// TestTimezoneFallbackChainSemantic：经测试注入钩子钉回退链语义——
// 有效 IANA 名逐字生效；不可加载名不 panic、不返回 nil（回退链兜底）。
// 本测试串行执行：钩子是包级全局，不与并行用例混跑。
func TestTimezoneFallbackChainSemantic(t *testing.T) {
	// 前提：真 IANA 名在测试进程内必须可加载（CI/开发机带系统 tzdata）。
	if _, err := time.LoadLocation("Asia/Shanghai"); err != nil {
		t.Fatalf("test env must resolve Asia/Shanghai (system tzdata expected): %v", err)
	}

	SystemTimezoneLocationForTest("Asia/Shanghai")
	defer SystemTimezoneLocationForTest("")
	if got := SystemTimezoneLocation().String(); got != "Asia/Shanghai" {
		t.Errorf("valid IANA name must take effect verbatim, got %s", got)
	}

	SystemTimezoneLocationForTest("Mars/Olympus_Mons")
	if loc := SystemTimezoneLocation(); loc == nil {
		t.Error("unloadable name must fall back to a non-nil location, never nil")
	}
}
