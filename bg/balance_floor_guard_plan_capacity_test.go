package bg

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 2026-11-09（minimax 套餐恢复感知轮 F-B）测试族。
//
// 事故：扫描器每 5min 拿到厂商套餐配额证据（success=6/轮），但 restore
// 路径只认 reason_code='balance_floor' ⇒ probe-v2 挂起（quota_permanent
// 族）的行即使厂商窗口有容量也无人唤醒 —— minimax 套餐恢复 ~24h 才被
// 人工恢复。修复：handlePlanCredential 在 persistPlanState 之后做证据门控
// re-arm。

// TestBalanceFloorGuardPlanCapacityAvailable_Structure 钉 F-B 块的判据形状：
// ① 必须在 persistPlanState 之后（先落证据再动作）；② 触发集 = 用尽族三态；
// ③ balance_floor 原因行不在触发集（它有自己的 restore 路径）；④ 容量判据
// = MaxUsedPercent < 100；⑤ warnGate 节流（日志与 re-arm 同闸）；⑥ 动作 =
// notifyPlanCapacityAvailable。
func TestBalanceFloorGuardPlanCapacityAvailable_Structure(t *testing.T) {
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	body := string(src)

	hidx := strings.Index(body, "func (g *BalanceFloorGuard) handlePlanCredential")
	if hidx < 0 {
		t.Fatalf("handlePlanCredential not found")
	}
	hend := strings.Index(body[hidx+1:], "\nfunc ")
	hfn := body[hidx : hidx+1+hend]

	persistIdx := strings.Index(hfn, "g.persistPlanState(")
	condIdx := strings.Index(hfn, `if (c.QuotaState == "balance_exhausted"`)
	fbIdx := strings.Index(hfn, "notifyPlanCapacityAvailable")
	if persistIdx < 0 || condIdx < 0 || fbIdx < 0 || condIdx < persistIdx || fbIdx < condIdx {
		t.Fatalf("F-B block must run after persistPlanState (evidence before action)")
	}
	fb := hfn[condIdx:]
	if !strings.Contains(fb, "balance_exhausted") ||
		!strings.Contains(fb, "permanently_exhausted") ||
		!strings.Contains(fb, "periodic_exhausted") {
		t.Fatalf("trigger set must cover all three quota-exhausted states")
	}
	if !strings.Contains(fb, `c.ReasonCode != "balance_floor"`) {
		t.Fatalf("balance_floor rows have their own restore path and must not double-fire")
	}
	if !strings.Contains(fb, "st.MaxUsedPercent < 100") {
		t.Fatalf("capacity gate must be MaxUsedPercent < 100 (fresh vendor evidence)")
	}
	if !strings.Contains(fb, "g.warnGate.allow(c.ID, time.Now())") {
		t.Fatalf("warn+rearm must share the 15min warnGate throttle")
	}

	// 钩子接线形态：SetOnPlanCapacityAvailable + nil-safe 异步派发。
	if !strings.Contains(body, "func (g *BalanceFloorGuard) SetOnPlanCapacityAvailable") {
		t.Fatalf("SetOnPlanCapacityAvailable setter missing")
	}
	if !strings.Contains(body, "Go(\"balance_floor_guard.notifyPlanCapacityAvailable\"") {
		t.Fatalf("notify must dispatch async via bg.Go (never block the guard cycle)")
	}
}

// TestBalanceFloorGuardNotifyPlanCapacityAvailable 行为测试：钩子异步送达、
// nil-safe、非正 ID 拒发。
func TestBalanceFloorGuardNotifyPlanCapacityAvailable(t *testing.T) {
	t.Run("hook receives credential id", func(t *testing.T) {
		g := &BalanceFloorGuard{}
		done := make(chan int64, 1)
		g.SetOnPlanCapacityAvailable(func(credID int64) { done <- credID })
		g.notifyPlanCapacityAvailable(42)
		select {
		case got := <-done:
			if got != 42 {
				t.Fatalf("hook got %d, want 42", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("hook not invoked within timeout")
		}
	})

	t.Run("nil hook is a no-op", func(t *testing.T) {
		g := &BalanceFloorGuard{}
		g.notifyPlanCapacityAvailable(42) // must not panic
	})

	t.Run("non-positive id is dropped", func(t *testing.T) {
		g := &BalanceFloorGuard{}
		called := make(chan int64, 1)
		g.SetOnPlanCapacityAvailable(func(credID int64) { called <- credID })
		g.notifyPlanCapacityAvailable(0)
		select {
		case <-called:
			t.Fatalf("non-positive id must not reach the hook")
		case <-time.After(200 * time.Millisecond):
		}
	})
}
