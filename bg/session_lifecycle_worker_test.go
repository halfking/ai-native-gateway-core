package bg

import (
	"os"
	"strings"
	"testing"
	"time"
)

// ★ 这道门存在的理由（2026-11-20 会话分析闭环审计轮）：
//
//	① applyRecycle 的 UPDATE 曾引用 session_dim 上不存在的列（stop_reason /
//	   updated_at，904 迁移头注登记的 42703 根因）——这个 worker 此前零生产
//	   接线，坏了没人知道；本轮接活后它成为「会话关闭」的事实判定器，一句
//	   42703 就会让全部回收失败、session_dim 永远停在 'active'。钉住语句的
//	   列集合 = 把「列必须真实存在」写成可执行断言（真库判据见
//	   session_lifecycle_worker_realdb_test.go）。
//	② session.closed 分析事件是整条「总结→元数据 final→项目归属」闭链的
//	   唯一触发源（此前全仓零发布者）。发布判据（soft_close 真翻转 + 闲置
//	   在消息保留窗口内）错误时：要么死信噪音（超窗会话 5 次重试后死信），
//	   要么闭链永不触发（漏发）。
func TestLifecycleApplyRecycleSQLUsesExistingColumns(t *testing.T) {
	// 从 applyRecycle 的源码形态提取 UPDATE 语句列集合（与实现逐字同步：
	// 该语句没有单独特意抽成常量——真库判据靠 realdb 测试跑它，本判据靠
	// 源码结构断言防「有人把不存在的列加回来」）。
	src := readFile(t, "session_lifecycle_worker.go")
	if !strings.Contains(src, "SET status = 'closed',") {
		t.Fatalf("applyRecycle 的 UPDATE 形状变了，本判据需要同步更新")
	}
	if strings.Contains(src, "updated_at = NOW()") {
		t.Fatalf("session_dim 没有 updated_at 列（904 头注登记的 42703 根因）——不许再写回 UPDATE")
	}
	if !strings.Contains(src, "stop_reason = $2") {
		t.Fatalf("stop_reason 写入丢失：关闭原因元数据是本轮交付物（迁移 993 补建该列）")
	}
	if !strings.Contains(src, "WHERE gw_session_id = $1 AND status = 'active'") {
		t.Fatalf("幂等守卫（status='active'）丢失 ⇒ 重复扫描会重复发布事件")
	}
}

func TestShouldPublishSessionClosed(t *testing.T) {
	lookback := DefaultSummaryLookback // 72h
	cases := []struct {
		name         string
		action       string
		rowsAffected int64
		idleFor      time.Duration
		lookback     time.Duration
		want         bool
	}{
		{"soft_close 真翻转且在窗口内", "soft_close", 1, 2 * time.Hour, lookback, true},
		{"notify_only 没有关闭事实", "notify_only", 1, 2 * time.Hour, lookback, false},
		{"行没翻转（幂等重扫）", "soft_close", 0, 2 * time.Hour, lookback, false},
		{"超窗：消息体已过保留期", "soft_close", 1, 73 * time.Hour, lookback, false},
		{"窗口边界（恰好 72h）", "soft_close", 1, 72 * time.Hour, lookback, true},
		{"lookback=0 表示不限窗", "soft_close", 1, 1000 * time.Hour, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldPublishSessionClosed(tc.action, tc.rowsAffected, tc.idleFor, tc.lookback)
			if got != tc.want {
				t.Fatalf("shouldPublishSessionClosed(%q,%d,%v,%v) = %v, want %v",
					tc.action, tc.rowsAffected, tc.idleFor, tc.lookback, got, tc.want)
			}
		})
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
