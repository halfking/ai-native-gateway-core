package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Repository 版本发布仓储
type Repository struct {
	db *sqlx.DB
}

// NewRepository 创建仓储
func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// CreateRelease 创建版本
func (r *Repository) CreateRelease(ctx context.Context, release *Release) error {
	// 序列化 JSONB 字段
	upgradeFromJSON, _ := json.Marshal(release.UpgradeFromVersions)
	breakingChangesJSON, _ := json.Marshal(release.BreakingChanges)

	query := `
		INSERT INTO releases (
			version, full_version, build_seq, git_sha, build_date,
			release_type, release_notes, changelog_url,
			upgrade_from_versions, breaking_changes,
			min_postgres_version, min_redis_version, min_go_version, min_node_version,
			status, is_public, created_by
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8,
			$9, $10,
			$11, $12, $13, $14,
			$15, $16, $17
		)
		RETURNING id, created_at, updated_at
	`

	err := r.db.QueryRowContext(
		ctx, query,
		release.Version, release.FullVersion, release.BuildSeq, release.GitSHA, release.BuildDate,
		release.ReleaseType, release.ReleaseNotes, release.ChangelogURL,
		upgradeFromJSON, breakingChangesJSON,
		release.MinPostgresVersion, release.MinRedisVersion, release.MinGoVersion, release.MinNodeVersion,
		release.Status, release.IsPublic, release.CreatedBy,
	).Scan(&release.ID, &release.CreatedAt, &release.UpdatedAt)

	return err
}

// GetReleaseByID 根据ID获取版本
func (r *Repository) GetReleaseByID(ctx context.Context, id int64) (*Release, error) {
	var release Release
	query := `SELECT * FROM releases WHERE id = $1`
	err := r.db.GetContext(ctx, &release, query, id)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("release not found")
	}
	return &release, err
}

// GetReleaseByVersion 根据版本号获取
func (r *Repository) GetReleaseByVersion(ctx context.Context, version string) (*Release, error) {
	var release Release
	query := `SELECT * FROM releases WHERE version = $1 ORDER BY build_seq DESC LIMIT 1`
	err := r.db.GetContext(ctx, &release, query, version)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("release not found")
	}
	return &release, err
}

// GetLatestRelease 获取最新版本
func (r *Repository) GetLatestRelease(ctx context.Context, isPublic bool) (*Release, error) {
	var release Release
	query := `
		SELECT * FROM releases 
		WHERE status = 'published' AND is_public = $1
		ORDER BY release_date DESC, build_seq DESC
		LIMIT 1
	`
	err := r.db.GetContext(ctx, &release, query, isPublic)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no published release found")
	}
	return &release, err
}

// ListReleases 列表查询
func (r *Repository) ListReleases(ctx context.Context, query ListReleasesQuery) ([]ReleaseOverview, int, error) {
	// 构建查询条件
	where := "WHERE 1=1"
	args := []interface{}{}
	argIndex := 1

	if query.Status != "" {
		where += fmt.Sprintf(" AND status = $%d", argIndex)
		args = append(args, query.Status)
		argIndex++
	}

	if query.ReleaseType != "" {
		where += fmt.Sprintf(" AND release_type = $%d", argIndex)
		args = append(args, query.ReleaseType)
		argIndex++
	}

	if query.IsPublic != nil {
		where += fmt.Sprintf(" AND is_public = $%d", argIndex)
		args = append(args, *query.IsPublic)
		argIndex++
	}

	// 查询总数
	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM releases %s", where)
	err := r.db.GetContext(ctx, &total, countQuery, args...)
	if err != nil {
		return nil, 0, err
	}

	// 分页查询
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 100 {
		query.PageSize = 20
	}

	offset := (query.Page - 1) * query.PageSize

	listQuery := fmt.Sprintf(`
		SELECT * FROM v_releases_overview
		%s
		ORDER BY release_date DESC, id DESC
		LIMIT $%d OFFSET $%d
	`, where, argIndex, argIndex+1)

	args = append(args, query.PageSize, offset)

	var releases []ReleaseOverview
	err = r.db.SelectContext(ctx, &releases, listQuery, args...)
	if err != nil {
		return nil, 0, err
	}

	return releases, total, nil
}

// UpdateReleaseStatus 更新状态
func (r *Repository) UpdateReleaseStatus(ctx context.Context, id int64, status string, updatedBy string) error {
	query := `
		UPDATE releases 
		SET status = $1, updated_by = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $3
	`
	_, err := r.db.ExecContext(ctx, query, status, updatedBy, id)
	return err
}

// SetLatestRelease 设置为最新版本
func (r *Repository) SetLatestRelease(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 清除其他版本的 is_latest 标记
	_, err = tx.ExecContext(ctx, "UPDATE releases SET is_latest = false WHERE is_latest = true")
	if err != nil {
		return err
	}

	// 设置当前版本为最新
	_, err = tx.ExecContext(ctx, "UPDATE releases SET is_latest = true WHERE id = $1", id)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// IncrementDownloadCount 增加下载次数
func (r *Repository) IncrementDownloadCount(ctx context.Context, id int64) error {
	query := `UPDATE releases SET download_count = download_count + 1 WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

// AddFile 添加文件
func (r *Repository) AddFile(ctx context.Context, file *ReleaseFile) error {
	query := `
		INSERT INTO release_files (
			release_id, filename, file_path, file_size, file_hash,
			platform, os, arch, download_url, share_url
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10
		)
		RETURNING id, created_at, updated_at
	`

	err := r.db.QueryRowContext(
		ctx, query,
		file.ReleaseID, file.Filename, file.FilePath, file.FileSize, file.FileHash,
		file.Platform, file.OS, file.Arch, file.DownloadURL, file.ShareURL,
	).Scan(&file.ID, &file.CreatedAt, &file.UpdatedAt)

	return err
}

// GetFilesByReleaseID 获取版本的所有文件
func (r *Repository) GetFilesByReleaseID(ctx context.Context, releaseID int64) ([]ReleaseFile, error) {
	var files []ReleaseFile
	query := `SELECT * FROM release_files WHERE release_id = $1 ORDER BY platform, os, arch`
	err := r.db.SelectContext(ctx, &files, query, releaseID)
	return files, err
}

// IncrementFileDownloadCount 增加文件下载次数
func (r *Repository) IncrementFileDownloadCount(ctx context.Context, fileID int64) error {
	query := `UPDATE release_files SET download_count = download_count + 1 WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, fileID)
	return err
}

// AddTest 添加测试记录
func (r *Repository) AddTest(ctx context.Context, test *ReleaseTest) error {
	query := `
		INSERT INTO release_tests (
			release_id, test_type, test_name, test_status,
			test_output, test_duration, error_message,
			test_environment, tester
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9
		)
		RETURNING id, tested_at
	`

	err := r.db.QueryRowContext(
		ctx, query,
		test.ReleaseID, test.TestType, test.TestName, test.TestStatus,
		test.TestOutput, test.TestDuration, test.ErrorMessage,
		test.TestEnvironment, test.Tester,
	).Scan(&test.ID, &test.TestedAt)

	return err
}

// GetTestsByReleaseID 获取版本的所有测试
func (r *Repository) GetTestsByReleaseID(ctx context.Context, releaseID int64) ([]ReleaseTest, error) {
	var tests []ReleaseTest
	query := `
		SELECT * FROM release_tests 
		WHERE release_id = $1 
		ORDER BY tested_at DESC
	`
	err := r.db.SelectContext(ctx, &tests, query, releaseID)
	return tests, err
}
