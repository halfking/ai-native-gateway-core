package bg

// 2026-11-09 探针对齐限流窗口（429 闪断族）· 判据面
//
// 背景（211 实测）：kimi-k3/glm-5.2/5.3 族紧 RPM 桶业务 429 冷却 60s（W1 节奏
// 校准），但探针侧＝ladder 3m 固定档 + 绑定冷却 5min ⇒ ① 复检探针要等 3 分钟
// 梯底才跑、绑定被多关 2 分钟；② 探针 429 写的 5min 冷却会覆盖业务侧 60s 冷却。
//
// 本文件钉住：
//   R1 探针梯 rate 族首跳 = 窗口（60s 默认），非 3m 固定档；更高梯级不动；
//   R2 绑定冷却 rate 族 = 窗口（60s），非 5min；非 rate 族 5min 不变；
//   R3 窗口口径关闭（0）⇒ 逐字节回 3m/5min（A2/旧口径）；
//   R4 持续失败档仍压过窗口档（风暴期盲探放慢）。

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func withRateWindow(t *testing.T, seconds int) {
	t.Helper()
	prev := rateWindowRefillSecondsFn
	rateWindowRefillSecondsFn = func() int { return seconds }
	t.Cleanup(func() { rateWindowRefillSecondsFn = prev })
}

// R1：rate 族首几跳对齐窗口；attempt 走到通用梯高位后按既有梯级（5m 封顶、
// sustained 升档），窗口只定「最早有用时刻」。
func TestProbeRateBackoffAlignsWithWindow(t *testing.T) {
	withRateWindow(t, 60)

	for _, attempt := range []int{1, 2, 3} {
		if got := ProbeBackoffForKind(errorsx.KindRateLimit, attempt); got != 60*time.Second {
			t.Errorf("rate attempt %d = %v, want window 60s（替换 3m 固定档）", attempt, got)
		}
	}
	// attempt 4+: 通用梯 5m > 3m 固定档 ⇒ 不受窗口替换影响（封顶 SLA=5m）。
	if got := ProbeBackoffForKind(errorsx.KindRateLimit, 4); got != 5*time.Minute {
		t.Errorf("rate attempt 4 = %v, want generic rung 5m", got)
	}
	// sustained 档压过窗口（R4）。
	threshold := sustainedFailureAttemptThreshold()
	if got := ProbeBackoffForKind(errorsx.KindRateLimit, threshold); got != sustainedFailureRecheck() {
		t.Errorf("rate sustained attempt %d = %v, want %v", threshold, got, sustainedFailureRecheck())
	}
	// 大窗口（operator 调大）：低于窗口的梯级抬到窗口。
	withRateWindow(t, 300)
	if got := ProbeBackoffForKind(errorsx.KindRateLimit, 1); got != 5*time.Minute {
		t.Errorf("rate attempt 1 with 300s window = %v, want 5m", got)
	}
}

// R2：绑定冷却 rate 族 = 窗口；非 rate 族保持 5min；sustained 档仍最高优先。
func TestUnavailableBindingHorizonRateWindow(t *testing.T) {
	withRateWindow(t, 60)

	_, h := unavailableBindingHorizon("http_429", 3)
	if h != 60*time.Second {
		t.Errorf("http_429 horizon = %v, want window 60s", h)
	}
	_, h = unavailableBindingHorizon("rate_limit", 1)
	if h != 60*time.Second {
		t.Errorf("rate_limit horizon = %v, want window 60s", h)
	}
	_, h = unavailableBindingHorizon("http_503", 3)
	if h != 5*time.Minute {
		t.Errorf("http_503 horizon = %v, want unchanged 5m", h)
	}
	threshold := sustainedFailureAttemptThreshold()
	_, h = unavailableBindingHorizon("http_429", threshold)
	if h != sustainedFailureRecheck() {
		t.Errorf("sustained http_429 horizon = %v, want %v", h, sustainedFailureRecheck())
	}
}

// R3：窗口口径关闭 ⇒ 逐字节回旧口径（3m 固定档 / 5min 绑定冷却）。
func TestRateWindowOffFallsBackToLegacy(t *testing.T) {
	withRateWindow(t, 0)

	if got := ProbeBackoffForKind(errorsx.KindRateLimit, 1); got != 3*time.Minute {
		t.Errorf("rate attempt 1 (window off) = %v, want legacy 3m fixed", got)
	}
	_, h := unavailableBindingHorizon("http_429", 3)
	if h != 5*time.Minute {
		t.Errorf("http_429 horizon (window off) = %v, want legacy 5m", h)
	}
}
