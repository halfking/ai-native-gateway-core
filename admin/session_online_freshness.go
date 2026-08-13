// Package admin — session_online_freshness.go
//
// Freshness 计算与 stale 标记（V3.2 LP6）。
//
// 契约（13号文档§7）：
//   - data_source: hot / merged / v2_archive
//   - freshness_ms: 数据新鲜度（now - updated_at），单位毫秒
//   - stale: bool，是否过期（freshness_ms > 阈值）
//
// 阈值定义：
//   - hot: 5分钟内视为新鲜
//   - merged: 1小时内视为新鲜
//   - v2_archive: 全部视为 stale
package admin

import (
	"time"
)

// DataSource 是数据来源类型。
type DataSource string

const (
	DataSourceHot       DataSource = "hot"
	DataSourceMerged    DataSource = "merged"
	DataSourceV2Archive DataSource = "v2_archive"
)

// FreshnessInfo 是数据新鲜度信息。
type FreshnessInfo struct {
	DataSource  DataSource `json:"data_source"`  // hot / merged / v2_archive
	FreshnessMs int64      `json:"freshness_ms"` // 新鲜度（毫秒）
	Stale       bool       `json:"stale"`        // 是否过期
}

// 新鲜度阈值（毫秒）
const (
	hotFreshnessThresholdMs    = 5 * 60 * 1000  // 5分钟
	mergedFreshnessThresholdMs = 60 * 60 * 1000 // 1小时
)

// CalculateFreshness 计算数据新鲜度。
//
// dataSource: 数据来源
// updatedAt: 数据最后更新时间
// now: 当前时间（用于测试注入）
//
// 返回：freshness_ms（now - updatedAt）和 stale 标记。
func CalculateFreshness(dataSource DataSource, updatedAt time.Time, now time.Time) FreshnessInfo {
	if updatedAt.IsZero() {
		// 无更新时间，视为极旧（1年）
		return FreshnessInfo{
			DataSource:  dataSource,
			FreshnessMs: 365 * 24 * 60 * 60 * 1000,
			Stale:       true,
		}
	}

	freshnessMs := now.Sub(updatedAt).Milliseconds()
	if freshnessMs < 0 {
		freshnessMs = 0 // 时钟回拨保护
	}

	stale := isStale(dataSource, freshnessMs)

	return FreshnessInfo{
		DataSource:  dataSource,
		FreshnessMs: freshnessMs,
		Stale:       stale,
	}
}

// isStale 判断数据是否过期。
func isStale(dataSource DataSource, freshnessMs int64) bool {
	switch dataSource {
	case DataSourceHot:
		return freshnessMs > hotFreshnessThresholdMs
	case DataSourceMerged:
		return freshnessMs > mergedFreshnessThresholdMs
	case DataSourceV2Archive:
		return true // v2_archive 全部视为 stale
	default:
		return true // 未知来源，保守标记为 stale
	}
}
