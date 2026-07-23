package release

import "time"

// Release 版本发布
type Release struct {
	ID          int64  `json:"id" db:"id"`
	Version     string `json:"version" db:"version"`
	FullVersion string `json:"full_version" db:"full_version"`
	BuildSeq    int    `json:"build_seq" db:"build_seq"`
	GitSHA      string `json:"git_sha" db:"git_sha"`
	BuildDate   string `json:"build_date" db:"build_date"`

	ReleaseType  string    `json:"release_type" db:"release_type"`
	ReleaseDate  time.Time `json:"release_date" db:"release_date"`
	ReleaseNotes string    `json:"release_notes" db:"release_notes"`
	ChangelogURL string    `json:"changelog_url" db:"changelog_url"`

	UpgradeFromVersions []string `json:"upgrade_from_versions" db:"upgrade_from_versions"`
	BreakingChanges     []string `json:"breaking_changes" db:"breaking_changes"`

	MinPostgresVersion string `json:"min_postgres_version" db:"min_postgres_version"`
	MinRedisVersion    string `json:"min_redis_version" db:"min_redis_version"`
	MinGoVersion       string `json:"min_go_version" db:"min_go_version"`
	MinNodeVersion     string `json:"min_node_version" db:"min_node_version"`

	Status        string `json:"status" db:"status"`
	IsPublic      bool   `json:"is_public" db:"is_public"`
	IsLatest      bool   `json:"is_latest" db:"is_latest"`
	DownloadCount int    `json:"download_count" db:"download_count"`
	ViewCount     int    `json:"view_count" db:"view_count"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
	CreatedBy string    `json:"created_by" db:"created_by"`
	UpdatedBy string    `json:"updated_by" db:"updated_by"`
}

// ReleaseFile 发布文件
type ReleaseFile struct {
	ID        int64  `json:"id" db:"id"`
	ReleaseID int64  `json:"release_id" db:"release_id"`
	Filename  string `json:"filename" db:"filename"`
	FilePath  string `json:"file_path" db:"file_path"`
	FileSize  int64  `json:"file_size" db:"file_size"`
	FileHash  string `json:"file_hash" db:"file_hash"`

	Platform string `json:"platform" db:"platform"`
	OS       string `json:"os" db:"os"`
	Arch     string `json:"arch" db:"arch"`

	DownloadURL   string `json:"download_url" db:"download_url"`
	ShareURL      string `json:"share_url" db:"share_url"`
	DownloadCount int    `json:"download_count" db:"download_count"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// ReleaseTest 测试记录
type ReleaseTest struct {
	ID         int64  `json:"id" db:"id"`
	ReleaseID  int64  `json:"release_id" db:"release_id"`
	TestType   string `json:"test_type" db:"test_type"`
	TestName   string `json:"test_name" db:"test_name"`
	TestStatus string `json:"test_status" db:"test_status"`

	TestOutput   string `json:"test_output" db:"test_output"`
	TestDuration int    `json:"test_duration" db:"test_duration"`
	ErrorMessage string `json:"error_message" db:"error_message"`

	TestEnvironment string    `json:"test_environment" db:"test_environment"`
	Tester          string    `json:"tester" db:"tester"`
	TestedAt        time.Time `json:"tested_at" db:"tested_at"`
}

// ReleaseOverview 版本概览（视图）
type ReleaseOverview struct {
	ID             int64     `json:"id" db:"id"`
	Version        string    `json:"version" db:"version"`
	FullVersion    string    `json:"full_version" db:"full_version"`
	ReleaseType    string    `json:"release_type" db:"release_type"`
	ReleaseDate    time.Time `json:"release_date" db:"release_date"`
	Status         string    `json:"status" db:"status"`
	IsPublic       bool      `json:"is_public" db:"is_public"`
	IsLatest       bool      `json:"is_latest" db:"is_latest"`
	TotalDownloads int       `json:"total_downloads" db:"total_downloads"`
	FileCount      int       `json:"file_count" db:"file_count"`
	TotalSize      int64     `json:"total_size" db:"total_size"`
	PassedTests    int       `json:"passed_tests" db:"passed_tests"`
	FailedTests    int       `json:"failed_tests" db:"failed_tests"`
	TotalTests     int       `json:"total_tests" db:"total_tests"`
}

// CreateReleaseRequest 创建版本请求
type CreateReleaseRequest struct {
	Version      string `json:"version" binding:"required"`
	FullVersion  string `json:"full_version" binding:"required"`
	BuildSeq     int    `json:"build_seq" binding:"required"`
	GitSHA       string `json:"git_sha" binding:"required"`
	BuildDate    string `json:"build_date" binding:"required"`
	ReleaseType  string `json:"release_type" binding:"required,oneof=stable beta alpha rc"`
	ReleaseNotes string `json:"release_notes"`
	ChangelogURL string `json:"changelog_url"`

	UpgradeFromVersions []string `json:"upgrade_from_versions"`
	BreakingChanges     []string `json:"breaking_changes"`

	MinPostgresVersion string `json:"min_postgres_version"`
	MinRedisVersion    string `json:"min_redis_version"`
	MinGoVersion       string `json:"min_go_version"`
	MinNodeVersion     string `json:"min_node_version"`

	IsPublic bool `json:"is_public"`
}

// AddFileRequest 添加文件请求
type AddFileRequest struct {
	Filename    string `json:"filename" binding:"required"`
	FilePath    string `json:"file_path" binding:"required"`
	FileSize    int64  `json:"file_size" binding:"required"`
	FileHash    string `json:"file_hash" binding:"required"`
	Platform    string `json:"platform" binding:"required,oneof=host docker"`
	OS          string `json:"os" binding:"required"`
	Arch        string `json:"arch" binding:"required"`
	DownloadURL string `json:"download_url"`
	ShareURL    string `json:"share_url"`
}

// AddTestRequest 添加测试记录请求
type AddTestRequest struct {
	TestType        string `json:"test_type" binding:"required"`
	TestName        string `json:"test_name" binding:"required"`
	TestStatus      string `json:"test_status" binding:"required,oneof=passed failed skipped"`
	TestOutput      string `json:"test_output"`
	TestDuration    int    `json:"test_duration"`
	ErrorMessage    string `json:"error_message"`
	TestEnvironment string `json:"test_environment"`
	Tester          string `json:"tester"`
}

// ListReleasesQuery 列表查询参数
type ListReleasesQuery struct {
	Status      string `form:"status"`
	ReleaseType string `form:"release_type"`
	IsPublic    *bool  `form:"is_public"`
	Page        int    `form:"page" binding:"min=1"`
	PageSize    int    `form:"page_size" binding:"min=1,max=100"`
}
