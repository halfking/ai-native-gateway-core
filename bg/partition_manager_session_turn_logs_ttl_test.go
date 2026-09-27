package bg

// partition_manager_session_turn_logs_ttl_test.go —
// R67 session-storage 审计子任务 2 (handoff §4) 的单测。
//
// 覆盖两条真实行为（不只做源码字符串断言）：
//   ① clampSessionTurnLogsTTLHours 的 [1,168] 夹取 —— 下限是数据安全
//      底线：0 / 负值若原样透传，一个 tick 就会把 session_turn_logs 整表
//      过期清空。
//   ② cleanupSessionTurnLogsByTTL 在 db == nil 时是 no-op 而非 panic
//      （与 AuditTrimmer.TrimOnce 同款，保证 manager 在测试与降级启动
//      顺序下仍可构造）。
//
// PartitionManager.db 是具体 *pgxpool.Pool 而非接口，pgxmock 注入不了，
// 因此「真的 DELETE 了 N 行」这一层留给真库测试覆盖；本文件钉住的是
// 纯函数与 nil-safe 契约。

import (
	"context"
	"testing"
)

func TestClampSessionTurnLogsTTLHours(t *testing.T) {
	cases := []struct {
		name string
		raw  int
		want int
	}{
		{"default 24 passes through", 24, 24},
		{"lower bound 1 is legal", 1, 1},
		{"upper bound 168 is legal", 168, 168},
		{"zero is clamped to the floor", 0, 1},
		{"negative is clamped to the floor", -7, 1},
		{"above cap is clamped", 9999, 168},
		{"one week is the cap", 24 * 7, 168},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampSessionTurnLogsTTLHours(tc.raw); got != tc.want {
				t.Errorf("clampSessionTurnLogsTTLHours(%d) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// TestClampSessionTurnLogsTTLHours_NeverZero pins the data-safety claim
// directly: no input, however absurd, may produce a 0h retention.
// A 0 would make every row in session_turn_logs immediately expired.
func TestClampSessionTurnLogsTTLHours_NeverZero(t *testing.T) {
	for _, raw := range []int{-1 << 30, -1, 0, 1, 24, 168, 1 << 30} {
		if got := clampSessionTurnLogsTTLHours(raw); got < 1 {
			t.Fatalf("clampSessionTurnLogsTTLHours(%d) = %d, must never be < 1h", raw, got)
		}
	}
}

func TestCleanupSessionTurnLogsByTTL_NilPoolIsNoOp(t *testing.T) {
	pm := &PartitionManager{} // db intentionally nil
	// Must not panic. Regression guard for the nil-pool boot order.
	pm.cleanupSessionTurnLogsByTTL(context.Background())
}
