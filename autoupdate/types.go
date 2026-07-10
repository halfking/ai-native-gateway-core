package autoupdate

import "time"

type Channel string

const (
	ChannelStable Channel = "stable"
	ChannelBeta   Channel = "beta"
	ChannelCanary Channel = "canary"
)

type Phase string

const (
	PhaseCanary Phase = "canary"
	PhaseBatch1 Phase = "batch_1"
	PhaseBatch2 Phase = "batch_2"
	PhaseBatch3 Phase = "batch_3"
	PhaseFull   Phase = "full"
)

type Release struct {
	ID          int64      `json:"id"`
	Version     string     `json:"version"`
	BuildSeq    int        `json:"build_seq"`
	Channel     Channel    `json:"channel"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Changelog   string     `json:"changelog"`
	ImageTag    string     `json:"image_tag"`
	ImageDigest string     `json:"image_digest,omitempty"`
	MinVersion  string     `json:"min_version,omitempty"`
	Mandatory   bool       `json:"mandatory"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

type GrayReleaseRule struct {
	ID        int64     `json:"id"`
	ReleaseID int64     `json:"release_id"`
	Phase     Phase     `json:"phase"`
	Percent   int       `json:"percent"`
	Selectors []byte    `json:"selectors,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type ReleaseStatus struct {
	ReleaseID   int64      `json:"release_id"`
	InstanceID  string     `json:"instance_id"`
	Status      string     `json:"status"`
	Version     string     `json:"version"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	RetryCount  int        `json:"retry_count"`
}

const (
	StatusPending   = "pending"
	StatusDownload  = "downloading"
	StatusReady     = "ready_to_restart"
	StatusUpgrading = "upgrading"
	StatusSuccess   = "success"
	StatusFailed    = "failed"
	StatusRollback  = "rolled_back"
)

type VersionInfo struct {
	Version   string `json:"version"`
	BuildSeq  int    `json:"build_seq"`
	Commit    string `json:"commit,omitempty"`
	BuiltAt   string `json:"built_at,omitempty"`
	GoVersion string `json:"go_version,omitempty"`
}

type UpgradePlan struct {
	CurrentVersion string        `json:"current_version"`
	TargetVersion  string        `json:"target_version"`
	Steps          []UpgradeStep `json:"steps"`
	EstimatedSecs  int           `json:"estimated_secs"`
}

type UpgradeStep struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Optional    bool   `json:"optional"`
}
