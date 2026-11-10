// reducer_empty_streak_window_test.go —— 空响应「连续」的时间维度 + 阈值读取的锁外性
// （★ 2026-11-11 审计 F-B1 / F-B2 的钉测）
//
// ★ F-B1：★ emptyStreak 曾是**纯计数**（★ 无时间维度）⇒ ★★ 3 个月前 4 次 + 今天 1 次
// ★ ★⇒ ★★ 「连续」被时间抹平 ⇒ ★★ 触发绑定降级；★ 而仓内自己的规格文本写的是「48 起/6h」。
// ★ F-B2：★ 阈值读取（★ 可能打 DB）曾发生在**进程级全局锁**内 ⇒ ★★ DB 抖动时
// ★ ★⇒ ★★ 全网关的 nodehealth 归约串行化阻塞。

package nodehealth

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

// emptyObservation 构造一次空响应观测（★ 与外部测试同形）。
func emptyObservation(node NodeKey, attempt string) Observation {
	return Observation{
		Node: node, AttemptID: attempt, Phase: PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: ErrorKindEmptyResponse,
	}
}

// TestEmptyResponseStreakResetsAfterWindow 钉住 F-B1：
//
// ★ 「连续」必须带**时间维度**：★ 窗口外的旧计数**不再连续** ⇒ ★ streak 归零后重新计数。
// ★ ⇒ ★★ 而且**不能**因此触发降级（★ 阈值以下只发 URSM 软信号）。
//
// ★ 变异臂：★ 把窗口判定去掉（★ 注释掉 lastEmptyAt 的检查）⇒ ★★ 本用例红
// ★ ★（★ streak 会停在 5 ⇒ ★★ 触发 EffectSetBindingUnavailable）。
func TestEmptyResponseStreakResetsAfterWindow(t *testing.T) {
	threshold := 5
	base := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	now := base
	r := NewOutcomeReducerWithConfig(ReducerConfig{
		Now:                          func() time.Time { return now },
		EmptyResponseStreakThreshold: func() int { return threshold },
	})
	node := NodeKey{TenantID: "t", CredentialID: 7, Model: "glm-5.3-flash"}

	// ① 窗口内攒到 threshold-1 次
	for i := 1; i < threshold; i++ {
		d, err := r.Reduce(emptyObservation(node, "a"+string(rune('0'+i))))
		if err != nil {
			t.Fatal(err)
		}
		if d.EmptyResponseStreak != i {
			t.Fatalf("in-window attempt %d: streak=%d want %d", i, d.EmptyResponseStreak, i)
		}
	}

	// ② 时间跨过窗口（★ 6h 窗口 ⇒ 跳 7h）
	now = base.Add(EmptyResponseStreakWindow + time.Hour)

	// ③ 再来一次空响应 ⇒ streak 必须**归零后重新计数** = 1
	d, err := r.Reduce(emptyObservation(node, "b1"))
	if err != nil {
		t.Fatal(err)
	}
	if d.EmptyResponseStreak != 1 {
		t.Fatalf("after the window elapsed: streak=%d want 1 (★ 跨窗的旧计数不再连续)", d.EmptyResponseStreak)
	}
	for _, e := range d.Effects {
		if e.Kind == EffectSetBindingUnavailable {
			t.Fatalf("streak reset by the window ⇒ must NOT demote the binding; effects=%v", d.Effects)
		}
	}
}

// TestOutcomeReducerReadsThresholdOutsideLock 钉住 F-B2：
//
// ★ 阈值读取（★ 注入的读取器可能打 DB）**必须发生在 r.mu 之外**。
// ★ ⇒ ★★ 否则 DB 抖动时，每一次 Reduce 都会在**进程级全局锁**内阻塞，
// ★ ★⇒ ★★ 全网关的 nodehealth 归约被**串行化**。
//
// ★ 断言方式：★ 读取器内部用 `TryLock` **试探**锁是否被占（★ 同包测试才能访问 mu）
// ★ ★⇒ ★★ 拿得到锁 ⇒ ★★ 读发生时**没持锁** ✔（★ 这比「读没读」更直接地测「锁的边界」）。
//
// ★ 变异臂：★ 把快照挪回锁内（★ 即在 Reduce 里先 Lock 再取阈值）⇒ ★★ 本用例红。
func TestOutcomeReducerReadsThresholdOutsideLock(t *testing.T) {
	r := NewOutcomeReducerWithConfig(ReducerConfig{})
	node := NodeKey{TenantID: "t", CredentialID: 9, Model: "glm-5.3-flash"}

	held := false
	r.emptyStreakThreshold = func() int {
		if r.mu.TryLock() {
			r.mu.Unlock()
			held = false
		} else {
			held = true
		}
		return 5
	}

	if _, err := r.Reduce(emptyObservation(node, "a1")); err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatalf("threshold read happened WHILE the reducer mutex was held " +
			"(★ ⇒ 该读取可能打 DB ⇒ ★★ DB 抖动会串行化全网关的归约)")
	}
}
