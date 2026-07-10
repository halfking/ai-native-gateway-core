package autoupdate

import (
	"context"
	"time"
)

// Store 自动升级存储接口
type Store interface {
	// Release CRUD
	CreateRelease(ctx context.Context, rel *Release) error
	GetRelease(ctx context.Context, version string) (*Release, error)
	GetLatestRelease(ctx context.Context, channel Channel) (*Release, error)
	ListReleases(ctx context.Context, channel Channel, offset, limit int) ([]Release, int, error)
	UpdateReleaseStatus(ctx context.Context, id int64, published bool) error

	// Gray Release Rules
	CreateGrayRule(ctx context.Context, rule *GrayReleaseRule) error
	GetGrayRule(ctx context.Context, releaseID int64) (*GrayReleaseRule, error)
	UpdateGrayPhase(ctx context.Context, releaseID int64, phase Phase, percent int) error

	// Upgrade Logs
	CreateUpgradeLog(ctx context.Context, instanceID string, oldVer, newVer string) (int64, error)
	UpdateUpgradeLog(ctx context.Context, id int64, status string, err string, completedAt time.Time) error
	GetUpgradeHistory(ctx context.Context, instanceID string, limit int) ([]ReleaseStatus, error)

	// Instance Release Status
	GetInstanceStatus(ctx context.Context, instanceID string) (*ReleaseStatus, error)
	UpdateInstanceStatus(ctx context.Context, status *ReleaseStatus) error
}
