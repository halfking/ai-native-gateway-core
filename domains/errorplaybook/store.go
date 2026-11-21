package errorplaybook

// 手册存储层（error_playbook_entries / error_playbook_executions）。
//
// ★ 面向两张新表（迁移 995）。表未建出的部署（installer/docker 通道尚未
//
//	跑 995）上，查询会 42P01 —— 管理面把它转成明确提示（playbook tables
//	not provisioned），worker 记 WARN 跳过本轮，绝不静默假装成功。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrStoreScan 是 JSONB 列 Scan 形态不支持的哨兵错误。
var ErrStoreScan = errors.New("errorplaybook: unsupported scan source")

// ErrNotProvisioned 是手册表不存在（42P01）的哨兵错误。
var ErrNotProvisioned = errors.New("errorplaybook: playbook tables not provisioned (migration 995 not applied)")

// isNotProvisioned 识别 42P01 / does not exist。
func isNotProvisioned(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "42P01") || strings.Contains(msg, "does not exist")
}

// EntryStore 是引擎依赖的存储面（测试用假实现；生产用 SQLStore）。
type EntryStore interface {
	ListEnabled(ctx context.Context) ([]Entry, error)
	RecordHit(ctx context.Context, entryID int64) error
	RecordExecution(ctx context.Context, rec Execution) error
	BumpExecutionCounters(ctx context.Context, entryID int64, status string) error
	UpsertFromAnalysis(ctx context.Context, in AnalysisEntryInput) (Entry, error)
	UpdatePlanFromRefinement(ctx context.Context, entryID int64, plan Plan, title, rootCause string, confidence float64) error
	ListRefineCandidates(ctx context.Context, olderThan time.Duration, limit int) ([]Entry, error)
	LastFailedExecution(ctx context.Context, entryID int64) (Execution, bool, error)
	// PruneExecutions 删除超过保留期的执行留痕（worker 每 24h 调一次；
	// 返回删除行数）。
	PruneExecutions(ctx context.Context, olderThan time.Duration) (int64, error)
}

// SQLStore 是 EntryStore 的 SQL 实现。
type SQLStore struct {
	db *sql.DB
}

// NewStore 创建 SQL 存储层。
func NewStore(db *sql.DB) *SQLStore {
	return &SQLStore{db: db}
}

// DB 暴露底层连接（管理面回滚等需要直查的路径）。
func (s *SQLStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// AnalysisEntryInput 是落手册条目的输入（PI 分析与管理面手工录入共用）。
type AnalysisEntryInput struct {
	ErrorType     string
	MatchPattern  string
	Title         string
	Description   string
	RootCause     string
	Plan          Plan
	Confidence    float64
	Enabled       bool
	AutoExecute   bool
	SourceQueueID int64
	CreatedBy     string
}

const entryColumns = `
	id, error_type, COALESCE(match_pattern, '') AS match_pattern, title, description, root_cause,
	plan_kind, plan_payload, confidence, source, COALESCE(source_queue_id, 0) AS source_queue_id,
	enabled, auto_execute, hit_count, execution_count, success_count, failure_count,
	last_hit_at, last_executed_at, last_status, refinement_count, created_by, created_at, updated_at`

func scanEntry(scan func(dest ...interface{}) error) (Entry, error) {
	var (
		e           Entry
		planKind    string
		planPayload jsonb
		confidence  float64
		// last_status 可空（未执行过的条目为 NULL）⇒ NullString 承载，
		// 直接 Scan string 会 500（真库契约测试实锤）。
		lastStatus sql.NullString
	)
	err := scan(
		&e.ID, &e.ErrorType, &e.MatchPattern, &e.Title, &e.Description, &e.RootCause,
		&planKind, &planPayload, &confidence, &e.Source, &e.SourceQueueID,
		&e.Enabled, &e.AutoExecute, &e.HitCount, &e.ExecutionCount, &e.SuccessCount, &e.FailureCount,
		&e.LastHitAt, &e.LastExecutedAt, &lastStatus, &e.RefinementCount, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt,
	)
	if err != nil {
		return Entry{}, err
	}
	e.PlanKind = planKind
	e.Confidence = confidence
	e.LastStatus = lastStatus.String
	plan, perr := decodePlan(planKind, planPayload)
	if perr != nil {
		// payload 与 kind 不一致（手工改库等）⇒ 条目仍可读，方案按不可执行处理。
		e.Plan = Plan{Kind: planKind}
	} else {
		e.Plan = plan
	}
	return e, nil
}

// decodePlan 反序列化 plan_payload（历史/异形数据容错：解不出 ⇒ 空方案）。
func decodePlan(kind string, payload []byte) (Plan, error) {
	if len(payload) == 0 || string(payload) == "null" {
		return Plan{Kind: kind}, nil
	}
	var p Plan
	if err := json.Unmarshal(payload, &p); err != nil {
		return Plan{Kind: kind}, err
	}
	p.Kind = kind // kind 以列为准
	return p, nil
}

// ListEnabled 取全部启用条目（手册量级 ~10^1~10^2 ⇒ 全量读进内存匹配）。
func (s *SQLStore) ListEnabled(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+entryColumns+`
		FROM error_playbook_entries
		WHERE enabled = TRUE
		ORDER BY confidence DESC, id ASC`)
	if err != nil {
		if isNotProvisioned(err) {
			return nil, ErrNotProvisioned
		}
		return nil, fmt.Errorf("list playbook entries failed: %w", err)
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan playbook entry failed: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate playbook entries failed: %w", err)
	}
	return out, nil
}

// GetByID 取单条条目。
func (s *SQLStore) GetByID(ctx context.Context, id int64) (Entry, error) {
	e, err := scanEntry(s.db.QueryRowContext(ctx, `
		SELECT `+entryColumns+`
		FROM error_playbook_entries WHERE id = $1`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		if isNotProvisioned(err) {
			return Entry{}, ErrNotProvisioned
		}
		return Entry{}, fmt.Errorf("get playbook entry failed: %w", err)
	}
	return e, nil
}

// ErrNotFound 是条目/执行不存在的哨兵错误。
var ErrNotFound = errors.New("errorplaybook: not found")

// ListEntries 管理面列表（过滤：enabled/source/error_type/关键词）。
func (s *SQLStore) ListEntries(ctx context.Context, f ListFilter) ([]Entry, int, error) {
	where := []string{"TRUE"}
	args := []interface{}{}
	pos := 1
	if f.Enabled != nil {
		where = append(where, fmt.Sprintf("enabled = $%d", pos))
		args = append(args, *f.Enabled)
		pos++
	}
	if f.Source != "" {
		where = append(where, fmt.Sprintf("source = $%d", pos))
		args = append(args, f.Source)
		pos++
	}
	if f.ErrorType != "" {
		where = append(where, fmt.Sprintf("error_type = $%d", pos))
		args = append(args, f.ErrorType)
		pos++
	}
	if f.Query != "" {
		where = append(where, fmt.Sprintf(
			"(error_type ILIKE '%%'||$%d||'%%' OR title ILIKE '%%'||$%d||'%%' OR description ILIKE '%%'||$%d||'%%')", pos, pos, pos))
		args = append(args, f.Query)
		pos++
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM error_playbook_entries WHERE `+whereSQL, args...,
	).Scan(&total); err != nil {
		if isNotProvisioned(err) {
			return nil, 0, ErrNotProvisioned
		}
		return nil, 0, fmt.Errorf("count playbook entries failed: %w", err)
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	q := fmt.Sprintf(`
		SELECT %s
		FROM error_playbook_entries
		WHERE %s
		ORDER BY updated_at DESC, id ASC
		LIMIT %d OFFSET %d`, entryColumns, whereSQL, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list playbook entries failed: %w", err)
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows.Scan)
		if err != nil {
			return nil, 0, fmt.Errorf("scan playbook entry failed: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate playbook entries failed: %w", err)
	}
	return out, int(total), nil
}

// ListFilter 是管理面列表的过滤条件。
type ListFilter struct {
	Enabled   *bool
	Source    string
	ErrorType string
	Query     string
	Limit     int
	Offset    int
}

// UpsertManualEntry 手工录入/更新条目（管理面 POST/PUT）。
//
//	id > 0 ⇒ 更新（含 plan/enabled/auto_execute 等）；id == 0 ⇒ 按
//	(error_type, match_pattern) 唯一键插入，已存在 ⇒ 更新同一行。
//
// UpsertManualEntry 手工录入/更新条目（管理面 POST/PUT）。
//
//	★ 审计轮（2026-11-21）原子化：原实现「先查后插/改」两步在并发创建
//	（两个管理员同时录同一 error_type+pattern）下会撞表达式唯一键 23505
//	⇒ 500。现在单条 INSERT ... ON CONFLICT ... DO UPDATE（对齐唯一键表达式）
//	⇒ 竞态消失；已存在 ⇒ 原地更新（manual 语义）。created_by 只在新建时
//	生效（DO UPDATE 不碰它）⇒ 修订者身份走 updated_by 语义由留痕承担。
func (s *SQLStore) UpsertManualEntry(ctx context.Context, in AnalysisEntryInput) (Entry, error) {
	payload, err := MarshalPlan(in.Plan)
	if err != nil {
		return Entry{}, fmt.Errorf("marshal plan failed: %w", err)
	}
	createdBy := strings.TrimSpace(in.CreatedBy)
	if createdBy == "" {
		createdBy = "system"
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO error_playbook_entries
			(error_type, match_pattern, title, description, root_cause, plan_kind, plan_payload,
			 confidence, source, enabled, auto_execute, created_by)
		VALUES ($1, NULLIF($2,''), $3, $4, $5, $6, $7::jsonb, $8, 'manual', $9, $10, $11)
		ON CONFLICT (error_type, COALESCE(match_pattern, '')) DO UPDATE SET
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			root_cause = EXCLUDED.root_cause,
			plan_kind = EXCLUDED.plan_kind,
			plan_payload = EXCLUDED.plan_payload,
			confidence = EXCLUDED.confidence,
			source = 'manual',
			enabled = EXCLUDED.enabled,
			auto_execute = EXCLUDED.auto_execute,
			updated_at = NOW()
		RETURNING id`,
		in.ErrorType, strings.TrimSpace(in.MatchPattern), in.Title, in.Description, in.RootCause,
		in.Plan.Kind, payload, in.Confidence, in.Enabled, in.AutoExecute, createdBy,
	).Scan(&id)
	if err != nil {
		if isNotProvisioned(err) {
			return Entry{}, ErrNotProvisioned
		}
		return Entry{}, fmt.Errorf("upsert manual playbook entry failed: %w", err)
	}
	return s.GetByID(ctx, id)
}

// DeleteEntry 删除条目（执行留痕按 FK ON DELETE SET NULL 保留）。
func (s *SQLStore) DeleteEntry(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM error_playbook_entries WHERE id = $1`, id)
	if err != nil {
		if isNotProvisioned(err) {
			return ErrNotProvisioned
		}
		return fmt.Errorf("delete playbook entry failed: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePlanEntry 管理面替换条目的方案与文案（PUT /entries/{id} 带 plan 时）。
func (s *SQLStore) UpdatePlanEntry(ctx context.Context, id int64, title, description, rootCause string, plan Plan) error {
	payload, err := MarshalPlan(plan)
	if err != nil {
		return fmt.Errorf("marshal plan failed: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE error_playbook_entries SET
			title = $2, description = $3, root_cause = $4,
			plan_kind = $5, plan_payload = $6::jsonb, source = 'manual',
			updated_at = NOW()
		WHERE id = $1`, id, title, description, rootCause, plan.Kind, payload)
	if err != nil {
		return fmt.Errorf("update playbook plan failed: %w", err)
	}
	return nil
}

// SetEntryFlags 更新 enabled / auto_execute（管理面的开关拨杆）。
func (s *SQLStore) SetEntryFlags(ctx context.Context, id int64, enabled, autoExecute *bool) (Entry, error) {
	if enabled != nil {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE error_playbook_entries SET enabled = $2, updated_at = NOW() WHERE id = $1`, id, *enabled); err != nil {
			return Entry{}, fmt.Errorf("update entry enabled failed: %w", err)
		}
	}
	if autoExecute != nil {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE error_playbook_entries SET auto_execute = $2, updated_at = NOW() WHERE id = $1`, id, *autoExecute); err != nil {
			return Entry{}, fmt.Errorf("update entry auto_execute failed: %w", err)
		}
	}
	return s.GetByID(ctx, id)
}

// RecordHit implements EntryStore.
func (s *SQLStore) RecordHit(ctx context.Context, entryID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE error_playbook_entries
		SET hit_count = hit_count + 1, last_hit_at = NOW()
		WHERE id = $1`, entryID)
	if err != nil {
		return fmt.Errorf("record playbook hit failed: %w", err)
	}
	return nil
}

// RecordExecution implements EntryStore。
//
//	★ entry_id > 0 时同时回写条目的执行计数与 last_status / last_executed_at
//	（一条执行 = 一次回写，计数口径永远与留痕一致）。
func (s *SQLStore) RecordExecution(ctx context.Context, rec Execution) error {
	var steps, execCtx interface{}
	if len(rec.Steps) > 0 {
		steps = []byte(rec.Steps)
	}
	if len(rec.Context) > 0 {
		execCtx = []byte(rec.Context)
	}
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO error_playbook_executions
			(entry_id, error_type, match_pattern, trigger_source, context, status, steps, error_message, duration_ms, executed_by)
		VALUES (NULLIF($1,0)::bigint, $2, NULLIF($3,''), $4, $5::jsonb, $6, $7::jsonb, NULLIF($8,''), $9, $10)
		RETURNING id, created_at`,
		rec.EntryID, rec.ErrorType, rec.MatchPattern, rec.TriggerSource, execCtx,
		rec.Status, steps, rec.ErrorMessage, rec.DurationMS, rec.ExecutedBy,
	).Scan(&rec.ID, &rec.CreatedAt)
	if err != nil {
		if isNotProvisioned(err) {
			return ErrNotProvisioned
		}
		return fmt.Errorf("record playbook execution failed: %w", err)
	}
	if rec.EntryID > 0 {
		if err := s.BumpExecutionCounters(ctx, rec.EntryID, rec.Status); err != nil {
			return err
		}
	}
	return nil
}

// BumpExecutionCounters implements EntryStore.
func (s *SQLStore) BumpExecutionCounters(ctx context.Context, entryID int64, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE error_playbook_entries SET
			execution_count = execution_count + 1,
			success_count = success_count + CASE WHEN $2 = 'succeeded' THEN 1 ELSE 0 END,
			failure_count = failure_count + CASE WHEN $2 = 'failed' THEN 1 ELSE 0 END,
			last_status = $2,
			last_executed_at = CASE WHEN $2 IN ('succeeded','failed') THEN NOW() ELSE last_executed_at END
		WHERE id = $1`, entryID, status)
	if err != nil {
		return fmt.Errorf("bump playbook counters failed: %w", err)
	}
	return nil
}

// UpsertFromAnalysis implements EntryStore：PI 分析落条目。
//
//	★ 唯一键冲突（管理面/另一实例已建同键条目）⇒ 不覆盖对方，返回既有行
//	（分析结果仍在执行留痕与日志里，不丢）。
func (s *SQLStore) UpsertFromAnalysis(ctx context.Context, in AnalysisEntryInput) (Entry, error) {
	payload, err := MarshalPlan(in.Plan)
	if err != nil {
		return Entry{}, fmt.Errorf("marshal plan failed: %w", err)
	}
	var (
		id          int64
		createdByID = in.CreatedBy
	)
	if createdByID == "" {
		createdByID = "pi_agent"
	}
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO error_playbook_entries
			(error_type, match_pattern, title, description, root_cause, plan_kind, plan_payload,
			 confidence, source, source_queue_id, enabled, auto_execute, created_by)
		VALUES ($1, NULLIF($2,''), $3, $4, $5, $6, $7::jsonb, $8, $9, NULLIF($10,0)::bigint, TRUE, $11, $12)
		ON CONFLICT (error_type, COALESCE(match_pattern, '')) DO NOTHING
		RETURNING id`,
		in.ErrorType, in.MatchPattern, in.Title, in.Description, in.RootCause,
		in.Plan.Kind, payload, in.Confidence, SourcePIAnalysis, in.SourceQueueID,
		in.AutoExecute, createdByID,
	).Scan(&id)
	switch {
	case err == nil:
		// 新建成功。
	case errors.Is(err, sql.ErrNoRows):
		// 冲突 ⇒ 取既有行（不覆盖别人的条目）。
		err = s.db.QueryRowContext(ctx, `
			SELECT id FROM error_playbook_entries
			WHERE error_type = $1 AND COALESCE(match_pattern, '') = $2`,
			in.ErrorType, in.MatchPattern).Scan(&id)
		if err != nil {
			return Entry{}, fmt.Errorf("lookup existing playbook entry failed: %w", err)
		}
	default:
		if isNotProvisioned(err) {
			return Entry{}, ErrNotProvisioned
		}
		return Entry{}, fmt.Errorf("upsert analysis entry failed: %w", err)
	}
	return s.GetByID(ctx, id)
}

// UpdatePlanFromRefinement implements EntryStore：自动提炼更新方案。
func (s *SQLStore) UpdatePlanFromRefinement(ctx context.Context, entryID int64, plan Plan, title, rootCause string, confidence float64) error {
	payload, err := MarshalPlan(plan)
	if err != nil {
		return fmt.Errorf("marshal plan failed: %w", err)
	}
	var errorType string
	err = s.db.QueryRowContext(ctx, `
		UPDATE error_playbook_entries SET
			title = COALESCE(NULLIF($2,'')::text, title), root_cause = $3, plan_kind = $4, plan_payload = $5::jsonb,
			confidence = $6, source = 'refinement', refinement_count = refinement_count + 1,
			updated_at = NOW()
		WHERE id = $1
		RETURNING error_type`, entryID, title, rootCause, plan.Kind, payload, confidence,
	).Scan(&errorType)
	if err != nil {
		return fmt.Errorf("update refinement failed: %w", err)
	}
	// ★ 提炼标记行（审计轮 2026-11-21）：不走 RecordExecution ⇒ 不碰
	//   执行计数与 last_status；ListRefineCandidates 以它为「失败之后
	//   已提炼」的判据（updated_at 被触发器顶成 NOW()，时间戳比较在
	//   真库恒失效——真库契约测试实测抓出）。
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO error_playbook_executions
			(entry_id, error_type, trigger_source, status, steps, executed_by)
		VALUES ($1, $2, 'refinement', 'succeeded', $3::jsonb, 'error_playbook:refine')`,
		entryID, errorType, []byte(`{"note":"plan refined after failed execution"}`))
	if err != nil {
		return fmt.Errorf("record refinement marker failed: %w", err)
	}
	return nil
}

// ListRefineCandidates implements EntryStore：标准执行失败且过了冷静期的条目。
//
//	★ 审计轮（2026-11-21）守卫判据＝**提炼标记行**（trigger_source=
//	'refinement' 的执行留痕，时间晚于 last_executed_at ⇒ 已提炼过）。
//	不能比 updated_at：该列被 updated_at 触发器在任何 UPDATE 时顶成
//	NOW()、与 last_executed_at 恒相等 ⇒ 时间戳比较在真库恒失效
//	（真库契约测试实测抓出首版设计的假绿）。
//	否则 last_status 恒为 failed（提炼不执行）⇒ 同一失败在每个 worker
//	重启后被重复提炼一次（内存 dedup 只在进程内生效）。
func (s *SQLStore) ListRefineCandidates(ctx context.Context, olderThan time.Duration, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 5 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+entryColumns+`
		FROM error_playbook_entries
		WHERE enabled = TRUE
		  AND last_status = 'failed'
		  AND last_executed_at IS NOT NULL
		  AND last_executed_at < NOW() - make_interval(secs => $1)
		  AND NOT EXISTS (
		      SELECT 1 FROM error_playbook_executions re
		      WHERE re.entry_id = error_playbook_entries.id
		        AND re.trigger_source = 'refinement'
		        AND re.created_at > error_playbook_entries.last_executed_at
		  )
		ORDER BY last_executed_at ASC
		LIMIT $2`, int(olderThan.Seconds()), limit)
	if err != nil {
		if isNotProvisioned(err) {
			return nil, ErrNotProvisioned
		}
		return nil, fmt.Errorf("list refine candidates failed: %w", err)
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan refine candidate failed: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate refine candidates failed: %w", err)
	}
	return out, nil
}

// LastFailedExecution implements EntryStore。
func (s *SQLStore) LastFailedExecution(ctx context.Context, entryID int64) (Execution, bool, error) {
	var (
		rec      Execution
		steps    jsonb
		execCtx  jsonb
		errMsg   sql.NullString
		matchPat sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, entry_id, error_type, COALESCE(match_pattern, ''), trigger_source, context,
		       status, steps, error_message, duration_ms, executed_by, created_at
		FROM error_playbook_executions
		WHERE entry_id = $1 AND status = 'failed'
		ORDER BY created_at DESC LIMIT 1`, entryID,
	).Scan(&rec.ID, &rec.EntryID, &rec.ErrorType, &matchPat, &rec.TriggerSource, &execCtx,
		&rec.Status, &steps, &errMsg, &rec.DurationMS, &rec.ExecutedBy, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Execution{}, false, nil
	}
	if err != nil {
		return Execution{}, false, fmt.Errorf("query last failed execution failed: %w", err)
	}
	rec.MatchPattern = matchPat.String
	rec.ErrorMessage = errMsg.String
	rec.Steps = json.RawMessage(steps)
	rec.Context = json.RawMessage(execCtx)
	return rec, true, nil
}

// PruneExecutions implements EntryStore：删除超保留期的执行留痕。
//
//	★ RowsAffected 纪律：返回真实删除行数（0 = 无可删，不是错误）。
//	表不在场（ErrNotProvisioned）⇒ 上抛，worker 按「跳过本轮」处理。
func (s *SQLStore) PruneExecutions(ctx context.Context, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM error_playbook_executions
		WHERE created_at < NOW() - make_interval(secs => $1)`, int(olderThan.Seconds()))
	if err != nil {
		if isNotProvisioned(err) {
			return 0, ErrNotProvisioned
		}
		return 0, fmt.Errorf("prune playbook executions failed: %w", err)
	}
	return res.RowsAffected()
}

// GetExecution 取单条执行留痕（管理面详情 + revert）。
func (s *SQLStore) GetExecution(ctx context.Context, id int64) (Execution, error) {
	var (
		rec      Execution
		steps    jsonb
		execCtx  jsonb
		errMsg   sql.NullString
		matchPat sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(entry_id, 0), error_type, COALESCE(match_pattern, ''), trigger_source, context,
		       status, steps, error_message, duration_ms, executed_by, created_at
		FROM error_playbook_executions WHERE id = $1`, id,
	).Scan(&rec.ID, &rec.EntryID, &rec.ErrorType, &matchPat, &rec.TriggerSource, &execCtx,
		&rec.Status, &steps, &errMsg, &rec.DurationMS, &rec.ExecutedBy, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Execution{}, ErrNotFound
	}
	if err != nil {
		if isNotProvisioned(err) {
			return Execution{}, ErrNotProvisioned
		}
		return Execution{}, fmt.Errorf("get playbook execution failed: %w", err)
	}
	rec.MatchPattern = matchPat.String
	rec.ErrorMessage = errMsg.String
	rec.Steps = json.RawMessage(steps)
	rec.Context = json.RawMessage(execCtx)
	return rec, nil
}

// ListExecutions 管理面执行列表。
func (s *SQLStore) ListExecutions(ctx context.Context, entryID int64, status, errorType string, limit, offset int) ([]Execution, int, error) {
	where := []string{"TRUE"}
	args := []interface{}{}
	pos := 1
	if entryID > 0 {
		where = append(where, fmt.Sprintf("entry_id = $%d", pos))
		args = append(args, entryID)
		pos++
	}
	if status != "" {
		where = append(where, fmt.Sprintf("status = $%d", pos))
		args = append(args, status)
		pos++
	}
	if errorType != "" {
		where = append(where, fmt.Sprintf("error_type = $%d", pos))
		args = append(args, errorType)
		pos++
	}
	whereSQL := strings.Join(where, " AND ")
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM error_playbook_executions WHERE `+whereSQL, args...).Scan(&total); err != nil {
		if isNotProvisioned(err) {
			return nil, 0, ErrNotProvisioned
		}
		return nil, 0, fmt.Errorf("count playbook executions failed: %w", err)
	}

	q := fmt.Sprintf(`
		SELECT id, COALESCE(entry_id, 0), error_type, COALESCE(match_pattern, ''), trigger_source, context,
		       status, steps, error_message, duration_ms, executed_by, created_at
		FROM error_playbook_executions
		WHERE %s
		ORDER BY created_at DESC, id DESC
		LIMIT %d OFFSET %d`, whereSQL, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list playbook executions failed: %w", err)
	}
	defer rows.Close()
	out := []Execution{}
	for rows.Next() {
		var (
			rec      Execution
			steps    jsonb
			execCtx  jsonb
			errMsg   sql.NullString
			matchPat sql.NullString
		)
		if err := rows.Scan(&rec.ID, &rec.EntryID, &rec.ErrorType, &matchPat, &rec.TriggerSource, &execCtx,
			&rec.Status, &steps, &errMsg, &rec.DurationMS, &rec.ExecutedBy, &rec.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan playbook execution failed: %w", err)
		}
		rec.MatchPattern = matchPat.String
		rec.ErrorMessage = errMsg.String
		rec.Steps = json.RawMessage(steps)
		rec.Context = json.RawMessage(execCtx)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate playbook executions failed: %w", err)
	}
	return out, int(total), nil
}

// ExecutionStats 是 /stats 的执行侧计数。
type ExecutionStats struct {
	Last24h map[string]int64 `json:"last_24h"`
	Total   int64            `json:"total"`
}

// EntrySummaryStats 是条目侧统计（SQL 聚合，不受列表 LIMIT 截断）。
//
//	★ 审计轮（2026-11-21）：原 /stats 走 ListEntries(Limit 200) 逐行累加
//	⇒ 条目超过 200 时统计静默欠数。现在 GROUP BY 直出。
type EntrySummaryStats struct {
	Total       int64            `json:"total_entries"`
	Enabled     int64            `json:"enabled_entries"`
	AutoExecute int64            `json:"auto_execute_entries"`
	BySource    map[string]int64 `json:"entries_by_source"`
	ByPlanKind  map[string]int64 `json:"entries_by_plan_kind"`
}

// EntrySummary 条目侧统计（管理面 /stats）。
func (s *SQLStore) EntrySummary(ctx context.Context) (EntrySummaryStats, error) {
	out := EntrySummaryStats{
		BySource:   map[string]int64{},
		ByPlanKind: map[string]int64{},
	}
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE enabled),
		       COUNT(*) FILTER (WHERE auto_execute)
		FROM error_playbook_entries`).
		Scan(&out.Total, &out.Enabled, &out.AutoExecute)
	if err != nil {
		if isNotProvisioned(err) {
			return out, ErrNotProvisioned
		}
		return out, fmt.Errorf("summarize playbook entries failed: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT source, COUNT(*) FROM error_playbook_entries GROUP BY source`)
	if err != nil {
		return out, fmt.Errorf("group entries by source failed: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var src string
		var n int64
		if err := rows.Scan(&src, &n); err != nil {
			return out, fmt.Errorf("scan source stats failed: %w", err)
		}
		out.BySource[src] = n
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("iterate source stats failed: %w", err)
	}

	kindRows, err := s.db.QueryContext(ctx, `SELECT plan_kind, COUNT(*) FROM error_playbook_entries GROUP BY plan_kind`)
	if err != nil {
		return out, fmt.Errorf("group entries by plan_kind failed: %w", err)
	}
	defer kindRows.Close()
	for kindRows.Next() {
		var kind string
		var n int64
		if err := kindRows.Scan(&kind, &n); err != nil {
			return out, fmt.Errorf("scan plan_kind stats failed: %w", err)
		}
		out.ByPlanKind[kind] = n
	}
	if err := kindRows.Err(); err != nil {
		return out, fmt.Errorf("iterate plan_kind stats failed: %w", err)
	}
	return out, nil
}

// ExecutionSummary 手册侧统计（管理面 /stats）。
func (s *SQLStore) ExecutionSummary(ctx context.Context) (ExecutionStats, error) {
	out := ExecutionStats{Last24h: map[string]int64{}}
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM error_playbook_executions`).Scan(&out.Total)
	if err != nil {
		if isNotProvisioned(err) {
			return out, ErrNotProvisioned
		}
		return out, fmt.Errorf("count executions failed: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM error_playbook_executions
		WHERE created_at > NOW() - interval '24 hours'
		GROUP BY status`)
	if err != nil {
		return out, fmt.Errorf("summarize executions failed: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return out, fmt.Errorf("scan execution stats failed: %w", err)
		}
		out.Last24h[st] = n
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("iterate execution stats failed: %w", err)
	}
	return out, nil
}
