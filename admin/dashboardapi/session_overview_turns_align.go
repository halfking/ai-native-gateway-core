package dashboardapi

// session_overview_turns_align.go — cross-check SUM(session_summaries.request_count)
// against business turn rows in the same days window so the stats tab can
// surface degraded instead of silent drift.
//
// Universe note (honest boundary):
//   · summaries = session-level snapshots (first_request_at window)
//   · turns     = request rows in session_turns_hot ∪ session_turns
//     (ts window, probe markers excluded; never probe_hot)
// They are designed to track, not to be bitwise identical. Large relative
// gaps mean the KPI cannot be trusted as a turns proxy.

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

// rowQuerier 是轮次计数对拍腿的最小查询面：pgx.Tx（只读事务）与 pgxpool.Pool
// 都满足。v2 投影端点把它钉在 tx 上（与投影主查询同一事务、同一 RLS scope），
// 遗留端点把它钉在 pool 上。
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TurnsAlignmentThresholdPct is the relative gap that flips degraded.
// Absolute floor avoids false positives on tiny windows.
const (
	TurnsAlignmentThresholdPct = 0.10
	TurnsAlignmentAbsFloor     = 100
)

// TurnsAlignment is the wire payload for KPI ↔ turns consistency.
type TurnsAlignment struct {
	SummaryRequests int64   `json:"summary_requests"`
	TurnsCount      int64   `json:"turns_count"`
	AbsDelta        int64   `json:"abs_delta"`
	Degraded        bool    `json:"degraded"`
	ThresholdPct    float64 `json:"threshold_pct"`
	Message         string  `json:"message,omitempty"`
}

func businessTurnsFilterSQL(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return fmt.Sprintf(`COALESCE(%sorigin_stage, '') NOT IN (%s)
		  AND COALESCE(%stask_type, '') <> 'probe_triggered'
		  AND COALESCE(%srequest_id, '') NOT LIKE 'probe-%%'`,
		prefix, probekind.SQLStageList(), prefix, prefix)
}

func evaluateTurnsAlignment(summaryRequests, turnsCount int64) TurnsAlignment {
	delta := summaryRequests - turnsCount
	if delta < 0 {
		delta = -delta
	}
	base := summaryRequests
	if turnsCount > base {
		base = turnsCount
	}
	thresh := int64(TurnsAlignmentAbsFloor)
	if base > 0 {
		rel := int64(math.Ceil(float64(base) * TurnsAlignmentThresholdPct))
		if rel > thresh {
			thresh = rel
		}
	}
	out := TurnsAlignment{
		SummaryRequests: summaryRequests,
		TurnsCount:      turnsCount,
		AbsDelta:        delta,
		ThresholdPct:    TurnsAlignmentThresholdPct,
		Degraded:        delta > thresh,
	}
	if out.Degraded {
		out.Message = "session summary request_count and business turns are not aligned"
	}
	return out
}

func (h *SessionOverviewHandler) queryBusinessTurnsCount(ctx context.Context, params QueryParams) (int64, error) {
	where := []string{
		fmt.Sprintf("rl.ts >= NOW() - INTERVAL '%d days'", params.Days),
		businessTurnsFilterSQL("rl"),
	}
	args := []interface{}{}
	argIdx := 1
	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("rl.tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	_ = argIdx
	query := fmt.Sprintf(`
		SELECT COUNT(*)::bigint
		  FROM %s rl
		 WHERE %s
	`, db.SessionFamilyTurnsSourceSQL(), joinStrings(where, " AND "))
	var n int64
	if err := h.db.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// queryBusinessTurnsCountWindowed 是轮次计数对拍腿的**窗口化**变体，供 v2
// 投影端点（HandleProjectedSessionDashboard）使用。
//
// 与遗留 queryBusinessTurnsCount 的两点差异（都是刻意的）：
//
//  1. 窗口谓词参数化 `[start, end)`，与 ReadSessionDashboard 的 $1/$2 同界同源
//     —— v2 支持显式日历窗口（start_date/end_date），照搬遗留的 rolling
//     `NOW()-N days` 会让日历模式下的对拍量的是另一个区间（假对拍）。
//     rolling 模式下调用方传入的 start/end 也是同一条 [start,end) 界
//     （projectedSessionWindow 折出的窗口）⇒ 两种模式对拍的都是「页面展示
//     的那个窗口」。
//  2. 查询面是接口（q rowQuerier）：v2 钉在只读事务 tx 上 ⇒ 与投影主查询
//     同一事务、同一 RLS scope（tenant/bypass 与主查询逐字一致）；遗留端点
//     仍走 pool（无 RLS scope，靠 TenantID 谓词收窄）。
//
// 判据本身与遗留逐语义一致：businessTurnsFilterSQL 三条款（origin_stage 不在
// probekind SSOT 清单 ∧ task_type≠probe_triggered ∧ request_id 不带 probe- 前缀）
// + 可选 tenant 收窄。**不套会话宇宙**：宇宙（owner/is_probe 计费豁免）是
// summaries 侧的口径，turns 侧两条腿（遗留与 v2）都保持纯业务轮次谓词。
// tenant 谓词带 ::text（会话族投影的 tenant_id 是 t.tenant_id::text 形态，
// 与遗留 rl.tenant_id 同型对齐）。
func queryBusinessTurnsCountWindowed(ctx context.Context, q rowQuerier, start, end time.Time, tenantID string) (int64, error) {
	where := []string{
		"rl.ts >= $1",
		"rl.ts < $2",
		businessTurnsFilterSQL("rl"),
	}
	args := []any{start, end}
	if tenantID != "" {
		where = append(where, "rl.tenant_id::text = $3")
		args = append(args, tenantID)
	}
	query := fmt.Sprintf(`
		SELECT COUNT(*)::bigint
		  FROM %s rl
		 WHERE %s
	`, db.SessionFamilyTurnsSourceSQL(), joinStrings(where, " AND "))
	var n int64
	if err := q.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
