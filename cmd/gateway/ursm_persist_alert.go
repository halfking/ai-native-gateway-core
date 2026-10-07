package main

// URSM v2 persist 写入失败的日志定级（2026-10-06，审计 §10.61）
//
// 起因是一次真实的静默数据丢失：2026-10-03 23:32 起，
// ursm_node_snapshot_min 的写入**每分钟失败一次、连续 701 次、共 62 小时**，
// 而这条失败只记 `slog.Warn` ⇒ 没有任何告警通道被触发，
// 表最终 0 行（根因见 runbook §10.59：已部署主键缺 tenant_id，SQLSTATE 42P10）。
//
// ★ 关键在于「WARN 不是告警」这件事：日志级别只是**给告警规则用的输入**，
//   级别停在 WARN，规则就不会匹配，于是 701 次失败在告警侧等于零次。
//   与「累计值不等于当前值」同族 —— 一条被记录下来的错误，不等于它被听见了。
//
// 为什么把判定抽成函数：级别写在调用点是测不到的。
// 本项目已有「测函数不测接线 ⇒ 门绿而生产没改」的教训（变异 M43），
// 所以这里既要有能单测的判定，也要有断言**调用点真的用了它**的门。

import (
	"log/slog"
)

// ursmPersistFlushContinuedStreak = 从第几次连续失败起，
// 改用「持续停摆」措辞并带上总时长量纲。
//
// ★ 为什么是 3 而不是 1：一次 flush 失败可能只是事务超时抖动，
//
//	直接喊「持续停摆」会训练人忽略它。但 3 次（默认周期下约 3 分钟）
//	仍然可能只是抖动 —— 真正的界线其实在分钟级。
//	保留这个阈值是为了在**不制造噪音**的前提下让持续停摆在日志里自成一类，
//	便于告警规则对两者区别处理，而不是一律同权。
const ursmPersistFlushContinuedStreak = 3

// ursmPersistFlushFailure 决定一次 flush 失败该怎么记。
//
// 返回：日志级别、消息、键值对。
// streak 是**含本次**的连续失败次数（由调用方维护，>= 1）。
//
// 契约（ursm_persist_alert_test.go 逐条钉住）：
//  1. 任何 streak >= 1 都必须是 Error —— 绝不返回 Warn。
//     这条是本文件存在的唯一理由；把它改回 Warn 就等于把 62 小时静音装回去。
//  2. streak 达阈值后消息必须与单次失败**不同**，
//     否则「一次抖动」与「永久停摆」在日志里同形，无法分级告警。
func ursmPersistFlushFailure(streak int, err error) (slog.Level, string, []any) {
	if streak < 1 {
		// 不该发生：调用方只在失败时才会走到这里。
		// 但仍不返回 Warn —— 宁可噪声，也不要静音。
		streak = 1
	}
	args := []any{
		"error", err,
		"streak", streak,
	}
	if streak >= ursmPersistFlushContinuedStreak {
		return slog.LevelError,
			"ursm.v2: persist flush FAILED CONTINUOUSLY (sustained outage, data not being written)",
			args
	}
	return slog.LevelError, "ursm.v2: persist flush failed", args
}
