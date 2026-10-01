package sensitive

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// R71 回归：Match 过去在每次命中时重算前缀字节长度
// （len([]byte(string(runes[:i+1])))，是 O(i) 的完整拷贝），整体退化为
// O(n²)。检测跑在同步的 governance 路径上，于是「让文本很容易命中敏感词」
// 本身就成了一种单请求 CPU 耗尽手段。
func TestMatch_ScalingIsLinear(t *testing.T) {
	eng := buildR71Engine(t, map[string][]string{"w": {"自由"}})
	const unit = "自由"
	mk := func(kb int) string { return strings.Repeat(unit, kb*1024/len(unit)) }

	// Warm up first: the first call pays for cold caches, which alone would
	// look like super-linear scaling.
	for i := 0; i < 3; i++ {
		eng.Match(mk(8))
	}

	bestOf := func(kb int) time.Duration {
		best := time.Duration(math.MaxInt64)
		for r := 0; r < 3; r++ {
			t0 := time.Now()
			eng.Match(mk(kb))
			if d := time.Since(t0); d < best {
				best = d
			}
		}
		return best
	}

	// 为什么不能「32KB 测完再测 64KB，只取各自 best」：两个尺寸落在时间轴的
	// 两端，一次 GC 或一次调度抢占只会污染其中一边，ratio 就被噪声顶穿。实测
	// 同一份二进制、单独跑同一个包，ratio 在 2.04~4.70 之间跳（阈值 2.6）——
	// 已经越过「二次方≈4.0」，说明这条门量的根本不是算法而是当时的机器负载。
	//
	// 改成**交替**采样并对多轮取最小 ratio：负载扰动同时命中两个尺寸，best 只
	// 会被噪声抬高不会被压低，而干扰只会让 ratio 变大，所以多轮的最小值就是
	// 「最接近真实伸缩」的估计。
	ratio := math.Inf(1)
	var d32, d64 time.Duration
	for round := 0; round < 6; round++ {
		a, b := bestOf(32), bestOf(64)
		if r := float64(b) / float64(a); r < ratio {
			ratio, d32, d64 = r, a, b
		}
	}
	// Match 除了 O(n) 的前缀表，每个命中还要写一次去重 map，最后 sortResults
	// 是 O(n log n) —— 所以真实伸缩是 n log n，比率落在 2.2 附近而不是 2.0。
	//
	// 阈值 3.0 的判别力是实测过的，不是估的：
	//   - 现状（O(n) 前缀表）：ratio 稳定在 2.25~2.44（8 次独立运行）
	//   - 变异（把 begin 退回逐命中的 len([]byte(string(runes[:i+1-k])))）：
	//     32KB=133.7ms 64KB=488.2ms ratio=3.65 → 本门红
	// 3.0 落在 2.44 与 3.65 之间的空档里，两侧都有余量。
	t.Logf("32KB=%v 64KB=%v ratio=%.2f (n log n≈2.2, quadratic≈4.0)", d32, d64, ratio)
	require.Less(t, ratio, 3.0,
		"Match scaled super-linearly: 32KB→64KB ratio %.2f", ratio)
}

func TestMatch_LargeBodyStaysCheap(t *testing.T) {
	eng := buildR71Engine(t, map[string][]string{"w": {"恐怖主义"}})
	const unit = "恐怖主义恐怖主义恐怖主义"
	text := strings.Repeat(unit, 256*1024/len(unit))

	best := time.Duration(0)
	for r := 0; r < 3; r++ {
		t0 := time.Now()
		eng.Match(text)
		if d := time.Since(t0); best == 0 || d < best {
			best = d
		}
	}
	// Before the fix this exact input took 937ms (O(n^2) in the hit count, via
	// a per-hit prefix recomputation AND an O(m^2) selection sort). 300ms
	// leaves generous headroom while still failing loudly on any regression
	// toward super-linear behaviour.
	t.Logf("256KB heavily-matching body: %v", best)
	require.Less(t, best, 300*time.Millisecond,
		"256KB of a heavily-matching text took %v — super-linear cost is back", best)
}

// R71 回归：空词会让 AC 自动机在 root 上产生 Begin == End 的片段，
// 违反 0 <= Begin < End <= len(text) 这条所有按位置消费的调用方都依赖的不变式。
func TestMatch_EmptyRuleNeverYieldsZeroLengthSpan(t *testing.T) {
	eng := NewSensitiveWordEngine()
	require.NoError(t, eng.Build(&SensitiveWordConfig{Categories: map[string]CategoryConf{
		"political": {Name: "政治", Words: []string{"政变", "", "   "}},
	}}))
	require.Equal(t, 1, eng.LoadedWordCount(), "blank rules must not be counted as words")

	for _, m := range eng.Match("今天讨论政变") {
		require.Greater(t, m.Begin, 0, "Begin must be positive: %+v", m)
		require.LessOrEqual(t, m.End, len("今天讨论政变"), "End must be inside the text: %+v", m)
	}
}

// R71 回归：configPath 曾无锁写入，而 WatchConfig 的轮询 goroutine 与
// admin 的 ReloadFromFile handler 并发读写它（-race 可复现）。
func TestConcurrentReloadAndMatch_NoRace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "words.json")
	writeR71Config(t, path, []string{"恐怖主义", "政变"})

	eng := NewSensitiveWordEngine()
	require.NoError(t, eng.BuildFromFile(path))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		_ = eng.WatchConfig(ctx, 2*time.Millisecond)
	}()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 30; n++ {
				switch n % 3 {
				case 0:
					_ = eng.ReloadFromFile()
				case 1:
					writeR71Config(t, path, []string{"恐怖主义", "政变", "racing"})
				default:
					_ = eng.Match("我们在讨论恐怖主义和政变")
				}
				_ = eng.GetHighestAlertLevel(eng.Match("恐怖主义"))
			}
		}(i)
	}
	wg.Wait()
	cancel()
	<-watchDone
}

// R71 回归：LevelP2=0 < LevelP1=1 < LevelP0=2（数值越大越严重），但
// EvaluateSafety 与 GetHighestAlertLevel 都用 `< highest` 去找「最严重」，
// 于是 Category 恒为 "mixed"、GetHighestAlertLevel 恒返回 P2(PASS)。
func TestSeveritySelectionPicksTheMostSevere(t *testing.T) {
	eng := buildR71Engine(t, map[string][]string{
		"political": {"政变"},
		"terrorism": {"恐怖主义"},
	})

	matches := eng.Match("政变和恐怖主义")
	require.NotEmpty(t, matches)
	require.Equal(t, LevelP0, eng.GetHighestAlertLevel(matches),
		"a BLOCK-level hit must not be reported as PASS")

	// 只有 P1 类目时，最高级别必须是 P1（修复前会掉到 P2/PASS）。
	eng2 := buildR71Engine(t, map[string][]string{
		"political":       {"政变"},
		"financial_crime": {"洗钱"},
	})
	require.Equal(t, LevelP1, eng2.GetHighestAlertLevel(eng2.Match("政变洗钱")))

	res := eng.EvaluateSafety("政变和恐怖主义")
	require.NotEqual(t, "mixed", res.Category,
		"a non-empty match set must not collapse to the \"mixed\" placeholder")
}

func TestGetHighestAlertLevel_P1OnlyIsNotDowngradedToPass(t *testing.T) {
	eng := buildR71Engine(t, map[string][]string{"political": {"政变"}})
	require.Equal(t, LevelP1, eng.GetHighestAlertLevel(eng.Match("政变")))
	require.Equal(t, LevelP2, eng.GetHighestAlertLevel(nil))
}

// 字节偏移语义（本次审计中反复被怀疑的一处）必须保持正确。
func TestMatch_ByteOffsetsAreExactForMultibyteText(t *testing.T) {
	eng := buildR71Engine(t, map[string][]string{"terrorism": {"恐怖主义"}})
	const text = "我要讨论政治和恐怖主义与人权"
	for _, m := range eng.Match(text) {
		require.Equal(t, "恐怖主义", text[m.Begin:m.End], "byte offsets must slice the original text")
	}
}

func buildR71Engine(t *testing.T, cats map[string][]string) *SensitiveWordEngine {
	t.Helper()
	eng := NewSensitiveWordEngine()
	cfg := &SensitiveWordConfig{Categories: map[string]CategoryConf{}}
	for key, words := range cats {
		cfg.Categories[key] = CategoryConf{Name: key, Words: words}
	}
	require.NoError(t, eng.Build(cfg))
	return eng
}

func writeR71Config(t *testing.T, path string, words []string) {
	t.Helper()
	content := `{"categories":{"terrorism":{"name":"恐怖主义","words":[` +
		quoteJoin(words) + `]}}}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func quoteJoin(words []string) string {
	parts := make([]string, 0, len(words))
	for _, w := range words {
		parts = append(parts, `"`+w+`"`)
	}
	return strings.Join(parts, ",")
}

func init() { runtime.GOMAXPROCS(runtime.NumCPU()) }
