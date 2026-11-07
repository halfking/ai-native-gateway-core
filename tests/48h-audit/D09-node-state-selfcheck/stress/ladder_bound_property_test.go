// stress/ladder_bound_property_test.go
//
// D09 · stress —— 梯子有界性性质测试（2026-11-07 探测时间点规划轮）。
//
// S-01 节约性性质：持续失败的行在 1..200 attempt 全域上，任何一次失败类的
//      下一跳都不得超过「恢复 SLA ∪ 配额固定节奏 ∪ 长停放档」构成的离散档位集，
//      不得出现无界指数膨胀 —— 「用尽期探测打爆」的回归钉（F4 前
//      rate_limit 族曾以 3~5m 节奏贯穿整个周窗口，本判据配合恢复 SLA 锁住上界）。
//
// 跑测：
//   go test -race -timeout 60s ./tests/48h-audit/D09-node-state-selfcheck/stress/...

package stress

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestStress_LadderBoundedAcrossAttempts(t *testing.T) {
	const sla = 300 * time.Second
	// 长停放档位（有意为之的稀疏复检，不算膨胀）：404/协议 6h、gateway-side 15m。
	parks := map[time.Duration]bool{
		6 * time.Hour:    true,
		15 * time.Minute: true,
		5 * time.Minute:  true, // quota periodic 固定档
		2 * time.Minute:  true, // quota balance/permanent 固定档
		3 * time.Minute:  true, // rate-limit policy floor
		// 2026-11-07 持续失败升级档：attempt ≥ sustained 阈值的稀疏复检
		//（默认 1h；有界、非膨胀 —— 见 business B-01 两档契约）。
		bg.SustainedFailureRecheck(): true,
	}
	kinds := []errorsx.ErrorKind{
		errorsx.KindTransient, errorsx.KindTimeout, errorsx.KindNetwork,
		errorsx.KindUpstreamDown, errorsx.KindStreamTimeout, errorsx.KindUpstreamOverloaded,
		errorsx.KindEmptyResponse, errorsx.KindUpstreamContextLoss,
		errorsx.KindRateLimit, errorsx.KindConcurrent,
		errorsx.KindQuota, errorsx.KindQuotaPeriodic, errorsx.KindQuotaBalance, errorsx.KindQuotaPermanent,
	}
	for _, kind := range kinds {
		var maxD time.Duration
		for attempt := 1; attempt <= 200; attempt++ {
			d := bg.ProbeBackoffForKind(kind, attempt)
			if d <= 0 {
				t.Fatalf("kind=%s attempt=%d 非正间隔 %v", kind, attempt, d)
			}
			if d > sla && !parks[d] {
				t.Fatalf("kind=%s attempt=%d 间隔 %v 超出恢复 SLA 且不是登记过的长停放/固定档 —— 梯子无界膨胀", kind, attempt, d)
			}
			if d > maxD {
				maxD = d
			}
		}
		t.Logf("kind=%s max_interval_over_200_attempts=%v", kind, maxD)
	}
}
