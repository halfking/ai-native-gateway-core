package bg

import (
	"testing"
	"time"
)

// TestRollupCatchupCapsWindow 钉住补窗封顶：游标停摆时补窗 = [lastTS, now]
// 每 tick 自增 1min，无封顶会在 rollupMain 自身越过 2min tick 预算后死锁
// （2026-11-20 211 实证 —— retire 挂死令游标停摆 68min+，窗口持续膨胀）。
// 封顶后每 tick 至多推进 statsRollupCatchupCap，缺口顺延不清零。
func TestRollupCatchupCapsWindow(t *testing.T) {
	now := time.Date(2026, 11, 20, 1, 30, 0, 0, time.UTC)

	// 正常滞后（1min）：窗口保持 [lastTS, now] 原样。
	lastTS := now.Add(-1 * time.Minute)
	since, until, capped := rollupCatchupBounds(lastTS, now)
	if capped || !since.Equal(lastTS) || !until.Equal(now) {
		t.Errorf("normal lag: capped=%v window=[%s,%s], want uncapped [lastTS,now]",
			capped, since.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano))
	}

	// 恰在封顶线上（窗口 == cap）：不封。
	lastTS = now.Add(-statsRollupCatchupCap)
	_, until, capped = rollupCatchupBounds(lastTS, now)
	if capped || !until.Equal(now) {
		t.Errorf("at cap: capped=%v until=%s, want uncapped until=now",
			capped, until.Format(time.RFC3339Nano))
	}

	// 停摆 3h：封到 lastTS+cap，缺口顺延（until < now）。
	lastTS = now.Add(-3 * time.Hour)
	since, until, capped = rollupCatchupBounds(lastTS, now)
	if !capped {
		t.Fatal("stalled cursor (3h gap) must be capped")
	}
	wantUntil := lastTS.Add(statsRollupCatchupCap)
	if !since.Equal(lastTS) || !until.Equal(wantUntil) {
		t.Errorf("stalled: window=[%s,%s], want [%s,%s]",
			since.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano),
			lastTS.Format(time.RFC3339Nano), wantUntil.Format(time.RFC3339Nano))
	}
	if !until.Before(now) {
		t.Error("capped until must leave the rest of the gap for later ticks")
	}

	// 倒挂时钟（lastTS > now）：不封，窗口原样交由下游幂等收敛。
	lastTS = now.Add(2 * time.Minute)
	since, until, capped = rollupCatchupBounds(lastTS, now)
	if capped || !since.Equal(lastTS) || !until.Equal(now) {
		t.Errorf("clock skew: capped=%v window=[%s,%s], want uncapped passthrough",
			capped, since.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano))
	}
}
