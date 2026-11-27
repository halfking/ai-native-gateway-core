package attachments

import (
	"context"
	"fmt"
	"log/slog"
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

// CheckAndReserve 检查租户配额并原子预留空间。
//
// 在保存附件前调用，失败时返回明确的配额错误。
// ★ 2026-11-27 12h 审计轮（P2-F）根修：预留改为数据库原子写回——此前
//   「读-判断」两步不落库，并发上传同租户时全部读到同一个旧 current_bytes
//   全部放行 ⇒ 配额超卖。现在单条条件 UPSERT 完成检查＋预留（reserved_bytes
//   /reserved_files_today 原子累加），并发请求在同一行上串行化：
//   - 超限请求 UPDATE 未命中 WHERE ⇒ rows affected=0 ⇒ 拒绝；
//   - CommitUsage 把 reserved 转入实际用量；ReleaseReservation 在保存失败时
//     释放预留（storage.go 保存失败路径调用）。
// 兼容：表尚无 reserved 列时（633 未含 reserved 迁移前部署的库）——迁移
//   633 已在本轮补列（ALTER TABLE ADD COLUMN IF NOT EXISTS，默认 0）。
func (qm *QuotaManager) CheckAndReserve(ctx context.Context, tenantID string, sizeBytes int64) error {
	if qm.db == nil {
		// 无数据库时跳过配额检查（向后兼容）
		return nil
	}

	limits := qm.getLimitsForTenant(tenantID)
	today := time.Now().UTC().Format("2006-01-02")

	// 原子检查＋预留：命中 = 预留成功；未命中 = 超限（storage 或 daily_files）。
	// last_reset_date != today 时预留写回同时把 files_today 归零（日切）。
	tag, err := qm.db.Exec(ctx, `
		INSERT INTO tenant_attachment_quotas AS q
			(tenant_id, current_bytes, files_today, last_reset_date,
			 reserved_bytes, reserved_files_today, updated_at)
		VALUES ($1, 0, 0, $2, 0, 1, NOW())
		ON CONFLICT (tenant_id) DO UPDATE SET
			reserved_bytes = q.reserved_bytes + $3,
			reserved_files_today = CASE
				WHEN q.last_reset_date = $2 THEN q.reserved_files_today + 1
				ELSE 1
			END,
			last_reset_date = $2,
			updated_at = NOW()
		WHERE
			q.current_bytes + q.reserved_bytes + $3 <= $4
			AND CASE WHEN q.last_reset_date = $2 THEN q.files_today + q.reserved_files_today + 1 ELSE 1 END <= $5
	`, tenantID, today, sizeBytes, limits.MaxBytes, limits.MaxFilesPerDay)

	if err != nil {
		return fmt.Errorf("failed to reserve quota: %w", err)
	}

	if tag.RowsAffected() == 0 {
		// 未命中 WHERE：区分超限类型给明确错误（读一次当前值仅用于报错文案）
		quota, qErr := qm.getOrCreateQuota(ctx, tenantID)
		if qErr != nil {
			return &QuotaExceededError{Type: "storage", TenantID: tenantID, Requested: sizeBytes}
		}
		reservedTotal := quota.CurrentBytes + sizeBytes // 粗粒度文案（无 reserved 读出时的保守展示）
		if quota.FilesToday >= limits.MaxFilesPerDay {
			return &QuotaExceededError{
				Type:      "daily_files",
				TenantID:  tenantID,
				Current:   int64(quota.FilesToday),
				Limit:     int64(limits.MaxFilesPerDay),
				Requested: 1,
			}
		}
		return &QuotaExceededError{
			Type:      "storage",
			TenantID:  tenantID,
			Current:   reservedTotal,
			Limit:     limits.MaxBytes,
			Requested: sizeBytes,
		}
	}

	return nil
}

// CommitUsage 确认附件已成功保存，更新配额使用量。
//
// 在附件保存成功后调用。使用 ON CONFLICT 处理并发更新。
// ★ 2026-11-27 12h 审计轮（P2-F）：同步把 CheckAndReserve 预留的
//   reserved_bytes/reserved_files_today 转销（扣减同一量），保证
//   「预留→确认」闭环；同请求重复 Commit（重试路径）预留可能已为 0，
//   GREATEST 防负数。
func (qm *QuotaManager) CommitUsage(ctx context.Context, tenantID string, sizeBytes int64) error {
	if qm.db == nil {
		return nil
	}

	today := time.Now().UTC().Format("2006-01-02")

	query := `
		INSERT INTO tenant_attachment_quotas AS q (
			tenant_id,
			current_bytes,
			files_today,
			last_reset_date,
			reserved_bytes,
			reserved_files_today,
			updated_at
		) VALUES ($1, $2, 1, $3, 0, 0, NOW())
		ON CONFLICT (tenant_id) DO UPDATE SET
			current_bytes = tenant_attachment_quotas.current_bytes + $2,
			files_today = CASE
				WHEN tenant_attachment_quotas.last_reset_date = $3
				THEN tenant_attachment_quotas.files_today + 1
				ELSE 1
			END,
			reserved_bytes = GREATEST(tenant_attachment_quotas.reserved_bytes - $2, 0),
			reserved_files_today = GREATEST(tenant_attachment_quotas.reserved_files_today - 1, 0),
			last_reset_date = $3,
			updated_at = NOW()
	`

	_, err := qm.db.Exec(ctx, query, tenantID, sizeBytes, today)
	if err != nil {
		return fmt.Errorf("failed to commit quota usage: %w", err)
	}

	return nil
}

// ReleaseReservation 释放 CheckAndReserve 的预留（附件保存失败时调用）。
// ★ 2026-11-27 12h 审计轮（P2-F）配套：失败不释放会让预留只增不减 ⇒
//   实际用量远小于 current+reserved 却持续拒绝新上传。best-effort：失败只
//   记 Warn（预留略偏高比拒绝服务安全）。
func (qm *QuotaManager) ReleaseReservation(ctx context.Context, tenantID string, sizeBytes int64) {
	if qm.db == nil {
		return
	}

	_, err := qm.db.Exec(ctx, `
		UPDATE tenant_attachment_quotas SET
			reserved_bytes = GREATEST(reserved_bytes - $2, 0),
			reserved_files_today = GREATEST(reserved_files_today - 1, 0),
			updated_at = NOW()
		WHERE tenant_id = $1
	`, tenantID, sizeBytes)
	if err != nil {
		slog.Warn("attachments: failed to release quota reservation",
			"tenant_id", tenantID,
			"size", sizeBytes,
			"error", err)
	}
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
