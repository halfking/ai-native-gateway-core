// Package summarystore persists session-level LLM summaries to the
// session_summaries table. Both the v2 dispatch summarizer
// (domains/sessionsummary/summarizer.go) and the new on-request auto
// summarizer (admin/auto_summary_generator.go) write here so that the row
// shape stays consistent across all summary paths.
//
// 2026-08-06: extracted from domains/sessionsummary/summarizer.go so the
// admin package can write summaries without importing the v2 dispatch code
// (which would create a cycle admin → sessionsummary → admin).
//
// 2026-08-22 (T11-P0):
//   - Upsert 的 ON CONFLICT 子句从 (session_key) 改为 (tenant_id, session_key)，
//     配合 migration 560 加的 UNIQUE(tenant_id, session_key) 约束；
//   - 移除 COALESCE(session_summaries.summary_version, 0) + 1 的自增语义，
//     改为 session_summaries.summary_version + 1（行存在时基于持久版本 +1）；
//   - 暴露 UpsertCAS(ctx, sum, expectedVersion) 严格 CAS 接口；
//   - 读侧函数补 tenantID 入参，WHERE 子句加 AND tenant_id = $N（不依赖 RLS）。
package summarystore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrStaleVersion 表示 UpsertCAS / Upsert 检测到持久版本与期望版本不一致。
// callers 可通过 errors.Is(err, ErrStaleVersion) 判断并按业务策略处理
// （重读、重试、放弃）。
//
// 2026-08-22（T11-P0）：T11 解锁后会接入 auto_summary 的 rolling-gate，
// 让两个 writer 跨进程 CAS 写入。
var ErrStaleVersion = errors.New("summarystore: stale summary_version")

// Summary is the persistent shape of a session-level LLM summary. The
// columns written match the session_summaries table DDL shipped in
// sql/init-complete-minimal.sql + the LLM-specific columns added by
// migration 358 (title, summary, key_topics, user_intent,
// last_summarized_at, summary_version).
//
// 2026-08-06: FirstRequestAt / LastRequestAt added because
// session_summaries.first_request_at and last_request_at are NOT NULL
// (migration 310). The previous Upsert omitted them from INSERT, so
// every first-insert failed with "null value in column first_request_at"
// (SQLSTATE 23502) — production measured 161/161 summary persist failures.
// Callers that don't know the timestamps can leave them zero; Upsert
// falls back to LastSummarized / NOW() so the NOT NULL constraint holds.
type Summary struct {
	SessionKey     string    // gw_session_id (PK)
	TenantID       string    // tenant namespace
	Title          string    // short session title
	Summary        string    // 80-200 字 Chinese summary
	KeyTopics      []string  // 3-5 key points (15-40 字 each)
	UserIntent     string    // user's underlying goal
	LastSummarized time.Time // when this row was last generated (read by the rolling gate)
	FirstRequestAt time.Time // first request ts for this session (NOT NULL column)
	LastRequestAt  time.Time // last request ts for this session (NOT NULL column)
}

// Store persists Summary rows. Construct with NewStore; nil-safe methods
// are no-ops so unit tests can pass a zero-value Store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store backed by pool. A nil pool is tolerated so
// tests / disabled deployments can construct a Store and have Upsert become
// a logged no-op.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying *pgxpool.Pool so callers (e.g. the v2
// dispatch summarizer after its 2026-08-06 migration) can run read
// queries directly without holding a second pool reference. Returns nil
// if the store was constructed with a nil pool.
func (s *Store) Pool() *pgxpool.Pool {
	if s == nil {
		return nil
	}
	return s.pool
}

// UpsertResult is the outcome of an Upsert. Returned so call sites that
// race (e.g. concurrent auto-summary + summary workers) can detect
// staleness via Version and Updated.
//
// 2026-08-06: added so the v2 dispatch summarizer can finally compare
// incoming vs persisted summary_version — previously it just called
// saveSummaryToDB and hoped for the best, which made concurrent
// overwrites silent.
type UpsertResult struct {
	// Version is the summary_version after the upsert. For a fresh insert
	// it's 1 (the column default is 1); for an update it's the
	// previous value + 1 (computed via COALESCE(...)+1 in the query).
	Version int
	// Updated is true when the row already existed and was updated by
	// this call, false when a new row was inserted. Useful for emitting
	// a "lost the race" metric when two writers serialize on the same
	// session_key.
	Updated bool
}

// Upsert writes the summary row, creating it on first insert and bumping
// summary_version on subsequent updates. Mirrors the schema of the v2
// dispatch writer (domains/sessionsummary/summarizer.go::saveSummaryToDB)
// so dashboards see one shape regardless of which generator wrote the row.
//
// Idempotent: safe to call concurrently from many goroutines for the same
// (tenant, session); PG's UPSERT handles the race via the
// UNIQUE(tenant_id, session_key) constraint added in migration 560.
//
// summary_version 是「持久化版本号」的递增：INSERT 时取 DEFAULT 1；
// UPDATE 时取 session_summaries.summary_version + 1（不再用 COALESCE(...,0)+1，
// 避免依赖默认值兜底语义）。
//
// 2026-08-06: changed return type from error to (UpsertResult, error) so
// the result carries summary_version.
//
// 2026-08-22 (T11-P0): ON CONFLICT 子句改为 (tenant_id, session_key)；
// summary_version 自增改为 session_summaries.summary_version + 1。
//
// IMPORTANT — 本函数不提供 CAS 语义：两个 writer 同时调用 Upsert 时，PG 的
// UPSERT 会让第二个 writer 静默覆盖第一个 writer 的内容。要拒绝基于「读到
// 的版本」写入，请用 UpsertCAS。
func (s *Store) Upsert(ctx context.Context, sum Summary) (UpsertResult, error) {
	if s == nil || s.pool == nil {
		return UpsertResult{}, fmt.Errorf("summarystore: pool not configured")
	}

	firstReq, lastReq := normalizeFirstLast(sum.FirstRequestAt, sum.LastRequestAt, sum.LastSummarized)
	title, summaryText, userIntent, dirty := sanitizeSummaryTexts(sum)

	const query = `
		INSERT INTO session_summaries (
			session_key, tenant_id, title, summary, key_topics,
			user_intent, last_summarized_at, created_at, updated_at,
			first_request_at, last_request_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW(), $8, $9)
		ON CONFLICT (tenant_id, session_key) DO UPDATE SET
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			key_topics = EXCLUDED.key_topics,
			user_intent = EXCLUDED.user_intent,
			last_summarized_at = EXCLUDED.last_summarized_at,
			last_request_at = GREATEST(session_summaries.last_request_at, EXCLUDED.last_request_at),
			summary_version = session_summaries.summary_version + 1,
			updated_at = NOW()
		RETURNING summary_version, (xmax = 0) AS inserted
	`
	if dirty {
		slog.Warn("summarystore: UTF-8 sanitization applied",
			"session_key", sum.SessionKey,
			"title_changed", title != sum.Title,
			"summary_changed", summaryText != sum.Summary,
			"user_intent_changed", userIntent != sum.UserIntent,
		)
	}
	var result UpsertResult
	var inserted bool
	err := s.pool.QueryRow(ctx, query,
		sum.SessionKey,
		sum.TenantID,
		title,
		summaryText,
		sum.KeyTopics,
		userIntent,
		sum.LastSummarized,
		firstReq,
		lastReq,
	).Scan(&result.Version, &inserted)
	if err != nil {
		return UpsertResult{}, err
	}
	result.Updated = !inserted
	return result, nil
}

// UpsertCAS 严格 CAS 写入：只在持久版本号 == expectedVersion 时才允许写入。
//
// 语义：
//   - expectedVersion == 0 → 行不存在时插入；行存在时返回 ErrStaleVersion
//     （caller 表达「我还没读过」）。
//   - expectedVersion > 0 → 行存在且 summary_version == expectedVersion 时
//     UPDATE；否则 ErrStaleVersion（caller 表达「我读到的版本是 N」）。
//
// 错误返回时 UpsertResult 是零值（Version=0, Updated=false）；caller 必须用
// errors.Is(err, ErrStaleVersion) 判断。
//
// 实现要点（T11-P0）：
//   - expectedVersion > 0 用单条 UPDATE + RETURNING summary_version；
//     WHERE 子句带 summary_version = $expected 即可原子判断。
//   - expectedVersion == 0 分两阶段：先尝试 INSERT（行不存在走这条），失败
//     转 SELECT 拿当前版本并返回 ErrStaleVersion。
//
// 与 Upsert 的差别：Upsert 让 PG UPSERT 静默覆盖；UpsertCAS 把"是否应该覆盖"
// 的判断留给 caller，符合 v2 dispatch + auto_summary 跨进程防双写的需求。
func (s *Store) UpsertCAS(ctx context.Context, sum Summary, expectedVersion int) (UpsertResult, error) {
	if s == nil || s.pool == nil {
		return UpsertResult{}, fmt.Errorf("summarystore: pool not configured")
	}
	if expectedVersion < 0 {
		return UpsertResult{}, fmt.Errorf("summarystore: expectedVersion must be >= 0, got %d", expectedVersion)
	}

	firstReq, lastReq := normalizeFirstLast(sum.FirstRequestAt, sum.LastRequestAt, sum.LastSummarized)
	title, summaryText, userIntent, dirty := sanitizeSummaryTexts(sum)
	if dirty {
		slog.Warn("summarystore: UTF-8 sanitization applied (CAS path)",
			"session_key", sum.SessionKey,
			"title_changed", title != sum.Title,
			"summary_changed", summaryText != sum.Summary,
			"user_intent_changed", userIntent != sum.UserIntent,
		)
	}

	if expectedVersion == 0 {
		// === 阶段 1：尝试 INSERT ===
		insertSQL := `
			INSERT INTO session_summaries (
				session_key, tenant_id, title, summary, key_topics,
				user_intent, last_summarized_at, created_at, updated_at,
				first_request_at, last_request_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW(), $8, $9)
			ON CONFLICT (tenant_id, session_key) DO NOTHING
			RETURNING summary_version
		`
		var newVersion int
		err := s.pool.QueryRow(ctx, insertSQL,
			sum.SessionKey, sum.TenantID, title, summaryText, sum.KeyTopics,
			userIntent, sum.LastSummarized, firstReq, lastReq,
		).Scan(&newVersion)
		if err == nil {
			// 成功 INSERT；返回 newVersion，Updated = false。
			return UpsertResult{Version: newVersion, Updated: false}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			// 非「无行」错误：返回原 error
			return UpsertResult{}, err
		}
		// ON CONFLICT DO NOTHING 走了 → 行已存在；进入阶段 2 读 currentVersion。
	}

	// === 阶段 2：UPDATE 或 SELECT 拿当前版本 ===
	var currentVersion int
	scanErr := s.pool.QueryRow(ctx,
		`SELECT summary_version FROM session_summaries WHERE tenant_id = $1 AND session_key = $2`,
		sum.TenantID, sum.SessionKey).Scan(&currentVersion)
	if scanErr != nil {
		return UpsertResult{}, scanErr
	}

	if expectedVersion == 0 {
		// 行已存在：caller 表达"我还没读过"，被拒绝
		return UpsertResult{}, fmt.Errorf("%w: expected=0 current=%d (tenant=%s key=%s)",
			ErrStaleVersion, currentVersion, sum.TenantID, sum.SessionKey)
	}
	if currentVersion != expectedVersion {
		return UpsertResult{}, fmt.Errorf("%w: expected=%d current=%d (tenant=%s key=%s)",
			ErrStaleVersion, expectedVersion, currentVersion, sum.TenantID, sum.SessionKey)
	}

	// === 阶段 3：CAS UPDATE ===
	updateSQL := `
		UPDATE session_summaries SET
			title = $3,
			summary = $4,
			key_topics = $5,
			user_intent = $6,
			last_summarized_at = $7,
			last_request_at = GREATEST(session_summaries.last_request_at, $9),
			summary_version = session_summaries.summary_version + 1,
			updated_at = NOW()
		WHERE tenant_id = $1 AND session_key = $2 AND summary_version = $8
		RETURNING summary_version
	`
	var newVersion int
	err := s.pool.QueryRow(ctx, updateSQL,
		sum.TenantID, sum.SessionKey, title, summaryText, sum.KeyTopics,
		userIntent, sum.LastSummarized, expectedVersion, lastReq,
	).Scan(&newVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		// WHERE 子句 summary_version 不匹配（并发 writer 抢先更新了）
		return UpsertResult{}, fmt.Errorf("%w: CAS race detected (tenant=%s key=%s)",
			ErrStaleVersion, sum.TenantID, sum.SessionKey)
	}
	if err != nil {
		return UpsertResult{}, err
	}
	return UpsertResult{Version: newVersion, Updated: true}, nil
}

// normalizeFirstLast 把 Summary 的 FirstRequestAt/LastRequestAt 缺失值
// fallback 到 LastSummarized / NOW()。抽出为内部 helper 让 Upsert 与 UpsertCAS
// 共享同一兜底语义。
//
// 2026-08-06: session_summaries.first_request_at and last_request_at 是
// NOT NULL（migration 310）。当 caller 没填时 fallback 到 LastSummarized for
// first and NOW() for last — 修复了 161/161 production first-insert 失败
// （SQLSTATE 23502）。
func normalizeFirstLast(firstReq, lastReq, lastSummarized time.Time) (time.Time, time.Time) {
	if firstReq.IsZero() {
		firstReq = lastSummarized
	}
	if firstReq.IsZero() {
		firstReq = time.Now()
	}
	if lastReq.IsZero() {
		lastReq = time.Now()
	}
	return firstReq, lastReq
}

// sanitizeSummaryTexts 把 title / summary / userIntent 三段做 UTF-8 sanitize，
// 标记是否发生替换（用于打 WARN 日志）。LLM 偶发产生 0xe5 0xe2 0x80 这种
// 截断 CJK 多字节序列，PG 拒绝 SQLSTATE 22021；实测 157 例 / 3h。
func sanitizeSummaryTexts(sum Summary) (title, summaryText, userIntent string, dirty bool) {
	title = sanitiseUTF8(sum.Title)
	summaryText = sanitiseUTF8(sum.Summary)
	userIntent = sanitiseUTF8(sum.UserIntent)
	if title != sum.Title || summaryText != sum.Summary || userIntent != sum.UserIntent {
		dirty = true
	}
	return
}

// sanitiseUTF8 returns s with all invalid UTF-8 byte sequences replaced by
// the Unicode replacement character (U+FFFD). This prevents PostgreSQL
// from rejecting the INSERT with SQLSTATE 22021 when the LLM produces
// truncated or corrupted multibyte characters (observed in production
// with CJK text: 0xe5 0xe2 0x80).
//
// strings.ToValidUTF8 is the standard library function for this; it is
// available since Go 1.13.
func sanitiseUTF8(s string) string {
	return strings.ToValidUTF8(s, "\ufffd")
}

// LastSummarized returns the last_summarized_at timestamp for the session
// (used by the rolling-gate trigger). Returns the zero time and nil error
// when no row exists yet — that signals "never summarized".
//
// 2026-08-22 (T11-P0): tenantID 入参 + WHERE 子句加 AND tenant_id = $N；
// 不依赖 RLS 防御跨租户读。
func (s *Store) LastSummarized(ctx context.Context, tenantID, sessionKey string) (time.Time, error) {
	if s == nil || s.pool == nil {
		return time.Time{}, fmt.Errorf("summarystore: pool not configured")
	}
	var ts *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT last_summarized_at FROM session_summaries WHERE tenant_id = $1 AND session_key = $2`,
		tenantID, sessionKey).Scan(&ts)
	if err != nil {
		// pgx returns ErrNoRows for missing rows; the caller treats that as
		// "never summarized" by checking the returned time.
		return time.Time{}, err
	}
	if ts == nil {
		return time.Time{}, nil
	}
	return *ts, nil
}

// CountNewTurns returns the number of successful request_logs rows for the
// session whose ts > since. Used by the rolling-gate trigger to decide
// whether enough new turns have accumulated since the last summary.
//
// 2026-08-22 (T11-P0): tenantID 入参 + WHERE 加 AND tenant_id = $N。
// 注：request_logs_hot 本身已有 RLS + tenant_id 索引，这里只是双保险。
func (s *Store) CountNewTurns(ctx context.Context, tenantID, sessionKey string, since time.Time) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("summarystore: pool not configured")
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM request_logs_hot
		WHERE tenant_id = $1 AND gw_session_id = $2 AND success = TRUE AND ts > $3
	`, tenantID, sessionKey, since).Scan(&n)
	return n, err
}

// CountTotalTurns returns the total number of successful request_logs rows
// for the session (all time). Used by the rolling-gate trigger to enforce
// the minimum session length requirement before allowing summary generation.
//
// 2026-08-06: added to implement the 5-turn minimum gate — summaries should
// only be generated for sessions with at least 5 successful turns, regardless
// of when the last summary was generated. This prevents premature summaries
// on short exploratory sessions and reduces cost waste.
//
// 2026-08-22 (T11-P0): tenantID 入参 + WHERE 加 AND tenant_id = $N。
func (s *Store) CountTotalTurns(ctx context.Context, tenantID, sessionKey string) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("summarystore: pool not configured")
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM request_logs_hot
		WHERE tenant_id = $1 AND gw_session_id = $2 AND success = TRUE
	`, tenantID, sessionKey).Scan(&n)
	return n, err
}
