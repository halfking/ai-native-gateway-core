package bg

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// ── 2026-10-12：session 轮遇 advisory lock 冲突改为退避重试 ───────────────────
//
// 缺陷形态：refreshView 的 (bool, error) 返回把「视图不存在」和「别的实例正
// 在刷」压成同一个 true，于是调用方只能二选一——全不重试（现状，撞锁就整轮
// 跳过）或全重试（那会把一个真正缺失的视图也重试到超时）。§10.106.37 §六的
// 读数钉住了代价：2026-10-12 02:00 那一轮三个视图全部 skipped_no_lock，
// MV 数据陈旧 2 小时。
//
// 这里测的是**策略**（retryOnLockHeld），不是 refreshView：锁冲突是两个进程
// 加一把 Postgres advisory lock 的性质，单测造不出来，能造出来的只有源码
// 文本断言。策略函数把 attempt 做成参数，于是下面每条都能真的驱动一次
// 「撞锁 → 退避 → 拿到锁」的完整路径。

// TestRetryOnLockHeld_RetriesUntilTheLockIsFree：撞锁必须重试，且拿到锁后立即停。
//
// calls==3 是这条判据的承重点：它同时钉住「确实重试了两次」和「拿到锁后
// 没有继续重试」。只断言最终状态是 mvRefreshed 是不够的——那和「一次就成功」
// 同形，而这正是回归后的形态。
func TestRetryOnLockHeld_RetriesUntilTheLockIsFree(t *testing.T) {
	calls := 0
	got, err := retryOnLockHeldWithBackoff(context.Background(), "session_task_stats",
		time.Now().Add(30*time.Second), time.Millisecond, 4*time.Millisecond,
		func() (mvSkipReason, error) {
			calls++
			if calls <= 2 {
				return mvSkippedLockHeld, nil
			}
			return mvRefreshed, nil
		})
	if err != nil {
		t.Fatalf("重试不应返回错误：%v", err)
	}
	if calls != 3 {
		t.Fatalf("撞锁 2 次后应恰好再试到成功，attempt 被调用 %d 次（期望 3）", calls)
	}
	if got != mvRefreshed {
		t.Fatalf("拿到锁后应返回 mvRefreshed，实际 %v", got)
	}
}

// TestNextLockRetryBackoff_DoublesThenClamps：退避序列本身按值断言。
//
// 不靠「睡一会儿再量」来测——那种判据在机器繁忙时会因为别的原因通过，
// 而本条要防的是「封顶被摘掉」这种只有长跑才暴露的退化。
func TestNextLockRetryBackoff_DoublesThenClamps(t *testing.T) {
	const max = 15 * time.Second
	cases := []struct {
		cur  time.Duration
		want time.Duration
	}{
		{2 * time.Second, 4 * time.Second},
		{4 * time.Second, 8 * time.Second},
		{8 * time.Second, max},  // 翻倍到 16s，必须被截到 15s
		{max, max},              // 已封顶就不再涨
		{14 * time.Second, max}, // 单步就会越过封顶
	}
	for _, c := range cases {
		if got := nextLockRetryBackoff(c.cur, max); got != c.want {
			t.Fatalf("nextLockRetryBackoff(%s, %s) = %s，期望 %s", c.cur, max, got, c.want)
		}
	}
	// 生产常量必须真的封顶，否则上面的表是在测一个没人用的数。
	if nextLockRetryBackoff(mvLockRetryMaxBackoff, mvLockRetryMaxBackoff) != mvLockRetryMaxBackoff {
		t.Fatalf("生产退避封顶 %s 没有自封顶", mvLockRetryMaxBackoff)
	}
	if mvLockRetryInitialBackoff >= mvLockRetryMaxBackoff {
		t.Fatalf("退避起点 %s 必须小于封顶 %s", mvLockRetryInitialBackoff, mvLockRetryMaxBackoff)
	}
}

// TestRetryOnLockHeld_DoesNotRetryAMissingView：只有锁冲突才重试。
//
// 这是本文件里那条「允许侧必须有前提断言」的同构：若把判定写成
// 「只要 skipped 就重试」，最终状态同样可能是 mvRefreshed，
// 只有把 attempt 的调用次数钉成 1 才分得开。
func TestRetryOnLockHeld_DoesNotRetryAMissingView(t *testing.T) {
	calls := 0
	got, err := retryOnLockHeld(context.Background(), "session_task_stats",
		time.Now().Add(30*time.Second), func() (mvSkipReason, error) {
			calls++
			return mvSkippedMissingView, nil
		})
	if err != nil {
		t.Fatalf("不应返回错误：%v", err)
	}
	if calls != 1 {
		t.Fatalf("视图不存在不该重试（30s 内它也不会出现），attempt 被调用 %d 次（期望 1）", calls)
	}
	if got != mvSkippedMissingView {
		t.Fatalf("应原样返回 mvSkippedMissingView，实际 %v", got)
	}
}

// TestRetryOnLockHeld_DoesNotRetryOnError：报错不是锁冲突，不重试。
func TestRetryOnLockHeld_DoesNotRetryOnError(t *testing.T) {
	boom := errors.New("connection reset")
	calls := 0
	_, err := retryOnLockHeld(context.Background(), "session_task_stats",
		time.Now().Add(30*time.Second), func() (mvSkipReason, error) {
			calls++
			return mvRefreshed, boom
		})
	if !errors.Is(err, boom) {
		t.Fatalf("应把错误原样返回，实际 %v", err)
	}
	if calls != 1 {
		t.Fatalf("错误不该重试，attempt 被调用 %d 次（期望 1）", calls)
	}
}

// TestRetryOnLockHeld_StopsWhenTheBudgetIsSpent：预算花完就收手。
//
// 退避起点是 2s，而预算给 40ms —— 正确实现必须把等待**截断到预算内**
// （本轮真正要测的就是截断），所以整轮只花 40ms 量级。
// 去掉截断的实现会睡满 2s 才醒，被 elapsed 上限抓住。
func TestRetryOnLockHeld_StopsWhenTheBudgetIsSpent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	calls := 0
	start := time.Now()
	got, err := retryOnLockHeld(ctx, "session_task_stats",
		time.Now().Add(40*time.Millisecond), func() (mvSkipReason, error) {
			calls++
			return mvSkippedLockHeld, nil
		})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("预算耗尽不是错误：%v", err)
	}
	if got != mvSkippedLockHeld {
		t.Fatalf("预算耗尽应返回 mvSkippedLockHeld，实际 %v", got)
	}
	if calls < 2 {
		t.Fatalf("预算内至少应重试过一次，attempt 只被调用 %d 次", calls)
	}
	if elapsed > time.Second {
		t.Fatalf("等待未截断到预算内：花掉 %s（退避起点就是 %s）", elapsed, mvLockRetryInitialBackoff)
	}
}

// TestRetryOnLockHeld_StopsWhenTheContextIsCancelled：ctx 一停就必须收手。
//
// 部署摘流量时 refreshLoop 依赖 ctx 退出；一个只认 deadline 不认 ctx 的
// 实现会把 Stop() 拖到预算走完。
func TestRetryOnLockHeld_StopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	start := time.Now()
	_, err := retryOnLockHeld(ctx, "session_task_stats",
		time.Now().Add(30*time.Second), func() (mvSkipReason, error) {
			calls++
			return mvSkippedLockHeld, nil
		})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("ctx 取消不是错误：%v", err)
	}
	if calls != 1 {
		t.Fatalf("ctx 已取消时不应再重试，attempt 被调用 %d 次（期望 1）", calls)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("ctx 取消后应立即返回，实际花掉 %s", elapsed)
	}
}

// TestSessionViewLockRetryBudget_FitsInsideTheCycleBudget：预算的两个不等式。
//
// 与同文件既有的 TestSessionViewsTimeout_ExceedsMeasuredRefresh 同族：
// 这类常量不是「给大点保险」，两个方向给错都会静默失败——
// 给小 ⇒ 整轮被 ctx 静默掐断；给大 ⇒ 重试拖到下一轮周期里。
func TestSessionViewLockRetryBudget_FitsInsideTheCycleBudget(t *testing.T) {
	// 实测最长一轮 195s（§10.106.37 §三，245 03:00 轮）。
	const measuredWorstRound = 200 * time.Second
	if SessionViewLockRetryBudget+measuredWorstRound >= SessionViewsTimeout {
		t.Fatalf("重试预算 %s + 实测最长一轮 %s 已达/超过轮内超时 %s —— 重试会把失败形态从「跳过」换成「被 ctx 静默掐断」",
			SessionViewLockRetryBudget, measuredWorstRound, SessionViewsTimeout)
	}
	if SessionViewLockRetryBudget >= SessionViewsInterval {
		t.Fatalf("重试预算 %s 必须小于周期 %s，否则重试会拖进下一轮", SessionViewLockRetryBudget, SessionViewsInterval)
	}
}

// TestRefreshSessionViews_RetriesThroughTheWrapper：接线必须真的走重试包装。
//
// 行为判据（上面六条）测的是 retryOnLockHeld 本身；这条钉的是「session 轮
// 调的是它，而不是裸的 refreshView」。这是源码文本断言——本仓这一族测试的
// 既有做法（见 materialised_view_refresher_session_test.go），它的强度只到
// 「调用点换没换」，换不掉的是 attempt 闭包里的实现，那由上面六条负责。
func TestRefreshSessionViews_RetriesThroughTheWrapper(t *testing.T) {
	b, err := os.ReadFile("materialized_view_refresher.go")
	if err != nil {
		t.Fatalf("读源文件：%v", err)
	}
	src := string(b)
	at := strings.Index(src, "func (r *MaterializedViewRefresher) refreshSessionViews(")
	if at < 0 {
		t.Fatal("没找到 refreshSessionViews")
	}
	end := strings.Index(src[at:], "\n}\n")
	if end < 0 {
		t.Fatal("refreshSessionViews 抽取失败")
	}
	body := src[at : at+end]

	if !strings.Contains(body, "retryOnLockHeld(") {
		t.Fatal("refreshSessionViews 没有走 retryOnLockHeld —— 撞锁后仍然整轮跳过（§10.106.37 §六的形态）")
	}
	if !strings.Contains(body, "SessionViewLockRetryBudget") {
		t.Fatal("refreshSessionViews 没有引用整轮共享的重试预算 —— 预算会退化成每视图一份，乘以三倍")
	}
	// 预算必须整轮共享：在循环外算一次 deadline，而不是循环内每次 Add。
	deadlineAt := strings.Index(body, "retryDeadline := time.Now().Add(")
	loopAt := strings.Index(body, "for _, view := range SessionAnalyticsViews")
	if deadlineAt < 0 {
		t.Fatal("refreshSessionViews 里没有 retryDeadline")
	}
	if loopAt < 0 {
		t.Fatal("refreshSessionViews 里没有视图循环")
	}
	if deadlineAt > loopAt {
		t.Fatal("retryDeadline 在循环内计算 —— 每个视图各拿一份预算，三视图最坏 3 倍")
	}
	// 裸调用不能留在 session 轮里（routing 轮保留裸调用是对的，它有自己的预算）。
	if strings.Contains(body, "r.refreshView(ctx, view, true)") &&
		!strings.Contains(body, "return r.refreshView(ctx, view, true)") {
		t.Fatal("session 轮里还留着裸的 r.refreshView(ctx, view, true) —— 绕过了重试包装")
	}
}
