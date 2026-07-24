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
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("diagnostic failed: %v", err))
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
	`, providerID)
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

	// Fetch credential-level state for each credential with bindings.
	credIDs := make([]int, 0, len(credBindingKeys))
	for cid := range credBindingKeys {
		credIDs = append(credIDs, cid)
	}
	sort.Ints(credIDs)

	credStateMap := make(map[int]struct {
		status, availabilityState, healthStatus, lifecycleStatus string
		manualDisabled                                           bool
	})
	if len(credIDs) > 0 {
		placeholders := make([]string, 0, len(credIDs))
		args := make([]any, 0, len(credIDs))
		for _, cid := range credIDs {
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)+1))
			args = append(args, cid)
		}
		credRows, err := db.Query(ctx, fmt.Sprintf(`
			SELECT id, status, availability_state, health_status,
			       COALESCE(manual_disabled, false), lifecycle_status
			FROM credentials WHERE id IN (%s)
		`, strings.Join(placeholders, ",")), args...)
		if err == nil {
			defer credRows.Close()
			for credRows.Next() {
				var cid int
				var st struct {
					status, availabilityState, healthStatus, lifecycleStatus string
					manualDisabled                                           bool
				}
				if err := credRows.Scan(&cid, &st.status, &st.availabilityState, &st.healthStatus,
					&st.manualDisabled, &st.lifecycleStatus); err == nil {
					credStateMap[cid] = st
				}
			}
		}
	}

	// Build per-credential response.
	credentials := make([]routingBlockedCredential, 0, len(credIDs))
	for _, cid := range credIDs {
		keys := credBindingKeys[cid]
		cs := credStateMap[cid]
		cred := routingBlockedCredential{
			CredentialID:    cid,
			Status:          cs.status,
			LifecycleStatus: cs.lifecycleStatus,
			ManualDisabled:  cs.manualDisabled,
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

		// Fill from credential-level query.
		if first, ok := bindingMap[keys[0]]; ok {
			cred.CredentialLabel = first.CredentialLabel
		}
		if cs2, ok := credStateMap[cid]; ok {
			cred.CredentialLabel = "" // we already have it from binding
			cred.Status = cs2.status
			cred.AvailabilityState = cs2.availabilityState
			cred.HealthStatus = cs2.healthStatus
			cred.ManualDisabled = cs2.manualDisabled
			cred.LifecycleStatus = cs2.lifecycleStatus
		}

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
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("credential reset failed: %v", err))
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
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("binding reset failed: %v", err))
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
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("probe state reset failed: %v", err))
		return
	}

	// 4. Invalidate routing caches.
	provider.InvalidateAllCandidateCache()
	bgCtx, bgCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer bgCancel()
	if _, err := h.db.Exec(bgCtx, "SELECT pg_notify('auto_route_refresh', $1)", fmt.Sprintf("routing-blocked-fix:provider_id=%d", req.ProviderID)); err != nil {
		slog.Warn("pg_notify failed", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"triggered":   true,
		"provider_id": req.ProviderID,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"message":     "provider force-recovered: credentials / bindings / probe_state reset",
	})
}
