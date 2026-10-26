package main

// 2026-10-18：persist 写入者守卫的**接线**门（§10.110.61）。
//
// # 为什么单测不够
//
// `domains/ursm/v2/persist/writerguard_test.go` 证明了 RunCycleGuarded 这个
// 函数本身对不对，但它**测不到 main.go 有没有真的调用它**。
// 少一次接线时：包内 7 条判据全绿，生产上三个写入者照旧，
// 而症状（写入量 3 倍、失败率 85%）完全指不到「少了一行调用」上。
//
// ★ 这与 AGENTS.md 里 §55 的「M43 只测函数不测接线」是同一族。
//
// # 三条契约
//
//	① 默认开启（`!= "0"`），关掉要显式写 0 —— 反过来写会让
//	   「环境变量拼错」退化成静默关闭守卫。
//	② 让位分支必须**在** CollectErr/FlushErr 判读**之前**返回：
//	   让位那一轮压根没跑循环，若落在错误判读之后会被当成 collect 失败，
//	   于是「正常让位」开始污染 flushFailStreak —— 而 streak 正是
//	   §10.110.60 里制造过假阳性的那个指标。
//	③ 让位**不得**计入 flushFailStreak。

import (
	"strings"
	"testing"
)

// persistLoopSource 截出 persist ticker 循环所在的代码段。
//
// ⚠ 锚点必须钉在**唯一**的标识上。用一个泛化词（比如 "for {"）会截到
// 整个 main.go，后面的位置比较全部失去意义。
func persistLoopSource(t *testing.T) string {
	t.Helper()
	src := mainGoSource(t)

	start := strings.Index(src, "cycleTimeouts := persist.CycleTimeoutsFromEnv()")
	if start < 0 {
		t.Fatal("anchor not found: cycleTimeouts assignment in the persist loop")
	}
	if n := strings.Count(src, "cycleTimeouts := persist.CycleTimeoutsFromEnv()"); n != 1 {
		t.Fatalf("anchor is not unique: %d occurrences — the slice below would be ambiguous", n)
	}
	rest := src[start:]
	end := strings.Index(rest, "persist writer stopped")
	if end < 0 {
		t.Fatal("end anchor not found: persist writer stopped")
	}
	return rest[:end]
}

// ① 守卫默认开启，且关掉必须显式写 0。
func TestPersistWriterGuardDefaultsToOn(t *testing.T) {
	src := persistLoopSource(t)

	if !strings.Contains(src, `os.Getenv("URSM_V2_PERSIST_WRITER_GUARD") != "0"`) {
		t.Fatal("the guard gate must read URSM_V2_PERSIST_WRITER_GUARD with `!= \"0\"` so the " +
			"guard is ON by default; a typo'd/empty var must not silently disable it")
	}
	if !strings.Contains(src, "persist.RunCycleGuarded(persistCtx, persistWriter, writerLocker, cycleTimeouts)") {
		t.Fatal("main.go must call persist.RunCycleGuarded when the guard is on")
	}
	if !strings.Contains(src, "persist.NewPgWriterLocker(dbConn.Pool())") {
		t.Fatal("the locker must be built from the same pool the writer flushes through")
	}
	// 回退路径必须保留，否则这道门出问题时没有开关可拉。
	if !strings.Contains(src, "persist.RunCycle(persistCtx, persistWriter, cycleTimeouts)") {
		t.Fatal("the unguarded fallback path must remain reachable via URSM_V2_PERSIST_WRITER_GUARD=0")
	}
}

// ②③ 让位必须先于错误判读返回，且不得计入 streak。
func TestPersistYieldIsNotCountedAsFailure(t *testing.T) {
	src := persistLoopSource(t)

	yieldIdx := strings.Index(src, "if outcome.YieldedToPeer {")
	collectErrIdx := strings.Index(src, "if outcome.CollectErr != nil {")
	flushErrIdx := strings.Index(src, "if outcome.FlushErr != nil {")
	streakIdx := strings.Index(src, "flushFailStreak++")

	for name, idx := range map[string]int{
		"if outcome.YieldedToPeer {":     yieldIdx,
		"if outcome.CollectErr != nil {": collectErrIdx,
		"if outcome.FlushErr != nil {":   flushErrIdx,
		"flushFailStreak++":              streakIdx,
	} {
		if idx < 0 {
			t.Fatalf("anchor not found: %s", name)
		}
	}

	if yieldIdx > collectErrIdx {
		t.Fatal("the yield branch must come BEFORE the CollectErr check — a yielded round " +
			"never ran the cycle, so judging it as a collect failure is a false positive " +
			"(and it would poison flushFailStreak, the exact metric that produced a false " +
			"'recovered' in §10.110.60)")
	}
	if yieldIdx > streakIdx {
		t.Fatal("the yield branch must come BEFORE flushFailStreak++ — standing down is " +
			"expected behaviour and must never count as a write failure")
	}

	// 让位块自身必须以 continue 收尾，否则会继续往下走错误判读。
	yieldBlock := src[yieldIdx:collectErrIdx]
	if !strings.Contains(yieldBlock, "continue") {
		t.Fatal("the yield branch must end in `continue` so the round skips the error handling")
	}

	// 让位不得打 Warn：它是预期行为，不是故障。
	if strings.Contains(yieldBlock, "slog.Warn") {
		t.Fatal("the yield branch must not log at Warn — standing down is expected " +
			"behaviour, and warning on it trains operators to ignore the level")
	}
	// 但角色切换必须可见，否则「谁在写」在日志里完全不可见。
	if !strings.Contains(yieldBlock, "slog.Info") {
		t.Fatal("the first yield must be logged at Info so the writer role change is " +
			"visible — otherwise a stale slot silently taking over leaves no trace")
	}
	if !strings.Contains(yieldBlock, `if !yieldedToPeer`) {
		t.Fatal("the yield log must be gated on `!yieldedToPeer` (log the transition, " +
			"not every tick) — otherwise every minute logs a line that says nothing changed")
	}
}

// 让位之外的循环必须把守卫状态带进失败日志。
func TestPersistFailureLogCarriesGuardState(t *testing.T) {
	src := persistLoopSource(t)
	if !strings.Contains(src, `"guard", outcome.GuardState()`) {
		t.Fatal("flush failures must log the guard state, otherwise \"no snapshot was " +
			"written\" and \"a peer is writing instead\" are indistinguishable in the log")
	}
}
