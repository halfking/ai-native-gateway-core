package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

type vendorCredentialErrorDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type vendorCredentialErrorHandlers struct{ db vendorCredentialErrorDB }

// withVendorRLSBypassReadTx 在单只读事务内执行 supplier_errors / candidate_failure_logs
// 读查询并回调消费行（2026-09-14 R28 审计 #6 扫尾）：两表为 FORCE RLS + tenant 隔离
// （V371/V367），网关应用角色非 superuser，直连读被静默过滤到 0 行、凭据详情页
// 错误集合恒空。事务内 set_config('app.bypass_rls','true',true)（is_local=true，
// 与 errors_trend.go withTrendReadTx 同一定式）保证旁路随事务提交即失效。
// 仅具体 *pgxpool.Pool 走事务路径；测试替身保持既有直连 Query 路径。
func (h *vendorCredentialErrorHandlers) withVendorRLSBypassReadTx(ctx context.Context, sql string, args []any, fn func(rows pgx.Rows) error) error {
	if pool, ok := h.db.(*pgxpool.Pool); ok {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			return fmt.Errorf("begin vendor error read tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
			return fmt.Errorf("set vendor error RLS bypass GUC: %w", err)
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		if err := fn(rows); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	rows, err := h.db.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return fn(rows)
}

type vendorCredentialMeta struct {
	ID                  int64      `json:"id"`
	Label               string     `json:"label"`
	ProviderID          int64      `json:"provider_id"`
	HealthStatus        string     `json:"health_status"`
	HealthError         *string    `json:"health_error"`
	HealthLatencyMs     *int       `json:"health_latency_ms"`
	AvailabilityState   string     `json:"availability_state"`
	StateReasonCode     *string    `json:"state_reason_code"`
	StateReasonDetail   *string    `json:"state_reason_detail"`
	StateUpdatedAt      *time.Time `json:"state_updated_at"`
	QuotaState          string     `json:"quota_state"`
	LifecycleStatus     string     `json:"lifecycle_status"`
	CircuitState        string     `json:"circuit_state"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	ManualDisabled      bool       `json:"manual_disabled"`
	BalanceUSD          *float64   `json:"balance_usd"`
	BalanceCurrency     *string    `json:"balance_currency"`
}

type vendorErrorKindStat struct {
	ErrorKind           string    `json:"error_kind"`
	Count               int       `json:"count"`
	LastSeen            time.Time `json:"last_seen"`
	DistinctStatusCodes int       `json:"distinct_status_codes"`
	// 2026-09-05 审计 E-#6（additive，向后兼容）：可重试率与阶段分布的
	// 聚合载体。SQL 按 (error_type, is_retryable, stage) 分组后在此折叠回
	// 每 error_kind 一行——前端 error_summary 表格以 error_kind 为 :key，
	// 行基数变化即破坏渲染；新增字段对既有消费者透明。
	RetryableCount int            `json:"retryable_count"`
	StageCounts    map[string]int `json:"stage_counts"`
}

type vendorRecentFailure struct {
	Ts                      time.Time `json:"ts"`
	RequestID               string    `json:"request_id"`
	RawModelName            string    `json:"raw_model_name"`
	AttemptIndex            int       `json:"attempt_index"`
	ErrorKind               string    `json:"error_kind"`
	ErrorMessage            *string   `json:"error_message"`
	UpstreamStatusCode      *int      `json:"upstream_status_code"`
	UpstreamResponsePreview *string   `json:"upstream_response_preview"`
	LatencyMs               *int      `json:"latency_ms"`
	// 2026-09-05 审计闭环1/2：结构化诊断维度（事实源列透传），
	// 前端以此做徽标展示，不再解析 error_message 自由文本。
	Supplier  *string `json:"supplier,omitempty"`
	ErrorCode *string `json:"error_code,omitempty"`
	Retryable *bool   `json:"retryable,omitempty"`
	Stage     *string `json:"stage,omitempty"`
}

type vendorQualityScore struct {
	ProfileDate       string  `json:"profile_date"`
	TotalScore        float64 `json:"total_score"`
	AvailabilityScore float64 `json:"availability_score"`
	StabilityScore    float64 `json:"stability_score"`
}

func (h *vendorCredentialErrorHandlers) getVendorCredentialErrorDetail(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.db == nil {
		writeErrorWithCode(w, http.StatusServiceUnavailable, "db_not_configured", "database is not configured")
		return
	}

	credentialID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || credentialID <= 0 {
		writeErrorWithCode(w, http.StatusBadRequest, "invalid_credential_id", "credential id must be a positive integer")
		return
	}
	hours, err := parseVendorErrorHours(r.URL.Query().Get("hours"))
	if err != nil {
		writeErrorWithCode(w, http.StatusBadRequest, "invalid_hours", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	since := time.Now().Add(-time.Duration(hours) * time.Hour)

	tenantID := EffectiveTenantIDAll(r)
	credential, err := h.loadVendorCredentialMeta(ctx, credentialID, tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErrorWithCode(w, http.StatusNotFound, "credential_not_found", "credential not found")
			return
		}
		slog.Error("load vendor credential detail failed", "operation", "load credential", "credential_id", credentialID, "error", err)
		writeErrorWithCode(w, http.StatusInternalServerError, "credential_query_failed", "failed to load credential detail")
		return
	}

	summary, err := h.loadVendorErrorSummary(ctx, credentialID, tenantID, since)
	if err != nil {
		slog.Error("load vendor error summary failed", "operation", "load error summary", "credential_id", credentialID, "error", err)
		writeErrorWithCode(w, http.StatusInternalServerError, "error_summary_query_failed", "failed to load error summary")
		return
	}
	recent, err := h.loadVendorRecentFailures(ctx, credentialID, tenantID, since)
	if err != nil {
		slog.Error("load vendor recent failures failed", "operation", "load recent failures", "credential_id", credentialID, "error", err)
		writeErrorWithCode(w, http.StatusInternalServerError, "recent_failures_query_failed", "failed to load recent failures")
		return
	}
	scores, err := h.loadVendorQualityScores(ctx, credentialID, tenantID)
	if err != nil {
		slog.Error("load vendor quality scores failed", "operation", "load quality scores", "credential_id", credentialID, "error", err)
		writeErrorWithCode(w, http.StatusInternalServerError, "quality_scores_query_failed", "failed to load quality scores")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id":     credentialID,
		"credential_label":  credential.Label,
		"credential":        credential,
		"error_summary":     summary,
		"recent_failures":   recent,
		"quality_scores_7d": scores,
		"hours":             hours,
		"since":             since,
	})
}

func parseVendorErrorHours(raw string) (int, error) {
	if raw == "" {
		return 24, nil
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || (hours != 1 && hours != 24 && hours != 168) {
		return 0, fmt.Errorf("hours must be 1, 24, or 168")
	}
	return hours, nil
}

func (h *vendorCredentialErrorHandlers) loadVendorCredentialMeta(ctx context.Context, id int64, tenantID string) (vendorCredentialMeta, error) {
	var c vendorCredentialMeta
	err := h.db.QueryRow(ctx, `
		SELECT id, label, provider_id, health_status, health_error, health_latency_ms,
		       availability_state, state_reason_code, state_reason_detail, state_updated_at,
		       quota_state, lifecycle_status, circuit_state, consecutive_failures,
		       manual_disabled, balance_usd, balance_currency
			FROM credentials
			WHERE id = $1 AND ($2 = '' OR tenant_id = $2)
		`, id, tenantID).Scan(&c.ID, &c.Label, &c.ProviderID, &c.HealthStatus, &c.HealthError, &c.HealthLatencyMs,
		&c.AvailabilityState, &c.StateReasonCode, &c.StateReasonDetail, &c.StateUpdatedAt,
		&c.QuotaState, &c.LifecycleStatus, &c.CircuitState, &c.ConsecutiveFailures,
		&c.ManualDisabled, &c.BalanceUSD, &c.BalanceCurrency)
	if err != nil {
		return c, fmt.Errorf("load credential metadata failed: %w (credential_id=%d)", err, id)
	}
	// Defense in depth (audit R8 P1): health_error is written by the HC
	// probe from the raw upstream error body; rows written before the
	// bg-side sanitize landed may still carry credential echoes.
	if c.HealthError != nil {
		s := string(errorsx.SanitizeErrorText([]byte(*c.HealthError), 320))
		c.HealthError = &s
	}
	return c, nil
}

// loadVendorErrorSummary 读 supplier_errors_unified（V371 事实源：hot 8h +
// columnar 历史分区）。2026-09-05 审计 E-#6：SQL 按
// (error_type, is_retryable, stage) 分组——可重试率与阶段分布不再只能肉眼看
// LIMIT 10 的样本——随后在 Go 侧折叠回每 error_kind 一行（行基数与旧响应
// 一致），折叠出 retryable_count / stage_counts 两个 additive 字段。
// distinct_status_codes 取各子组 DISTINCT 的最大值（子组求和会把同一状态码
// 跨组重复计数；最大值是保守下界，旧行为的"整组 DISTINCT"无法从分组行精确
// 重建）。空串 stage 映射 'unknown'（与 errors_trend 读端约定一致）。
// 旧读源 candidate_failure_logs_with_current_month 保留给双写过渡期的直接
// SQL 消费者，admin 读端已统一切换。
func (h *vendorCredentialErrorHandlers) loadVendorErrorSummary(ctx context.Context, id int64, tenantID string, since time.Time) ([]vendorErrorKindStat, error) {
	folded := make(map[string]*vendorErrorKindStat)
	err := h.withVendorRLSBypassReadTx(ctx, `
		SELECT error_type, is_retryable, COALESCE(NULLIF(stage, ''), 'unknown') AS stage_bucket,
		       COUNT(*)::int, MAX(occurred_at), COUNT(DISTINCT http_status)::int
		FROM supplier_errors_unified
			WHERE credential_id = $1 AND ($2 = '' OR tenant_id = $2) AND occurred_at >= $3
			GROUP BY 1, 2, 3
		`, []any{id, tenantID, since}, func(rows pgx.Rows) error {
		for rows.Next() {
			var kind, stageBucket string
			var retryable bool
			var groupCount, distinctStatus int
			var lastSeen time.Time
			if err := rows.Scan(&kind, &retryable, &stageBucket, &groupCount, &lastSeen, &distinctStatus); err != nil {
				return fmt.Errorf("scan vendor error summary failed: %w (credential_id=%d)", err, id)
			}
			item, ok := folded[kind]
			if !ok {
				item = &vendorErrorKindStat{
					ErrorKind:   kind,
					LastSeen:    lastSeen,
					StageCounts: make(map[string]int),
				}
				folded[kind] = item
			}
			item.Count += groupCount
			if retryable {
				item.RetryableCount += groupCount
			}
			if lastSeen.After(item.LastSeen) {
				item.LastSeen = lastSeen
			}
			if distinctStatus > item.DistinctStatusCodes {
				item.DistinctStatusCodes = distinctStatus
			}
			item.StageCounts[stageBucket] += groupCount
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("query vendor error summary failed: %w (credential_id=%d)", err, id)
	}
	result := make([]vendorErrorKindStat, 0, len(folded))
	for _, item := range folded {
		result = append(result, *item)
	}
	// 保持旧 ORDER BY COUNT(*) DESC 的响应次序（前端按行序渲染）。
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].ErrorKind < result[j].ErrorKind
	})
	return result, nil
}

// vendorRecentFailureRow 是 loadVendorRecentFailures 的行投影：
// 结构化维度来自 supplier_errors_unified；响应体预览（上游 body 片段）
// 不入事实源表（保持其精简），从 candidate_failure_logs_unified 按定位键
// LEFT JOIN 回补，双写过渡期内行总能在 hot 侧命中。
type vendorRecentFailureRow struct {
	Ts           time.Time
	RequestID    string
	Model        string
	AttemptSeq   int
	ErrorKind    string
	ErrorMessage *string
	HTTPStatus   *int
	Retryable    *bool
	Stage        *string
	Supplier     *string
	ErrorCode    *string
	LatencyMs    *int
	Preview      *string
}

// VendorRecentFailuresSQL 取该凭据最近 N 条失败事实，并回补上游响应体预览。
//
// R75 P1（2026-10-01 真库实证）：(request_id, credential_id, attempt_index)
// 在 candidate_failure_logs 侧**不是唯一键**。同一次 dispatch 内，先命中的准入
// 降级（fp_slot_saturated / rate_limit / key_rotation_exhausted）与随后的上游
// 失败**各写一行**，两行共用这三个列——logDispatchPreflightRejection 不写
// failureLogged（它是独立函数，拿不到那个闭包变量），且 fp 饱和是
// degraded_continue：请求继续执行、随后失败，于是同一候选产生两行。
//
// 旧写法把该三元组当唯一键用 LEFT JOIN，两个后果同时发生（真库实测）：
//  1. 行数扇出：2 条失败 → 4 行，外层 LIMIT 10 只能显示 5 条不同失败；
//  2. 更糟的是**错配**：fp_slot_saturated 那一行是网关侧准入事件、本无上游
//     body，却被贴上了 network 失败的上游 body 预览——运维按错误类型排查时
//     看到的是另一条错误的响应体。
//
// 修法两处同施：
//   - 外层先子查询取 10 条**不同**失败再回补，LIMIT 语义回到「10 条失败」；
//   - 回补改 LATERAL ... LIMIT 1，定位键补 error_kind（preflight 拒绝对应的
//     c.error_kind 与 u.error_type 来自同一个 buildRow，天然同值），并要求
//     preview 非空——命中即唯一，且只有真正带上游 body 的行才提供预览。
//
// 子查询必须显式列名、禁用 SELECT *：unified 视图跨 citus-columnar 分区，
// `SELECT *` 直接触发 "cache lookup failed for attribute source of relation"
// （XX000），与 bg/provider_error_aggregator.go 记载的同一 planner 缺陷——
// 这一条是本轮真库验证当场否掉的第一版修法，不是预防性提醒。
// 排序列也不可省：少了 ORDER BY，LIMIT 10 取哪 10 条不确定。
const VendorRecentFailuresSQL = `
	SELECT u.occurred_at, u.request_id, u.model, u.attempt_seq, u.error_type, u.error_message,
	       u.http_status, u.is_retryable, u.stage, u.supplier, u.error_code, u.latency_ms,
	       c.upstream_response_preview
	FROM (
		SELECT occurred_at, request_id, credential_id, model, attempt_seq, error_type, error_message,
		       http_status, is_retryable, stage, supplier, error_code, latency_ms
		FROM supplier_errors_unified
		WHERE credential_id = $1 AND ($2 = '' OR tenant_id = $2) AND occurred_at >= $3
		ORDER BY occurred_at DESC
		LIMIT 10
	) u
	LEFT JOIN LATERAL (
		SELECT cf.upstream_response_preview
		FROM candidate_failure_logs_unified cf
		WHERE cf.request_id = u.request_id
		  AND cf.credential_id = u.credential_id
		  AND cf.attempt_index = u.attempt_seq
		  AND cf.error_kind = u.error_type
		  AND cf.upstream_response_preview IS NOT NULL
		ORDER BY cf.ts DESC
		LIMIT 1
	) c ON true
	ORDER BY u.occurred_at DESC`

func (h *vendorCredentialErrorHandlers) loadVendorRecentFailures(ctx context.Context, id int64, tenantID string, since time.Time) ([]vendorRecentFailure, error) {
	var result []vendorRecentFailure
	err := h.withVendorRLSBypassReadTx(ctx, VendorRecentFailuresSQL, []any{id, tenantID, since}, func(rows pgx.Rows) error {
		result = make([]vendorRecentFailure, 0, 10)
		for rows.Next() {
			var row vendorRecentFailureRow
			var item vendorRecentFailure
			if err := rows.Scan(&row.Ts, &row.RequestID, &row.Model, &row.AttemptSeq, &row.ErrorKind,
				&row.ErrorMessage, &row.HTTPStatus, &row.Retryable, &row.Stage, &row.Supplier, &row.ErrorCode,
				&row.LatencyMs, &row.Preview); err != nil {
				return fmt.Errorf("scan vendor recent failure failed: %w (credential_id=%d)", err, id)
			}
			item = vendorRecentFailure{
				Ts:                      row.Ts,
				RequestID:               row.RequestID,
				RawModelName:            row.Model,
				AttemptIndex:            row.AttemptSeq,
				ErrorKind:               row.ErrorKind,
				ErrorMessage:            row.ErrorMessage,
				UpstreamStatusCode:      row.HTTPStatus,
				UpstreamResponsePreview: row.Preview,
				LatencyMs:               row.LatencyMs,
				Supplier:                row.Supplier,
				ErrorCode:               row.ErrorCode,
				Retryable:               row.Retryable,
				Stage:                   row.Stage,
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("query vendor recent failures failed: %w (credential_id=%d)", err, id)
	}
	// Defense in depth: rows written before errorsx.SanitizeErrorText was
	// wired into buildRow may still carry credential echoes from the upstream
	// body. Re-sanitize on read so the JSON response to admin callers can
	// never expose a Bearer token or API key surface.
	for i := range result {
		if result[i].ErrorMessage != nil {
			s := string(errorsx.SanitizeErrorText([]byte(*result[i].ErrorMessage), 320))
			result[i].ErrorMessage = &s
		}
		if result[i].UpstreamResponsePreview != nil {
			s := string(errorsx.SanitizeErrorText([]byte(*result[i].UpstreamResponsePreview), 320))
			result[i].UpstreamResponsePreview = &s
		}
	}
	return result, nil
}

func (h *vendorCredentialErrorHandlers) loadVendorQualityScores(ctx context.Context, id int64, tenantID string) ([]vendorQualityScore, error) {
	rows, err := h.db.Query(ctx, `
		SELECT p.profile_date, p.total_score, p.availability_score, p.stability_score
		FROM provider_profile_daily p
		JOIN credentials c ON c.id = p.credential_id
		WHERE p.credential_id = $1 AND ($2 = '' OR c.tenant_id = $2)
		  AND p.profile_date >= CURRENT_DATE - 7
		ORDER BY p.profile_date DESC
	`, id, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query vendor quality scores failed: %w (credential_id=%d)", err, id)
	}
	defer rows.Close()
	result := make([]vendorQualityScore, 0, 7)
	for rows.Next() {
		var item vendorQualityScore
		var profileDate time.Time
		if err := rows.Scan(&profileDate, &item.TotalScore, &item.AvailabilityScore, &item.StabilityScore); err != nil {
			return nil, fmt.Errorf("scan vendor quality score failed: %w (credential_id=%d)", err, id)
		}
		item.ProfileDate = profileDate.Format("2006-01-02")
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vendor quality scores failed: %w (credential_id=%d)", err, id)
	}
	return result, nil
}
