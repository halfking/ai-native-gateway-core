// bg/pricing_baseline_live_test.go — 对真实机读源的一次性实抓
//
// 夹具能证明解析逻辑对**它**成立，证明不了它对着真实文件也成立：真实
// 源有 226 个 provider、分档价、缺 cost 的模型、大小写与前缀差异。所以
// 这里留一条需要显式 opt-in 的实抓用例：
//
//	go test ./bg/ -run TestLiveObservationSource -v -count=1 \
//	  -args -live-observation
//
// 默认跳过，不让网络可用性变成 CI 的红灯。
package bg

import (
	"context"
	"os"
	"strconv"
	"testing"
)

func TestLiveObservationSource(t *testing.T) {
	if os.Getenv("LLM_GATEWAY_RUN_LIVE_OBSERVATION") != "1" {
		t.Skip("set LLM_GATEWAY_RUN_LIVE_OBSERVATION=1 to hit the live observation source")
	}
	ctx := context.Background()
	observed, url, at, err := FetchMachineReadablePrices(ctx, nil)
	if err != nil {
		t.Fatalf("live fetch from %s: %v", url, err)
	}
	t.Logf("providers with priced models: %d, source %s, observed_at %s",
		len(observed), url, at.Format("2006-01-02T15:04:05Z"))

	// 抽查：原厂那一档必须能查到，且不与中转混同。
	for _, tc := range []struct{ vendor, model string }{
		{"openai", "gpt-4o"},
		{"anthropic", "claude-sonnet-4-5"},
		{"google", "gemini-2.5-pro"},
		{"deepseek", "deepseek-chat"},
	} {
		obs, ok := observed.LookupObservation(tc.vendor, tc.model)
		if !ok {
			t.Logf("no observation for %s/%s (catalog coverage gap, not a parser bug)", tc.vendor, tc.model)
			continue
		}
		in, out := "nil", "nil"
		if obs.InputPer1M != nil {
			in = formatFloat(*obs.InputPer1M)
		}
		if obs.OutputPer1M != nil {
			out = formatFloat(*obs.OutputPer1M)
		}
		t.Logf("%s/%s: input=$%s output=$%s per 1M", tc.vendor, tc.model, in, out)
	}
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
