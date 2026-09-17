package admin

// provider_credential_balance.go — on-demand vendor balance refresh for the
// /providers credential drawer (migration 721, 2026-09-18).
//
//	POST /api/providers/{pid}/credentials/{cid}/refresh-balance
//
// Purpose: the balance_floor_guard only refreshes balance_usd for credentials
// that have a floor configured, and probe_v2's cycleAll refreshes at most
// hourly. After a recharge (or a manual correction the operator wants to
// verify) the drawer's "刷新余额" button probes the vendor balance endpoint
// immediately instead of waiting for the next sweep.
//
// Semantics:
//   - GET-only against the vendor balance API (providercap.FetchBalanceUSD) —
//     never consumes tokens.
//   - Success writes balance_usd + balance_source='api' +
//     balance_last_checked_at=now() + balance_error=NULL.
//   - Failure writes a truncated balance_error and keeps the previous
//     balance_usd (fail-open, same as the floor guard) — the endpoint still
//     returns 200 with success=false so the UI can render the error inline.
//   - Vendors without a balance endpoint (providercap.BalanceURL == "") return
//     400 — that is a configuration fact, not a transient probe failure, and
//     writing it into balance_error would mislabel e.g. zhipu plan vendors
//     whose quota sensing lives in plan_quota_* instead.
//
// Contract with migration 721's manual-protection window: unlike the
// background writers, an explicit operator click OVERWRITES balance_source
// even on a manual row — the operator asked for fresh data, so the freshest
// API reading wins and the manual stamp is intentionally replaced.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
)

const balanceProbeTimeout = 10 * time.Second

func (h *Handler) refreshCredentialBalance(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), balanceProbeTimeout+2*time.Second)
	defer cancel()

	var (
		ciphertext []byte
		baseURL    string
		protocol   string
		catalog    string
	)
	err := h.db.QueryRow(ctx, `
		SELECT c.secret_ciphertext,
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       COALESCE(p.catalog_code, '')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1 AND c.provider_id = $2
		  AND c.status <> 'deleted'
	`, credID, providerID).Scan(&ciphertext, &baseURL, &protocol, &catalog)
	if err != nil {
		// R42: only a missing row is 404 — pool exhaustion / timeout / other
		// infrastructure failures are 500 so they are not misread as "the
		// credential does not exist" by the drawer.
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "credential not found")
			return
		}
		slog.Warn("refresh-balance: credential lookup failed",
			"provider_id", providerID, "credential_id", credID, "error", err)
		writeError(w, http.StatusInternalServerError, "credential lookup failed")
		return
	}

	desc := providercap.Resolve(protocol, catalog)
	balURL := providercap.BalanceURL(baseURL, desc)
	if balURL == "" {
		writeError(w, http.StatusBadRequest,
			"vendor does not expose a balance API (catalog="+catalog+"); plan vendors report via plan_quota_* instead")
		return
	}
	if blocked, reason := providercap.EgressBlocked(balURL); blocked {
		providercap.WarnBlocked("admin.refresh-balance", balURL, reason)
		writeError(w, http.StatusBadRequest, "egress blocked: "+reason)
		return
	}

	apiKey, err := h.decryptCredStr(string(ciphertext))
	if err != nil || strings.TrimSpace(apiKey) == "" {
		slog.Warn("refresh-balance: decrypt failed",
			"provider_id", providerID, "credential_id", credID, "error", err)
		writeError(w, http.StatusInternalServerError, "credential decrypt failed")
		return
	}

	balUSD, ok := providercap.FetchBalanceUSD(ctx, nil, balURL, apiKey, desc)
	if !ok {
		// R42: leave a slog trace — the DB stamp alone meant a persistently
		// failing vendor endpoint produced zero server-side evidence.
		slog.Warn("refresh-balance: vendor balance probe failed",
			"provider_id", providerID, "credential_id", credID, "catalog", catalog)
		errMsg := "balance probe failed (network/HTTP/parse) at " + time.Now().UTC().Format(time.RFC3339)
		if _, uerr := h.db.Exec(ctx, `
			UPDATE credentials
			SET balance_error = $1,
			    balance_last_checked_at = NOW()
			WHERE id = $2
		`, truncateBalanceError(errMsg), credID); uerr != nil {
			slog.Warn("refresh-balance: error stamp write failed",
				"credential_id", credID, "error", uerr)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"error":   errMsg,
		})
		return
	}

	if _, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET balance_usd = $1,
		    balance_source = 'api',
		    balance_last_checked_at = NOW(),
		    balance_error = NULL
		WHERE id = $2
	`, balUSD, credID); err != nil {
		slog.Warn("refresh-balance: balance write failed",
			"credential_id", credID, "error", err)
		writeError(w, http.StatusInternalServerError, "balance write failed")
		return
	}

	now := time.Now().UTC()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":            true,
		"balance_usd":        balUSD,
		"balance_currency":   "USD",
		"balance_source":     "api",
		"balance_checked_at": now.Format(time.RFC3339),
	})
}

// truncateBalanceError caps the stored probe error at the column-comment
// budget (≤500 chars) so a chatty vendor error page cannot bloat the row.
func truncateBalanceError(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	// Cut on a rune boundary so CJK vendor errors stay valid UTF-8 for PG.
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
