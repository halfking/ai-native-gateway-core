package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"
)

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
// A per-key value of 0 means "unlimited" (CheckConcurrent treats limit<=0 as no cap).
func (ki *KeyInfo) EffectiveConcurrent() int {
	if ki.RateLimitConcurrent != nil && *ki.RateLimitConcurrent >= 0 {
		return *ki.RateLimitConcurrent
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

type KeyVerifier struct {
	dbPool    *pgxpool.Pool
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

func (kv *KeyVerifier) SetDB(pool *pgxpool.Pool, secretKey string) {
	kv.dbPool = pool
	kv.secretKey = secretKey
}

func (kv *KeyVerifier) Verify(ctx context.Context, rawKey string) (*KeyInfo, error) {
	if !kv.Enabled() {
		return nil, fmt.Errorf("key verifier not configured")
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
			ak.application_id
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
			ak.key_alias
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

func hashAPIKey(secretKey, rawKey string) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(rawKey))
	return hex.EncodeToString(mac.Sum(nil))
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
	if err := kv.dbPool.QueryRow(ctx, "SELECT COALESCE(SUM(cost_usd), 0)::float8 FROM usage_ledger WHERE api_key_id = $1", keyID).Scan(&spent); err != nil {
		return err
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
