package sanitizerules

// PostgresStore 对 ReviewPersistence 的实现（迁移 917 的三张表）。
//
// 纪律（与 postgres.go 既有实现同一条线）：
//   - 不跑 DDL（表由迁移 917 建）；
//   - 错误清洗：返回前剥掉驱动错误里的原文片段（pgx 把 SQL 参数带进
//     message），与 TestPostgresDoesNotRunDDLAndSanitizesErrors 的口径一致；
//   - 记录里没有载荷原文——列集与迁移 917 一一对应。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *PostgresStore) FindPendingBlockReview(ctx context.Context, tenantID, scope, rule, digest, requestID string) (BlockReview, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+blockReviewColumns+`
FROM sanitize_block_reviews
WHERE status='pending' AND tenant_id=$1 AND scope=$2 AND rule_id=$3 AND payload_digest=$4 AND request_id=$5
ORDER BY id DESC LIMIT 1`, tenantID, scope, rule, digest, requestID)
	r, err := scanBlockReview(row)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		return BlockReview{}, false, nil
	}
	if err != nil {
		return BlockReview{}, false, storeError("block review dedupe read", err)
	}
	return r, true, nil
}

func (s *PostgresStore) InsertBlockReview(ctx context.Context, r BlockReview) (BlockReview, error) {
	row := s.db.QueryRowContext(ctx, `INSERT INTO sanitize_block_reviews
 (tenant_id, client_type, scope, rule_id, rule_name, reason, field_path, payload_digest, request_id, match_detail, session_id, status, created_at, degraded)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
 RETURNING `+blockReviewColumns,
		r.TenantID, r.ClientType, r.Scope, r.Rule, r.RuleName, r.Reason, r.Field,
		r.Digest, r.RequestID, r.MatchDetail, r.SessionID, BlockReviewPending, r.CreatedAt, r.Degraded)
	out, err := scanBlockReview(row)
	if err != nil {
		return BlockReview{}, storeError("block review insert", err)
	}
	return out, nil
}

func (s *PostgresStore) ListBlockReviewsDB(ctx context.Context, status, tenantID string, limit int) ([]BlockReview, error) {
	q := `SELECT ` + blockReviewColumns + `
FROM sanitize_block_reviews WHERE TRUE`
	args := []any{}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(" AND status=$%d", len(args))
	}
	if tenantID != "" {
		args = append(args, tenantID)
		q += fmt.Sprintf(" AND tenant_id=$%d", len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, storeError("block review list read", err)
	}
	defer rows.Close()
	out := make([]BlockReview, 0, limit)
	for rows.Next() {
		r, err := scanBlockReview(rows)
		if err != nil {
			return nil, storeError("block review list read", err)
		}
		out = append(out, r)
	}
	return out, storeError("block review list read", rows.Err())
}

func (s *PostgresStore) GetBlockReviewDB(ctx context.Context, id int64) (BlockReview, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+blockReviewColumns+`
FROM sanitize_block_reviews WHERE id=$1`, id)
	r, err := scanBlockReview(row)
	if errors.Is(err, sql.ErrNoRows) {
		return BlockReview{}, ErrNotFound
	}
	if err != nil {
		return BlockReview{}, storeError("block review get", err)
	}
	return r, nil
}

func (s *PostgresStore) DecideBlockReviewDB(ctx context.Context, id int64, status, decidedBy string, decidedAt time.Time) (BlockReview, error) {
	tag, err := s.db.ExecContext(ctx, `UPDATE sanitize_block_reviews
SET status=$2, decided_by=$3, decided_at=$4
WHERE id=$1 AND status='pending'`, id, status, decidedBy, decidedAt)
	if err != nil {
		return BlockReview{}, storeError("block review decide", err)
	}
	if affectedRows, _ := tag.RowsAffected(); affectedRows == 0 {
		// 不存在，或已被决定（幂等语义：重复决定不再改写）。
		row, getErr := s.GetBlockReviewDB(ctx, id)
		if getErr != nil {
			return BlockReview{}, getErr
		}
		return row, ErrConflict
	}
	return s.GetBlockReviewDB(ctx, id)
}

// LabelBlockReviewDB 打人工标注。标注与状态**正交** ⇒ 允许对已决定的单
// 补标（规则优化需要「已放行但其实是误报」这类知识）。
func (s *PostgresStore) LabelBlockReviewDB(ctx context.Context, id int64, label, comment, labeledBy string, labeledAt time.Time) (BlockReview, error) {
	tag, err := s.db.ExecContext(ctx, `UPDATE sanitize_block_reviews
SET label=$2, label_comment=$3, labeled_by=$4, labeled_at=$5
WHERE id=$1`, id, label, comment, labeledBy, labeledAt)
	if err != nil {
		return BlockReview{}, storeError("block review label", err)
	}
	if affectedRows, _ := tag.RowsAffected(); affectedRows == 0 {
		return BlockReview{}, ErrNotFound
	}
	return s.GetBlockReviewDB(ctx, id)
}

// CountBlockReviewLabelsDB 数同租户同规则的某标注条数。
func (s *PostgresStore) CountBlockReviewLabelsDB(ctx context.Context, tenantID, rule, label string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sanitize_block_reviews
WHERE tenant_id=$1 AND rule_id=$2 AND label=$3`, tenantID, rule, label).Scan(&count)
	if err != nil {
		return 0, storeError("block review label count", err)
	}
	return count, nil
}

// BlockReviewInsightsDB 按规则聚合复核读数。
//
// ★ 误报率的分母是**有判断的记录数**（labeled > 0），不是总量 —— 用总量做
// 分母会让「没人看的记录」把误报率稀释成假绿。
// unattributedRuleBucket 是「拦截点没说出规则」那批记录的读数桶名。
//
// ★ 它是一个**哨兵**，不是一条规则：自动放行不会拿它当键（自动放行走标注
// 事件里的 entry.Rule，空规则在 maybeAutoRelax 里已被显式跳过）。
const unattributedRuleBucket = "unattributed"

func (s *PostgresStore) BlockReviewInsightsDB(ctx context.Context, tenantID string, limit int) ([]BlockReviewRuleInsight, error) {
	// ★ 2026-10-29：**没有规则归因**的记录也要进读数（否则它们在读数上「不存在」）。
	// 空白审核单（拦截点没说话 ⇒ rule_id 为空）是**误报轮最重要的信号** ——
	// 把它藏起来，读数就会长成「没有数据」而不是「22 条没有归因」。
	// ⇒ 空 rule_id 归到哨兵桶 `unattributed`（★ 哨兵**不是**真实规则 ⇒
	// 自动放行不会碰它：自动放行走标注事件里的 entry.Rule，空规则在
	// maybeAutoRelax 里已被显式跳过）。
	// ★ 2026-10-30（审计轮两处）：
	// ① 补上缺失的 WHERE —— 旧拼接在带租户过滤时生成
	//    `FROM sanitize_block_reviews AND tenant_id=$1`（非法 SQL）⇒
	//    租户管理员的读数恒 503、super_admin 的 ?tenant_id= 过滤恒 503
	//    （无过滤路径不受影响，这正是它一直没被发现的原因）。
	// ② 降级放行单（degraded，迁移 926）不进读数：它是「守卫让步」的遥测，
	//    不是规则反馈 —— 211 实测 24h 内 226/226 条全是降级单，留着它们，
	//    规则优化的输入面就只剩噪音。
	q := `SELECT tenant_id, CASE WHEN rule_id='' THEN '` + unattributedRuleBucket + `' ELSE rule_id END AS rule_id, max(rule_name) AS rule_name,
 count(*) AS total,
 count(*) FILTER (WHERE status='pending')  AS pending,
 count(*) FILTER (WHERE status='approved') AS approved,
 count(*) FILTER (WHERE status='rejected') AS rejected,
 count(*) FILTER (WHERE label='false_positive') AS false_positive,
 count(*) FILTER (WHERE label='true_positive')  AS true_positive,
 count(*) FILTER (WHERE label='rule_too_broad') AS rule_too_broad,
 count(*) FILTER (WHERE label='needs_rule')     AS needs_rule,
 count(*) FILTER (WHERE label <> '')           AS labeled
FROM sanitize_block_reviews WHERE NOT degraded`
	args := []any{}
	if tenantID != "" {
		args = append(args, tenantID)
		q += fmt.Sprintf(" AND tenant_id=$%d", len(args))
	}
	q += " GROUP BY tenant_id, CASE WHEN rule_id='' THEN '" + unattributedRuleBucket + "' ELSE rule_id END ORDER BY count(*) DESC, rule_id ASC"
	args = append(args, limit)
	q += fmt.Sprintf(" LIMIT $%d", len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, storeError("block review insights read", err)
	}
	defer rows.Close()
	out := make([]BlockReviewRuleInsight, 0, limit)
	for rows.Next() {
		var insight BlockReviewRuleInsight
		if err := rows.Scan(&insight.TenantID, &insight.Rule, &insight.RuleName,
			&insight.Total, &insight.Pending, &insight.Approved, &insight.Rejected,
			&insight.FalsePositive, &insight.TruePositive, &insight.RuleTooBroad,
			&insight.NeedsRule, &insight.Labeled); err != nil {
			return nil, storeError("block review insights read", err)
		}
		if insight.Labeled > 0 {
			insight.FalsePositiveR = float64(insight.FalsePositive) / float64(insight.Labeled)
		}
		out = append(out, insight)
	}
	return out, storeError("block review insights read", rows.Err())
}

// InsertBlockReviewEvidence 落一条证据摘录。
//
// ★ 幂等：同一 review_id 重复落 ⇒ 覆盖（审核单只有一条证据）。
func (s *PostgresStore) InsertBlockReviewEvidence(ctx context.Context, e BlockReviewEvidence) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sanitize_block_evidence
 (review_id, tenant_id, kind, content, content_bytes, truncated, created_at, expires_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
 ON CONFLICT (review_id) DO UPDATE SET kind=EXCLUDED.kind, content=EXCLUDED.content,
   content_bytes=EXCLUDED.content_bytes, truncated=EXCLUDED.truncated,
   created_at=EXCLUDED.created_at, expires_at=EXCLUDED.expires_at`,
		e.ReviewID, e.TenantID, e.Kind, e.Content, e.Bytes, e.Truncated, e.CreatedAt, e.ExpiresAt)
	if err != nil {
		return storeError("block review evidence insert", err)
	}
	return nil
}

// ListBlockReviewEvidenceDB 按审核单 ID 批量取证据摘录（过期的不返回）。
func (s *PostgresStore) ListBlockReviewEvidenceDB(ctx context.Context, reviewIDs []int64) (map[int64]BlockReviewEvidence, error) {
	out := map[int64]BlockReviewEvidence{}
	if len(reviewIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, 0, len(reviewIDs))
	args := make([]any, 0, len(reviewIDs)+1)
	for i, id := range reviewIDs {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		args = append(args, id)
	}
	args = append(args, time.Now().UTC())
	q := `SELECT review_id, tenant_id, kind, content, content_bytes, truncated, created_at, expires_at
FROM sanitize_block_evidence WHERE review_id IN (` + strings.Join(placeholders, ",") + `) AND expires_at > $` + fmt.Sprint(len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, storeError("block review evidence list read", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e BlockReviewEvidence
		if err := rows.Scan(&e.ReviewID, &e.TenantID, &e.Kind, &e.Content, &e.Bytes,
			&e.Truncated, &e.CreatedAt, &e.ExpiresAt); err != nil {
			return nil, storeError("block review evidence list read", err)
		}
		out[e.ReviewID] = e
	}
	return out, storeError("block review evidence list read", rows.Err())
}

func (s *PostgresStore) UpsertEgressReleaseDB(ctx context.Context, r EgressRelease) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sanitize_egress_releases
 (tenant_id, scope, rule_id, payload_digest, client_type, approved_by, source, created_at, expires_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
 ON CONFLICT (tenant_id, scope, rule_id, payload_digest)
 DO UPDATE SET client_type=EXCLUDED.client_type, approved_by=EXCLUDED.approved_by,
   source=EXCLUDED.source, created_at=EXCLUDED.created_at, expires_at=EXCLUDED.expires_at`,
		r.TenantID, r.Scope, r.Rule, r.PayloadDigest, r.ClientType, r.ApprovedBy,
		releaseSourceOrManual(r.Source), time.Now().UTC(), r.ExpiresAt.UTC())
	if err != nil {
		return storeError("egress release upsert", err)
	}
	return nil
}

func releaseSourceOrManual(source string) string {
	switch source {
	case "review", ReleaseSourceAutoLabel:
		return source
	default:
		return "manual"
	}
}

func (s *PostgresStore) DeleteEgressReleaseDB(ctx context.Context, tenantID, scope, rule, digest string) (bool, error) {
	var err error
	var deleted int64
	if digest == "" {
		// digest 为空 = 撤销该键下全部摘要粒度的行（管理面撤销语义）。
		res, execErr := s.db.ExecContext(ctx, `DELETE FROM sanitize_egress_releases
WHERE tenant_id=$1 AND scope=$2 AND rule_id=$3`, tenantID, scope, rule)
		if execErr == nil {
			deleted, _ = res.RowsAffected()
		}
		err = execErr
	} else {
		res, execErr := s.db.ExecContext(ctx, `DELETE FROM sanitize_egress_releases
WHERE tenant_id=$1 AND scope=$2 AND rule_id=$3 AND payload_digest=$4`, tenantID, scope, rule, digest)
		if execErr == nil {
			deleted, _ = res.RowsAffected()
		}
		err = execErr
	}
	if err != nil {
		return false, storeError("egress release delete", err)
	}
	return deleted > 0, nil
}

func (s *PostgresStore) LoadEgressReleasesDB(ctx context.Context) ([]EgressRelease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id, scope, rule_id, payload_digest, client_type, approved_by, source, created_at, expires_at
FROM sanitize_egress_releases`)
	if err != nil {
		return nil, storeError("egress release boot load", err)
	}
	defer rows.Close()
	out := []EgressRelease{}
	now := time.Now().UTC()
	for rows.Next() {
		var r EgressRelease
		var createdAt, expiresAt sql.NullTime
		if err := rows.Scan(&r.TenantID, &r.Scope, &r.Rule, &r.PayloadDigest, &r.ClientType, &r.ApprovedBy, &r.Source, &createdAt, &expiresAt); err != nil {
			return nil, storeError("egress release boot load", err)
		}
		r.ExpiresAt = expiresAt.Time
		// 已过期行不进内存表（写侧也不再保留）。
		if !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt) {
			continue
		}
		out = append(out, r)
	}
	return out, storeError("egress release boot load", rows.Err())
}

func (s *PostgresStore) LoadHardBlockState(ctx context.Context) ([]HardBlockAllow, bool, error) {
	var raw []byte
	var updatedBy string
	err := s.db.QueryRowContext(ctx, `SELECT rules, updated_by FROM sanitize_hardblock_state WHERE singleton=TRUE`).Scan(&raw, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, storeError("hardblock state read", err)
	}
	// ★ 迁移 917 会种下一行默认记录（rules='[]'、updated_by=''）——那不是
	// 「管理员保存了空清单」，是「从未被管理面保存过」。判据用 updated_by：
	// 管理面保存（SaveHardBlockState）恒写非空 actor，种子行恒为空 ⇒
	// 种子行不算 found ⇒ 启动回落 env/出厂默认。否则全新安装会在首次启动
	// 把硬阻断清单覆盖成空（211 实测踩到：种子行让 allows=[]，含请求侧
	// fail-closed 一并被覆盖；当时以 SQL 恢复 ["input:*"] 止血）。
	if strings.TrimSpace(updatedBy) == "" {
		return nil, false, nil
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, storeError("hardblock state parse", err)
	}
	allows, err := ParseHardBlockAllows(strings.Join(items, ","))
	if err != nil {
		return nil, false, fmt.Errorf("stored hardblock state invalid: %w", err)
	}
	return allows, true, nil
}

func (s *PostgresStore) SaveHardBlockState(ctx context.Context, allows []HardBlockAllow, updatedBy string) error {
	items := make([]string, 0, len(allows))
	for _, a := range allows {
		items = append(items, a.Scope+":"+a.Rule)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sanitize_hardblock_state (singleton, rules, updated_by, updated_at)
VALUES (TRUE, $1::jsonb, $2, now())
ON CONFLICT (singleton) DO UPDATE SET rules=EXCLUDED.rules, updated_by=EXCLUDED.updated_by, updated_at=now()`,
		string(raw), updatedBy)
	if err != nil {
		return storeError("hardblock state save", err)
	}
	return nil
}

// blockReviewColumns 是审核单的列序唯一真值源：所有 SELECT 与 RETURNING
// 都引用它，scanBlockReview 与它逐字对应。
//
// ★ 迁移 921 追加了 session_id / label / label_comment / labeled_by / labeled_at
// —— 追加在**末尾**，这样既有列序不变（列序变更会静默改错 scan 的对应关系）。
// ★ 迁移 926 同样在末尾追加 degraded（降级放行标记）。
const blockReviewColumns = `id, tenant_id, client_type, scope, rule_id, rule_name, reason, field_path, payload_digest, request_id, match_detail, status, created_at, decided_by, decided_at, session_id, label, label_comment, labeled_by, labeled_at, degraded`

// scanBlockReview 是列序的唯一真值源：查询列序与这里逐字对应。
// （rowScanner 由 postgres.go 声明，这里复用同一份。）
func scanBlockReview(row rowScanner) (BlockReview, error) {
	var r BlockReview
	var decidedAt, labeledAt sql.NullTime
	err := row.Scan(&r.ID, &r.TenantID, &r.ClientType, &r.Scope, &r.Rule, &r.RuleName,
		&r.Reason, &r.Field, &r.Digest, &r.RequestID, &r.MatchDetail, &r.Status,
		&r.CreatedAt, &r.DecidedBy, &decidedAt, &r.SessionID, &r.Label,
		&r.LabelComment, &r.LabeledBy, &labeledAt, &r.Degraded)
	if err != nil {
		return BlockReview{}, err
	}
	r.DecidedAt = decidedAt.Time
	r.LabeledAt = labeledAt.Time
	return r, nil
}

// ListEgressRuleEventsInWindowDB 取与 [from, to] 有重叠的出站侧规则命中事件。
//
// ★ 只取 phase='egress'（★ 出站侧 = 拦截发生的那一侧）★★
// ★ 窗口判据是「**last_seen 落在窗内**」（★ 不是「[first_seen,last_seen] 与窗重叠」★★）：
// ★ 事件是**按指纹聚合**的（★ 同一规则反复命中聚成一行）⇒ ★★ 它的 first/last
//
//	可以**横跨数周**（★ 211 生产实测：★ 一条 pii.server_ip 事件的 first_seen
//	在 10-15、last_seen 在 10-29）★★ ⇒ ★★ 于是「区间重叠」几乎**恒真** ⇒ ★★
//	候选清单会被**全量历史**淹没（★ 实测 26 条）★★ ⇒ ★★ 真正有意义的问题是
//	「**这条规则在拦截前后还在命中吗**」⇒ ★★ 那就是 **last_seen**（★ 每次命中
//	都会刷新 last_seen ⇒ ★★ 它就是「最近一次命中时间」）★★。
func (s *PostgresStore) ListEgressRuleEventsInWindowDB(ctx context.Context, tenantIDs []string, from, to time.Time, limit int) ([]EgressRuleEventInWindow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return nil, nil
	}
	// ★★ 租户门（2026-10-30 12h 审计轮）★★：★★ 原先这个读面没有任何租户
	//   条件 ⇒ ★★ sanitize_rule_events 是全租户表 ⇒ ★★ 候选跨租户泄漏 ⇒ ★★
	//   tenant_id 列有 NOT NULL DEFAULT '' 且带 (tenant_id,last_seen DESC)
	//   索引 ⇒ ★★ 加上这条谓词是**索引可用**的（★ 不是全表扫）★★。
	//   空集合 ⇒ ★★ fail-closed（返回空）★★：★ 「空 = 不限」是本仓最贵的
	//   一种误读（★ 见 [A1]）★★。
	if len(tenantIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rule_id, COALESCE(action,''), COALESCE(status,''), COALESCE(count,0), first_seen, last_seen
FROM sanitize_rule_events
WHERE phase='egress' AND last_seen >= $2 AND last_seen <= $1 AND tenant_id = ANY($4)
ORDER BY last_seen DESC, id DESC LIMIT $3`, to, from, limit, tenantIDs)
	if err != nil {
		return nil, storeError("egress rule events window read", err)
	}
	defer rows.Close()
	out := make([]EgressRuleEventInWindow, 0, limit)
	for rows.Next() {
		var ev EgressRuleEventInWindow
		if err := rows.Scan(&ev.RuleID, &ev.Action, &ev.Status, &ev.Count, &ev.FirstSeen, &ev.LastSeen); err != nil {
			return nil, storeError("egress rule events window read", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("egress rule events window read", err)
	}
	return out, nil
}

// CountBlockReviewsDB 数符合条件的审核单。
func (s *PostgresStore) CountBlockReviewsDB(ctx context.Context, status, tenantID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sanitize_block_reviews WHERE ($1='' OR status=$1) AND ($2='' OR tenant_id=$2)`, status, tenantID).Scan(&n)
	if err != nil {
		return 0, storeError("block review count read", err)
	}
	return n, nil
}

// CountPendingBlockDecisionsDB 数「待人工决断」的拦截单：pending 且非降级
// （迁移 926）。管理面徽标口径 —— 降级放行单没有决断可做，不计。
func (s *PostgresStore) CountPendingBlockDecisionsDB(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sanitize_block_reviews
WHERE status='pending' AND NOT degraded AND ($1='' OR tenant_id=$1)`, tenantID).Scan(&n)
	if err != nil {
		return 0, storeError("block review pending-decision count read", err)
	}
	return n, nil
}
