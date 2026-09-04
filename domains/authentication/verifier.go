package authentication

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	// ExpiresAt carries api_keys.expires_at for the in-memory key store
	// (keystore_sync.go, 2026-09-04). The store validates expiry at READ
	// time so a key whose expires_at crosses "now" between syncs stops
	// authorizing without a DB round-trip. Serialized for the local
	// snapshot round-trip (HMAC hash + metadata only, no key material).
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
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
// 真实实现是 *pgxpool.Pool；测试可以用 mock（pgxmock.PgxPoolIface 天然满足）。
// Query (2026-09-04) 支撑 keystore_sync.go 的全量/增量加载。
type DBQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
}

type KeyVerifier struct {
	dbPool    DBQuerier
	secretKey string

	cache   map[string]*keyCacheEntry
	mu      sync.RWMutex
	sfGroup singleflight.Group

	ttl time.Duration
	// staleGrace bounds how long an EXPIRED cache entry may still authorize
	// requests while the verify query itself fails with an infrastructure
	// (non-InvalidKeyError) error — the DB-outage availability gear
	// (2026-09-04). Without it every key 503s 60s after the DB dies.
	// Default 10 minutes, env-tunable via LLM_GATEWAY_AUTH_STALE_GRACE_SECONDS
	// (0 disables stale serving and restores the strict behaviour).
	staleGrace time.Duration

	// keyStore (keystore_sync.go, 2026-09-04) is the full in-memory api_keys
	// replica keyed by key_hash. Once loaded, Verify() answers from it with
	// zero DB IO; a DB outage stops the delta sync but keeps the replica
	// serving for as long as the process lives. Guarded by storeMu.
	keyStore       map[string]*KeyInfo
	keyStoreLoaded atomic.Bool
	// keyStoreFromSnapshot marks a store populated from the LOCAL snapshot
	// file rather than a successful DB full load. Snapshot state is
	// untrusted: the sync loop keeps retrying the full load (instead of
	// deltas) until it succeeds, then clears the flag.
	keyStoreFromSnapshot atomic.Bool
	storeMu              sync.RWMutex

	// snapshotDir, when set, receives the periodic local snapshot that
	// lets a process boot WITH authentication even when PostgreSQL is
	// unreachable at startup (2026-09-04 cold-start availability gear).
	snapshotDir string

	// lastUsedTouch throttles the fire-and-forget UPDATE api_keys SET
	// last_used_at to at most one write per key per touchInterval (60s) —
	// the store fast path would otherwise fire it on every request. Those
	// writes also act as the delta-sync watermark (see keystore_sync.go).
	lastUsedTouch   map[int]time.Time
	lastUsedTouchMu sync.Mutex
}

type keyCacheEntry struct {
	info      *KeyInfo
	expiresAt time.Time
}

const lastUsedTouchInterval = 60 * time.Second

func NewKeyVerifier() *KeyVerifier {
	return &KeyVerifier{
		cache:         make(map[string]*keyCacheEntry),
		ttl:           60 * time.Second,
		staleGrace:    authStaleGraceFromEnv(),
		keyStore:      make(map[string]*KeyInfo),
		lastUsedTouch: make(map[int]time.Time),
	}
}

// authStaleGraceFromEnv reads LLM_GATEWAY_AUTH_STALE_GRACE_SECONDS.
// Missing/malformed/negative values fall back to the 10-minute default;
// an explicit 0 disables DB-outage stale serving.
func authStaleGraceFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_AUTH_STALE_GRACE_SECONDS"))
	if raw == "" {
		return 10 * time.Minute
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		slog.Warn("key verifier: invalid LLM_GATEWAY_AUTH_STALE_GRACE_SECONDS, using default",
			"value", raw, "default", "600s")
		return 10 * time.Minute
	}
	return time.Duration(n) * time.Second
}

// Enabled reports whether the verifier can authorize requests. That is the
// case with a DB pool configured, OR — the 2026-09-04 cold-start gear —
// with a loaded key store (DB pool may be nil when the process booted from
// the local snapshot while PostgreSQL was unreachable).
func (kv *KeyVerifier) Enabled() bool {
	if kv.secretKey == "" {
		return false
	}
	return kv.dbPool != nil || kv.keyStoreLoaded.Load()
}

// SetSecretKey configures the HMAC secret WITHOUT a DB pool: snapshot-only
// mode for processes that boot while PostgreSQL is unreachable. Store hits
// authorize; store misses fail closed (callVerifyDB has no pool).
func (kv *KeyVerifier) SetSecretKey(secretKey string) {
	kv.secretKey = secretKey
}

// SetDB 注入数据库连接池与 HMAC 密钥。
func (kv *KeyVerifier) SetDB(pool *pgxpool.Pool, secretKey string) {
	kv.dbPool = &pgxPoolAdapter{pool: pool}
	kv.secretKey = secretKey
	if kv.keyStore == nil {
		kv.keyStore = make(map[string]*KeyInfo)
	}
	if kv.lastUsedTouch == nil {
		kv.lastUsedTouch = make(map[int]time.Time)
	}
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
func (a *pgxPoolAdapter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return a.pool.Query(ctx, sql, args...)
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
		return nil, &InvalidKeyError{Message: "Invalid API key"}
	}

	// Key-store fast path (keystore_sync.go, 2026-09-04): once the full
	// replica is loaded, answer with zero DB IO. During a DB outage the
	// sync simply stops refreshing — the replica keeps authorizing for the
	// life of the process, which is the availability posture this store
	// exists for.
	if info := kv.lookupStore(hashAPIKey(kv.secretKey, rawKey)); info != nil {
		kv.touchLastUsedThrottled(info.ID)
		return info, nil
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
			// DB-outage availability gear (2026-09-04): an infrastructure
			// error (pool exhausted, connection refused, timeout) must not
			// lock out keys this process authenticated recently. Serve the
			// expired-but-present cache entry within staleGrace. A genuine
			// InvalidKeyError is NEVER substituted — unknown keys keep
			// failing 401 while the DB is down.
			var invalid *InvalidKeyError
			if !errors.As(verifyErr, &invalid) {
				if stale, staleAge := kv.getStaleCache(rawKey); stale != nil {
					slog.Warn("key verify: db unavailable, serving stale cache entry",
						"error", verifyErr,
						"stale_for", staleAge.Round(time.Second).String())
					return stale, nil
				}
			}
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
			app.customer_id,
			ak.expires_at
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
		&info.ExpiresAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Reconcile the store against hard deletes: a row that vanished
			// from the DB must not keep authorizing from the replica.
			kv.removeFromStore(keyHash)
			return nil, &InvalidKeyError{Message: "Invalid or expired API key"}
		}
		return nil, err
	}
	info.ApplicationID = int(appID)
	// Lazy misses converge into the store so the next request for this key
	// needs no DB IO (also covers keys created between delta syncs).
	kv.upsertStore(keyHash, &info)
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

// VerifyByID re-validates the server-side authorization context of an API key
// by its DB id — the durable recovery path (doc 18 §11.2: the snapshot keeps
// only api_key_id; workers must re-verify instead of replaying credentials).
// The predicate mirrors callVerifyDB: disabled / revoked / expired keys fail.
// It does NOT update last_used_at (the key owner is not making a request).
func (kv *KeyVerifier) VerifyByID(ctx context.Context, id int) (*KeyInfo, error) {
	if !kv.Enabled() {
		return nil, fmt.Errorf("key verifier not configured")
	}
	if id <= 0 {
		return nil, fmt.Errorf("VerifyByID: invalid api key id %d", id)
	}
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
		WHERE ak.id = $1
		  AND ak.enabled = TRUE
		  AND COALESCE(ak.status, 'active') NOT IN ('revoked', 'disabled')
		  AND (ak.expires_at IS NULL OR ak.expires_at > now())
	`, id).Scan(
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
			return nil, &InvalidKeyError{Message: fmt.Sprintf("api key %d invalid, revoked or expired", id)}
		}
		return nil, err
	}
	info.ApplicationID = int(appID)
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

// getStaleCache returns the cached KeyInfo for key when the entry exists,
// is already expired, and its age past expiry is within the stale grace
// window (DB-outage availability gear, 2026-09-04). Expired entries survive
// in the map until the opportunistic cap-based eviction inside setCache, so
// this is best-effort: an evicted key simply fails as before. Returns the
// info and its age past expiry (for logging); (nil, 0) when not servable.
func (kv *KeyVerifier) getStaleCache(key string) (*KeyInfo, time.Duration) {
	kv.mu.RLock()
	defer kv.mu.RUnlock()
	entry, ok := kv.cache[key]
	if !ok || entry.info == nil {
		return nil, 0
	}
	age := time.Since(entry.expiresAt)
	if kv.staleGrace <= 0 || age <= 0 || age > kv.staleGrace {
		return nil, 0
	}
	return entry.info, age
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
//
// 2026-09-04: also drops the matching in-memory key-store entry (the store
// is keyed by hash and carries no raw key, so the same scan applies) —
// same-process revocations stay immediate despite the 5-minute delta sync.
func (kv *KeyVerifier) InvalidateKeyID(id int) {
	if id <= 0 {
		return
	}
	kv.mu.Lock()
	for k, e := range kv.cache {
		if e.info != nil && e.info.ID == id {
			delete(kv.cache, k)
		}
	}
	kv.mu.Unlock()
	kv.storeMu.Lock()
	for h, info := range kv.keyStore {
		if info != nil && info.ID == id {
			delete(kv.keyStore, h)
		}
	}
	kv.storeMu.Unlock()
}

// InvalidateAll drops every cached KeyInfo. Useful for tests and as a coarse
// escape hatch; prefer InvalidateKeyID in request paths to avoid stampeding
// the DB.
func (kv *KeyVerifier) InvalidateAll() {
	kv.mu.Lock()
	clear(kv.cache)
	kv.mu.Unlock()
	kv.storeMu.Lock()
	clear(kv.keyStore)
	kv.storeMu.Unlock()
	kv.keyStoreLoaded.Store(false)
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
