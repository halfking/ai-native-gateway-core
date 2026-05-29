package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"
)

type KeyInfo struct {
	ID                   int      `json:"id"`
	TenantID             string   `json:"tenant_id"`
	ApplicationID        int      `json:"application_id"`
	ApplicationCode      string   `json:"application_code"`
	DefaultClientProfile *string  `json:"default_client_profile"`
	OwnerUser            *string  `json:"owner_user"`
	RateLimitRPM         *int     `json:"rate_limit_rpm"`
	BudgetUSD            *float64 `json:"budget_usd"`
}

type KeyVerifier struct {
	endpoint   string
	adminKey   string
	httpClient *http.Client
	dbPool     *pgxpool.Pool
	secretKey  string

	cache   map[string]*keyCacheEntry
	mu      sync.RWMutex
	sfGroup singleflight.Group

	ttl time.Duration
}

type keyCacheEntry struct {
	info      *KeyInfo
	expiresAt time.Time
}

func NewKeyVerifier(pythonEndpoint, adminAPIKey string) *KeyVerifier {
	return &KeyVerifier{
		endpoint: pythonEndpoint,
		adminKey: adminAPIKey,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		cache: make(map[string]*keyCacheEntry),
		ttl:   60 * time.Second,
	}
}

func (kv *KeyVerifier) Enabled() bool {
	return (kv.dbPool != nil && kv.secretKey != "") || (kv.endpoint != "" && kv.adminKey != "")
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
		return info, nil
	}

	v, err, _ := kv.sfGroup.Do("key:"+rawKey, func() (any, error) {
		info, verifyErr := kv.callVerifyDB(ctx, rawKey)
		if verifyErr != nil {
			if _, ok := verifyErr.(*InvalidKeyError); ok {
				return nil, verifyErr
			}
			info, verifyErr = kv.callVerifyRPC(ctx, rawKey)
			if verifyErr != nil {
				return nil, verifyErr
			}
		}
		kv.setCache(rawKey, info)
		return info, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*KeyInfo), nil
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
			app.default_client_profile,
			ak.owner_user,
			ak.rate_limit_rpm,
			ak.budget_usd::float8
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.key_hash = $1
		  AND ak.enabled = TRUE
		  AND COALESCE(ak.status, 'active') <> 'revoked'
		  AND (ak.expires_at IS NULL OR ak.expires_at > now())
	`, keyHash).Scan(
		&info.ID,
		&info.TenantID,
		&appID,
		&info.ApplicationCode,
		&info.DefaultClientProfile,
		&info.OwnerUser,
		&info.RateLimitRPM,
		&info.BudgetUSD,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, &InvalidKeyError{Message: "Invalid or expired API key"}
		}
		return nil, err
	}
	info.ApplicationID = int(appID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = kv.dbPool.Exec(ctx, "UPDATE api_keys SET last_used_at = now() WHERE id = $1", info.ID)
	}()
	slog.Debug("key verified via db", "key_id", info.ID, "tenant_id", info.TenantID, "app_code", info.ApplicationCode)
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

func (kv *KeyVerifier) callVerifyRPC(ctx context.Context, rawKey string) (*KeyInfo, error) {
	reqBody := fmt.Sprintf(`{"api_key":"%s"}`, rawKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kv.endpoint+"/api/keys/verify", strings.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+kv.adminKey)

	resp, err := kv.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("key verify RPC failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, &InvalidKeyError{Message: "Invalid or expired API key"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("key verify RPC returned %d: %s", resp.StatusCode, string(body))
	}

	var info KeyInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("key verify RPC invalid response: %w", err)
	}

	slog.Debug("key verified",
		"key_id", info.ID,
		"tenant_id", info.TenantID,
		"app_code", info.ApplicationCode,
	)
	return &info, nil
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

type budgetCheckResponse struct {
	APIKeyID  int      `json:"api_key_id"`
	BudgetUSD *float64 `json:"budget_usd"`
	SpentUSD  float64  `json:"spent_usd"`
	Exceeded  bool     `json:"exceeded"`
}

func (kv *KeyVerifier) CheckBudget(ctx context.Context, keyID int) error {
	if !kv.Enabled() {
		return nil
	}
	if kv.dbPool != nil {
		if err := kv.checkBudgetDB(ctx, keyID); err == nil || isBudgetExceeded(err) {
			return err
		} else {
			slog.Warn("budget check DB failed, falling back to RPC", "error", err)
		}
	}
	if kv.endpoint == "" || kv.adminKey == "" {
		return nil
	}

	payload, _ := json.Marshal(map[string]any{"api_key_id": keyID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kv.endpoint+"/api/keys/budget-check", bytes.NewReader(payload))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+kv.adminKey)

	resp, err := kv.httpClient.Do(req)
	if err != nil {
		slog.Warn("budget check RPC failed", "error", err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		slog.Warn("budget check RPC non-200", "status", resp.StatusCode, "body", string(body))
		return nil
	}

	var result budgetCheckResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}

	if result.Exceeded {
		budget := 0.0
		if result.BudgetUSD != nil {
			budget = *result.BudgetUSD
		}
		return &BudgetExceededError{KeyID: keyID, Budget: budget, Spent: result.SpentUSD}
	}
	return nil
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
