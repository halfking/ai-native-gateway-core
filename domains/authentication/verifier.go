package authentication

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"
)

// usageLedgerViewRe extracts the relation name from a 42P01 error when
// usage_ledger_with_current_month (or its dependencies) is missing.
// PostgreSQL does not populate TableName for this class of error, so we
// fall back to the message body.
var usageLedgerViewRe = regexp.MustCompile(`relation "([^"]+)" does not exist`)

// budgetViewMatches reports whether the relation reported in a 42P01
// message is part of the usage_ledger view family. We accept the parent
// view or any of its underlying tables (hot + monthly partitions) so a
// brief inconsistency in PostgreSQL's reported relation name does not
// trigger the wrong error path.
func budgetViewMatches(relation string) bool {
	switch relation {
	case "usage_ledger_with_current_month",
		"usage_ledger",
		"usage_ledger_hot",
		"usage_ledger_2026_07",
		"usage_ledger_default":
		return true
	}
	// Partitioned tables use monthly suffixes (e.g. usage_ledger_2026_08).
	if strings.HasPrefix(relation, "usage_ledger_20") {
		return true
	}
	return false
}

// isMissingUsageLedgerView reports whether err is a Postgres 42P01
// complaining about usage_ledger_with_current_month (or its inner tables).
// The auth path treats this as "budget tracking is unavailable" rather
// than failing every request during a migration window.
func isMissingUsageLedgerView(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
		return false
	}
	if pgErr.TableName != "" {
		return budgetViewMatches(pgErr.TableName)
	}
	if m := usageLedgerViewRe.FindStringSubmatch(pgErr.Message); len(m) == 2 {
		return budgetViewMatches(m[1])
	}
	return false
}

// Tier default limits
var tierDefaults = map[string][2]int{
	"system":     {300, 50},
	"production": {60, 20},
	"default":    {12, 6},
	"applicant":  {6, 2},
}

type KeyInfo struct {
	ID                   int      `json:"id"`
	TenantID             string   `json:"tenant_id"`
	ApplicationID        int      `json:"application_id"`
	ApplicationCode      string   `json:"application_code"`
	KeyPrefix            string   `json:"key_prefix"`
	DefaultClientProfile *string  `json:"default_client_profile"`
	OwnerUser            *string  `json:"owner_user"`
	RateLimitRPM         *int     `json:"rate_limit_rpm"`
	RateLimitConcurrent  *int     `json:"rate_limit_concurrent"`
	RateLimitTPM         *int     `json:"rate_limit_tpm"`
	KeyTier              string   `json:"key_tier"`
	BudgetUSD            *float64 `json:"budget_usd"`
	Status               string   `json:"status"`
	IsInternal           bool     `json:"is_internal"`
	KeyAlias             *string  `json:"key_alias"`

	// 2026-07-15: 客户/组织归属。来源 applications.customer_id（迁移 407 新增），
	// 用于 request_context_attrs.customer_id 的派生。无映射时为 nil。
	CustomerID *int64 `json:"customer_id,omitempty"`
}

// EffectiveRPM returns the applicable RPM limit (per-key or tier default).
// A per-key value of 0 means "unlimited" (CheckRPM treats limit<=0 as no cap).
// Negative values (should not exist in DB) fall through to the tier default.
func (ki *KeyInfo) EffectiveRPM() int {
	if ki.RateLimitRPM != nil {
		if *ki.RateLimitRPM == 0 {
			return 0 // explicit unlimited
		}
		if *ki.RateLimitRPM > 0 {
			return *ki.RateLimitRPM
		}
	}
	tier := ki.KeyTier
	if tier == "" {
		tier = "default"
	}
	if d, ok := tierDefaults[tier]; ok {
		return d[0]
	}
	return tierDefaults["default"][0]
}

// EffectiveConcurrent returns the applicable concurrent limit.
// A per-key value of 0 means "unlimited" (AcquireAll skips per-key check when limit <= 0).
// Negative values (should not exist in DB) fall through to the tier default.
func (ki *KeyInfo) EffectiveConcurrent() int {
	if ki.RateLimitConcurrent != nil {
		if *ki.RateLimitConcurrent == 0 {
			return 0 // explicit unlimited
		}
		if *ki.RateLimitConcurrent > 0 {
			return *ki.RateLimitConcurrent
		}
	}
	tier := ki.KeyTier
	if tier == "" {
		tier = "default"
	}
	if d, ok := tierDefaults[tier]; ok {
		return d[1]
	}
	return tierDefaults["default"][1]
}

// DBQuerier 是 KeyVerifier 用于查询 api_keys 表的最小化接口。
// 真实实现是 *pgxpool.Pool；测试可以用 mock。
type DBQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
}

type KeyVerifier struct {
	dbPool    DBQuerier
	secretKey string

	cache   map[string]*keyCacheEntry
	mu      sync.RWMutex
	sfGroup singleflight.Group

	ttl time.Duration
}

type keyCacheEntry struct {
	info      *KeyInfo
	expiresAt time.Time
}

func NewKeyVerifier() *KeyVerifier {
	return &KeyVerifier{
		cache: make(map[string]*keyCacheEntry),
		ttl:   60 * time.Second,
	}
}

func (kv *KeyVerifier) Enabled() bool {
	return kv.dbPool != nil && kv.secretKey != ""
}

// SetDB 注入数据库连接池与 HMAC 密钥。
func (kv *KeyVerifier) SetDB(pool *pgxpool.Pool, secretKey string) {
	kv.dbPool = &pgxPoolAdapter{pool: pool}
	kv.secretKey = secretKey
}

// setDBQuerier (测试用) 注入 DBQuerier mock。
func (kv *KeyVerifier) setDBQuerier(q DBQuerier, secretKey string) {
	kv.dbPool = q
	kv.secretKey = secretKey
}

// pgxPoolAdapter 把 *pgxpool.Pool 适配为 DBQuerier 接口。
type pgxPoolAdapter struct {
	pool *pgxpool.Pool
}

func (a *pgxPoolAdapter) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return a.pool.QueryRow(ctx, sql, args...)
}
func (a *pgxPoolAdapter) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := a.pool.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (kv *KeyVerifier) Verify(ctx context.Context, rawKey string) (*KeyInfo, error) {
	if !kv.Enabled() {
		return nil, fmt.Errorf("key verifier not configured")
	}

	// Rule 20 §2: data plane only accepts sk-* API keys
	if !strings.HasPrefix(rawKey, "sk-") {
		return nil, fmt.Errorf("invalid api key: data plane requires sk-* prefix")
	}

	if info := kv.getCache(rawKey); info != nil {
		// Stale cache entries from before key_prefix was populated must refresh.
		if strings.TrimSpace(info.KeyPrefix) != "" {
			return info, nil
		}
	}

	v, err, _ := kv.sfGroup.Do("key:"+rawKey, func() (any, error) {
		info, verifyErr := kv.callVerifyDB(ctx, rawKey)
		if verifyErr != nil {
			return nil, verifyErr
		}
		kv.setCache(rawKey, info)
		return info, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*KeyInfo), nil
}

type KeyLookupMeta struct {
	ID                   int
	KeyPrefix            string
	OwnerUser            *string
	Status               string
	Enabled              bool
	ApplicationCode      string
	DefaultClientProfile *string
	TenantID             string
	ApplicationID        int

	// 2026-07-15: 客户维度（迁移 407 applications.customer_id）
	CustomerID *int64
}

func (kv *KeyVerifier) LookupKeyMeta(ctx context.Context, rawKey string) (*KeyLookupMeta, error) {
	if !kv.Enabled() || strings.TrimSpace(rawKey) == "" {
		return nil, nil
	}
	keyHash := hashAPIKey(kv.secretKey, rawKey)
	var appID int64
	var meta KeyLookupMeta
	err := kv.dbPool.QueryRow(ctx, `
		SELECT
			ak.id,
			COALESCE(ak.key_prefix, ''),
			ak.owner_user,
			COALESCE(ak.status, 'active'),
			ak.enabled,
			app.code,
			app.default_client_profile,
			ak.tenant_id,
			ak.application_id,
			app.customer_id
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.key_hash = $1
	`, keyHash).Scan(
		&meta.ID,
		&meta.KeyPrefix,
		&meta.OwnerUser,
		&meta.Status,
		&meta.Enabled,
		&meta.ApplicationCode,
		&meta.DefaultClientProfile,
		&meta.TenantID,
		&appID,
		&meta.CustomerID,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	meta.ApplicationID = int(appID)
	return &meta, nil
}

func (kv *KeyVerifier) callVerifyDB(ctx context.Context, rawKey string) (*KeyInfo, error) {
	if kv.dbPool == nil || kv.secretKey == "" {
		return nil, fmt.Errorf("key verify DB not configured")
	}
	keyHash := hashAPIKey(kv.secretKey, rawKey)

	var appID int64
	var info KeyInfo
	err := kv.dbPool.QueryRow(ctx, `
		SELECT
			ak.id,
			ak.tenant_id,
			ak.application_id,
			app.code AS application_code,
			COALESCE(ak.key_prefix, '') AS key_prefix,
			app.default_client_profile,
			ak.owner_user,
			ak.rate_limit_rpm,
			ak.rate_limit_concurrent,
			ak.rate_limit_tpm,
			COALESCE(ak.key_tier, 'default') AS key_tier,
			ak.budget_usd::float8,
			COALESCE(ak.status, 'active') AS status,
			ak.key_alias,
			app.customer_id
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.key_hash = $1
		  AND ak.enabled = TRUE
		  AND COALESCE(ak.status, 'active') NOT IN ('revoked', 'disabled')
		  AND (ak.expires_at IS NULL OR ak.expires_at > now())
	`, keyHash).Scan(
		&info.ID,
		&info.TenantID,
		&appID,
		&info.ApplicationCode,
		&info.KeyPrefix,
		&info.DefaultClientProfile,
		&info.OwnerUser,
		&info.RateLimitRPM,
		&info.RateLimitConcurrent,
		&info.RateLimitTPM,
		&info.KeyTier,
		&info.BudgetUSD,
		&info.Status,
		&info.KeyAlias,
		&info.CustomerID,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, &InvalidKeyError{Message: "Invalid or expired API key"}
		}
		return nil, err
	}
	info.ApplicationID = int(appID)
	// Throttled keys are allowed through (rate-limit enforced downstream)
	// but we surface the status so the relay handler can set appropriate headers.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = kv.dbPool.Exec(ctx, "UPDATE api_keys SET last_used_at = now() WHERE id = $1", info.ID)
	}()
	slog.Debug("key verified via db", "key_id", info.ID, "tenant_id", info.TenantID, "app_code", info.ApplicationCode, "tier", info.KeyTier, "status", info.Status)
	return &info, nil
}

// HashAPIKey hashes an API key using HMAC-SHA256 keyed with secretKey, returning
// the hex digest. This is the canonical key_hash stored in api_keys and used by
// the verifier for lookups. All writers (admin, self-check worker, etc.) MUST use
// this so the verifier's WHERE key_hash = $1 matches.
func HashAPIKey(secretKey, rawKey string) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(rawKey))
	return hex.EncodeToString(mac.Sum(nil))
}

// hashAPIKey is the unexported alias kept for the internal call sites below.
func hashAPIKey(secretKey, rawKey string) string {
	return HashAPIKey(secretKey, rawKey)
}

func (kv *KeyVerifier) getCache(key string) *KeyInfo {
	kv.mu.RLock()
	defer kv.mu.RUnlock()
	entry, ok := kv.cache[key]
	if !ok {
		return nil
	}
	if time.Now().After(entry.expiresAt) {
		return nil
	}
	return entry.info
}

func (kv *KeyVerifier) setCache(key string, info *KeyInfo) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	kv.cache[key] = &keyCacheEntry{
		info:      info,
		expiresAt: time.Now().Add(kv.ttl),
	}
	if len(kv.cache) > 10000 {
		now := time.Now()
		for k, e := range kv.cache {
			if now.After(e.expiresAt) {
				delete(kv.cache, k)
			}
		}
	}
}

// InvalidateKeyID drops the cached KeyInfo for the given api_key id.
//
// Admin write endpoints (update rate limits, enable/disable, revoke, patch
// profile) only know the key's DB id, not its raw plaintext, so the cache —
// which is keyed by raw key — must be scanned for a matching entry.info.ID.
// The map is capped at 10000 entries (see setCache), so this scan is cheap.
// After invalidation, the next Verify() reloads from DB and picks up the new
// rate_limit_rpm / rate_limit_concurrent / status immediately, rather than
// serving a stale entry for up to ttl (60s).
func (kv *KeyVerifier) InvalidateKeyID(id int) {
	if id <= 0 {
		return
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()
	for k, e := range kv.cache {
		if e.info != nil && e.info.ID == id {
			delete(kv.cache, k)
		}
	}
}

// InvalidateAll drops every cached KeyInfo. Useful for tests and as a coarse
// escape hatch; prefer InvalidateKeyID in request paths to avoid stampeding
// the DB.
func (kv *KeyVerifier) InvalidateAll() {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	clear(kv.cache)
}

type InvalidKeyError struct {
	Message string
}

func (e *InvalidKeyError) Error() string {
	return e.Message
}

type BudgetExceededError struct {
	KeyID  int
	Budget float64
	Spent  float64
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("budget exceeded for key %d: spent %.4f >= budget %.4f", e.KeyID, e.Spent, e.Budget)
}

func (kv *KeyVerifier) CheckBudget(ctx context.Context, keyID int) error {
	if !kv.Enabled() {
		return nil
	}
	return kv.checkBudgetDB(ctx, keyID)
}

func (kv *KeyVerifier) checkBudgetDB(ctx context.Context, keyID int) error {
	var budget *float64
	err := kv.dbPool.QueryRow(ctx, "SELECT budget_usd::float8 FROM api_keys WHERE id = $1 AND COALESCE(status, 'active') <> 'revoked'", keyID).Scan(&budget)
	if err != nil {
		return err
	}
	if budget == nil {
		return nil
	}
	var spent float64
	// Query from view (hot + partitions) to include recent 7-day data.
	// Note: usage_ledger_with_current_month is an optional aggregation
	// view that may not be migrated yet. In that case fall back to
	// spending=0 — budget enforcement is disabled until the view is
	// created, since the alternative would be failing every budgeted
	// request during a migration window. The primary access control
	// is API key validation, not the budget check, so this is safe.
	viewErr := kv.dbPool.QueryRow(ctx, "SELECT COALESCE(SUM(cost_usd), 0)::float8 FROM usage_ledger_with_current_month WHERE api_key_id = $1", keyID).Scan(&spent)
	if viewErr != nil {
		if isMissingUsageLedgerView(viewErr) {
			spent = 0
			slog.Warn("key verifier: usage_ledger_with_current_month view missing; budget enforcement disabled until migration is applied",
				"key_id", keyID,
				"hint", "apply migration 344 (sql/migrations/startup/344_usage_ledger_hot_independence.sql)",
			)
		} else {
			return viewErr
		}
	}
	if spent >= *budget {
		return &BudgetExceededError{KeyID: keyID, Budget: *budget, Spent: spent}
	}
	return nil
}

func isBudgetExceeded(err error) bool {
	_, ok := err.(*BudgetExceededError)
	return ok
}

// Verifier 是 KeyVerifier 的别名（兼容 pipeline.Hook 与外部调用方）。
type Verifier = KeyVerifier
