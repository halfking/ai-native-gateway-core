package executors

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// 降级模式请求总数
	fpSlotDegradedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_fp_slot_degraded_total",
			Help: "Total requests running in FpSlot degraded mode",
		},
		[]string{"model", "reason"},
	)

	// 降级模式请求占比（1分钟滑动窗口）
	fpSlotDegradationRatio = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llmgw_fp_slot_degradation_ratio",
			Help: "Ratio of requests in degraded mode (last 1 minute)",
		},
		[]string{"model"},
	)

	// 凭据 FpSlot 饱和度
	fpSlotSaturationRatio = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llmgw_fp_slot_saturation_ratio",
			Help: "FpSlot saturation ratio per credential",
		},
		[]string{"credential_id"},
	)
)

// DegradationTracker 跟踪降级模式统计
type DegradationTracker struct {
	mu         sync.RWMutex
	windows    map[string]*slidingWindow
	windowSize time.Duration
}

type slidingWindow struct {
	total    int64
	degraded int64
	resetAt  time.Time
}

func NewDegradationTracker() *DegradationTracker {
	return &DegradationTracker{
		windows:    make(map[string]*slidingWindow),
		windowSize: 1 * time.Minute,
	}
}

// RecordRequest 记录请求（是否降级）
func (dt *DegradationTracker) RecordRequest(model string, degraded bool) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	now := time.Now()
	window, exists := dt.windows[model]

	if !exists || now.After(window.resetAt) {
		// 创建新窗口
		window = &slidingWindow{
			total:    0,
			degraded: 0,
			resetAt:  now.Add(dt.windowSize),
		}
		dt.windows[model] = window
	}

	window.total++
	if degraded {
		window.degraded++
		fpSlotDegradedTotal.WithLabelValues(model, "all_saturated").Inc()
	}

	// 更新降级率指标
	ratio := float64(window.degraded) / float64(window.total)
	fpSlotDegradationRatio.WithLabelValues(model).Set(ratio)
}

// GetDegradationRatio 获取当前降级率
func (dt *DegradationTracker) GetDegradationRatio(model string) float64 {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	window, exists := dt.windows[model]
	if !exists || window.total == 0 {
		return 0.0
	}

	return float64(window.degraded) / float64(window.total)
}
