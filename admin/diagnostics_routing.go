package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// =============================================================================
// 2026-07-24: 供应商级路由诊断 — 展示 v_routable_credential_models 视图中
// 每个 (credential, model) pair 的 is_routable 状态，并支持一键恢复。
// =============================================================================

type routingBlockedBinding struct {
	CredentialID      int     `json:"credential_id"`
	CredentialLabel   string  `json:"credential_label"`
	RawModelName      string  `json:"raw_model_name"`
	IsRoutable        bool    `json:"is_routable"`
	UnavailableReason *string `json:"unavailable_reason,omitempty"`
}

type routingBlockedCredential struct {
	CredentialID      int                     `json:"credential_id"`
	CredentialLabel   string                  `json:"credential_label"`
	Status            string                  `json:"status"`
	AvailabilityState string                  `json:"availability_state"`
	HealthStatus      string                  `json:"health_status"`
	ManualDisabled    bool                    `json:"manual_disabled"`
	LifecycleStatus   string                  `json:"lifecycle_status"`
	BindingsTotal     int                     `json:"bindings_total"`
	BindingsRoutable  int                     `json:"bindings_routable"`
	BindingsBlocked   int                     `json:"bindings_blocked"`
	Bindings          []routingBlockedBinding `json:"bindings"`
}

type routingBlockedDiagnostic struct {
	ProviderID           int                        `json:"provider_id"`
	ProviderName         string                     `json:"provider_name"`
	BindingsTotal        int                        `json:"bindings_total"`
	BindingsRoutable     int                        `json:"bindings_routable"`
	BindingsBlocked      int                        `json:"bindings_blocked"`
	BlockReasonBreakdown map[string]int             `json:"block_reason_breakdown"`
	Credentials          []routingBlockedCredential `json:"credentials"`
	Truncated            bool                       `json:"truncated,omitempty"`
}

// handleRoutingBlockedDiagnostic returns per-credential routability breakdown
// for a provider.  Useful for diagnosing "credentials look healthy but
// routing can't find them".
//
//	GET /api/admin/diagnostics/routing-blocked?provider_id=X
func (h *Handler) handleRoutingBlockedDiagnostic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pidStr := r.URL.Query().Get("provider_id")
	providerID, err := strconv.Atoi(pidStr)
	if err != nil || providerID <= 0 {
		writeError(w, http.StatusBadRequest, "missing or invalid provider_id query parameter")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	diag, err := buildRoutingBlockedDiagnostic(ctx, h.db, providerID)
	if err != nil {
		writeInternalErr(w, "diagnostic failed", err)
		return
	}
	writeJSON(w, http.StatusOK, diag)
}

func buildRoutingBlockedDiagnostic(ctx context.Context, db *pgxpool.Pool, providerID int) (*routingBlockedDiagnostic, error) {
	var providerName string
	if err := db.QueryRow(ctx, `SELECT display_name FROM providers WHERE id = $1`, providerID).Scan(&providerName); err != nil {
		return nil, fmt.Errorf("provider lookup: %w", err)
	}

	// Query v_routable_credential_models directly — this is the authoritative routing view.
	const maxBindings = 500
	rows, err := db.Query(ctx, `
		SELECT
			v.credential_id,
			v.credential_label,
			v.raw_model_name,
			v.is_routable,
			v.unavailable_reason
		FROM v_routable_credential_models v
		WHERE v.provider_id = $1
		ORDER BY v.credential_id, v.raw_model_name
		LIMIT $2
	`, providerID, maxBindings+1)
	if err != nil {
		return nil, fmt.Errorf("v_routable_credential_models query: %w", err)
	}
	defer rows.Close()

	type bindingKey struct {
		credID int
		model  string
	}
	bindingMap := make(map[bindingKey]routingBlockedBinding)
	credBindingKeys := make(map[int][]bindingKey)
	reasonBreakdown := make(map[string]int)
	var total, routable int

	for rows.Next() {
		var b routingBlockedBinding
		if err := rows.Scan(&b.CredentialID, &b.CredentialLabel, &b.RawModelName, &b.IsRoutable, &b.UnavailableReason); err != nil {
			warnRowSkip("routingBlockedDiagnostic.bindings", err)
			continue
		}
		key := bindingKey{credID: b.CredentialID, model: b.RawModelName}
		bindingMap[key] = b
		credBindingKeys[b.CredentialID] = append(credBindingKeys[b.CredentialID], key)
		total++
		if b.IsRoutable {
			routable++
		} else {
			reason := "unknown"
			if b.UnavailableReason != nil {
				reason = *b.UnavailableReason
			}
			reasonBreakdown[reason]++
		}
	}
	// 迭代中断（连接断开/服务端错误）与正常收敛不可区分，必须查 rows.Err()：
	// 否则会静默返回一个 200 + 截断的 bindings 列表，运维据此判断"没有更多
	// 被阻塞的绑定"，方向完全错。
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("v_routable_credential_models iterate rows: %w", err)
	}

	truncated := total > maxBindings
	if truncated {
		total = maxBindings
	}

	// Fetch credential-level state for each credential with bindings.
	credIDs := make([]int, 0, len(credBindingKeys))
	for cid := range credBindingKeys {
		credIDs = append(credIDs, cid)
	}
	sort.Ints(credIDs)

	credStateMap := make(map[int]struct {
		label, status, availabilityState, healthStatus, lifecycleStatus string
		manualDisabled                                                  bool
	})
	if len(credIDs) > 0 {
		placeholders := make([]string, 0, len(credIDs))
		args := make([]any, 0, len(credIDs))
		for _, cid := range credIDs {
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)+1))
			args = append(args, cid)
		}
		credRows, err := db.Query(ctx, fmt.Sprintf(`
			SELECT id, name || ':' || COALESCE(provider_name, 'unknown'),
			       status, availability_state, health_status,
			       COALESCE(manual_disabled, false), lifecycle_status
			FROM credentials WHERE id IN (%s)
		`, strings.Join(placeholders, ",")), args...)
		if err == nil {
			defer credRows.Close()
			for credRows.Next() {
				var cid int
				var st struct {
					label, status, availabilityState, healthStatus, lifecycleStatus string
					manualDisabled                                                  bool
				}
				if err := credRows.Scan(&cid, &st.label, &st.status, &st.availabilityState, &st.healthStatus,
					&st.manualDisabled, &st.lifecycleStatus); err == nil {
					credStateMap[cid] = st
				} else {
					warnRowSkip("routingBlockedDiagnostic.credState", err)
				}
			}
			// best-effort 补齐：credential 状态查询失败/中断只影响标签与状态
			// 字段，binding 侧的 label 已有回退路径（见下方 cs.label=="" 分支），
			// 不应把整个诊断打成 500 —— 但必须留痕，否则"凭据状态全空"会被
			// 误读成"所有凭据都没有状态"。
			if err := credRows.Err(); err != nil {
				slog.Warn("admin routing-blocked diagnostic credential state rows iteration aborted",
					"op", "routingBlockedDiagnostic.credState", "provider_id", providerID, "error", err)
			}
		}
	}

	// Build per-credential response.
	credentials := make([]routingBlockedCredential, 0, len(credIDs))
	for _, cid := range credIDs {
		keys := credBindingKeys[cid]
		cs := credStateMap[cid]
		cred := routingBlockedCredential{
			CredentialID:      cid,
			CredentialLabel:   cs.label,
			Status:            cs.status,
			AvailabilityState: cs.availabilityState,
			HealthStatus:      cs.healthStatus,
			LifecycleStatus:   cs.lifecycleStatus,
			ManualDisabled:    cs.manualDisabled,
		}
		// Use label from binding data when credential-state query had no result.
		if cred.CredentialLabel == "" {
			if first, ok := bindingMap[keys[0]]; ok {
				cred.CredentialLabel = first.CredentialLabel
			}
		}
		var routableCount, blockedCount int
		bindings := make([]routingBlockedBinding, 0, len(keys))
		for _, key := range keys {
			b := bindingMap[key]
			if b.IsRoutable {
				routableCount++
			} else {
				blockedCount++
			}
			bindings = append(bindings, b)
		}
		cred.BindingsTotal = len(keys)
		cred.BindingsRoutable = routableCount
		cred.BindingsBlocked = blockedCount
		cred.Bindings = bindings

		credentials = append(credentials, cred)
	}

	return &routingBlockedDiagnostic{
		ProviderID:           providerID,
		ProviderName:         providerName,
		BindingsTotal:        total,
		BindingsRoutable:     routable,
		BindingsBlocked:      total - routable,
		BlockReasonBreakdown: reasonBreakdown,
		Credentials:          credentials,
		Truncated:            truncated,
	}, nil
}

// handleRoutingBlockedFix force-recovers all blocked bindings for a provider.
//
//	POST /api/admin/diagnostics/routing-blocked/fix
//	Body: {"provider_id": X}
func (h *Handler) handleRoutingBlockedFix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		ProviderID int `json:"provider_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ProviderID <= 0 {
		writeError(w, http.StatusBadRequest, "provider_id required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 0. Enumerate the credentials the reset below will hit — BEFORE the
	// UPDATE clears the very markers the WHERE matches on. The in-process
	// recovery chain (step 5) runs for exactly this list, so it stays
	// same-source with the DB WHERE: no credential gets its DB gates cleared
	// while its in-memory breaker survives, and none is reset in-memory only.
	//
	// A race between this SELECT and the UPDATE is benign by construction:
	// a credential that trips between the two gets its DB gates cleared but
	// keeps its (fresh, truthful) in-memory breaker — it will recover through
	// the normal cooling path, which is the correct outcome for a live trip.
	targetRows, err := h.db.Query(ctx, `
		SELECT id FROM credentials
		WHERE provider_id = $1
		  AND lifecycle_status = 'active'
		  AND (
		      availability_state IN ('unavailable', 'degraded', 'rate_limited')
		      OR circuit_state IN ('open', 'half_open')
		      OR consecutive_failures > 0
		  )
	`, req.ProviderID)
	if err != nil {
		writeInternalErr(w, "credential enumeration failed", err)
		return
	}
	targetCredIDs := make([]int, 0, 8)
	for targetRows.Next() {
		var cid int
		if err := targetRows.Scan(&cid); err == nil {
			targetCredIDs = append(targetCredIDs, cid)
		} else {
			warnRowSkip("routingBlockedFix.targets", err)
		}
	}
	// A truncated enumeration would silently narrow the in-process recovery
	// below (step 5) while the DB reset still covers the full set — the exact
	// "HTTP 200 but half the state survives" gap this endpoint exists to fix.
	if err := targetRows.Err(); err != nil {
		targetRows.Close()
		writeInternalErr(w, "credential enumeration aborted", err)
		return
	}
	targetRows.Close()

	// 1. Reset all credential-level blocking for this provider.
	//    (availability_state, circuit_state, consecutive_failures)
	if _, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = 'ready',
		    availability_recover_at = NULL,
		    circuit_state = 'closed',
		    consecutive_failures = 0,
		    state_reason_code = NULL,
		    state_reason_detail = 'routing-blocked-fix: all credentials force-recovered',
		    state_updated_at = now()
		WHERE provider_id = $1
		  AND lifecycle_status = 'active'
		  AND (
		      availability_state IN ('unavailable', 'degraded', 'rate_limited')
		      OR circuit_state IN ('open', 'half_open')
		      OR consecutive_failures > 0
		  )
	`, req.ProviderID); err != nil {
		writeInternalErr(w, "credential reset failed", err)
		return
	}

	// 2. Reset all cmb bindings for this provider.
	if _, err := h.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available = true,
		    unavailable_reason = NULL,
		    unavailable_at = NULL,
		    unavailable_recover_at = NULL,
		    updated_at = now()
		FROM credentials c
		WHERE cmb.credential_id = c.id
		  AND c.provider_id = $1
		  AND (cmb.available IS NOT TRUE OR cmb.unavailable_reason IS NOT NULL)
	`, req.ProviderID); err != nil {
		writeInternalErr(w, "binding reset failed", err)
		return
	}

	// 3. Reset model_probe_state for all credentials under this provider.
	if _, err := h.db.Exec(ctx, `
		UPDATE model_probe_state mps
		SET state = 'healthy_confirmed',
		    consecutive_successes = 5,
		    consecutive_failures = 0,
		    next_retry_at = now() + interval '4 hours',
		    last_state_change_at = now()
		FROM credentials c
		WHERE mps.credential_id = c.id
		  AND c.provider_id = $1
		  AND mps.state IN ('broken_confirmed', 'recovering', 'unknown', 'suspicious', 'failing')
	`, req.ProviderID); err != nil {
		writeInternalErr(w, "probe state reset failed", err)
		return
	}

	// 3.5 R36 (2026-09-17 audit): dual-clear contract — v_routable gates on
	// node_probe_state, so a provider-wide repair must reset the new system
	// too or the binding stays blocked despite the legacy table showing
	// healthy. Same shape as force_enable (admin/routing.go).
	if _, err := h.db.Exec(ctx, `
		UPDATE node_probe_state nps SET
		    last_direct_ok    = TRUE,
		    last_gateway_ok   = TRUE,
		    last_err_code     = NULL,
		    last_err_detail   = NULL,
		    next_retry_at     = now(),
		    next_retry_seconds = 0,
		    consecutive_failures = 0,
		    paused            = FALSE,
		    in_flight_until   = NULL,
		    updated_at        = now()
		FROM credentials c
		WHERE nps.credential_id = c.id
		  AND c.provider_id = $1
		  AND nps.last_direct_ok = FALSE
	`, req.ProviderID); err != nil {
		writeInternalErr(w, "node probe state reset failed", err)
		return
	}

	// 4. Invalidate routing caches.
	provider.InvalidateAllCandidateCache()
	bgCtx, bgCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer bgCancel()
	if _, err := h.db.Exec(bgCtx, "SELECT pg_notify('auto_route_refresh', $1)", fmt.Sprintf("routing-blocked-fix:provider_id=%d", req.ProviderID)); err != nil {
		slog.Warn("pg_notify failed", "error", err)
	}

	// 5. In-process recovery chain — 待裁决 85 (R45 移交首位, now closed).
	// Mirrors handleForceRecoverSingle (admin/diagnostics_credential.go):
	// the WHERE above deliberately targets circuit_state IN ('open','half_open')
	// and consecutive_failures > 0 — precisely the credentials whose in-process
	// breakers are also open. Clearing only the DB side returns HTTP 200 while
	// the router keeps filtering these nodes until the cooling windows expire
	// (the failure mode the shared in-process recovery helper's doc comment
	// warns about, admin/routing.go:1962-1969). URSM v2 stays untouched —
	// parity with handleForceRecoverSingle, which also leaves that layer to
	// its owner. NOTE: keep symbol names out of this comment — the
	// healthstateguard parity gate anchors on raw text and a mention here
	// would satisfy it without the calls.
	inMemoryReset := 0
	for _, cid := range targetCredIDs {
		provider.InvalidateCredentialKeyCache(cid)
		provider.ResetKeyRotatorForCredential(cid)
		h.resetInMemoryNodeState(ctx, cid, req.ProviderID, "", true)
		inMemoryReset++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"triggered":                   true,
		"provider_id":                 req.ProviderID,
		"timestamp":                   time.Now().UTC().Format(time.RFC3339),
		"message":                     "provider force-recovered: credentials / bindings / probe_state reset",
		"in_memory_reset_credentials": inMemoryReset,
	})
}
