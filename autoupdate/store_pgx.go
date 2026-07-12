package autoupdate

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgxStore PostgreSQL实现
type PgxStore struct {
	db *pgxpool.Pool
}

// NewPgxStore 创建PostgreSQL存储
func NewPgxStore(db *pgxpool.Pool) *PgxStore {
	return &PgxStore{db: db}
}

// CreateRelease 创建发布版本
func (s *PgxStore) CreateRelease(ctx context.Context, rel *Release) error {
	query := `
		INSERT INTO releases (version, build_seq, channel, title, description, changelog,
		                      image_tag, image_digest, min_version, mandatory, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at
	`
	return s.db.QueryRow(ctx, query,
		rel.Version, rel.BuildSeq, rel.Channel, rel.Title, rel.Description, rel.Changelog,
		rel.ImageTag, rel.ImageDigest, rel.MinVersion, rel.Mandatory, rel.CreatedBy,
	).Scan(&rel.ID, &rel.CreatedAt)
}

// GetRelease 获取指定版本
func (s *PgxStore) GetRelease(ctx context.Context, version string) (*Release, error) {
	query := `
		SELECT id, version, build_seq, channel, title, description, changelog,
		       image_tag, image_digest, min_version, mandatory, created_by, created_at, published_at
		FROM releases
		WHERE version = $1
	`
	rel := &Release{}
	err := s.db.QueryRow(ctx, query, version).Scan(
		&rel.ID, &rel.Version, &rel.BuildSeq, &rel.Channel, &rel.Title, &rel.Description, &rel.Changelog,
		&rel.ImageTag, &rel.ImageDigest, &rel.MinVersion, &rel.Mandatory, &rel.CreatedBy, &rel.CreatedAt, &rel.PublishedAt,
	)
	if err != nil {
		return nil, err
	}
	return rel, nil
}

// GetLatestRelease 获取最新发布版本
func (s *PgxStore) GetLatestRelease(ctx context.Context, channel Channel) (*Release, error) {
	query := `
		SELECT id, version, build_seq, channel, title, description, changelog,
		       image_tag, image_digest, min_version, mandatory, created_by, created_at, published_at
		FROM releases
		WHERE channel = $1 AND published_at IS NOT NULL
		ORDER BY build_seq DESC, created_at DESC
		LIMIT 1
	`
	rel := &Release{}
	err := s.db.QueryRow(ctx, query, channel).Scan(
		&rel.ID, &rel.Version, &rel.BuildSeq, &rel.Channel, &rel.Title, &rel.Description, &rel.Changelog,
		&rel.ImageTag, &rel.ImageDigest, &rel.MinVersion, &rel.Mandatory, &rel.CreatedBy, &rel.CreatedAt, &rel.PublishedAt,
	)
	if err != nil {
		return nil, err
	}
	return rel, nil
}

// GetLatestReleaseAfter 获取比指定 build_seq 更新的发布版本
func (s *PgxStore) GetLatestReleaseAfter(ctx context.Context, channel Channel, currentBuildSeq int) (*Release, error) {
	query := `
		SELECT id, version, build_seq, channel, title, description, changelog,
		       image_tag, image_digest, min_version, mandatory, created_by, created_at, published_at
		FROM releases
		WHERE channel = $1 AND published_at IS NOT NULL AND build_seq > $2
		ORDER BY build_seq DESC, created_at DESC
		LIMIT 1
	`
	rel := &Release{}
	err := s.db.QueryRow(ctx, query, channel, currentBuildSeq).Scan(
		&rel.ID, &rel.Version, &rel.BuildSeq, &rel.Channel, &rel.Title, &rel.Description, &rel.Changelog,
		&rel.ImageTag, &rel.ImageDigest, &rel.MinVersion, &rel.Mandatory, &rel.CreatedBy, &rel.CreatedAt, &rel.PublishedAt,
	)
	if err != nil {
		return nil, err
	}
	return rel, nil
}

// ListReleases 列出发布版本
func (s *PgxStore) ListReleases(ctx context.Context, channel Channel, offset, limit int) ([]Release, int, error) {
	// 查询总数
	var total int
	countQuery := `SELECT COUNT(*) FROM releases WHERE channel = $1`
	if err := s.db.QueryRow(ctx, countQuery, channel).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 查询列表
	query := `
		SELECT id, version, build_seq, channel, title, description, changelog,
		       image_tag, image_digest, min_version, mandatory, created_by, created_at, published_at
		FROM releases
		WHERE channel = $1
		ORDER BY build_seq DESC, created_at DESC
		OFFSET $2 LIMIT $3
	`
	rows, err := s.db.Query(ctx, query, channel, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var releases []Release
	for rows.Next() {
		var rel Release
		if err := rows.Scan(
			&rel.ID, &rel.Version, &rel.BuildSeq, &rel.Channel, &rel.Title, &rel.Description, &rel.Changelog,
			&rel.ImageTag, &rel.ImageDigest, &rel.MinVersion, &rel.Mandatory, &rel.CreatedBy, &rel.CreatedAt, &rel.PublishedAt,
		); err != nil {
			return nil, 0, err
		}
		releases = append(releases, rel)
	}

	return releases, total, nil
}

// UpdateReleaseStatus 更新发布状态
func (s *PgxStore) UpdateReleaseStatus(ctx context.Context, id int64, published bool) error {
	var query string
	if published {
		query = `UPDATE releases SET published_at = now() WHERE id = $1`
	} else {
		query = `UPDATE releases SET published_at = NULL WHERE id = $1`
	}
	_, err := s.db.Exec(ctx, query, id)
	return err
}

// CreateGrayRule 创建灰度规则
func (s *PgxStore) CreateGrayRule(ctx context.Context, rule *GrayReleaseRule) error {
	query := `
		INSERT INTO gray_release_rules (release_id, phase, percent, selectors, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at
	`
	return s.db.QueryRow(ctx, query,
		rule.ReleaseID, rule.Phase, rule.Percent, rule.Selectors, rule.Status,
	).Scan(&rule.ID, &rule.CreatedAt)
}

// GetGrayRule 获取灰度规则
func (s *PgxStore) GetGrayRule(ctx context.Context, releaseID int64) (*GrayReleaseRule, error) {
	query := `
		SELECT id, release_id, phase, percent, selectors, status, created_at
		FROM gray_release_rules
		WHERE release_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`
	rule := &GrayReleaseRule{}
	err := s.db.QueryRow(ctx, query, releaseID).Scan(
		&rule.ID, &rule.ReleaseID, &rule.Phase, &rule.Percent, &rule.Selectors, &rule.Status, &rule.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return rule, nil
}

// UpdateGrayPhase 更新灰度阶段
func (s *PgxStore) UpdateGrayPhase(ctx context.Context, releaseID int64, phase Phase, percent int) error {
	query := `
		UPDATE gray_release_rules
		SET phase = $2, percent = $3
		WHERE release_id = $1
	`
	_, err := s.db.Exec(ctx, query, releaseID, phase, percent)
	return err
}

// CreateUpgradeLog 创建升级日志
func (s *PgxStore) CreateUpgradeLog(ctx context.Context, instanceID string, oldVer, newVer string) (int64, error) {
	query := `
		INSERT INTO upgrade_logs (instance_id, old_version, new_version, status, started_at)
		VALUES ($1, $2, $3, $4, now())
		RETURNING id
	`
	var id int64
	err := s.db.QueryRow(ctx, query, instanceID, oldVer, newVer, StatusPending).Scan(&id)
	return id, err
}

// UpdateUpgradeLog 更新升级日志
func (s *PgxStore) UpdateUpgradeLog(ctx context.Context, id int64, status string, errMsg string, completedAt time.Time) error {
	query := `
		UPDATE upgrade_logs
		SET status = $2, error_message = $3, completed_at = $4
		WHERE id = $1
	`
	_, err := s.db.Exec(ctx, query, id, status, errMsg, completedAt)
	return err
}

// GetUpgradeHistory 获取升级历史
func (s *PgxStore) GetUpgradeHistory(ctx context.Context, instanceID string, limit int) ([]ReleaseStatus, error) {
	query := `
		SELECT id, instance_id, old_version, new_version, status, started_at, completed_at, error_message, retry_count
		FROM upgrade_logs
		WHERE instance_id = $1
		ORDER BY started_at DESC
		LIMIT $2
	`
	rows, err := s.db.Query(ctx, query, instanceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []ReleaseStatus
	for rows.Next() {
		var rs ReleaseStatus
		var id int64
		var oldVer string
		var errMsg *string
		if err := rows.Scan(&id, &rs.InstanceID, &oldVer, &rs.Version, &rs.Status, &rs.StartedAt, &rs.CompletedAt, &errMsg, &rs.RetryCount); err != nil {
			return nil, err
		}
		if errMsg != nil {
			rs.Error = *errMsg
		}
		history = append(history, rs)
	}

	return history, nil
}

// GetInstanceStatus 获取实例升级状态
func (s *PgxStore) GetInstanceStatus(ctx context.Context, instanceID string) (*ReleaseStatus, error) {
	query := `
		SELECT release_id, instance_id, status, version, started_at, completed_at, error, retry_count
		FROM instance_release_status
		WHERE instance_id = $1
	`
	rs := &ReleaseStatus{}
	err := s.db.QueryRow(ctx, query, instanceID).Scan(
		&rs.ReleaseID, &rs.InstanceID, &rs.Status, &rs.Version, &rs.StartedAt, &rs.CompletedAt, &rs.Error, &rs.RetryCount,
	)
	if err != nil {
		return nil, err
	}
	return rs, nil
}

// UpdateInstanceStatus 更新实例升级状态
func (s *PgxStore) UpdateInstanceStatus(ctx context.Context, status *ReleaseStatus) error {
	query := `
		INSERT INTO instance_release_status (release_id, instance_id, status, version, started_at, completed_at, error, retry_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (instance_id) DO UPDATE SET
			release_id = EXCLUDED.release_id,
			status = EXCLUDED.status,
			version = EXCLUDED.version,
			started_at = EXCLUDED.started_at,
			completed_at = EXCLUDED.completed_at,
			error = EXCLUDED.error,
			retry_count = EXCLUDED.retry_count
	`
	_, err := s.db.Exec(ctx, query,
		status.ReleaseID, status.InstanceID, status.Status, status.Version,
		status.StartedAt, status.CompletedAt, status.Error, status.RetryCount,
	)
	return err
}

// RecordUpdateReport 记录升级结果上报
func (s *PgxStore) RecordUpdateReport(ctx context.Context, report *UpdateReportData) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 1. 查找 release_id（通过 to_version）
	var releaseID int64
	releaseQuery := `SELECT id FROM releases WHERE version = $1`
	if err := tx.QueryRow(ctx, releaseQuery, report.ToVersion).Scan(&releaseID); err != nil {
		// 如果找不到 release，使用 0（允许主控端先上报，release 记录稍后创建）
		releaseID = 0
	}

	// 2. 写入 instance_release_status
	now := time.Now()
	var completedAt *time.Time
	if report.Status == StatusSuccess || report.Status == StatusFailed || report.Status == StatusRollback {
		completedAt = &now
	}

	statusQuery := `
		INSERT INTO instance_release_status (
			release_id, instance_id, status, version, started_at, completed_at, error, retry_count, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 0, now())
		ON CONFLICT (instance_id) DO UPDATE SET
			release_id = EXCLUDED.release_id,
			status = EXCLUDED.status,
			version = EXCLUDED.version,
			completed_at = EXCLUDED.completed_at,
			error = EXCLUDED.error,
			updated_at = now()
	`
	_, err = tx.Exec(ctx, statusQuery,
		releaseID, report.InstanceID, report.Status, report.ToVersion,
		now, completedAt, report.Error,
	)
	if err != nil {
		return err
	}

	// 3. 写入 upgrade_logs
	logQuery := `
		INSERT INTO upgrade_logs (
			instance_id, old_version, new_version, status, started_at, completed_at, error_message, duration_ms
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err = tx.Exec(ctx, logQuery,
		report.InstanceID, report.FromVersion, report.ToVersion, report.Status,
		now, completedAt, report.Error, report.DurationMS,
	)
	if err != nil {
		return err
	}

	// 4. 如果成功，更新 gateway_instances.current_version 和 build_seq
	if report.Status == StatusSuccess {
		// 从 version 提取 build_seq（假设格式为 v1.2.3-build456）
		// 简化处理：先查询 releases 表获取 build_seq，如果没有则不更新
		var buildSeq int
		buildQuery := `SELECT build_seq FROM releases WHERE version = $1`
		if err := tx.QueryRow(ctx, buildQuery, report.ToVersion).Scan(&buildSeq); err == nil {
			updateInstanceQuery := `
				UPDATE gateway_instances
				SET current_version = $1, build_seq = $2
				WHERE instance_id = $3
			`
			_, err = tx.Exec(ctx, updateInstanceQuery, report.ToVersion, buildSeq, report.InstanceID)
			if err != nil {
				return err
			}
		}
		// 如果找不到 build_seq，只更新 version
		if buildSeq == 0 {
			updateInstanceQuery := `
				UPDATE gateway_instances
				SET current_version = $1
				WHERE instance_id = $2
			`
			_, err = tx.Exec(ctx, updateInstanceQuery, report.ToVersion, report.InstanceID)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit(ctx)
}

// Ensure interface compliance
var (
	_ Store = (*PgxStore)(nil)
)
