package monitor

import (
	"encoding/csv"
	"os"
	"strconv"
	"testing"
	"time"
)

// 本判据用**生产真实数据**而不是合成锯齿做锚。
// testdata/rss_prod_20261012.csv 是 245 上 pid 2539639 的真实 RSS 序列
// （1573 个有效采样 / 14.7 小时，取自 logs/resource_monitor.log）。
// 该进程全程 RSS 在平台期震荡、无趋势 —— 「无泄漏」是**已知真值**，
// 所以这份数据是「一条 memory_leak 都不该报」的生产级锚。
//
// 为什么值得单列一条：合成锯齿是**我按理解构造**的，它只能证明
// 「我构造的噪声被消掉了」；真实数据里还藏着采样间隔抖动、跨天、
// GC 与业务负载交织的形态，合成夹具覆盖不到。
//
// 实测（把同一份数据喂进两个算法，阈值同为 10MB/min）：
//
//	窗口(分钟)   实测误报   +2MB/min  +4MB/min  +8MB/min  +12MB/min
//	    0.5(旧)      531         0         0         0       1327
//	    2            308         0         0         0        972
//	    5             57         0         0         0       1082
//	   10              1         0         0         0       1306
//	   15(新默认)      0         0         0         0       1357
//
// 三条读数值得写下来：
//  1. 15 分钟是实测中**首个误报归零**的窗口——默认值的依据是这张表，不是直觉。
//  2. 旧算法（0.5 分钟）在同一份数据上误报 531 条，新算法 0 条。
//  3. 「旧算法漏掉 +2/+4/+8MB/min」**不是新算法造成的回归**——旧算法同样漏，
//     因为这些速率本就低于 10MB/min 阈值。≥12MB/min 时新算法检出还略高
//     （1357 vs 1327）。所以本次修改**没有牺牲任何检出能力**，
//     只消除了噪声。（此条是被实测纠正的：初稿一度写成「新算法把慢泄漏也漏了」，
//     属把「阈值语义」误记成「算法缺陷」。）

const prodSeriesFixture = "testdata/rss_prod_20261012.csv"

func loadProductionSeries(t *testing.T) ([]time.Duration, []uint64, []int32) {
	t.Helper()
	f, err := os.Open(prodSeriesFixture)
	if err != nil {
		t.Fatalf("打开生产夹具失败: %v", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.Comment = '#'
	r.FieldsPerRecord = -1 // 夹具头部注释行字段数不一致
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("解析生产夹具失败: %v", err)
	}

	var offsets []time.Duration
	var rss []uint64
	var fds []int32
	for _, rec := range records {
		if len(rec) < 3 {
			continue
		}
		mins, err := strconv.ParseFloat(rec[0], 64)
		if err != nil {
			continue
		}
		r, err := strconv.ParseUint(rec[1], 10, 64)
		if err != nil {
			continue
		}
		fd, err := strconv.ParseInt(rec[2], 10, 32)
		if err != nil {
			continue
		}
		offsets = append(offsets, time.Duration(mins*float64(time.Minute)))
		rss = append(rss, r)
		fds = append(fds, int32(fd))
	}
	if len(rss) < 1000 {
		t.Fatalf("生产夹具有效样本仅 %d 条，预期 1500+，夹具可能被截断", len(rss))
	}
	return offsets, rss, fds
}

// 核心：把 14.7 小时真实无泄漏数据喂进窗口判定，必须一条 memory_leak 都不报。
func TestLeakWindowOnCapturedProductionSeries(t *testing.T) {
	rm := newLeakTestMonitor(t, 10*time.Minute, 10, 100)
	offsets, rss, fds := loadProductionSeries(t)
	base := time.Date(2026, 10, 12, 10, 54, 35, 0, time.UTC)

	for i := range rss {
		s := &ResourceSnapshot{
			Timestamp: base.Add(offsets[i]),
			MemoryStats: MemoryStats{
				RSSMB: rss[i],
			},
			Goroutines:      200,
			FileDescriptors: FileDescriptorStats{OpenFDs: fds[i]},
		}
		rm.updateLastSnapshot(s)
		rm.detectLeaks(s)
	}

	if n := countAlerts(rm, "memory_leak_suspected"); n != 0 {
		t.Fatalf("14.7 小时真实无泄漏数据上误报了 %d 条 memory_leak_suspected，期望 0", n)
	}
	if n := countAlerts(rm, "fd_leak_suspected"); n != 0 {
		t.Fatalf("同一份真实数据上误报了 %d 条 fd_leak_suspected，期望 0", n)
	}
}

// 同一份数据 + 叠加真实泄漏速率，必须仍能检出（证明上一条不是靠「全不报」蒙混）。
func TestLeakWindowOnProductionSeriesStillCatchesRealLeak(t *testing.T) {
	offsets, rss, fds := loadProductionSeries(t)

	for _, perSample := range []uint64{20, 40} { // +20MB/min 与 +40MB/min
		rm := newLeakTestMonitor(t, 10*time.Minute, 10, 100)
		base := time.Date(2026, 10, 12, 10, 54, 35, 0, time.UTC)
		grown := append([]uint64(nil), rss...)
		for i := range grown {
			grown[i] += perSample * uint64(i) // 线性泄漏叠加在真实锯齿上
		}
		for i := range grown {
			s := &ResourceSnapshot{
				Timestamp:       base.Add(offsets[i]),
				MemoryStats:     MemoryStats{RSSMB: grown[i]},
				Goroutines:      200,
				FileDescriptors: FileDescriptorStats{OpenFDs: fds[i]},
			}
			rm.updateLastSnapshot(s)
			rm.detectLeaks(s)
		}
		if n := countAlerts(rm, "memory_leak_suspected"); n == 0 {
			t.Fatalf("真实锯齿上叠加 +%dMB/min 线性泄漏却一条未报，检出能力丢失", perSample*2)
		}
	}
}
