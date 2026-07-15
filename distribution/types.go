package distribution

import "time"

type HolderType string

const (
	HolderIndividual   HolderType = "individual"
	HolderOrganization HolderType = "organization"
)

type LicenseHolder struct {
	ID             int64      `json:"id"`
	Email          string     `json:"email"`
	DisplayName    string     `json:"display_name"`
	HolderType     HolderType `json:"holder_type"`
	ConsentVersion string     `json:"consent_version,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
	LicenseCount   int        `json:"license_count,omitempty"`
	DeviceCount    int        `json:"device_count,omitempty"`
	DonationTotal  int        `json:"donation_total_cents,omitempty"`
}

type DownloadEvent struct {
	ID             int64      `json:"id"`
	RequestID      string     `json:"request_id"`
	ReleaseVersion string     `json:"release_version"`
	Platform       string     `json:"platform"`
	Arch           string     `json:"arch"`
	Edition        string     `json:"edition"`
	Channel        string     `json:"channel"`
	HolderID       *int64     `json:"holder_id,omitempty"`
	DonationID     *int64     `json:"donation_id,omitempty"`
	Result         string     `json:"result"`
	DurationMS     *int       `json:"duration_ms,omitempty"`
	Source         string     `json:"source"`
	CreatedAt      time.Time  `json:"created_at"`
}

type Donation struct {
	ID          int64      `json:"id"`
	OrderNo     string     `json:"order_no"`
	HolderID    *int64     `json:"holder_id,omitempty"`
	Email       string     `json:"email"`
	AmountCents int        `json:"amount_cents"`
	Currency    string     `json:"currency"`
	Channel     string     `json:"channel"`
	Status      string     `json:"status"`
	TierLabel   string     `json:"tier_label"`
	PaidAt      *time.Time `json:"paid_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type ReleaseArtifact struct {
	ID             int64  `json:"id"`
	ReleaseVersion string `json:"release_version"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
	Edition        string `json:"edition"`
	ArtifactName   string `json:"artifact_name"`
	SHA256         string `json:"sha256"`
	SizeBytes      int64  `json:"size_bytes"`
	DownloadPath   string `json:"download_path"`
}

type CatalogItem struct {
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
	Label          string `json:"label"`
	ArtifactName   string `json:"artifact_name"`
	SHA256         string `json:"sha256,omitempty"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
	SizeLabel      string `json:"size_label,omitempty"`
}

type VersionGroup struct {
	Version       string        `json:"version"`
	BuildSeq      int           `json:"build_seq"`
	ReleaseDate   string        `json:"release_date,omitempty"`
	Items         []CatalogItem `json:"items"`
	InstallDocURL string        `json:"install_doc_url,omitempty"`
}

type CatalogResponse struct {
	Version      string         `json:"version"`
	BuildSeq     int            `json:"build_seq"`
	Channel      string         `json:"channel"`
	ReleaseDate  string         `json:"release_date,omitempty"`
	Items        []CatalogItem  `json:"items"`
	Versions     []VersionGroup `json:"versions,omitempty"`
	Supporters   int            `json:"supporters"`
	GitRepoURL   string         `json:"git_repo_url,omitempty"`
	GitBranch    string         `json:"git_branch,omitempty"`
	DocsURL      string         `json:"docs_url,omitempty"`
	ContactEmail string         `json:"contact_email,omitempty"`
}

type DownloadTicket struct {
	RequestID  string    `json:"request_id"`
	URL        string    `json:"url"`
	ExpiresAt  time.Time `json:"expires_at"`
	SHA256     string    `json:"sha256,omitempty"`
	FileName   string    `json:"file_name"`
}

type DownloadStats struct {
	TodayDownloads   int     `json:"today_downloads"`
	WeekDownloads    int     `json:"week_downloads"`
	TotalDownloads   int     `json:"total_downloads"`
	TodayDonations   int     `json:"today_donations"`
	TotalDonations   int     `json:"total_donations"`
	DonationAmount   int     `json:"donation_amount_cents"`
	ActivationRate   float64 `json:"activation_rate_pct"`
	SupporterCount   int     `json:"supporter_count"`
}
