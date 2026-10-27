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
	authRevocationMu     sync.RWMutex
	authRevocationMap    map[int]time.Time
	authRevocationLoaded time.Time
	// authRevocationGen is bumped by every invalidation. A fetch that was
	// already in flight when an invalidation happened must NOT re-install its
	// (now stale) result — otherwise a just-revoked epoch could stay
	// invisible for a whole TTL. See authRevocationEpoch.
	authRevocationGen uint64
)

// authRevocationFetch is the seam for the settings_kv read (tests replace it
// to simulate latency / interleavings).
var authRevocationFetch = fetchAuthRevocationsRaw

// authRevocationProbe is the test seam; production uses probeAuthRevocation.
var authRevocationProbe = probeAuthRevocation

// RevokedByPasswordChange reports whether a token issued at issuedAt was
// invalidated by a later password change for userID.
//
// ★ 2026-10-27（r1039 后续轮，审计档 §8 第 9 条）：ServeMux 侧的管理中间件
// （AdminMiddleware / SuperAdminMiddleware / tenantAdminOrSuperAdmin）一直带
// 这个探测，而 **Echo 侧**（cmd/gateway/main_jwt_middleware.go 注册的
// `/api/admin/*`）**没有** ⇒ 改密之后，旧会话在 Echo 侧端点上继续全权可用。
//
// 本函数是 `authRevocationProbe` 的**导出外壳** ⇒ 两边共用同一份实现、
// 同一份 5s TTL 缓存、同一把「读失败一律放行」的尺子 ⇒ 口径不会漂。
//
// 语义边界（不要误读）：
//   - 只判「**签发时间早于该用户的改密纪元**」这一条，**不新增任何撤销条件**；
//   - 读不到 settings_kv 一律**放行**（fail-open）：设置库不可用不能把整个
//     管理面锁死在 401。
func RevokedByPasswordChange(ctx context.Context, db *pgxpool.Pool, userID int, issuedAt time.Time) bool {
	return authRevocationProbe(ctx, db, userID, issuedAt)
}

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
//
// ★ 2026-10-28（12h 审计轮）—— 本函数此前有**两个**并发/正确性缺陷，
//
//	两者都因「缓存是否新鲜」这件事**用错了判据**而生：
//
// (1) ★★ **缓存永远不命中**（正确性 + 性能）：新鲜度判据原本是
//
//	`authRevocationMap != nil && ...` ⇒ 用「map 是不是 nil」当「缓存
//	有没有内容」。而 parseAuthRevocations 在 **settings_kv 里还没有那
//	一行**（= 系统里**从未有人改过密码** ⇒ 最常见的常态）时返回 **nil**
//	⇒ map 恒为 nil ⇒ **每一次**探测都判定「缓存不新鲜」⇒ **每次都重新
//	查一次库**。实测（临时插桩）：连做 5 次探测 ⇒ **5 次 settings_kv
//	取数**。
//	⇒ 判据必须改成「**在 TTL 内**」这一件事本身，与 map 的内容无关 ⇒
//	  下面用 authRevocationLoaded（非零 ⇒ 已装载过）作为新鲜度判据。
//
// (2) ★★ **全局锁被跨越一次 DB 往返持有**（并发锁控制）：原实现是
//
//	`Lock(); …; fetchAuthRevocationsRaw(ctx, db); …; Unlock()`
//	⇒ 取数期间**全局互斥**被占着 ⇒ 每一个并发的管理面鉴权请求都排在
//	它后面。实测（临时插桩，把一次取数模拟成 50ms）：**8 个并发探测的
//	墙钟 = 414ms**（= 8 × 50ms，完全串行）⇒ 单个慢查询会把所有并发
//	管理面请求一起拖住（取数自身还有 2s 超时 ⇒ 最坏把整个管理面拖 2s）。
//	⇒ 改成：**先在读锁下取快照 → 锁外做 DB 取数 → 再用写锁回填**。
//	⇒ 取数期间不再持锁 ⇒ 并发探测互不阻塞。
//
// (3) ★★ **回填可能覆盖一次更新的失效**（竞态）：把取数挪到锁外之后，
//
//	一次**在途**的取数可能在「改密 ⇒ 失效缓存」之后才回来 ⇒ 把它读到的
//	**旧**纪元写回缓存 ⇒ 刚被吊销的 token 在整个 TTL（5s）内**继续有效**
//	⇒ 这是安全语义的漏洞，不是性能问题。
//	⇒ 用 authRevocationGen（每次失效自增）挡住：取数前记下 gen，回填时
//	若 gen 已变 ⇒ **丢弃这次结果**（不写回）⇒ 失效语义不被覆盖。
func authRevocationEpoch(ctx context.Context, db *pgxpool.Pool, userID int) (time.Time, bool) {
	now := time.Now()

	// ── 快路径：读锁下取快照（读锁不阻塞彼此）────────────────────────
	authRevocationMu.RLock()
	loadedAt := authRevocationLoaded
	mapAtRead := authRevocationMap
	genAtRead := authRevocationGen
	authRevocationMu.RUnlock()

	// ★ 判据 = 「在 TTL 内」这一件事本身（**不再**看 map 是不是 nil）。
	//   loadedAt 非零 ⇒ 至少装载过一次 ⇒ 即使那次装载的结果是「空」
	//   （= 还没有任何改密记录），它也是**新鲜的**⇒ 不必再查库。
	if !loadedAt.IsZero() && now.Sub(loadedAt) < authRevocationCacheTTL {
		epoch, ok := mapAtRead[userID]
		return epoch, ok
	}

	// ── 慢路径：**锁外**取数（不占全局互斥）──────────────────────────
	raw := authRevocationFetch(ctx, db)
	parsed := parseAuthRevocations(raw)

	// ── 回填：写锁下，且**必须**确认期间没有发生过失效 ───────────────
	authRevocationMu.Lock()
	if authRevocationGen == genAtRead {
		// gen 未变 ⇒ 期间没有失效 ⇒ 这次结果是当前真相 ⇒ 写回。
		authRevocationMap = parsed
		authRevocationLoaded = time.Now()
	}
	authRevocationMu.Unlock()

	epoch, ok := parsed[userID]
	return epoch, ok
}

func invalidateAuthRevocationCache() {
	authRevocationMu.Lock()
	authRevocationMap = nil
	authRevocationLoaded = time.Time{}
	authRevocationGen++
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
