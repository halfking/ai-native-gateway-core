package admin

// 改密吊销（Wave 3 B4，2026-09-22 设计差距审计）。
//
// 设计差距：JWT 是无状态 HMAC（无 jti/版本号），handleChangePassword 只清
// must_change_password 标记，改密后旧 token 在剩余 TTL（默认 24h）内依然
// 全权可用——共享过密码的会话无法被"改密即踢出"。
//
// 机制：settings_kv 表的 admin.auth_revocations 键存
// {"<user_id>": "<RFC3339 epoch>"}。改密成功时以 jsonb_set 原子推进该用户
// 的纪元（并发改密不丢更新，零迁移）；认证中间件在 JWT 验签通过后比对
// claims 签发时间早于纪元即 401。读取走 5s 进程内 TTL 缓存（所有验证点
// 共享，跨实例靠 DB 落盘，滞后 ≤5s）。读取失败一律放行（fail-open）：
// settings_kv 不可用不能把整个管理面锁死在 401。
//
// 该键刻意不注册 settings spec：它是内部安全簿记，不应出现在管理 UI 的
// 可编辑列表里（也避免 object 值挤进标量编辑器）。

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// authRevocationsKey is the settings_kv row holding the per-user epochs.
const authRevocationsKey = "admin.auth_revocations"

// authRevocationCacheTTL bounds staleness of the epoch map shared by the
// auth middlewares. Invalidated eagerly on the write path.
const authRevocationCacheTTL = 5 * time.Second

// authRevocationMaxAge drops epochs older than this while parsing. JWT
// lifetime is ≤~24h (LLM_GATEWAY_JWT_EXPIRY default), so an epoch this old
// can never again reject a live token and is pure ballast.
const authRevocationMaxAge = 7 * 24 * time.Hour

var (
	authRevocationMu     sync.Mutex
	authRevocationMap    map[int]time.Time
	authRevocationLoaded time.Time
)

// authRevocationProbe is the test seam; production uses probeAuthRevocation.
var authRevocationProbe = probeAuthRevocation

// revokeUserTokensForPasswordChange advances the user's revocation epoch to
// now, atomically merging into the shared settings_kv row.
func revokeUserTokensForPasswordChange(ctx context.Context, db *pgxpool.Pool, userID int) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(ctx, `
		INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at, updated_by)
		VALUES ($1, jsonb_build_object($2::text, to_jsonb(now())), 'string', 'platform', 'security', now(), 'system:change_password')
		ON CONFLICT (key) DO UPDATE SET
			value = jsonb_set(settings_kv.value, ARRAY[$2::text], to_jsonb(now())),
			updated_at = now()
	`, authRevocationsKey, strconv.Itoa(userID))
	if err != nil {
		return err
	}
	// The key is not settings-registry resident, but clearing it is cheap
	// and keeps the two caches coherent if a spec is ever added.
	settings.InvalidatePlatformValue(authRevocationsKey)
	invalidateAuthRevocationCache()
	return nil
}

// probeAuthRevocation reports whether the token was issued before the
// user's current revocation epoch. Any infrastructure failure returns
// false: a settings_kv outage must not lock the admin plane out entirely.
func probeAuthRevocation(ctx context.Context, db *pgxpool.Pool, userID int, issuedAt time.Time) bool {
	if db == nil || userID <= 0 {
		return false
	}
	epoch, ok := authRevocationEpoch(ctx, db, userID)
	if !ok {
		return false
	}
	return issuedAt.Before(epoch)
}

// authRevocationEpoch returns the user's revocation epoch via the shared
// TTL cache, querying settings_kv on miss.
func authRevocationEpoch(ctx context.Context, db *pgxpool.Pool, userID int) (time.Time, bool) {
	authRevocationMu.Lock()
	defer authRevocationMu.Unlock()
	now := time.Now()
	if authRevocationMap != nil && now.Sub(authRevocationLoaded) < authRevocationCacheTTL {
		epoch, ok := authRevocationMap[userID]
		return epoch, ok
	}
	raw := fetchAuthRevocationsRaw(ctx, db)
	parsed := parseAuthRevocations(raw)
	authRevocationMap = parsed
	authRevocationLoaded = now
	epoch, ok := parsed[userID]
	return epoch, ok
}

func invalidateAuthRevocationCache() {
	authRevocationMu.Lock()
	authRevocationMap = nil
	authRevocationLoaded = time.Time{}
	authRevocationMu.Unlock()
}

func fetchAuthRevocationsRaw(ctx context.Context, db *pgxpool.Pool) []byte {
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var raw []byte
	err := db.QueryRow(queryCtx, `
		SELECT value::text FROM settings_kv WHERE key = $1
	`, authRevocationsKey).Scan(&raw)
	if err != nil {
		// ErrNoRows (no revocation ever recorded) and transport errors both
		// land here — fail-open either way.
		return nil
	}
	return raw
}

func parseAuthRevocations(raw []byte) map[int]time.Time {
	if len(raw) == 0 {
		return nil
	}
	var entries map[string]string
	if err := json.Unmarshal(raw, &entries); err != nil {
		slog.Warn("auth revocation map unparsable; ignoring", "error", err)
		return nil
	}
	cutoff := time.Now().Add(-authRevocationMaxAge)
	out := make(map[int]time.Time, len(entries))
	for k, v := range entries {
		userID, err := strconv.Atoi(k)
		if err != nil || userID <= 0 {
			continue
		}
		epoch, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			continue
		}
		if epoch.Before(cutoff) {
			continue
		}
		out[userID] = epoch
	}
	return out
}
