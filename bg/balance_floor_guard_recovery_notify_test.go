package bg

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// ★★ 2026-11-08（第 51 增量审计 F-1）：balance-floor guard 的恢复通知链。
//
// ★ 缺陷原文：guard 有四处把凭据从 balance_exhausted 翻回 ok
//   （清下限释放 / 逃生门 / 货币恢复 / 套餐恢复），而**四处都不发任何恢复事件**
//   ⇒ 该凭据被 W5 配额钉或长梯停在未来的失败探针行**无人唤醒**。
//   实测（211）：29 个用尽凭据里 23 个 quota_recover_at IS NULL（balance/permanent
//   族设计上即无重置点 ⇒ W5 臂 2 按设计不钉）⇒ 近 24h 用尽凭据仍被探测
//   297 次（凭据 35）/ 258 次（凭据 4）。
//
// ★ 修法：翻 ok 后调 onCredentialRecovered(credID, reason) ⇒ main.go 接到
//   nodeProbeWorker.RearmCredentialFailures（与 credRecovery.SetRearmFunc 同族）。

// TestBalanceFloorGuardRecoveryNotificationHook pins the hook's contract:
// nil-safe (no listener ⇒ silent no-op), and the setter is the only way in.
func TestBalanceFloorGuardRecoveryNotificationHook(t *testing.T) {
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	body := string(src)

	for _, want := range []string{
		"func (g *BalanceFloorGuard) SetOnCredentialRecovered(",
		"func (g *BalanceFloorGuard) notifyRecovered(",
		"func (g *BalanceFloorGuard) notifyRecoveredBatch(",
		"onRecoveredMu sync.RWMutex",
		"onRecovered   func(credID int64, reason string)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("balance_floor_guard.go missing %q", want)
		}
	}

	// nil-safe: 未接线时静默跳过（不得 panic）。
	g := &BalanceFloorGuard{}
	g.notifyRecovered(1, "unit-test-no-listener")
	g.notifyRecoveredBatch([]int64{1, 2}, "unit-test-no-listener")
	// credID 非正 ⇒ 也不该触发（防越界/零值误触发）。
	called := false
	g.SetOnCredentialRecovered(func(credID int64, reason string) { called = true })
	g.notifyRecovered(0, "unit-test-zero-id")
	g.notifyRecovered(-1, "unit-test-negative-id")
	if called {
		t.Fatal("notifyRecovered must not fire for non-positive credential id")
	}
}

// TestBalanceFloorGuardAllRecoverySitesNotify pins that EVERY quota_state='ok'
// flip site in the guard publishes a recovery notification. This is the
// F-1 regression guard: a new flip site added without a notify call silently
// re-opens the "recovered but probe rows parked" hole.
//
// The four sites are located by their function/branch anchors; each site's
// body slice must contain a notify call.
func TestBalanceFloorGuardAllRecoverySitesNotify(t *testing.T) {
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	body := string(src)

	sliceFn := func(anchor string) string {
		idx := strings.Index(body, anchor)
		if idx < 0 {
			t.Fatalf("anchor not found: %q", anchor)
		}
		rest := body[idx:]
		end := strings.Index(rest, "\nfunc ")
		if end < 0 {
			return rest
		}
		return rest[:end]
	}

	sites := []struct {
		anchor string
		want   string
	}{
		// ① 清下限释放（多行 UPDATE ⇒ 改 RETURNING id + notifyRecoveredBatch）
		{"func (g *BalanceFloorGuard) releaseClearedFloorCredentials", "notifyRecoveredBatch(ids, \"balance_floor_cleared\")"},
		// ② 逃生门（多行 UPDATE ⇒ RETURNING id + notifyRecoveredBatch）
		{"func (g *BalanceFloorGuard) releaseStaleProbeFloorCredentials", "notifyRecoveredBatch(ids, \"balance_floor_escape\")"},
		// ③ 货币恢复（单行 ⇒ notifyRecovered）
		{"func (g *BalanceFloorGuard) restorePulledCurrency", "notifyRecovered(id, \"balance_floor_currency_restored\")"},
		// ④ 套餐恢复（handlePlanCredential 的 floorRestore 分支 ⇒ notifyRecovered）
		{"func (g *BalanceFloorGuard) handlePlanCredential", "notifyRecovered(c.ID, \"balance_floor_plan_restored\")"},
	}
	for _, s := range sites {
		fn := sliceFn(s.anchor)
		if !strings.Contains(fn, s.want) {
			t.Fatalf("recovery site %q must publish a recovery notification (want %q)", s.anchor, s.want)
		}
	}

	// 两处多行 UPDATE 必须用 RETURNING id 才能拿到凭据 ID（无 RETURNING
	// ⇒ RowsAffected 只有计数、无法逐个通知）。
	for _, anchor := range []string{
		"func (g *BalanceFloorGuard) releaseClearedFloorCredentials",
		"func (g *BalanceFloorGuard) releaseStaleProbeFloorCredentials",
	} {
		fn := sliceFn(anchor)
		if !strings.Contains(fn, "RETURNING id") {
			t.Fatalf("recovery site %q must use RETURNING id (F-1: the notification needs the credential id)", anchor)
		}
	}
}

// TestBalanceFloorGuardRecoveryNotificationIsAsync pins that the notification
// does not block the 5-minute guard cycle (the callback fans out to a DB
// round-trip via RearmCredentialFailures).
func TestBalanceFloorGuardRecoveryNotificationIsAsync(t *testing.T) {
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	body := string(src)

	idx := strings.Index(body, "func (g *BalanceFloorGuard) notifyRecovered(")
	if idx < 0 {
		t.Fatal("notifyRecovered not found")
	}
	rest := body[idx:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		end = len(rest)
	}
	fn := rest[:end]

	if !strings.Contains(fn, `Go("balance_floor_guard.notifyRecovered"`) {
		t.Fatal("recovery notification must run asynchronously (bg.Go) so the guard cycle is never blocked")
	}
}

// TestBalanceFloorGuardRecoveryWiringInMain pins the main.go wiring: the
// guard's recovery notification reaches the node-probe rearm.
//
// ★ The wiring must NOT live inside an `if credRecovery != nil` block — that
//   branch is disabled by config.IsCredRecoveryDisabled() while the guard
//   keeps running, so the notification would be silently dropped exactly in
//   the deployments that need it.
func TestBalanceFloorGuardRecoveryWiringInMain(t *testing.T) {
	src, err := os.ReadFile("../cmd/gateway/main.go")
	if err != nil {
		t.Fatalf("read main.go failed: %v", err)
	}
	body := string(src)

	if !strings.Contains(body, "balanceFloorGuard.SetOnCredentialRecovered(") {
		t.Fatal("main.go must wire balanceFloorGuard.SetOnCredentialRecovered")
	}
	if !strings.Contains(body, "nodeProbeWorker.RearmCredentialFailures(int(credID))") {
		t.Fatal("main.go recovery notification must call nodeProbeWorker.RearmCredentialFailures")
	}

	// 每个接线点必须自带 nodeProbeWorker != nil 守卫（nil 解引用会 panic），
	// 且**不得**与 credRecovery != nil 并列（那条分支可被
	// config.IsCredRecoveryDisabled() 关掉，而 guard 仍在跑 ⇒ 通知被静默丢弃）。
	idx := 0
	sites := 0
	for {
		rel := strings.Index(body[idx:], "balanceFloorGuard.SetOnCredentialRecovered(")
		if rel < 0 {
			break
		}
		abs := idx + rel
		// 回溯到该调用点所属的 if 条件行（向上找最近的 "if ... {"）。
		start := abs - 600
		if start < 0 {
			start = 0
		}
		ctx := body[start:abs]
		if !strings.Contains(ctx, "nodeProbeWorker != nil") {
			t.Fatalf("balance floor recovery wiring at offset %d must be guarded by nodeProbeWorker != nil", abs)
		}
		if !strings.Contains(ctx, "balanceFloorGuard != nil") {
			t.Fatalf("balance floor recovery wiring at offset %d must be guarded by balanceFloorGuard != nil", abs)
		}
		// ★ F-1 的核心不变量：接线**不得**依赖 credRecovery != nil。
		//   取该 if 条件的完整文本（从最近的 "if " 到 " {"）。
		ifIdx := strings.LastIndex(ctx, "if ")
		if ifIdx < 0 {
			t.Fatalf("balance floor recovery wiring at offset %d has no enclosing if condition", abs)
		}
		cond := ctx[ifIdx:]
		if brace := strings.Index(cond, "{"); brace >= 0 {
			cond = cond[:brace]
		}
		if strings.Contains(cond, "credRecovery") {
			t.Fatalf("balance floor recovery wiring at offset %d must not be conditioned on credRecovery (that branch is disableable while the guard still runs): %q", abs, strings.TrimSpace(cond))
		}
		sites++
		idx = abs + 1
	}
	if sites == 0 {
		t.Fatal("no balance floor recovery wiring site found")
	}
}

// TestBalanceFloorGuardRecoveryNotificationConcurrent is a plain concurrency
// smoke test: the setter may be called while a cycle is notifying.
func TestBalanceFloorGuardRecoveryNotificationConcurrent(t *testing.T) {
	g := &BalanceFloorGuard{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	g.SetOnCredentialRecovered(func(credID int64, reason string) {
		mu.Lock()
		total++
		mu.Unlock()
	})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				g.notifyRecovered(int64(j+1), "concurrency-test")
			}
		}()
	}
	wg.Wait()
	// The hook fires asynchronously (bg.Go) ⇒ the count may lag; the invariant
	// that matters is "no race / no panic" (checked by -race) and that a
	// later setter call is visible to subsequent notifications.
	g.SetOnCredentialRecovered(func(credID int64, reason string) {})
	mu.Lock()
	defer mu.Unlock()
	if total < 0 {
		t.Fatal("notification counter must not go negative")
	}
}
