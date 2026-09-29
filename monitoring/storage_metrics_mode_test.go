package monitoring

import (
	"sync"
	"testing"
)

// H5 hotzone 维度（2026-09-24 方案 §3-H5，P5）单测：镜像走进程模式戳
//（SetStorageMode）、L1.5 走显式 mode 参数，且无维度合计口径与旧版完全
// 一致（兼容断言——既有消费方零感知）。

// TestStorageMetricsModeDimension 验证 per-mode 分桶与合计口径兼容。
func TestStorageMetricsModeDimension(t *testing.T) {
	m := NewStorageMetrics()
	m.SetStorageMode(ModeLite)

	// 镜像：进程戳 lite → 全部落 lite 桶；合计 2 成功 1 失败。
	m.RecordMirrorWrite(false)
	m.RecordMirrorWrite(false)
	m.RecordMirrorWrite(true)

	// L1.5：显式 mode。full 1/1（命中率 0.5），lite 2/0（命中率 1.0）。
	m.RecordL15HitForMode(ModeFull)
	m.RecordL15MissForMode(ModeFull)
	m.RecordL15HitForMode(ModeLite)
	m.RecordL15HitForMode(ModeLite)

	snap := m.Snapshot()

	// 兼容：合计口径不变。
	if got := uintOf(t, layerOf(t, snap, "mirror"), "total"); got != 2 {
		t.Errorf("mirror.total = %d, want 2", got)
	}
	if got := uintOf(t, layerOf(t, snap, "l1_5"), "hits"); got != 3 {
		t.Errorf("l1_5.hits = %d, want 3（合计含两 mode）", got)
	}

	// mirror.by_mode：全部落 lite。
	byMode := layerOf(t, layerOf(t, snap, "mirror"), "by_mode")
	if got := uintOf(t, layerOf(t, byMode, ModeLite), "total"); got != 2 {
		t.Errorf("mirror.by_mode.lite.total = %d, want 2", got)
	}
	if got := uintOf(t, layerOf(t, byMode, ModeLite), "errors"); got != 1 {
		t.Errorf("mirror.by_mode.lite.errors = %d, want 1", got)
	}
	if got := uintOf(t, layerOf(t, byMode, ModeFull), "total"); got != 0 {
		t.Errorf("mirror.by_mode.full.total = %d, want 0（进程戳为 lite）", got)
	}

	// l1_5_by_mode 命中率。
	l15ByMode := layerOf(t, snap, "l1_5_by_mode")
	if got := floatOf(t, layerOf(t, l15ByMode, ModeFull), "hit_rate"); !approx(got, 0.5) {
		t.Errorf("l1_5_by_mode.full.hit_rate = %v, want 0.5", got)
	}
	if got := floatOf(t, layerOf(t, l15ByMode, ModeLite), "hit_rate"); !approx(got, 1.0) {
		t.Errorf("l1_5_by_mode.lite.hit_rate = %v, want 1.0", got)
	}

	// hotzone.hit_total_by_mode 与 l1_5_by_mode[*].hits 同源。
	hzHits := layerOf(t, layerOf(t, snap, "hotzone"), "hit_total_by_mode")
	if got := uintOf(t, hzHits, ModeFull); got != 1 {
		t.Errorf("hotzone.hit_total_by_mode.full = %d, want 1", got)
	}
	if got := uintOf(t, hzHits, ModeLite); got != 2 {
		t.Errorf("hotzone.hit_total_by_mode.lite = %d, want 2", got)
	}
}

// TestStorageMetricsUnknownModeNotBucketed 未盖章实例 / 未知标签：仅累计
// 无维度计数，per-mode 桶恒 0（历史行为兜底）。
func TestStorageMetricsUnknownModeNotBucketed(t *testing.T) {
	m := NewStorageMetrics() // 不调 SetStorageMode
	m.RecordMirrorWrite(false)
	m.RecordL15HitForMode("weird") // 未知标签：只进合计
	snap := m.Snapshot()
	if got := uintOf(t, layerOf(t, snap, "mirror"), "total"); got != 1 {
		t.Errorf("mirror.total = %d, want 1", got)
	}
	byMode := layerOf(t, layerOf(t, snap, "mirror"), "by_mode")
	for _, mode := range []string{ModeFull, ModeLite} {
		if got := uintOf(t, layerOf(t, byMode, mode), "total"); got != 0 {
			t.Errorf("mirror.by_mode.%s.total = %d, want 0（未知模式不落桶）", mode, got)
		}
	}
	if got := uintOf(t, layerOf(t, layerOf(t, snap, "l1_5_by_mode"), ModeFull), "hits"); got != 0 {
		t.Errorf("l1_5_by_mode.full.hits = %d, want 0（未知标签不落桶）", got)
	}
}

// TestStorageMetricsModeConcurrent 并发下 per-mode 桶精确可加（-race 门禁）。
func TestStorageMetricsModeConcurrent(t *testing.T) {
	const goroutines, rounds = 100, 50
	m := NewStorageMetrics()
	m.SetStorageMode(ModeFull)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				m.RecordMirrorWrite(false)
				m.RecordL15HitForMode(ModeLite)
				m.RecordL15MissForMode(ModeLite)
			}
		}()
	}
	wg.Wait()
	snap := m.Snapshot()
	want := uint64(goroutines * rounds)
	if got := uintOf(t, layerOf(t, layerOf(t, layerOf(t, snap, "mirror"), "by_mode"), ModeFull), "total"); got != want {
		t.Errorf("mirror.by_mode.full.total = %d, want %d", got, want)
	}
	lite := layerOf(t, layerOf(t, snap, "l1_5_by_mode"), ModeLite)
	if got := uintOf(t, lite, "hits"); got != want {
		t.Errorf("l1_5_by_mode.lite.hits = %d, want %d", got, want)
	}
	if got := uintOf(t, lite, "misses"); got != want {
		t.Errorf("l1_5_by_mode.lite.misses = %d, want %d", got, want)
	}
}

// TestStorageMetricsHotZoneEnabledFlag SetHotZoneEnabled 透出到快照
// （P5 顺带闭合「setter 无调用方、hotzone_enabled 恒 false」的预存缺口）。
func TestStorageMetricsHotZoneEnabledFlag(t *testing.T) {
	m := NewStorageMetrics()
	if snap := m.Snapshot(); snap["hotzone_enabled"] != false {
		t.Errorf("hotzone_enabled = %v, want false（未装配）", snap["hotzone_enabled"])
	}
	m.SetHotZoneEnabled(true)
	if snap := m.Snapshot(); snap["hotzone_enabled"] != true {
		t.Errorf("hotzone_enabled = %v, want true（已装配）", snap["hotzone_enabled"])
	}
}
