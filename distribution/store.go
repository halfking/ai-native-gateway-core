package distribution

import (
	"context"
	"time"
)

type Store interface {
	EnsureHolderByEmail(ctx context.Context, email, displayName string) (*LicenseHolder, error)
	GetHolder(ctx context.Context, id int64) (*LicenseHolder, error)
	ListHolders(ctx context.Context, offset, limit int, query string) ([]LicenseHolder, int, error)

	RecordDownloadEvent(ctx context.Context, ev *DownloadEvent) error
	UpdateDownloadResult(ctx context.Context, requestID, result string, durationMS int) error
	GetDownloadStats(ctx context.Context) (*DownloadStats, error)

	CreateDonation(ctx context.Context, d *Donation) error
	GetDonationByOrderNo(ctx context.Context, orderNo string) (*Donation, error)
	MarkDonationPaid(ctx context.Context, orderNo string) error
	CountSupporters(ctx context.Context) (int, error)

	ListArtifacts(ctx context.Context, version string) ([]ReleaseArtifact, error)
	UpsertArtifact(ctx context.Context, a *ReleaseArtifact) error
}

type CatalogProvider interface {
	LatestPublishedVersion(ctx context.Context) (version string, buildSeq int, publishedAt *time.Time, err error)
}
