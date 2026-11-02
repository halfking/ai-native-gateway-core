// Package admin — helpers.go
//
// 共享给整个 admin 包使用的工具函数。
// 2026-06-27 audit fix: 把原本散落在 session_list.go / session_approval.go
// 的 formatDuration 合并到这里，避免重定义冲突。

package admin

import (
	"fmt"
	"math"
	"time"
)

// formatDuration 英文（短）格式：d/h/m/s 单一单位，>=24h 按天数四舍五入。
//
// 用于 session_list 等前端表格场景。
func formatDuration(d time.Duration) string {
	if d.Hours() >= 24 {
		days := int(math.Round(d.Hours() / 24))
		if days < 1 {
			days = 1
		}
		return fmt.Sprintf("%dd", days)
	}
	if d.Hours() >= 1 {
		return fmt.Sprintf("%.0fh", d.Hours())
	}
	if d.Minutes() >= 1 {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	return fmt.Sprintf("%.0fs", d.Seconds())
}

// formatDurationCN 中文（短）格式：秒/分钟/小时。
//
// 用于 session_approval 等面向中国大陆运维的界面。
func formatDurationCN(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d分钟", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1f小时", d.Hours())
}

// normalizeTimestamp 把数据库里的 timestamptz 归一成 **RFC3339** 文本。
//
// ★ 为什么 admin 面必须统一走这一个函数（2026-11-02 第十八轮 F-33）：
// 同一个 JSON key（availability_recover_at / quota_recover_at /
// effective_at / expires_at / cooling_until / last_sampled_at / …）在仓内
// **一半**端点给 RFC3339、另一半给 **PG 文本**（"2026-11-02 08:00:00+08"——
// 空格分隔、没有 'T'）⇒ 客户端得同时认两种写法。
//
// ★ PG 文本不是 RFC3339：ECMAScript 的 Date Time String Format 要求
// 'T' 分隔 ⇒ PG 文本落在**规范之外** ⇒ 能不能解析取决于引擎的**宽松
// 解析器**（V8 与 JSC 今天都收，但那是实现细节，不是规范保证）。
// ⇒ 统一成 RFC3339 之后，解析不再依赖引擎的宽容度。
//
// ★ 口径：**UTC + 秒精度**（time.RFC3339）——与本包既有的
// credential_monitor.go（`recoverAt.Format(time.RFC3339)`）以及
// routing.go 的 URSM 路径（`v.CoolUntil.UTC().Format(time.RFC3339)`）一致
// ⇒ **同一个响应里 DB 路径与 URSM 路径产出同一种格式**。
//
// ★ nil ⇒ nil（JSON 落 null）；零值 ⇒ nil（零值不是"某年 1 月 1 日"，
// 落进响应会是个假时刻）。
func normalizeTimestamp(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
