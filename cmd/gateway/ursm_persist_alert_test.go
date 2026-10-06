package main

// §10.61 的判据：persist 写入失败的日志定级。
//
// 这道门钉三件事，缺一不可：
//  1. 任何 flush 失败都不得记 Warn（WARN 不是告警，62h 静音的根因）
//  2. 连续失败达阈值后措辞必须与单次失败不同（否则无法分级告警）
//  3. ★ main.go 的调用点**真的用了**这个判定（变异 M43 的教训：
//     只测函数不测接线 ⇒ 门全绿而生产仍记 Warn）

import (
	"errors"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPersistFlushFailureIsNeverWarn(t *testing.T) {
	err := errors.New("boom")
	for streak := 1; streak <= 10; streak++ {
		lvl, msg, args := ursmPersistFlushFailure(streak, err)
		if lvl < slog.LevelError {
			t.Errorf("streak=%d 的级别是 %v，期望 >= Error。\n"+
				"★ 本次事故就是这里：701 次写入全丢、62 小时、零告警。\n"+
				"   级别停在 WARN，告警规则就不匹配 —— 一条被记下来的错误不等于它被听见了。",
				streak, lvl)
		}
		if !strings.HasPrefix(msg, "ursm.v2: persist flush") {
			t.Errorf("streak=%d 的消息 %q 应保留可检索前缀（现场就是靠 grep 这串定位的）", streak, msg)
		}
		// streak 必须出现在结构化字段里，否则运维只能靠数行数
		if !hasArg(args, "streak") {
			t.Errorf("streak=%d 的日志参数里没有 streak 字段：%v", streak, args)
		}
	}
	// streak=0 也不许降级成 Warn（虽然调用方不该走到这里）
	if lvl, _, _ := ursmPersistFlushFailure(0, err); lvl < slog.LevelError {
		t.Errorf("streak=0 的级别是 %v，期望 >= Error（宁可噪声也不要静音）", lvl)
	}
}

func TestPersistFlushContinuedIsDistinguishable(t *testing.T) {
	err := errors.New("boom")
	once, onceMsg, _ := ursmPersistFlushFailure(1, err)
	cont, contMsg, _ := ursmPersistFlushFailure(ursmPersistFlushContinuedStreak, err)
	if onceMsg == contMsg {
		t.Fatalf("连续失败与单次失败的消息相同：%q\n"+
			"★ 单次抖动和永久停摆在告警规则眼里必须能分开，否则要么一直吵、要么一直没听见。",
			onceMsg)
	}
	// 「不得低于」而不是「必须高于」：持续停摆**不应该被降级**成更轻的级别。
	// 要求严格高于会把契约变成「必须 ERROR+1」这种无法实施的形状。
	if cont < once {
		t.Errorf("持续停摆的级别 %v 低于单次失败 %v —— 越严重的事故级别越轻，读日志的人会先看错的那条", cont, once)
	}
	// 阈值本身不能是 1，否则「措辞不同」就等于每次都喊持续停摆
	if ursmPersistFlushContinuedStreak < 2 {
		t.Errorf("持续阈值是 %d —— 阈值为 1 会让每��抖动都喊「持续停摆」，"+
			"正好制造我们要避免的告警噪音。", ursmPersistFlushContinuedStreak)
	}
}

func hasArg(args []any, key string) bool {
	for i := 0; i+1 < len(args); i += 2 {
		if s, ok := args[i].(string); ok && s == key {
			return true
		}
	}
	return false
}

// TestFlushFailureCallSiteIsWired —— 接线门。
//
// 只测 ursmPersistFlushFailure 是不够的：把 main.go 的调用点改回
// slog.Warn 之后，上面所有单测**依然全绿**，而生产仍然静音 62 小时。
// 这正是本项目已踩过的形态（变异 M43：「只测函数不测接线」）。
var flushWarnCallSite = regexp.MustCompile(
	`(?m)^\s*if err := persistWriter\.Flush\(ctx, rows\); err != nil \{` +
		`[\s\S]{0,400}?slog\.Warn\(`)

func TestFlushFailureCallSiteIsWired(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if flushWarnCallSite.Match(b) {
		t.Errorf("main.go 的 flush 失败分支里仍出现 slog.Warn —— 级别没真正改到生产路径上。\n"+
			"★ 本次事故的根因就在这里；函数测得再对，调用点没换就是零效果。")
	}
	if !strings.Contains(string(b), "ursmPersistFlushFailure(") {
		t.Errorf("main.go 的 flush 失败分支没有调用 ursmPersistFlushFailure —— " +
			"连续失败计数与「持续停摆」措辞不会生效。")
	}
	// 成功一次必须清零，否则「持续停摆」会在多次偶发失败后永久挂着
	if !regexp.MustCompile(`flushFailStreak = 0`).Match(b) {
		t.Errorf("main.go 里找不到 flushFailStreak = 0 —— 成功一次后计数不清零的话，"+
			"「持续停摆」会在若干次偶发失败后一直成立。")
	}
}
