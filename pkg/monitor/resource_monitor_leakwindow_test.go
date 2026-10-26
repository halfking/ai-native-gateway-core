package monitor

import (
	"testing"
	"time"
)

// 本组判据锁的是「趋势窗口」语义，来源是生产实测：
// 245 上 resource_monitor.log 22645 行里有 6646 条 memory_leak_suspected（占 30%），
// 而最长跑的进程 14.7 小时内 RSS 始终在 181~293MB 之间震荡、无趋势。
// 即：这些告警全是 GC 锯齿被当成增长率，不是泄漏。
//
// 每条判据都必须能被变异打红，否则它不构成保护（见 AGENTS.md 判据纪律）。

func newLeakTestMonitor(t *testing.T, window time.Duration, memThreshold, fdThreshold float64) *ResourceMonitor {
	t.Helper()
	rm, err := NewResourceMonitor(ResourceMonitorConfig{
		LogDir:             t.TempDir(),
		Interval:           time.Second,
		InstanceID:         "leak-window-test",
		MemGrowthThreshold: memThreshold,
		FDGrowthThreshold:  fdThreshold,
		GoroutineThreshold: 1 << 30, // 关掉 goroutine 判据，只测内存/FD
		LeakWindow:         window,
	})
	if err != nil {
		t.Fatalf("NewResourceMonitor: %v", err)
	}
	return rm
}

// feed 按固定步长灌入一串 (RSSMB, OpenFDs) 采样点，返回最后一个采样点。
func feed(t *testing.T, rm *ResourceMonitor, start time.Time, step time.Duration, rss []uint64, fds []int32) *ResourceSnapshot {
	t.Helper()
	var last *ResourceSnapshot
	for i := range rss {
		s := &ResourceSnapshot{
			Timestamp: start.Add(time.Duration(i) * step),
			MemoryStats: MemoryStats{
				RSSMB: rss[i],
			},
			Goroutines:      10,
			FileDescriptors: FileDescriptorStats{OpenFDs: fds[i]},
		}
		rm.updateLastSnapshot(s)
		rm.detectLeaks(s)
		last = s
	}
	return last
}

func countAlerts(rm *ResourceMonitor, alertType string) int {
	n := 0
	for _, a := range rm.GetAlerts() {
		if a.AlertType == alertType {
			n++
		}
	}
	return n
}

// 核心回归：GC 锯齿（无趋势）跨满整个窗口，必须一条都不报。
// 旧实现比较相邻两点，30MB 的相邻跳变 / 0.5min = 60MB/min，会全报。
func TestLeakWindowIgnoresGCSawtoothWithNoTrend(t *testing.T) {
	rm := newLeakTestMonitor(t, 10*time.Minute, 10, 100)

	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var rss []uint64
	var fds []int32
	// 40 个点 = 20 分钟，覆盖 10 分钟窗口多次；RSS 在 200~260 之间锯齿、无趋势
	for i := 0; i < 40; i++ {
		rss = append(rss, 200+uint64([]int{0, 30, 10, 45, 20, 60, 15, 35}[i%8]))
		fds = append(fds, 100)
	}
	feed(t, rm, start, 30*time.Second, rss, fds)

	if n := countAlerts(rm, "memory_leak_suspected"); n != 0 {
		t.Fatalf("GC 锯齿无趋势却报了 %d 条 memory_leak_suspected，期望 0", n)
	}
}

// 单点尖刺后回落，窗口内无趋势 ⇒ 不报。
// 这是最锋利的一条：旧实现在 200→280 的相邻跳变上立刻告警。
func TestLeakWindowIgnoresSingleSpikeThatRecovers(t *testing.T) {
	rm := newLeakTestMonitor(t, 10*time.Minute, 10, 100)

	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	rss := []uint64{200}
	fds := []int32{100}
	for i := 0; i < 19; i++ {
		rss = append(rss, 200)
		fds = append(fds, 100)
	}
	rss = append(rss, 280) // 唯一一次尖刺 +80MB
	fds = append(fds, 100)
	for i := 0; i < 20; i++ {
		rss = append(rss, 200) // 立刻回落到平台
		fds = append(fds, 100)
	}
	feed(t, rm, start, 30*time.Second, rss, fds)

	if n := countAlerts(rm, "memory_leak_suspected"); n != 0 {
		t.Fatalf("单点尖刺后回落却报了 %d 条 memory_leak_suspected，期望 0", n)
	}
}

// 真泄漏：窗口内持续单调增长，必须报。
func TestLeakWindowFiresOnSustainedGrowth(t *testing.T) {
	rm := newLeakTestMonitor(t, 10*time.Minute, 10, 100)

	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var rss []uint64
	var fds []int32
	// 40 个点 = 20 分钟，RSS 每 30s 涨 15MB ⇒ 30MB/min，远超 10MB/min 阈值
	for i := 0; i < 40; i++ {
		rss = append(rss, uint64(200+i*15))
		fds = append(fds, 100)
	}
	feed(t, rm, start, 30*time.Second, rss, fds)

	if n := countAlerts(rm, "memory_leak_suspected"); n == 0 {
		t.Fatal("持续增长 30MB/min 却一条都没报，泄漏判据失效")
	}
}

// 窗口未铺满前不得判定——否则等于偷偷退回了相邻两点差值。
func TestLeakWindowSilentUntilWindowIsFull(t *testing.T) {
	rm := newLeakTestMonitor(t, 10*time.Minute, 10, 100)

	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	// 只有 3 个点 = 1 分钟，跨度远小于 10 分钟窗口，但相邻差值巨大
	rss := []uint64{200, 400, 600}
	fds := []int32{100, 100, 100}
	feed(t, rm, start, 30*time.Second, rss, fds)

	if n := countAlerts(rm, "memory_leak_suspected"); n != 0 {
		t.Fatalf("窗口仅 1 分钟（未铺满）却报了 %d 条，期望 0", n)
	}
}

// FD 分支同样必须走窗口基线。
func TestLeakWindowFDUsesWindowBaseline(t *testing.T) {
	t.Run("锯齿不报", func(t *testing.T) {
		rm := newLeakTestMonitor(t, 10*time.Minute, 1000, 100)
		start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
		var rss []uint64
		var fds []int32
		for i := 0; i < 40; i++ {
			rss = append(rss, 200)
			fds = append(fds, int32(100+[]int{0, 30, 10, 45, 20}[i%5]))
		}
		feed(t, rm, start, 30*time.Second, rss, fds)
		if n := countAlerts(rm, "fd_leak_suspected"); n != 0 {
			t.Fatalf("FD 锯齿无趋势却报了 %d 条，期望 0", n)
		}
	})

	t.Run("持续增长要报", func(t *testing.T) {
		rm := newLeakTestMonitor(t, 10*time.Minute, 1000, 100)
		start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
		var rss []uint64
		var fds []int32
		for i := 0; i < 40; i++ {
			rss = append(rss, 200)
			// 每 30s +100 FD ⇒ 200 FD/min > 100/min 阈值
			fds = append(fds, int32(100+i*100))
		}
		feed(t, rm, start, 30*time.Second, rss, fds)
		if n := countAlerts(rm, "fd_leak_suspected"); n == 0 {
			t.Fatal("FD 持续增长 200/min（阈值 100/min）却一条都没报，FD 判据失效")
		}
	})
}

// 阈值语义钉子：增长 20MB/min、阈值 30MB/min ⇒ 不报；阈值 10 ⇒ 报。
// 锁住「阈值作用在窗口平均速率上」而不是别的什么量。
func TestLeakWindowThresholdAppliesToWindowedRate(t *testing.T) {
	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	build := func(threshold float64) *ResourceMonitor {
		rm := newLeakTestMonitor(t, 10*time.Minute, threshold, 1<<30)
		var rss []uint64
		var fds []int32
		for i := 0; i < 40; i++ {
			rss = append(rss, uint64(200+i*10)) // 每 30s +10MB ⇒ 20MB/min
			fds = append(fds, 100)
		}
		feed(t, rm, start, 30*time.Second, rss, fds)
		return rm
	}

	if n := countAlerts(build(30), "memory_leak_suspected"); n != 0 {
		t.Fatalf("20MB/min 增长对 30MB/min 阈值却报了 %d 条，期望 0", n)
	}
	if n := countAlerts(build(10), "memory_leak_suspected"); n == 0 {
		t.Fatal("20MB/min 增长对 10MB/min 阈值却没报，阈值未作用于窗口速率")
	}
}
