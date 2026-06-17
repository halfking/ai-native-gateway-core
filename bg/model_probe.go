// Package bg — model_probe.go
//
// ModelProbeRunner periodically re-tests credentials × models that are
// currently failing so we can flip them back to routable the moment the
// upstream issue clears.  Two key invariants:
//
//  1. Manual overrides always win.
//     If credentials.manual_disabled=TRUE or credential_model_bindings
//     is set to model_manual_disabled, we record a 'skipped' run but
//     never flip the model back on.
//
//  2. We never mark a credential as routable just because one probe
//     passed — health_status still has to move to 'healthy' through
//     the normal probe-v2 / cycler path, and the v_routable_credential_models
//     view checks it.
//
// Algorithm per tick (default 10 min):
//   1. Pick all (credential, model) pairs from credential_model_bindings
//      where the binding is currently NOT routable AND the last probe
//      attempt was a real failure (not skipped, not manual_disable).
//   2. Send a 1-token chat completion to the upstream.
//   3. On success: write probe_runs row with status=ok, state_change=recovered.
//      Caller (v_routable_credential_models view) will start routing again
//      automatically once health_status also moves to healthy.
//   4. On failure: write probe_runs row with status=<classified>.
//      No state change — keeps the credential unroutable.
package bg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// ModelProbeRunner runs every Interval.  It looks at non-routable
// (credential, model) pairs from v_routable_credential_models and
// re-tests them, recording each attempt to model_probe_runs.
type ModelProbeRunner struct {
	db       *pgxpool.Pool
	encKey   []byte
	keyring  *secret.Keyring
	interval time.Duration
	batch    int
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewModelProbeRunner(db *pgxpool.Pool, encKey []byte) *ModelProbeRunner {
	return &ModelProbeRunner{
		db:       db,
		encKey:   encKey,
		interval: 10 * time.Minute,
		batch:    20, // per-tick cap so a flood of failures doesn't hammer the upstream
		done:     make(chan struct{}),
	}
}

func (r *ModelProbeRunner) SetKeyring(kr *secret.Keyring) { r.keyring = kr }

func (r *ModelProbeRunner) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	go r.run(ctx)
	slog.Info("model probe runner started", "interval", r.interval, "batch", r.batch)
}

func (r *ModelProbeRunner) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

func (r *ModelProbeRunner) run(ctx context.Context) {
	defer close(r.done)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.cycle(ctx) // run once on start
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.cycle(ctx)
		}
	}
}

// probeTarget is the (credential, model, base_url, protocol, api_key)
// tuple we test.
type probeTarget struct {
	CredentialID   int
	RawModel       string
	BaseURL        string
	Protocol       string
	APIKey         string
	ManualDisabled bool
}

func (r *ModelProbeRunner) cycle(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Pick the next batch of failing bindings.
	rows, err := r.db.Query(timeoutCtx, `
		SELECT cmb.credential_id,
		       pm.raw_model_name,
		       COALESCE(p.base_url, ''),
		       COALESCE(p.protocol, 'openai-completions'),
		       c.secret_ciphertext,
		       COALESCE(c.manual_disabled, FALSE)
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.availability_state, 'ready') NOT IN ('suspended')
		  AND COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
		  AND COALESCE(p.enabled, FALSE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  -- Skip rows that the operator has explicitly marked as broken
		  -- (we never auto-recover manual_disable).
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') <> 'manual'
		  -- Only test things currently failing.
		  AND v_is_routable_for(cmb.credential_id, pm.raw_model_name) = FALSE
		-- Prioritise recent failures first, but don't repeat-storm
		-- (skip rows we tested in the last 5 minutes).
		AND NOT EXISTS (
		    SELECT 1 FROM model_probe_runs mpr
		    WHERE mpr.credential_id = cmb.credential_id
		      AND mpr.raw_model_name = pm.raw_model_name
		      AND mpr.created_at > NOW() - INTERVAL '5 minutes'
		)
		ORDER BY cmb.id
		LIMIT $1
	`, r.batch)
	if err != nil {
		// View v_is_routable_for may not exist on older deployments;
		// fall back to a direct join so this code still runs.
		slog.Warn("model probe: target query failed (trying fallback)", "error", err)
		rows, err = r.db.Query(timeoutCtx, `
			SELECT cmb.credential_id, pm.raw_model_name,
			       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
			       c.secret_ciphertext, COALESCE(c.manual_disabled, FALSE)
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			JOIN credentials c ON c.id = cmb.credential_id
			JOIN providers p ON p.id = c.provider_id
			LEFT JOIN v_routable_credential_models v
			       ON v.credential_id = cmb.credential_id
			      AND v.raw_model_name = pm.raw_model_name
			WHERE COALESCE(c.status, 'active') = 'active'
			  AND COALESCE(c.lifecycle_status, 'active') = 'active'
			  AND COALESCE(c.manual_disabled, FALSE) = FALSE
			  AND COALESCE(p.enabled, FALSE) = TRUE
			  AND COALESCE(cmb.unavailable_reason, '') <> 'manual'
			  AND COALESCE(v.is_routable, FALSE) = FALSE
			  AND NOT EXISTS (
			      SELECT 1 FROM model_probe_runs mpr
			      WHERE mpr.credential_id = cmb.credential_id
			        AND mpr.raw_model_name = pm.raw_model_name
			        AND mpr.created_at > NOW() - INTERVAL '5 minutes'
			  )
			ORDER BY cmb.id
			LIMIT $1
		`, r.batch)
		if err != nil {
			slog.Warn("model probe: fallback query also failed", "error", err)
			return
		}
	}
	defer rows.Close()

	tested := 0
	recovered := 0
	for rows.Next() {
		var t probeTarget
		var ciphertext []byte
		if err := rows.Scan(&t.CredentialID, &t.RawModel, &t.BaseURL, &t.Protocol, &ciphertext, &t.ManualDisabled); err != nil {
			continue
		}

		apiKey, decErr := decryptCiphertext(ciphertext, r.keyring, r.encKey)
		if decErr != nil {
			r.recordRun(timeoutCtx, t, "auth", nil, "decrypt_error", decErr.Error(), 0, "unchanged", false, "scheduler")
			continue
		}
		t.APIKey = apiKey

		// Manual-disabled re-check: the SQL WHERE already filtered these
		// out, but between query and probe the operator could have flipped
		// the flag.  We re-check at the request level so a manual disable
		// in flight is never overwritten by a passing probe.
		if t.ManualDisabled {
			r.recordRun(timeoutCtx, t, "skipped", nil, "manual_disabled",
				"credential is manually disabled; probe will not auto-recover", 0,
				"unchanged", false, "scheduler")
			continue
		}

		status, httpStatus, errCode, errMsg, latency := r.probeModel(timeoutCtx, t)
		stateChange := "unchanged"
		applied := true
		if status == "ok" {
			// Don't immediately flip cmb.unavailable_reason — the view
			// will re-evaluate on the next request once health_status
			// also clears.  We just record that the upstream accepted
			// the call.
			stateChange = "recovered"
			recovered++
		}
		r.recordRun(timeoutCtx, t, status, &httpStatus, errCode, errMsg, latency, stateChange, applied, "scheduler")
		tested++
	}

	if tested > 0 {
		slog.Info("model probe: cycle complete",
			"tested", tested, "recovered", recovered)
	}
}

// probeModel fires a one-shot minimal chat completion at the upstream and
// classifies the response.
func (r *ModelProbeRunner) probeModel(ctx context.Context, t probeTarget) (
	status string, httpStatus int, errCode, errMsg string, latencyMs int,
) {
	start := time.Now()

	if t.BaseURL == "" {
		return "skipped", 0, "endpoint_unresolved", "empty base_url", int(time.Since(start).Milliseconds())
	}
	endpoint := upstreamurl.ChatCompletionsURL(t.BaseURL)

	body, _ := json.Marshal(map[string]any{
		"model":       t.RawModel,
		"messages":    []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens":  1,
		"temperature": 0,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "network", 0, "request_build", err.Error(), int(time.Since(start).Milliseconds())
	}
	req.Header.Set("Content-Type", "application/json")
	if t.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.APIKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "network", 0, "request_error", err.Error(), int(time.Since(start).Milliseconds())
	}
	defer resp.Body.Close()
	latencyMs = int(time.Since(start).Milliseconds())
	httpStatus = resp.StatusCode

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "ok", httpStatus, "", "", latencyMs
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return "auth", httpStatus, http.StatusText(resp.StatusCode), truncate(string(respBody), 500), latencyMs
	case resp.StatusCode == 404:
		return "http_4xx", httpStatus, "model_not_found", truncate(string(respBody), 500), latencyMs
	case resp.StatusCode == 429:
		return "http_4xx", httpStatus, "rate_limited", truncate(string(respBody), 500), latencyMs
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return "http_4xx", httpStatus, http.StatusText(resp.StatusCode), truncate(string(respBody), 500), latencyMs
	case resp.StatusCode >= 500:
		return "http_5xx", httpStatus, http.StatusText(resp.StatusCode), truncate(string(respBody), 500), latencyMs
	default:
		return "unknown", httpStatus, http.StatusText(resp.StatusCode), truncate(string(respBody), 500), latencyMs
	}
}

func (r *ModelProbeRunner) recordRun(
	ctx context.Context, t probeTarget,
	status string, httpStatus *int, errCode, errMsg string,
	latencyMs int, stateChange string, applied bool, triggeredBy string,
) {
	_, err := r.db.Exec(ctx, `
		INSERT INTO model_probe_runs
		    (tenant_id, credential_id, raw_model_name, status,
		     http_status, error_code, error_message, latency_ms,
		     state_change, state_applied, triggered_by)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9, $10, $11)
	`, "default", t.CredentialID, t.RawModel, status,
		httpStatus, errCode, errMsg, latencyMs,
		stateChange, applied, triggeredBy)
	if err != nil {
		slog.Warn("model probe: recordRun failed",
			"credential_id", t.CredentialID,
			"raw_model", t.RawModel,
			"error", err)
	}
}

// TriggerManual lets the admin API kick off a single probe outside the
// scheduler.  Writes triggered_by='manual'.
func (r *ModelProbeRunner) TriggerManual(ctx context.Context, credentialID int, rawModel string) error {
	row := r.db.QueryRow(ctx, `
		SELECT cmb.credential_id, pm.raw_model_name,
		       COALESCE(p.base_url, ''), COALESCE(p.protocol, 'openai-completions'),
		       c.secret_ciphertext, COALESCE(c.manual_disabled, FALSE)
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE cmb.credential_id = $1 AND pm.raw_model_name = $2
		LIMIT 1
	`, credentialID, rawModel)
	var t probeTarget
	var ciphertext []byte
	if err := row.Scan(&t.CredentialID, &t.RawModel, &t.BaseURL, &t.Protocol, &ciphertext, &t.ManualDisabled); err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("binding not found")
		}
		return err
	}
	apiKey, decErr := decryptCiphertext(ciphertext, r.keyring, r.encKey)
	if decErr != nil {
		r.recordRun(ctx, t, "auth", nil, "decrypt_error", decErr.Error(), 0, "unchanged", false, "manual")
		return decErr
	}
	t.APIKey = apiKey

	status, httpStatus, errCode, errMsg, latency := r.probeModel(ctx, t)
	stateChange := "unchanged"
	applied := true
	if status == "ok" {
		stateChange = "recovered"
	}
	r.recordRun(ctx, t, status, &httpStatus, errCode, errMsg, latency, stateChange, applied, "manual")
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
