package settings

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// CNYToUSDFX 是 CNY→USD 汇率的【记账锚点】（billing anchor）：
// domains/streaming/usage.go 的 KPI 折算用同值。P1-C（audit 2026-10-28 §4）
// 把它上收为本包常量，路由评分侧经 CNYScoreFX() 读取 —— 默认与记账同值，
// 两侧天然同尺；换汇率时 env 一次配置、两边口径显式分工。
const CNYToUSDFX = 7.2

var (
	cnyFXOnce  sync.Once
	cnyFXValue float64
)

// CNYScoreFX 返回路由成本评分用的 CNY→USD 汇率（P1-C 汇率配置化）。
// env LLM_GATEWAY_CNY_TO_USD_FX 优先；缺失/非法回落记账锚点 CNYToUSDFX。
// 读一次进程内固化：汇率中途翻转会让「翻转前后评出的候选」口径不可比
// —— 比汇率略旧更糟，所以宁可陈旧、不可漂移。记账侧（usage.go 的
// AssignRequestCost）继续用常量本值，算术零变化。
func CNYScoreFX() float64 {
	cnyFXOnce.Do(func() {
		v := CNYToUSDFX
		if s := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CNY_TO_USD_FX")); s != "" {
			if parsed, err := strconv.ParseFloat(s, 64); err == nil && parsed > 0 {
				v = parsed
			}
		}
		cnyFXValue = v
	})
	return cnyFXValue
}
