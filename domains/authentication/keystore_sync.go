// Package authentication — keystore_sync.go
//
// 全量 + 增量的 api_keys 内存副本（2026-09-04 可用性方案）。
//
// 背景：per-key 60s TTL 缓存在 DB 宕机时只有 staleGrace（默认 10 分钟）的
// 兜底窗口，且冷进程没有缓存可用。本文件把整张（有效）key 表复制进内存：
//
//   - 启动时全量加载（DB 不可达则跳过，退回懒加载路径，不阻塞启动）；
//   - 之后每个同步周期做两件事：
//     1) 增量水位拉取：api_keys 没有 updated_at 列，但验证路径本身就以
//     ≥60s/key 的节流更新 last_used_at（见 touchLastUsedThrottled）。
//     以 last_used_at 为水位拉取最近活跃 key 的完整行，限流/预算/别名
//     等字段变更随下一次同步传播（≤ 周期 + 节流）。
//     2) 失效集移除：撤销/禁用不会触碰 last_used_at，因此单独查询当前
//     无效 key 的 key_hash 并从副本删除——撤销传播延迟 ≤ 同步周期。
//   - 每小时一次全量对账，兜底硬删除与漏网变更；
//   - DB 宕机时同步失败仅告警，副本继续服务（这正是本方案的目的）。
//
// 副本按 key_hash 键控（不保留原始 key），读取时校验 expires_at，因此
// 一个 key 自然过期不需要任何同步动作就会立刻失效。
package authentication

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const (
	// defaultKeyStoreSyncInterval is the delta-sync period (2026-09-04
	// design: 5 minutes). Env-tunable via LLM_GATEWAY_KEYSTORE_SYNC_INTERVAL;
	// 0 disables the whole feature (pure lazy verification as before).
	defaultKeyStoreSyncInterval = 5 * time.Minute

	// keyStoreFullRefreshInterval reconciles the replica with a full reload:
	// catches hard-deleted rows and any change the last_used_at watermark
	// cannot observe (e.g. an admin edit on a key that nobody used).
	keyStoreFullRefreshInterval = time.Hour

	// keyStoreQueryTimeout bounds each sync query so a hung DB cannot pile
	// up goroutines; sync failures are non-fatal by design.
	keyStoreQueryTimeout = 30 * time.Second
)

// StartKeyStoreSync launches the background sync goroutine and returns
// immediately. Cancel ctx (process shutdown) to stop it. The initial full
// load happens on the goroutine: a DB that is down at boot logs a warning
// and the verifier keeps using the lazy per-key path — startup is never
// blocked. interval <= 0 falls back to the default.
func (kv *KeyVerifier) StartKeyStoreSync(ctx context.Context, interval time.Duration) {
	if kv == nil || kv.dbPool == nil {
		return
	}
	if interval <= 0 {
		interval = defaultKeyStoreSyncInterval
	}
	go kv.keyStoreSyncLoop(ctx, interval)
}

// SetSnapshotDir enables the local snapshot (cold-start availability
// gear). After every successful full load and delta sync the store is
// persisted to dir/api_keys_snapshot.json; a process that boots while
// PostgreSQL is unreachable can load it via LoadSnapshot and keep
// authenticating. Empty dir disables persistence.
func (kv *KeyVerifier) SetSnapshotDir(dir string) {
	kv.snapshotDir = dir
}

func (kv *KeyVerifier) keyStoreSyncLoop(ctx context.Context, interval time.Duration) {
	if err := kv.keyStoreFullLoad(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		// DB down at boot: a local snapshot (if any) bridges the gap with
		// its last-known state while the loop keeps retrying the full load.
		if n := kv.tryLoadSnapshot(); n > 0 {
			slog.Warn("key store: initial full load failed, serving from local snapshot until the database recovers",
				"error", err, "keys", n)
		} else {
			slog.Warn("key store: initial full load failed, retrying on next tick (lazy verification active)",
				"error", err)
		}
	} else {
		kv.keyStoreFromSnapshot.Store(false)
		kv.saveSnapshotQuietly()
		slog.Info("key store: full load complete", "keys", kv.keyStoreLen())
	}
	lastFull := time.Now()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Snapshot state is untrusted and an empty store means the
			// initial load never succeeded: in both cases retry the FULL
			// load (not the delta) so the store converges as soon as the
			// DB answers; otherwise fall back to the hourly full refresh
			// for hard-delete reconciliation.
			if kv.keyStoreFromSnapshot.Load() || !kv.keyStoreLoaded.Load() ||
				time.Since(lastFull) >= keyStoreFullRefreshInterval {
				if err := kv.keyStoreFullLoad(ctx); err != nil {
					if ctx.Err() != nil {
						return
					}
					slog.Warn("key store: full refresh failed, replica unchanged", "error", err)
					continue
				}
				lastFull = time.Now()
				kv.keyStoreFromSnapshot.Store(false)
				kv.saveSnapshotQuietly()
				slog.Info("key store: full refresh complete", "keys", kv.keyStoreLen())
				continue
			}
			if err := kv.keyStoreSyncDelta(ctx, interval); err != nil {
				if ctx.Err() != nil {
					return
				}
				// DB outage posture: warn and keep serving from the replica.
				slog.Warn("key store: delta sync failed, replica unchanged (serving from memory)", "error", err)
				continue
			}
			kv.saveSnapshotQuietly()
		}
	}
}

// keyStoreFullLoad replaces the replica with every currently-valid key.
// The predicate mirrors callVerifyDB exactly so store hits and lazy hits
// authorize the identical key set.
func (kv *KeyVerifier) keyStoreFullLoad(ctx context.Context) error {
	qctx, cancel := context.WithTimeout(ctx, keyStoreQueryTimeout)
	defer cancel()
	rows, err := kv.dbPool.Query(qctx, keyStoreSelectSQL+`
		WHERE ak.enabled = TRUE
		  AND COALESCE(ak.status, 'active') NOT IN ('revoked', 'disabled')
		  AND (ak.expires_at IS NULL OR ak.expires_at > now())
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := make(map[string]*KeyInfo, 1024)
	for rows.Next() {
		info, scanErr := scanKeyStoreRow(rows)
		if scanErr != nil {
			return scanErr
		}
		next[info.keyHash] = info.info
	}
	if err := rows.Err(); err != nil {
		return err
	}
	kv.storeMu.Lock()
	kv.keyStore = next
	kv.storeMu.Unlock()
	kv.keyStoreLoaded.Store(true)
	return nil
}

// keyStoreSyncDelta refreshes recently-used keys and removes currently
// invalid ones. window is 2× the interval so a single missed tick (DB
// blip, slow query) cannot drop a key out of the watermark.
func (kv *KeyVerifier) keyStoreSyncDelta(ctx context.Context, interval time.Duration) error {
	qctx, cancel := context.WithTimeout(ctx, keyStoreQueryTimeout)
	defer cancel()
	window := 2 * interval
	rows, err := kv.dbPool.Query(qctx, keyStoreSelectSQL+`
		WHERE ak.enabled = TRUE
		  AND COALESCE(ak.status, 'active') NOT IN ('revoked', 'disabled')
		  AND (ak.expires_at IS NULL OR ak.expires_at > now())
		  AND ak.last_used_at >= now() - make_interval(mins => $1)
	`, int64(window.Minutes()))
	if err != nil {
		return err
	}
	upserted := 0
	for rows.Next() {
		entry, scanErr := scanKeyStoreRow(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		kv.upsertStore(entry.keyHash, entry.info)
		upserted++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Revocation propagation: status flips never touch last_used_at, so the
	// invalid set is queried explicitly and removed from the replica.
	invRows, err := kv.dbPool.Query(qctx, `
		SELECT ak.key_hash
		FROM api_keys ak
		WHERE ak.enabled = FALSE
		   OR COALESCE(ak.status, 'active') IN ('revoked', 'disabled')
		   OR (ak.expires_at IS NOT NULL AND ak.expires_at <= now())
	`)
	if err != nil {
		return err
	}
	removed := 0
	kv.storeMu.Lock()
	for invRows.Next() {
		var hash string
		if err := invRows.Scan(&hash); err != nil {
			continue
		}
		if _, ok := kv.keyStore[hash]; ok {
			delete(kv.keyStore, hash)
			removed++
		}
	}
	kv.storeMu.Unlock()
	invRows.Close()
	if err := invRows.Err(); err != nil {
		return err
	}
	if upserted > 0 || removed > 0 {
		slog.Debug("key store: delta sync", "upserted", upserted, "removed", removed, "total", kv.keyStoreLen())
	}
	return nil
}

// keyStoreSelectSQL is the shared column list. It matches callVerifyDB's
// projection plus ak.expires_at (read-time validity) and ak.key_hash (the
// store key).
const keyStoreSelectSQL = `
	SELECT
		ak.key_hash,
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
`

type keyStoreRow struct {
	keyHash string
	info    *KeyInfo
}

func scanKeyStoreRow(rows interface{ Scan(dest ...any) error }) (keyStoreRow, error) {
	var appID int64
	var hash string
	var info KeyInfo
	if err := rows.Scan(
		&hash,
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
	); err != nil {
		return keyStoreRow{}, err
	}
	info.ApplicationID = int(appID)
	return keyStoreRow{keyHash: hash, info: &info}, nil
}

// lookupStore returns the store entry for a key_hash when the replica is
// loaded and the entry is still valid at READ time (expires_at may have
// crossed now since the last sync). Returns nil otherwise; the caller then
// falls back to the lazy per-key path.
func (kv *KeyVerifier) lookupStore(keyHash string) *KeyInfo {
	if !kv.keyStoreLoaded.Load() {
		return nil
	}
	kv.storeMu.RLock()
	info, ok := kv.keyStore[keyHash]
	kv.storeMu.RUnlock()
	if !ok || info == nil {
		return nil
	}
	if info.ExpiresAt != nil && !info.ExpiresAt.After(time.Now()) {
		return nil
	}
	return info
}

func (kv *KeyVerifier) upsertStore(keyHash string, info *KeyInfo) {
	if keyHash == "" || info == nil {
		return
	}
	kv.storeMu.Lock()
	if kv.keyStore == nil {
		kv.keyStore = make(map[string]*KeyInfo)
	}
	kv.keyStore[keyHash] = info
	kv.storeMu.Unlock()
}

func (kv *KeyVerifier) removeFromStore(keyHash string) {
	kv.storeMu.Lock()
	delete(kv.keyStore, keyHash)
	kv.storeMu.Unlock()
}

func (kv *KeyVerifier) keyStoreLen() int {
	kv.storeMu.RLock()
	defer kv.storeMu.RUnlock()
	return len(kv.keyStore)
}

// touchLastUsedThrottled fires the same fire-and-forget UPDATE api_keys SET
// last_used_at as the lazy path, but at most once per key per
// lastUsedTouchInterval. Those writes are also the delta-sync watermark:
// an active key reappears in each 5-minute delta with its freshest row, so
// admin edits to rate limits/budget propagate within one sync period.
func (kv *KeyVerifier) touchLastUsedThrottled(id int) {
	if id <= 0 || kv.dbPool == nil {
		return
	}
	now := time.Now()
	kv.lastUsedTouchMu.Lock()
	if last, ok := kv.lastUsedTouch[id]; ok && now.Sub(last) < lastUsedTouchInterval {
		kv.lastUsedTouchMu.Unlock()
		return
	}
	kv.lastUsedTouch[id] = now
	if len(kv.lastUsedTouch) > 100000 {
		for k, t := range kv.lastUsedTouch {
			if now.Sub(t) > 10*lastUsedTouchInterval {
				delete(kv.lastUsedTouch, k)
			}
		}
	}
	kv.lastUsedTouchMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = kv.dbPool.Exec(ctx, "UPDATE api_keys SET last_used_at = now() WHERE id = $1", id)
	}()
}

// ── Local snapshot (cold-start availability gear, 2026-09-04) ──────────
//
// The snapshot closes the last cold-start hole: a process that (re)starts
// while PostgreSQL is unreachable previously had NO authentication data at
// all. The file contains key_hash (HMAC-SHA256 keyed with the server
// secret — not reversible without it) plus per-key metadata. It holds NO
// key material, so persisting it is the same trust class as the api_keys
// row itself minus the ciphertext columns.

const (
	snapshotFileName = "api_keys_snapshot.json"
	// maxSnapshotAge bounds how old a snapshot may be and still be served
	// at boot. Admins cannot revoke keys while the DB is down (the admin
	// API needs it too), so the practical staleness risk is revocations
	// issued between the last snapshot write and the DB crash — minutes,
	// not days. Seven days is a deliberately generous sanity ceiling.
	maxSnapshotAge = 7 * 24 * time.Hour
)

type keystoreSnapshotFile struct {
	SavedAt time.Time           `json:"saved_at"`
	Entries map[string]*KeyInfo `json:"entries"`
}

// SaveSnapshot persists the current store to dir (atomic tmp+rename,
// 0600). Called after successful syncs only; failures are the caller's
// to log via saveSnapshotQuietly.
func (kv *KeyVerifier) SaveSnapshot(dir string) error {
	if dir == "" {
		return nil
	}
	kv.storeMu.RLock()
	snap := keystoreSnapshotFile{SavedAt: time.Now().UTC(), Entries: kv.keyStore}
	data, err := json.Marshal(snap)
	kv.storeMu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, snapshotFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// saveSnapshotQuietly persists the store when a snapshot dir is configured.
// Never fatal: a read-only filesystem or full disk only costs the
// cold-start gear, not live serving.
func (kv *KeyVerifier) saveSnapshotQuietly() {
	if kv.snapshotDir == "" {
		return
	}
	if err := kv.SaveSnapshot(kv.snapshotDir); err != nil {
		slog.Warn("key store: snapshot save failed (cold-start fallback unavailable)", "error", err)
	}
}

// LoadSnapshot populates the store from dir's snapshot when it exists and
// is fresh enough (≤ maxSnapshotAge). Marks the store snapshot-sourced so
// the sync loop prefers full reloads until the DB answers. Returns the
// number of entries loaded (0 = nothing loaded).
func (kv *KeyVerifier) LoadSnapshot(dir string) (int, error) {
	if dir == "" {
		return 0, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, snapshotFileName))
	if err != nil {
		return 0, err
	}
	var snap keystoreSnapshotFile
	if err := json.Unmarshal(data, &snap); err != nil {
		return 0, err
	}
	if snap.SavedAt.IsZero() || time.Since(snap.SavedAt) > maxSnapshotAge {
		return 0, fmt.Errorf("snapshot too old (saved_at %s, max age %s)", snap.SavedAt.Format(time.RFC3339), maxSnapshotAge)
	}
	if len(snap.Entries) == 0 {
		return 0, fmt.Errorf("snapshot has no entries")
	}
	kv.storeMu.Lock()
	kv.keyStore = snap.Entries
	kv.storeMu.Unlock()
	kv.keyStoreLoaded.Store(true)
	kv.keyStoreFromSnapshot.Store(true)
	return len(snap.Entries), nil
}

// tryLoadSnapshot is LoadSnapshot with logging only on success; used by
// the sync loop's initial-load failure path where absence of a snapshot is
// the normal case, not a warning-worthy event.
func (kv *KeyVerifier) tryLoadSnapshot() int {
	n, err := kv.LoadSnapshot(kv.snapshotDir)
	if err != nil || n == 0 {
		return 0
	}
	return n
}
