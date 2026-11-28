package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminAPI 计费系统管理API
//
// 提供以下功能：
// - 计费配置管理（CRUD）
// - 套餐管理（CRUD）
// - 增值包购买和管理
// - 配额查询和调整
// - 账单查询和导出
type AdminAPI struct {
	db           *pgxpool.Pool
	configLoader BillingConfigLoader
	configCache  BillingConfigCache
	quotaManager QuotaManager
	packManager  PackManager
	factory      *BillingEngineFactory
}

// NewAdminAPI 创建管理API实例
func NewAdminAPI(
	db *pgxpool.Pool,
	configLoader BillingConfigLoader,
	configCache BillingConfigCache,
	quotaManager QuotaManager,
	packManager PackManager,
	factory *BillingEngineFactory,
) *AdminAPI {
	return &AdminAPI{
		db:           db,
		configLoader: configLoader,
		configCache:  configCache,
		quotaManager: quotaManager,
		packManager:  packManager,
		factory:      factory,
	}
}

// ============================================================================
// 计费配置管理
// ============================================================================

// GetBillingConfigRequest 获取计费配置请求
type GetBillingConfigRequest struct {
	TenantID string `json:"tenant_id"`
}

// GetBillingConfigResponse 获取计费配置响应
type GetBillingConfigResponse struct {
	Config *BillingConfig `json:"config"`
}

// GetBillingConfig 获取租户计费配置
//
// GET /api/admin/billing/configs/:tenant_id
func (api *AdminAPI) GetBillingConfig(ctx context.Context, tenantID string) (*BillingConfig, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}

	config, err := api.configLoader.LoadByTenantID(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	return config, nil
}

// UpdateBillingConfigRequest 更新计费配置请求
type UpdateBillingConfigRequest struct {
	TenantID     string                 `json:"tenant_id"`
	BillingMode  BillingMode            `json:"billing_mode"`
	PlanID       *int64                 `json:"plan_id,omitempty"`
	EngineConfig map[string]interface{} `json:"engine_config"`
	CachePriority int                   `json:"cache_priority,omitempty"`
	CacheTTL     int                    `json:"cache_ttl_seconds,omitempty"`
	PreloadHint  bool                   `json:"preload_hint,omitempty"`
}

// UpdateBillingConfig 更新租户计费配置
//
// PUT /api/admin/billing/configs/:tenant_id
func (api *AdminAPI) UpdateBillingConfig(ctx context.Context, req *UpdateBillingConfigRequest) error {
	if req.TenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}

	// 验证配置有效性
	config := &BillingConfig{
		TenantID:     req.TenantID,
		BillingMode:  req.BillingMode,
		PlanID:       req.PlanID,
		EngineConfig: req.EngineConfig,
		CacheHint: CacheHint{
			Priority:    req.CachePriority,
			TTL:         time.Duration(req.CacheTTL) * time.Second,
			PreloadHint: req.PreloadHint,
		},
	}

	// 获取对应的引擎进行验证
	engine, err := api.factory.Get(req.BillingMode)
	if err != nil {
		return fmt.Errorf("invalid billing_mode: %w", err)
	}

	if err := engine.Validate(config); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	// 保存到数据库
	engineConfigJSON, _ := json.Marshal(req.EngineConfig)

	query := `
		INSERT INTO billing_configs (
			tenant_id, billing_mode, plan_id, engine_config,
			cache_priority, cache_ttl_seconds, preload_hint,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())
		ON CONFLICT (tenant_id, COALESCE(user_id, ''))
		DO UPDATE SET
			billing_mode = $2,
			plan_id = $3,
			engine_config = $4,
			cache_priority = $5,
			cache_ttl_seconds = $6,
			preload_hint = $7,
			updated_at = NOW()
	`

	_, err = api.db.Exec(ctx, query,
		req.TenantID,
		req.BillingMode,
		req.PlanID,
		engineConfigJSON,
		req.CachePriority,
		req.CacheTTL,
		req.PreloadHint,
	)

	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// 失效缓存
	if api.configLoader != nil {
		api.configLoader.InvalidateCache(ctx, req.TenantID)
	}

	return nil
}

// ListBillingModesResponse 列出可用计费模式响应
type ListBillingModesResponse struct {
	Modes []BillingModeInfo `json:"modes"`
}

// BillingModeInfo 计费模式信息
type BillingModeInfo struct {
	Mode        BillingMode `json:"mode"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Available   bool        `json:"available"`
}

// ListBillingModes 列出所有可用的计费模式
//
// GET /api/admin/billing/modes
func (api *AdminAPI) ListBillingModes(ctx context.Context) (*ListBillingModesResponse, error) {
	availableModes := api.factory.ListModes()

	modes := []BillingModeInfo{
		{
			Mode:        ModePayAsYouGo,
			Name:        "按量付费",
			Description: "根据实际使用量计费，灵活按需付费",
			Available:   contains(availableModes, ModePayAsYouGo),
		},
		{
			Mode:        ModeTokenPlan,
			Name:        "包月套餐",
			Description: "固定月费+配额，超额按量计费",
			Available:   contains(availableModes, ModeTokenPlan),
		},
		{
			Mode:        ModeBoosterPack,
			Name:        "增值包",
			Description: "预付费购买，灵活使用",
			Available:   contains(availableModes, ModeBoosterPack),
		},
		{
			Mode:        ModeCustom,
			Name:        "自定义",
			Description: "定制化计费方案",
			Available:   contains(availableModes, ModeCustom),
		},
	}

	return &ListBillingModesResponse{Modes: modes}, nil
}

// ============================================================================
// 套餐管理
// ============================================================================

// Plan 套餐定义
type Plan struct {
	PlanID        int64   `json:"plan_id"`
	PlanName      string  `json:"plan_name"`
	Description   string  `json:"description"`
	MonthlyQuota  int64   `json:"monthly_quota"`
	BasePriceCents int64  `json:"base_price_cents"`
	OverageRate   float64 `json:"overage_rate"`
	Currency      string  `json:"currency"`
	IsActive      bool    `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ListPlansResponse 列出套餐响应
type ListPlansResponse struct {
	Plans []Plan `json:"plans"`
	Total int    `json:"total"`
}

// ListPlans 列出所有套餐
//
// GET /api/admin/billing/plans
func (api *AdminAPI) ListPlans(ctx context.Context, activeOnly bool) (*ListPlansResponse, error) {
	query := `
		SELECT plan_id, plan_name, description, monthly_quota, 
		       base_price_cents, overage_rate, currency, is_active,
		       created_at, updated_at
		FROM billing_plans
	`

	if activeOnly {
		query += " WHERE is_active = true"
	}

	query += " ORDER BY plan_id"

	rows, err := api.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query plans: %w", err)
	}
	defer rows.Close()

	plans := []Plan{}
	for rows.Next() {
		var plan Plan
		err := rows.Scan(
			&plan.PlanID,
			&plan.PlanName,
			&plan.Description,
			&plan.MonthlyQuota,
			&plan.BasePriceCents,
			&plan.OverageRate,
			&plan.Currency,
			&plan.IsActive,
			&plan.CreatedAt,
			&plan.UpdatedAt,
		)
		if err != nil {
			continue
		}
		plans = append(plans, plan)
	}

	return &ListPlansResponse{
		Plans: plans,
		Total: len(plans),
	}, nil
}

// CreatePlanRequest 创建套餐请求
type CreatePlanRequest struct {
	PlanName       string  `json:"plan_name"`
	Description    string  `json:"description"`
	MonthlyQuota   int64   `json:"monthly_quota"`
	BasePriceCents int64   `json:"base_price_cents"`
	OverageRate    float64 `json:"overage_rate"`
	Currency       string  `json:"currency"`
}

// CreatePlan 创建新套餐
//
// POST /api/admin/billing/plans
func (api *AdminAPI) CreatePlan(ctx context.Context, req *CreatePlanRequest) (int64, error) {
	if req.PlanName == "" {
		return 0, fmt.Errorf("plan_name is required")
	}

	if req.MonthlyQuota <= 0 {
		return 0, fmt.Errorf("monthly_quota must be positive")
	}

	if req.BasePriceCents < 0 {
		return 0, fmt.Errorf("base_price_cents cannot be negative")
	}

	if req.Currency == "" {
		req.Currency = "CNY"
	}

	query := `
		INSERT INTO billing_plans (
			plan_name, description, monthly_quota, base_price_cents,
			overage_rate, currency, is_active, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, true, NOW(), NOW())
		RETURNING plan_id
	`

	var planID int64
	err := api.db.QueryRow(ctx, query,
		req.PlanName,
		req.Description,
		req.MonthlyQuota,
		req.BasePriceCents,
		req.OverageRate,
		req.Currency,
	).Scan(&planID)

	if err != nil {
		return 0, fmt.Errorf("create plan: %w", err)
	}

	return planID, nil
}

// ============================================================================
// 配额管理
// ============================================================================

// GetQuotaUsageRequest 获取配额使用请求
type GetQuotaUsageRequest struct {
	TenantID string `json:"tenant_id"`
	Period   string `json:"period"` // "current" or "YYYY-MM"
}

// GetQuotaUsageResponse 获取配额使用响应
type GetQuotaUsageResponse struct {
	TenantID       string  `json:"tenant_id"`
	PeriodStart    string  `json:"period_start"`
	PeriodEnd      string  `json:"period_end"`
	TotalQuota     int64   `json:"total_quota"`
	UsedQuota      int64   `json:"used_quota"`
	RemainingQuota int64   `json:"remaining_quota"`
	Utilization    float64 `json:"utilization"` // 使用率百分比
}

// GetQuotaUsage 获取配额使用情况
//
// GET /api/admin/billing/quotas/:tenant_id
func (api *AdminAPI) GetQuotaUsage(ctx context.Context, tenantID string, period string) (*GetQuotaUsageResponse, error) {
	if api.quotaManager == nil {
		return nil, fmt.Errorf("quota manager not configured")
	}

	// 解析周期
	var timePeriod TimePeriod
	if period == "" || period == "current" {
		now := time.Now()
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		end := start.AddDate(0, 1, 0).Add(-time.Second)
		timePeriod = TimePeriod{Start: start, End: end}
	} else {
		// 解析 "YYYY-MM" 格式
		t, err := time.Parse("2006-01", period)
		if err != nil {
			return nil, fmt.Errorf("invalid period format: %s (expected YYYY-MM)", period)
		}
		start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 1, 0).Add(-time.Second)
		timePeriod = TimePeriod{Start: start, End: end}
	}

	// 获取使用量
	usage, err := api.quotaManager.GetUsage(ctx, tenantID, timePeriod)
	if err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
	}

	// 获取总配额（从配置）
	config, err := api.configLoader.LoadByTenantID(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	totalQuota := int64(0)
	if config.BillingMode == ModeTokenPlan {
		if mq, ok := config.EngineConfig["monthly_quota"].(float64); ok {
			totalQuota = int64(mq)
		}
	}

	remaining := totalQuota - usage.UsedQuota
	if remaining < 0 {
		remaining = 0
	}

	utilization := 0.0
	if totalQuota > 0 {
		utilization = float64(usage.UsedQuota) / float64(totalQuota) * 100
	}

	return &GetQuotaUsageResponse{
		TenantID:       tenantID,
		PeriodStart:    timePeriod.Start.Format("2006-01-02"),
		PeriodEnd:      timePeriod.End.Format("2006-01-02"),
		TotalQuota:     totalQuota,
		UsedQuota:      usage.UsedQuota,
		RemainingQuota: remaining,
		Utilization:    utilization,
	}, nil
}

// ============================================================================
// 增值包管理
// ============================================================================

// PurchasePackRequest 购买增值包请求
type PurchasePackRequest struct {
	TenantID    string     `json:"tenant_id"`
	PackType    string     `json:"pack_type"`
	QuotaAmount int64      `json:"quota_amount"`
	PriceCents  int64      `json:"price_cents"`
	Currency    string     `json:"currency"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// PurchasePack 购买增值包
//
// POST /api/admin/billing/booster-packs
func (api *AdminAPI) PurchasePack(ctx context.Context, req *PurchasePackRequest) (int64, error) {
	if api.packManager == nil {
		return 0, fmt.Errorf("pack manager not configured")
	}

	if req.TenantID == "" {
		return 0, fmt.Errorf("tenant_id is required")
	}

	if req.QuotaAmount <= 0 {
		return 0, fmt.Errorf("quota_amount must be positive")
	}

	pack := &Pack{
		TenantID:        req.TenantID,
		PackType:        req.PackType,
		QuotaAmount:     req.QuotaAmount,
		RemainingAmount: req.QuotaAmount,
		PriceCents:      req.PriceCents,
		Currency:        req.Currency,
		PurchasedAt:     time.Now(),
		ExpiresAt:       req.ExpiresAt,
		Status:          "active",
	}

	// 保存到数据库
	query := `
		INSERT INTO booster_packs (
			tenant_id, pack_type, quota_amount, remaining_amount,
			price_cents, currency, purchased_at, expires_at, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING pack_id
	`

	var packID int64
	err := api.db.QueryRow(ctx, query,
		pack.TenantID,
		pack.PackType,
		pack.QuotaAmount,
		pack.RemainingAmount,
		pack.PriceCents,
		pack.Currency,
		pack.PurchasedAt,
		pack.ExpiresAt,
		pack.Status,
	).Scan(&packID)

	if err != nil {
		return 0, fmt.Errorf("purchase pack: %w", err)
	}

	pack.PackID = packID

	// 添加到管理器
	if err := api.packManager.AddPack(ctx, pack); err != nil {
		return 0, fmt.Errorf("add to manager: %w", err)
	}

	return packID, nil
}

// ListPacksResponse 列出增值包响应
type ListPacksResponse struct {
	Packs []Pack `json:"packs"`
	Total int    `json:"total"`
}

// ListPacks 列出租户的增值包
//
// GET /api/admin/billing/booster-packs/:tenant_id
func (api *AdminAPI) ListPacks(ctx context.Context, tenantID string) (*ListPacksResponse, error) {
	if api.packManager == nil {
		return nil, fmt.Errorf("pack manager not configured")
	}

	packs, err := api.packManager.GetPacks(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get packs: %w", err)
	}

	// 转换为值类型
	packValues := make([]Pack, len(packs))
	for i, p := range packs {
		packValues[i] = *p
	}

	return &ListPacksResponse{
		Packs: packValues,
		Total: len(packValues),
	}, nil
}

// ============================================================================
// 辅助函数
// ============================================================================

func contains(slice []BillingMode, item BillingMode) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// ParseInt64 解析int64参数
func ParseInt64(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty string")
	}
	return strconv.ParseInt(s, 10, 64)
}

// WriteJSONResponse 写入JSON响应
func WriteJSONResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// WriteErrorResponse 写入错误响应
func WriteErrorResponse(w http.ResponseWriter, status int, message string) {
	WriteJSONResponse(w, status, map[string]string{
		"error": message,
	})
}
