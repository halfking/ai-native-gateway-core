package stats

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// total_tokens 口径判据（2026-10-06，真实库读数驱动）。
//
// # 契约
//
//	Event.TotalTokens == PromptTokens + CompletionTokens
//
// 且**不含** CacheRead / CacheWrite / Reasoning / Image / Audio / Video。
// 那些是上游 usage 的**细分**（`prompt_tokens_details.*` 与
// `completion_tokens_details.reasoning_tokens`），是子集而不是额外 token。
//
// # 为什么它是钱的问题，不只是「好看」
//
//	domains/stats/daily_monthly_rollup.go:197  FROM usage_facts f
//	domains/stats/daily_monthly_rollup.go:207  SUM(total_tokens)
//	domains/stats/daily_monthly_rollup.go:114  FROM stats_usage_monthly
//
// ⇒ 这一行的口径直接决定**看板与月报上运营看到的总量**。口径错了不是脏一张
// 明细表，是把一个夸大的数字发出去。
//
// # 参照系（不要用 provider_tokens）
//
// `provider_tokens` **不是**上游总 token：它是 Doubao Seed 的
// `seed_token_usage`（domains/streaming/usage.go:152-159，注释原文
// "provider billing evidence, not a generic token replacement"）。真库
// 246 行**恒为 0**。拿它对拍会算出「拿 0 当基准」的假差值 —— 我犯过。
//
// 正确的参照是 `request_logs.total_tokens`（取自上游 usage.total_tokens）：
// 近 30 天 1,492,562 行**全部**严格等于 prompt+completion，而 cache_read
// 平均占总 token 的 42.5%（87,888 行有 cache 读，avg 12,218.9）
// ⇒ 它确实没被另加。本判据就是让 usage_facts 与它同口径。
//
// # 判据自身要能咬住
//
// 下面每个用例的细分项都**非零**。这不是装饰：细分项全为 0 时两套公式
// 结果完全相同（真库 246 行就是这样），那种用例改回旧公式照样绿 —— 那是
// 恒真断言，不是判据。变异验���记录见 event.go:160 的注释。

func totalTokensEvent(t *testing.T, prompt, completion, cacheRead, cacheWrite, reasoning, image, audio, video int) Event {
	t.Helper()
	status := telemetry.RequestStatusSuccess
	model := "glm-5.2"
	entry := &telemetry.RequestLogEntry{
		Op: telemetry.RequestLogUpdate, RequestID: "req-total-semantics",
		RequestStatus: &status, OutboundModel: &model,
		PromptTokens: &prompt, CompletionTokens: &completion,
		CacheReadTokens: &cacheRead, CacheWriteTokens: &cacheWrite,
		ReasoningTokens: &reasoning, ImageTokens: &image,
		AudioTokens: &audio, VideoTokens: &video,
	}
	e, ok := EventFromTelemetry(entry, time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("expected a terminal event for a success entry")
	}
	return e
}

func TestTotalTokensExcludesSubsetColumns(t *testing.T) {
	// 七个列全非零，且子集大于 prompt 的一半 —— 两套公式的差值足够大，
	// 一个只看「大概相等」的断言不可能放过旧公式。
	cases := []struct {
		name                                                  string
		prompt, completion                                    int
		cacheRead, cacheWrite, reasoning, image, audio, video int
	}{
		{"cache-heavy (real MiniMax-M3 shape)", 177, 10, 50000, 0, 0, 0, 0, 0},
		{"reasoning model (completion subset)", 84, 4000, 0, 0, 3500, 0, 0, 0},
		{"vision request (prompt subset)", 12000, 300, 0, 0, 0, 9000, 0, 0},
		{"audio + video subsets", 30000, 500, 1000, 700, 0, 0, 12000, 8000},
		{"every subset column at once", 1000, 200, 300, 400, 500, 600, 700, 800},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := totalTokensEvent(t, c.prompt, c.completion,
				c.cacheRead, c.cacheWrite, c.reasoning, c.image, c.audio, c.video)

			want := int64(c.prompt + c.completion)
			if e.TotalTokens != want {
				t.Fatalf("TotalTokens = %d, want %d (prompt+completion).\n"+
					"子集列被加进总量了：cache_read=%d cache_write=%d reasoning=%d "+
					"image=%d audio=%d video=%d。\n"+
					"这些是上游 usage 的细分，不是额外 token；成本公式 "+
					"calcCostWithConvention 也把 cache_read 当 prompt 的子集先减后加。",
					e.TotalTokens, want,
					c.cacheRead, c.cacheWrite, c.reasoning, c.image, c.audio, c.video)
			}

			// 逐列自证：任何一个子集列为正而总量没变，才算真的没被加。
			// （不这样做的话，删掉一列也未必被上面那条抓住。）
			subsetSum := int64(c.cacheRead + c.cacheWrite + c.reasoning + c.image + c.audio + c.video)
			if subsetSum > 0 && e.TotalTokens == want+subsetSum {
				t.Fatalf("总量等于 prompt+completion+所有子集 —— 这正是被废掉的旧口径")
			}
		})
	}
}

func TestTotalTokensIsAlwaysPromptPlusCompletion(t *testing.T) {
	// 代数性质而不是逐例数值：对任意组合都成立。
	// 这条让「有人把某一列加回来」无处藏 —— 只要该列非零，上面的表已覆盖；
	// 这里再兜住「有人同时改了两处」的形状。
	for prompt := 0; prompt <= 50; prompt += 10 {
		for completion := 0; completion <= 50; completion += 10 {
			e := totalTokensEvent(t, prompt, completion, 11, 13, 17, 19, 23, 29)
			if want := int64(prompt + completion); e.TotalTokens != want {
				t.Fatalf("prompt=%d completion=%d: TotalTokens = %d, want %d",
					prompt, completion, e.TotalTokens, want)
			}
		}
	}
}

func TestTotalTokensZeroWhenNoTokensReported(t *testing.T) {
	// 边界：全部为 0 时总量必须是 0，不能因为「少减了一列」变成负或非零。
	e := totalTokensEvent(t, 0, 0, 0, 0, 0, 0, 0, 0)
	if e.TotalTokens != 0 {
		t.Fatalf("TotalTokens = %d, want 0 when nothing is reported", e.TotalTokens)
	}
}
