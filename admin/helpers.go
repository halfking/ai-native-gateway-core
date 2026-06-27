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
