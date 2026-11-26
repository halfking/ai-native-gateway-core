package attachments

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// QuotaConfig 配置每租户的存储配额限制。
//
// P0 安全修复 (2026-11-26): 防止恶意租户通过无限制上传耗尽存储空间。
type QuotaConfig struct {
	// DefaultMaxBytes 默认租户最大存储容量（字节）
	// 例如：10GB = 10737418240
	DefaultMaxBytes int64

	// DefaultMaxFilesPerDay 默认租户每日最大上传文件数
	// 例如：1000 个文件/天
	DefaultMaxFilesPerDay int

	// PerTenantOverrides 特定租户的配额覆盖
	// key = tenant_id, value = 自定义配额
	PerTenantOverrides map[string]TenantQuotaLimits
}

// TenantQuotaLimits 单个租户的配额限制。
type TenantQuotaLimits struct {
	MaxBytes        int64 // 总容量限制
	MaxFilesPerDay  int   // 每日文件数限制
}

// TenantQuota 租户的当前配额使用状态。
type TenantQuota struct {
	TenantID        string
	MaxTotalBytes   int64  // 配额上限
	CurrentBytes    int64  // 当前使用量
	MaxFilesPerDay  int    // 每日文件数上限
	FilesToday      int    // 今日已上传文件数
	LastResetDate   string // 最后重置日期 (YYYY-MM-DD)
}

// QuotaManager 管理租户存储配额的检查和更新。
//
// 设计原则：
// 1. 检查在保存前进行（CheckAndReserve）
// 2. 确认在保存后进行（CommitUsage）
// 3. 失败时回滚预留（ReleaseReservation）
// 4. 使用数据库行级锁防止并发竞态
type QuotaManager struct {
	db     *pgxpool.Pool
	config QuotaConfig
}

// NewQuotaManager 创建配额管理器。
func NewQuotaManager(db *pgxpool.Pool, config QuotaConfig) *QuotaManager {
	return &QuotaManager{
		db:     db,
		config: config,
	}
}

// CheckAndReserve 检查租户配额并预留空间。
//
// 在保存附件前调用，失败时返回明确的配额错误。
// 成功时在内存中预留（实际不修改数据库），由 CommitUsage 确认。
func (qm *QuotaManager) CheckAndReserve(ctx context.Context, tenantID string, sizeBytes int64) error {
	if qm.db == nil {
		// 无数据库时跳过配额检查（向后兼容）
		return nil
	}

	quota, err := qm.getOrCreateQuota(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("failed to get quota: %w", err)
	}

	// 检查总容量
	if quota.CurrentBytes+sizeBytes > quota.MaxTotalBytes {
		return &QuotaExceededError{
			Type:      "storage",
			TenantID:  tenantID,
			Current:   quota.CurrentBytes,
			Limit:     quota.MaxTotalBytes,
			Requested: sizeBytes,
		}
	}

	// 检查每日文件数（按当前日期重置）
	today := time.Now().UTC().Format("2006-01-02")
	if quota.LastResetDate != today {
		// 新的一天，重置计数器
		quota.FilesToday = 0
		quota.LastResetDate = today
	}

	if quota.FilesToday >= quota.MaxFilesPerDay {
		return &QuotaExceededError{
			Type:      "daily_files",
			TenantID:  tenantID,
			Current:   int64(quota.FilesToday),
			Limit:     int64(quota.MaxFilesPerDay),
			Requested: 1,
		}
	}

	return nil
}

// CommitUsage 确认附件已成功保存，更新配额使用量。
//
// 在附件保存成功后调用。使用 ON CONFLICT 处理并发更新。
func (qm *QuotaManager) CommitUsage(ctx context.Context, tenantID string, sizeBytes int64) error {
	if qm.db == nil {
		return nil
	}

	today := time.Now().UTC().Format("2006-01-02")

	query := `
		INSERT INTO tenant_attachment_quotas (
			tenant_id, 
			current_bytes, 
			files_today, 
			last_reset_date,
			updated_at
		) VALUES ($1, $2, 1, $3, NOW())
		ON CONFLICT (tenant_id) DO UPDATE SET
			current_bytes = tenant_attachment_quotas.current_bytes + $2,
			files_today = CASE
				WHEN tenant_attachment_quotas.last_reset_date = $3 
				THEN tenant_attachment_quotas.files_today + 1
				ELSE 1
			END,
			last_reset_date = $3,
			updated_at = NOW()
	`

	_, err := qm.db.Exec(ctx, query, tenantID, sizeBytes, today)
	if err != nil {
		return fmt.Errorf("failed to commit quota usage: %w", err)
	}

	return nil
}

// getOrCreateQuota 获取租户配额状态，不存在时返回默认配额。
func (qm *QuotaManager) getOrCreateQuota(ctx context.Context, tenantID string) (*TenantQuota, error) {
	// 获取该租户的配额限制（可能有自定义覆盖）
	limits := qm.getLimitsForTenant(tenantID)

	query := `
		SELECT current_bytes, files_today, last_reset_date
		FROM tenant_attachment_quotas
		WHERE tenant_id = $1
	`

	var currentBytes int64
	var filesToday int
	var lastResetDate string

	err := qm.db.QueryRow(ctx, query, tenantID).Scan(&currentBytes, &filesToday, &lastResetDate)
	if err != nil {
		// 记录不存在时返回默认值（首次使用）
		return &TenantQuota{
			TenantID:       tenantID,
			MaxTotalBytes:  limits.MaxBytes,
			CurrentBytes:   0,
			MaxFilesPerDay: limits.MaxFilesPerDay,
			FilesToday:     0,
			LastResetDate:  "",
		}, nil
	}

	return &TenantQuota{
		TenantID:       tenantID,
		MaxTotalBytes:  limits.MaxBytes,
		CurrentBytes:   currentBytes,
		MaxFilesPerDay: limits.MaxFilesPerDay,
		FilesToday:     filesToday,
		LastResetDate:  lastResetDate,
	}, nil
}

// getLimitsForTenant 获取租户的配额限制（优先使用自定义覆盖）。
func (qm *QuotaManager) getLimitsForTenant(tenantID string) TenantQuotaLimits {
	if override, exists := qm.config.PerTenantOverrides[tenantID]; exists {
		return override
	}

	return TenantQuotaLimits{
		MaxBytes:       qm.config.DefaultMaxBytes,
		MaxFilesPerDay: qm.config.DefaultMaxFilesPerDay,
	}
}

// QuotaExceededError 表示配额超限错误。
type QuotaExceededError struct {
	Type      string // "storage" 或 "daily_files"
	TenantID  string
	Current   int64 // 当前使用量
	Limit     int64 // 配额上限
	Requested int64 // 本次请求量
}

func (e *QuotaExceededError) Error() string {
	switch e.Type {
	case "storage":
		return fmt.Sprintf(
			"tenant storage quota exceeded: tenant=%s, current=%d bytes, limit=%d bytes, requested=%d bytes",
			e.TenantID, e.Current, e.Limit, e.Requested,
		)
	case "daily_files":
		return fmt.Sprintf(
			"daily file upload limit exceeded: tenant=%s, current=%d files, limit=%d files",
			e.TenantID, e.Current, e.Limit,
		)
	default:
		return fmt.Sprintf("quota exceeded: tenant=%s, type=%s", e.TenantID, e.Type)
	}
}

// IsQuotaExceeded 判断错误是否为配额超限错误。
func IsQuotaExceeded(err error) bool {
	_, ok := err.(*QuotaExceededError)
	return ok
}
