package providerprofile

import (
	"context"
	"time"
)

// MetricsStore 指标数据存储接口
type MetricsStore interface {
	// SaveSnapshot 保存采集快照
	SaveSnapshot(ctx context.Context, snapshot *MetricSnapshot) error

	// GetSnapshotsByDateRange 获取指定时间范围的快照
	GetSnapshotsByDateRange(ctx context.Context, credentialID int64, start, end time.Time) ([]*MetricSnapshot, error)

	// CleanupOldMetrics 清理过期数据（7天前）
	CleanupOldMetrics(ctx context.Context) (int64, error)
}

// ProfileStore 画像数据存储接口
type ProfileStore interface {
	// SaveDailyProfile 保存天级画像
	SaveDailyProfile(ctx context.Context, profile *DailyProfile) error

	// GetDailyProfile 获取指定日期的画像
	GetDailyProfile(ctx context.Context, credentialID int64, date time.Time) (*DailyProfile, error)

	// GetRecentProfiles 获取最近N天的画像
	GetRecentProfiles(ctx context.Context, credentialID int64, days int) ([]*DailyProfile, error)

	// GetProfilesByProvider 获取供应商所有credential的最新画像
	GetProfilesByProvider(ctx context.Context, providerID int64) ([]*DailyProfile, error)
}
