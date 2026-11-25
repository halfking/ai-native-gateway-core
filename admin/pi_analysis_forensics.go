package admin

// pi_analysis_forensics.go — PI 分析的取证轮端点（2026-11-25）。
//
//	GET  /queue/{id}/evidence        按需取证（请求账本 / 上游尝试 / 会话 / 服务日志 / 手册）
//	POST /queue/{id}/analyze         定向分析（这一条；pending / failed / analyzed 都可）
//	POST /queue/{id}/verify          复测（规则层重放 / 出错层复查 / 复发统计）并留痕
//	POST /results/{id}/reparse       用当前解析器重读某条已落库结果的原始响应
//	POST /results/reparse-failed     批量重读所有「解析失败」的存量结果
//
// ★ 鉴权与租户（2026-11-26 复读后收紧）：
//
//	全部经 AdminMiddleware（与既有端点一致）。在此之上：
//
//	· **写动作只收超级管理员**：定向分析 / 复测留痕 / 重解析，连同既有的
//	  apply / revert / trigger，统一在派发点过 piRequireSuperAdminForWrite——
//	  本仓管理面里 tenant_admin 是只读角色，而页面的 requiresSuper 只是体验层。
//	· **取证（读）**：调用方须是超级管理员，或与队列行同租户；非超级管理员
//	  拿到的服务日志只保留**提到本请求号 / 会话号**的行（进程内日志缓冲是
//	  整个实例共用的，见 piScopeServiceLogsToRequest）。
//	· 既有的 queue / results 列表读端点维持原口径（不在本轮改）。
//
// ★ 为什么复测结论不是「点一下说修好了」：
//
//	结论由**可验证的检查项**聚合出来，每一项都如实标状态；一项都定不了时
//	结论是 inconclusive（无法判定），不是 pass。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
)

// piRequireSuperAdminForWrite 是本处理面所有**写**动作的统一闸门（在派发点调用）。
//
//	★ 为什么要补：页面的 requiresSuper 只是体验层，权限真源在后端；而 2026-11-26
//	  之前 apply / revert / trigger 在后端没有任何角色闸——tenant_admin（本仓
//	  约定的只读角色）持有效令牌可以直调，apply 改的是**全平台**的 PI 规则。
func piRequireSuperAdminForWrite(w http.ResponseWriter, r *http.Request) bool {
	return RequireSuperAdminForWrite(w, r)
}

// SetEvidenceGatherer 注入取证采集面（装配侧调用；nil ⇒ 端点回 503）。
func (h *PIAnalysisHandler) SetEvidenceGatherer(g *PIEvidenceGatherer) {
	if h == nil {
		return
	}
	h.evidence = g
}

// SetReplayJudge 注入在线 PI 检测器（复测里的「判官复查」要用它的端点解析器；
// nil ⇒ 判官复查如实报不可用，其余复测项不受影响）。
func (h *PIAnalysisHandler) SetReplayJudge(d *promptinjection.Detector) {
	if h == nil {
		return
	}
	h.replayJudge = d
}

// piAuthorizeQueueTenant 校验调用方与队列行同租户（超级管理员不限）。
func (h *PIAnalysisHandler) piAuthorizeQueueTenant(w http.ResponseWriter, r *http.Request, queueID int64) bool {
	if IsSuperAdminOrLegacy(r) {
		return true
	}
	var tenant string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT tenant_id FROM error_analysis_queue WHERE id = $1`, queueID).Scan(&tenant)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "queue item not found", http.StatusNotFound)
		return false
	}
	if err != nil {
		writeInternalTextErr(w, "query queue tenant failed", err)
		return false
	}
	if tenant != GetTenantID(r) {
		http.Error(w, "queue item belongs to another tenant", http.StatusForbidden)
		return false
	}
	return true
}

// piLoadQueueItem 取队列行（不存在 ⇒ 404）。
func (h *PIAnalysisHandler) piLoadQueueItem(w http.ResponseWriter, r *http.Request, queueID int64) *promptinjection.QueuedError {
	if h.collector == nil {
		http.Error(w, "PI error collector is not configured", http.StatusServiceUnavailable)
		return nil
	}
	item, err := h.collector.GetQueueItem(r.Context(), queueID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "queue item not found", http.StatusNotFound)
		return nil
	}
	if err != nil {
		writeInternalTextErr(w, "query error analysis queue item failed", err)
		return nil
	}
	return item
}

// piEvidenceTargetOf 由队列行构造取证锚点。
func piEvidenceTargetOf(item *promptinjection.QueuedError) promptinjection.EvidenceTarget {
	target := promptinjection.EvidenceTarget{
		QueueID:   item.ID,
		ErrorType: item.ErrorType,
		TenantID:  item.TenantID,
		SessionID: item.SessionID.String,
		RequestID: item.RequestID.String,
		ErrorAt:   item.CreatedAt,
	}
	// 时间锚点优先取「分析完成」之后的时刻没有意义；取入队时刻即错误发生时刻。
	if len(item.ErrorDetails) > 0 {
		var details map[string]any
		if json.Unmarshal(item.ErrorDetails, &details) == nil {
			target.ErrorDetails = details
		}
	}
	return target
}

// ── GET /queue/{id}/evidence ───────────────────────────────────────────────

func (h *PIAnalysisHandler) handleQueueEvidence(w http.ResponseWriter, r *http.Request, queueID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.evidence == nil {
		http.Error(w, "evidence gatherer is not configured", http.StatusServiceUnavailable)
		return
	}
	if !h.piAuthorizeQueueTenant(w, r, queueID) {
		return
	}
	item := h.piLoadQueueItem(w, r, queueID)
	if item == nil {
		return
	}
	bundle, err := h.evidence.Gather(r.Context(), piEvidenceTargetOf(item))
	if err != nil {
		writeInternalTextErr(w, "gather evidence failed", err)
		return
	}
	if !IsSuperAdminOrLegacy(r) {
		piScopeServiceLogsToRequest(bundle, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queue_id":          item.ID,
		"request_id":        nullablePIString(item.RequestID),
		"session_id":        nullablePIString(item.SessionID),
		"error_type":        item.ErrorType,
		"error_at":          item.CreatedAt.Format(time.RFC3339),
		"request":           bundle.Request,
		"session_context":   emptyIfNil(bundle.Session),
		"upstream_attempts": emptyIfNil(bundle.Attempts),
		"error_window":      emptyIfNil(bundle.ErrorWindow),
		"service_logs":      emptyIfNil(bundle.ServiceLogs),
		"service_log_meta":  bundle.ServiceLogMeta,
		"playbook_entries":  emptyIfNil(bundle.Playbook),
		"notes":             emptyIfNil(bundle.Notes),
		// 机器可读的降级标记：哪几路没算出来（调用方据此区分「空」与「算不出来」）。
		"degraded":          emptyIfNil(bundle.Degraded),
		"gathered_at":       bundle.GatheredAt.Format(time.RFC3339),
		"duration_ms":       bundle.DurationMs,
		"verifications":     verificationsOf(item.Metadata),
		"last_verification": lastVerificationOf(item.Metadata),
	})
}

func nullablePIString(ns sql.NullString) any {
	if !ns.Valid {
		return nil
	}
	return ns.String
}

// piScopeServiceLogsToRequest 把服务日志收窄到「与这一条请求有关」的行（非超级
// 管理员查看取证时调用）。
//
//	★ 为什么必须收窄：进程内日志缓冲是**整个实例共用**的——「错误时刻附近的
//	  WARN/ERROR」那一遍检索不带租户条件，原样返回等于把别的租户的告警行交给
//	  了本租户的管理员。只有提到本请求号 / 会话号的行才能确定属于本租户。
//	★ 被收窄掉的行数如实留在说明里：不冒充「那个时段没有告警」。
func piScopeServiceLogsToRequest(bundle *promptinjection.EvidenceBundle, item *promptinjection.QueuedError) {
	anchors := piEvidenceDedupe([]string{item.RequestID.String, item.SessionID.String})
	kept := bundle.ServiceLogs[:0]
	hidden := 0
	for _, l := range bundle.ServiceLogs {
		related := false
		for _, a := range anchors {
			if strings.Contains(l.Line, a) {
				related = true
				break
			}
		}
		if related {
			kept = append(kept, l)
			continue
		}
		hidden++
	}
	bundle.ServiceLogs = kept
	if bundle.ServiceLogMeta == nil {
		bundle.ServiceLogMeta = map[string]any{}
	}
	bundle.ServiceLogMeta["scoped_to_request"] = true
	if hidden > 0 {
		bundle.ServiceLogMeta["hidden_unrelated"] = hidden
		bundle.Notes = append(bundle.Notes, fmt.Sprintf(
			"service logs: %d line(s) from the same time window do not mention this request and are shown to super admins only", hidden))
	}
}

// emptyIfNil 把 nil 切片换成空切片（前端不必区分 null 与 []）。
func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ── POST /queue/{id}/analyze ───────────────────────────────────────────────

func (h *PIAnalysisHandler) handleQueueAnalyze(w http.ResponseWriter, r *http.Request, queueID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.analyzer == nil {
		http.Error(w, "PI error analyzer is not configured", http.StatusServiceUnavailable)
		return
	}
	item := h.piLoadQueueItem(w, r, queueID)
	if item == nil {
		return
	}
	// 正在被别的流程分析（领取时间在回收阈值之内）⇒ 明确拒绝，不重复花一次模型。
	if item.Status == promptinjection.QueueStatusProcessing && item.StartedAt.Valid &&
		time.Since(item.StartedAt.Time) < 60*time.Minute {
		http.Error(w, "queue item is already being analysed", http.StatusConflict)
		return
	}
	// 用 bg.SpawnLoop（自带 panic recover）而不是裸 go func——goroutine 里的
	// panic 会终止整个进程（与 TriggerAnalysis 同款纪律）。
	bg.SpawnLoop(context.Background(), "pi_analysis.targeted", func(ctx context.Context) {
		if _, err := h.analyzer.AnalyzeQueueItem(ctx, queueID); err != nil {
			slog.Warn("pi_analysis: targeted analysis failed", "queue_id", queueID, "error", err)
		}
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"queue_id": queueID, "status": "queued"})
}

// ── POST /results/{id}/reparse ／ POST /results/reparse-failed ─────────────

func (h *PIAnalysisHandler) handleResultReparse(w http.ResponseWriter, r *http.Request, queueID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, diag, err := promptinjection.ReparseStoredResult(r.Context(), h.db, queueID)
	switch {
	case errors.Is(err, promptinjection.ErrAnalysisResultNotFound):
		http.Error(w, "analysis result not found", http.StatusNotFound)
		return
	case errors.Is(err, promptinjection.ErrNothingToReparse), errors.Is(err, promptinjection.ErrStillUnparsable):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case err != nil:
		writeInternalTextErr(w, "re-parse analysis result failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queue_id":         queueID,
		"mode":             diag.Mode,
		"truncated":        diag.Truncated,
		"lost_keys":        emptyIfNil(diag.LostKeys),
		"root_cause":       result.RootCause,
		"error_category":   result.ErrorCategory,
		"confidence_score": result.ConfidenceScore,
	})
}

// piReparseFailedSummary 是批量重解析的读数。
type piReparseFailedSummary struct {
	Scanned          int      `json:"scanned"`
	Reparsed         int      `json:"reparsed"`
	StillUnparsable  int      `json:"still_unparsable"`
	Failed           int      `json:"failed"`
	ReparsedQueueIDs []int64  `json:"reparsed_queue_ids"`
	Errors           []string `json:"errors,omitempty"`
}

const piReparseFailedBatch = 500

// handleReparseFailed 批量重读所有「解析失败」的存量结果。
//
//	★ 只动 root_cause = 'llm_parse_failed' 的行（那些行本来就只有占位结论），
//	  逐条调 ReparseStoredResult：读不出的一条不改。
func (h *PIAnalysisHandler) handleReparseFailed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if piRequireSuperAdminForWrite(w, r) {
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT queue_id FROM error_analysis_results
		WHERE root_cause = 'llm_parse_failed' AND llm_response_raw IS NOT NULL AND llm_response_raw <> ''
		ORDER BY queue_id ASC LIMIT $1`, piReparseFailedBatch)
	if err != nil {
		writeInternalTextErr(w, "list parse-failed results failed", err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeInternalTextErr(w, "iterate parse-failed results failed", err)
		return
	}
	rows.Close()

	summary := piReparseFailedSummary{Scanned: len(ids), ReparsedQueueIDs: []int64{}}
	for _, id := range ids {
		_, _, err := promptinjection.ReparseStoredResult(r.Context(), h.db, id)
		switch {
		case err == nil:
			summary.Reparsed++
			summary.ReparsedQueueIDs = append(summary.ReparsedQueueIDs, id)
		case errors.Is(err, promptinjection.ErrStillUnparsable):
			summary.StillUnparsable++
		default:
			summary.Failed++
			if len(summary.Errors) < 10 {
				summary.Errors = append(summary.Errors, fmt.Sprintf("queue %d: %v", id, err))
			}
		}
	}
	slog.Info("pi_analysis: bulk re-parse of parse-failed results done",
		"scanned", summary.Scanned, "reparsed", summary.Reparsed,
		"still_unparsable", summary.StillUnparsable, "failed", summary.Failed, "by", piAppliedBy(r))
	writeJSON(w, http.StatusOK, summary)
}

// ── POST /queue/{id}/verify ────────────────────────────────────────────────

// 复测检查项的状态闭集。
const (
	piCheckPass          = "pass"
	piCheckFail          = "fail"
	piCheckSkipped       = "skipped"
	piCheckNotApplicable = "not_applicable"
	piCheckInfo          = "info"
	piCheckError         = "error"
)

// 复测结论闭集。
const (
	piVerdictNotReproducing = "not_reproducing"
	piVerdictReproducing    = "reproducing"
	piVerdictInconclusive   = "inconclusive"
)

// piVerifyCheck 是一项复测检查。
type piVerifyCheck struct {
	Name     string         `json:"name"`
	Status   string         `json:"status"`
	Summary  string         `json:"summary"`
	Decisive bool           `json:"decisive"`
	Detail   map[string]any `json:"detail,omitempty"`
}

// piVerifyRecord 是一次复测的留痕（落在队列行 metadata.verifications）。
type piVerifyRecord struct {
	At          time.Time       `json:"at"`
	By          string          `json:"by"`
	Verdict     string          `json:"verdict"`
	Summary     string          `json:"summary"`
	FixApplied  bool            `json:"fix_applied"`
	ReferenceAt string          `json:"reference_at,omitempty"`
	Checks      []piVerifyCheck `json:"checks"`
}

func (h *PIAnalysisHandler) handleQueueVerify(w http.ResponseWriter, r *http.Request, queueID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	// 写动作的超级管理员闸在派发点（handleQueueItem）——这里只管复测本身。
	item := h.piLoadQueueItem(w, r, queueID)
	if item == nil {
		return
	}
	var req struct {
		IncludeLLM bool `json:"include_llm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	record := h.runVerification(r, item, req.IncludeLLM)

	// 留痕：失败只 WARN（结论已经算出来了，不该因为写不进留痕就把它吞掉）。
	if h.collector != nil {
		payload, _ := json.Marshal(record)
		var asMap map[string]any
		if json.Unmarshal(payload, &asMap) == nil {
			if err := h.collector.AppendVerification(r.Context(), queueID, asMap); err != nil {
				slog.Warn("pi_analysis: failed to append verification record", "queue_id", queueID, "error", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, record)
}

// runVerification 跑全部适用的检查项并聚合结论。
func (h *PIAnalysisHandler) runVerification(r *http.Request, item *promptinjection.QueuedError, includeLLM bool) *piVerifyRecord {
	rec := &piVerifyRecord{At: time.Now().UTC(), By: piAppliedBy(r), Checks: []piVerifyCheck{}}
	tenant := item.TenantID
	if tenant == "" {
		tenant = "default"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()

	appliedAt, analyzedAt := h.piFixReferenceTimes(item.ID)

	// ── ① 规则层重放 ────────────────────────────────────────────────────
	rec.Checks = append(rec.Checks, h.verifyRulesReplay(ctx, item, tenant))

	// ── ② 出错的那一层现在通不通 ────────────────────────────────────────
	lr, err := h.recheckLayer(ctx, h.db, item.ErrorType, tenant, item.InputText.String,
		promptinjection.RecheckOptions{IncludeLLM: includeLLM, Judge: h.replayJudge, Timeout: 20 * time.Second})
	switch {
	case err != nil:
		rec.Checks = append(rec.Checks, piVerifyCheck{Name: "layer_recheck", Status: piCheckError,
			Summary: "layer recheck failed to run: " + piEvidenceTruncate(err.Error(), 200)})
	case lr.Status == "ok":
		rec.Checks = append(rec.Checks, piVerifyCheck{Name: "layer_recheck", Status: piCheckPass, Decisive: true,
			Summary: "the layer that failed now responds", Detail: map[string]any{"layer": lr.Layer, "detail": lr.Detail, "latency_ms": lr.LatencyMs}})
	case lr.Status == "failed":
		rec.Checks = append(rec.Checks, piVerifyCheck{Name: "layer_recheck", Status: piCheckFail, Decisive: true,
			Summary: "the layer that failed is still failing", Detail: map[string]any{"layer": lr.Layer, "detail": lr.Detail, "latency_ms": lr.LatencyMs}})
	case lr.Status == "skipped":
		rec.Checks = append(rec.Checks, piVerifyCheck{Name: "layer_recheck", Status: piCheckSkipped,
			Summary: "judge call not requested (tick include_llm to call it once)", Detail: map[string]any{"layer": lr.Layer}})
	default:
		rec.Checks = append(rec.Checks, piVerifyCheck{Name: "layer_recheck", Status: piCheckNotApplicable,
			Summary: "this error type has no re-checkable layer"})
	}

	// ── ③ 复发统计 ──────────────────────────────────────────────────────
	rec.ReferenceAt, rec.FixApplied = piReferenceTime(appliedAt, analyzedAt)
	rec.Checks = append(rec.Checks, h.verifyRecurrence(ctx, item, tenant, rec.ReferenceAt))

	// ── 聚合 ────────────────────────────────────────────────────────────
	fail, pass := 0, 0
	for _, c := range rec.Checks {
		if !c.Decisive {
			continue
		}
		switch c.Status {
		case piCheckFail:
			fail++
		case piCheckPass:
			pass++
		}
	}
	switch {
	case fail > 0:
		rec.Verdict = piVerdictReproducing
	case pass > 0:
		rec.Verdict = piVerdictNotReproducing
	default:
		rec.Verdict = piVerdictInconclusive
	}
	rec.Summary = fmt.Sprintf("%d decisive check(s) failed, %d passed, %d total",
		fail, pass, len(rec.Checks))
	return rec
}

// verifyRulesReplay 把存档输入重过一遍本地规则层，与当初命中的规则对比。
func (h *PIAnalysisHandler) verifyRulesReplay(ctx context.Context, item *promptinjection.QueuedError, tenant string) piVerifyCheck {
	// 规则重放只对「规则本身就是问题」的错误类型有判定力；其余类型只作参考。
	decisive := item.ErrorType == promptinjection.ErrorTypeRuleFalsePositive ||
		item.ErrorType == promptinjection.ErrorTypeHeuristicMisfire ||
		item.ErrorType == promptinjection.ErrorTypeUnexpectedBehavior
	if strings.TrimSpace(item.InputText.String) == "" {
		return piVerifyCheck{Name: "rules_replay", Status: piCheckSkipped, Decisive: decisive,
			Summary: "queue item has no stored input to replay"}
	}
	res, err := h.replayRules(ctx, h.db, tenant, item.InputText.String)
	if err != nil || res == nil {
		return piVerifyCheck{Name: "rules_replay", Status: piCheckError, Decisive: decisive,
			Summary: "rules replay failed to run: " + piEvidenceTruncate(fmt.Sprint(err), 200)}
	}
	detail := map[string]any{
		"originally_matched": piOriginallyMatched(item.MatchedRules),
		"now_matched":        emptyIfNil(res.MatchedRules),
		"layers":             emptyIfNil(res.Layers),
		"score":              res.Score,
		"risk_level":         res.RiskLevel,
		"rule_count":         res.RuleCount,
	}
	if !res.Available {
		return piVerifyCheck{Name: "rules_replay", Status: piCheckSkipped, Decisive: decisive,
			Summary: "rules replay unavailable: " + res.Reason, Detail: detail}
	}
	originally := piOriginallyMatched(item.MatchedRules)
	still := make([]string, 0)
	for _, name := range originally {
		if piContains(res.MatchedRules, name) {
			still = append(still, name)
		}
	}
	detail["still_matching"] = still
	if len(originally) > 0 {
		if len(still) > 0 {
			return piVerifyCheck{Name: "rules_replay", Status: piCheckFail, Decisive: decisive,
				Summary: "the rules that fired originally still match the same input", Detail: detail}
		}
		return piVerifyCheck{Name: "rules_replay", Status: piCheckPass, Decisive: decisive,
			Summary: "none of the rules that fired originally match the same input any more", Detail: detail}
	}
	// 当初没记录命中规则：现在的命中情况只作参考。
	status, summary := piCheckInfo, "no originally matched rules on record; current matches shown for reference"
	if len(res.MatchedRules) > 0 && decisive {
		status, summary = piCheckFail, "local rule layers still match the same input"
	}
	return piVerifyCheck{Name: "rules_replay", Status: status, Decisive: decisive, Summary: summary, Detail: detail}
}

// verifyRecurrence 统计参照时点之后同型错误有没有再出现。
func (h *PIAnalysisHandler) verifyRecurrence(ctx context.Context, item *promptinjection.QueuedError, tenant, referenceAt string) piVerifyCheck {
	if referenceAt == "" {
		return piVerifyCheck{Name: "recurrence", Status: piCheckSkipped,
			Summary: "no reference time yet (neither a fix nor an analysis to measure against)"}
	}
	var queueRows int64
	if err := h.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM error_analysis_queue
		WHERE tenant_id = $1 AND error_type = $2 AND id <> $3 AND created_at > $4::timestamptz`,
		tenant, item.ErrorType, item.ID, referenceAt).Scan(&queueRows); err != nil {
		return piVerifyCheck{Name: "recurrence", Status: piCheckError,
			Summary: "recurrence query failed: " + piEvidenceTruncate(err.Error(), 200)}
	}
	var failureRows int64
	var lastSeen sql.NullString
	if kind := piEvidenceString(piDetailsOf(item.ErrorDetails), "error_kind"); kind != "" {
		// 上游失败类：同型失败在候选失败记录里的新增条数（与发现器同一张表）。
		q := `SELECT COUNT(*), to_char(MAX(ts), 'YYYY-MM-DD"T"HH24:MI:SS') FROM candidate_failure_logs_hot
		      WHERE tenant_id = $1 AND error_kind = $2 AND ts > $3::timestamptz`
		args := []any{tenant, kind, referenceAt}
		details := piDetailsOf(item.ErrorDetails)
		if provider, model := piEvidenceString(details, "provider_id"), piEvidenceString(details, "model"); provider != "" {
			q += ` AND provider_id::text = $4`
			args = append(args, provider)
			if model != "" {
				q += ` AND COALESCE(raw_model_name, '') = $5`
				args = append(args, model)
			}
		}
		if err := h.db.QueryRowContext(ctx, q, args...).Scan(&failureRows, &lastSeen); err != nil {
			slog.Warn("pi_analysis: recurrence (candidate failures) query failed", "queue_id", item.ID, "error", err)
		}
	}
	total := queueRows + failureRows
	detail := map[string]any{"queue_rows": queueRows, "failure_rows": failureRows, "last_seen": nullablePIString(lastSeen)}
	if total == 0 {
		return piVerifyCheck{Name: "recurrence", Status: piCheckPass, Decisive: true,
			Summary: "no occurrence of this error type since the reference time", Detail: detail}
	}
	return piVerifyCheck{Name: "recurrence", Status: piCheckFail, Decisive: true,
		Summary: fmt.Sprintf("%d occurrence(s) of this error type since the reference time", total), Detail: detail}
}

// piFixReferenceTimes 取「修复应用时刻」与「分析完成时刻」。
func (h *PIAnalysisHandler) piFixReferenceTimes(queueID int64) (appliedAt, analyzedAt string) {
	var applied, analyzed sql.NullString
	err := h.db.QueryRowContext(context.Background(), `
		SELECT to_char(applied_at, 'YYYY-MM-DD"T"HH24:MI:SS'), to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS')
		FROM error_analysis_results WHERE queue_id = $1`, queueID).Scan(&applied, &analyzed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("pi_analysis: read fix reference times failed", "queue_id", queueID, "error", err)
	}
	return applied.String, analyzed.String
}

// piReferenceTime 选参照时点：有已应用的修复 ⇒ 用它；否则用分析完成时刻。
func piReferenceTime(appliedAt, analyzedAt string) (string, bool) {
	if appliedAt != "" {
		return appliedAt, true
	}
	return analyzedAt, false
}

// piOriginallyMatched 读出队列行当初记录的命中规则名。
func piOriginallyMatched(raw json.RawMessage) []string {
	out := []string{}
	if len(raw) == 0 || string(raw) == "null" {
		return out
	}
	var items []map[string]any
	if json.Unmarshal(raw, &items) != nil {
		var single map[string]any
		if json.Unmarshal(raw, &single) == nil {
			items = []map[string]any{single}
		}
	}
	for _, it := range items {
		if name, _ := it["rule_name"].(string); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func piDetailsOf(raw json.RawMessage) map[string]any {
	var details map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &details)
	}
	return details
}

func piContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// verificationsOf / lastVerificationOf 从队列行 metadata 里读复测留痕。
func verificationsOf(raw json.RawMessage) []any {
	var meta map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &meta) != nil || meta == nil {
		return []any{}
	}
	if list, ok := meta["verifications"].([]any); ok {
		return list
	}
	return []any{}
}

func lastVerificationOf(raw json.RawMessage) any {
	var meta map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &meta) != nil || meta == nil {
		return nil
	}
	return meta["last_verification"]
}

// ── 修正方案视图（后端单点判定「能不能自动执行」）─────────────────────────

// fixPlanView 把「这条结果能不能一键自动修正、为什么不能」算成结构化字段。
//
//	★ 为什么放在后端：判定要素有四个（动作白名单能否归一出动作、置信度门、
//	  类型闸、是否已应用），其中三个本来就只在 ApplyFix 里。前端若自己再判一遍
//	  就是第二份真值——按钮亮了、点下去 422，正是此前「按钮可点但应用被拒」的来源。
//	  现在：按钮状态与接口结论同源。
func (h *PIAnalysisHandler) fixPlanView(result map[string]any) map[string]any {
	conf, _ := result["confidence_score"].(float64)
	autoApplied, _ := result["auto_applied"].(bool)
	errType, _ := result["error_type"].(string)
	rootCause, _ := result["root_cause"].(string)

	actions := []RuleFixAction{}
	note := ""
	if raw, err := json.Marshal(result["rule_adjustment"]); err == nil {
		if normalized, n := NormalizeRuleAdjustment(raw); len(normalized) > 0 {
			actions = normalized
		} else {
			note = n
		}
	}
	blockers := []string{}
	// 判定顺序与 ApplyFix 的闸门顺序一致（先类型闸、再动作、再置信度）。
	switch {
	case errType == promptinjection.ErrorTypeOutputGuardBlocked:
		blockers = append(blockers, "view_only_type")
	case rootCause == "llm_parse_failed":
		blockers = append(blockers, "analysis_unparsable")
	case len(actions) == 0:
		blockers = append(blockers, "no_actions")
	case conf < piFixConfidenceFloor:
		blockers = append(blockers, "low_confidence")
	}
	if autoApplied {
		blockers = append(blockers, "already_applied")
	}
	view := map[string]any{
		"auto_actions": actions,
		"auto_fixable": len(blockers) == 0,
		"blockers":     blockers,
	}
	if note != "" {
		view["note"] = note
	}
	return view
}

// writeResultWithPlan 输出结果详情并附上修正方案视图。
func (h *PIAnalysisHandler) writeResultWithPlan(w http.ResponseWriter, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		writeInternalTextErr(w, "marshal analysis result failed", err)
		return
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		writeInternalTextErr(w, "decode analysis result failed", err)
		return
	}
	out["fix_plan_view"] = h.fixPlanView(out)
	writeJSON(w, http.StatusOK, out)
}
