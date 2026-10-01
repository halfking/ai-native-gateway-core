package sensitive

import (
	"context"
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
//
// 判据用「分配字节数的比值」而不是墙钟。墙钟版在 32KB/64KB 这种亚毫秒量级上
// 量的根本是机器负载而不是算法：同一份二进制、单独跑同一个包，墙钟 ratio 在
// 2.04~4.70 之间跳（旧阈值 2.6），已经越过「二次方≈4.0」；即便改成交替采样 +
// 多轮取最小值，在 14 个 CPU hog 压满的机器上仍测到 1.28~2.74，而「取最小」
// 本身会**系统性低估** ratio（两个尺寸各自挑走最走运的一次采样），也就是可能
// 把真回归一起抹掉。那道墙在这里拆不掉。
//
// 分配字节数与负载无关，且精确对应被测性质：命中数与输入长度成正比，旧写法每次
// 命中拷贝整个前缀 ⇒ 分配**总量** O(n²)；修复后每次命中 O(1) 查表 ⇒ 总量 O(n)。
// 倍数直接落在 2 / 4 上，不是估的。墙钟那道绝对成本门（TestMatch_LargeBodyStaysCheap，
// 300ms 预算对 ~10ms 实测）仍然保留，两条门各测各的。
func TestMatch_ScalingIsLinear(t *testing.T) {
	eng := buildR71Engine(t, map[string][]string{"w": {"自由"}})
	const unit = "自由"
	mk := func(kb int) string { return strings.Repeat(unit, kb*1024/len(unit)) }

	// Warm up first: the first call pays for the rule table's lazy init, and
	// both sample strings are built outside the measured region.
	for i := 0; i < 3; i++ {
		eng.Match(mk(8))
	}

	allocBytes := func(f func()) uint64 {
		runtime.GC()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		f()
		runtime.ReadMemStats(&m1)
		return m1.TotalAlloc - m0.TotalAlloc
	}

	in32, in64 := mk(32), mk(64)
	b32 := allocBytes(func() { eng.Match(in32) })
	b64 := allocBytes(func() { eng.Match(in64) })
	require.NotZero(t, b32, "32KB 基准样本没有分配任何字节，比值失去意义")

	ratio := float64(b64) / float64(b32)
	// 阈值 3.0 的判别力是实测的：
	//   - 现状：32KB=994,608B 64KB=1,982,128B ratio=1.9929（三次重复逐字节相同）
	//   - 变异（begin 退回逐命中的 O(i) 前缀拷贝）：ratio=4.0754
	//     （32KB=94,695,144B 64KB=385,908,904B，即二次方的 4×）
	// 3.0 落在 1.99 与 4.08 正中间，两侧余量都很大，且完全不受机器负载影响。
	t.Logf("32KB=%dB 64KB=%dB allocRatio=%.4f (linear≈2, quadratic≈4)", b32, b64, ratio)
	require.Less(t, ratio, 3.0,
		"Match 的每命中代价不是 O(1)：32KB→64KB 分配字节比值 %.4f（超过 3 说明前缀被重复拷贝，O(n²) 回来了）", ratio)
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
