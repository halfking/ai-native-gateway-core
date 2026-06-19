package bg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/probeutil"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// CredentialProbeV2 runs every 1 hour to probe credentials with a real
// mini chat completion. Writes health_* + availability_state. Skips
// credentials with manual_disabled=true or lifecycle_status<>'active'
// (the UPDATE WHERE guards enforce this last-resort).
//
// P2 (2026-06-19): after writing auth_failed or unreachable, the credential
// is pushed onto fastReprobeQueue so a follow-up probe fires after
// fastReprobeDelay (default 5 min) instead of waiting for the next hourly
// cycle. This shrinks the detection window for transient failures.
//
// Spec: docs/superpowers/specs/2026-06-12-credential-availability-audit-design.md §5
type CredentialProbeV2 struct {
	db               *pgxpool.Pool
	encKey           []byte
	keyring          *secret.Keyring
	interval         time.Duration
	fastReprobeDelay time.Duration
	fastReprobeQueue chan int // credential IDs
	cancel           context.CancelFunc
	done             chan struct{}
}

func NewCredentialProbeV2(db *pgxpool.Pool, encKey []byte) *CredentialProbeV2 {
	return &CredentialProbeV2{
		db:               db,
		encKey:           encKey,
		interval:         1 * time.Hour,
		fastReprobeDelay: 5 * time.Minute,
		fastReprobeQueue: make(chan int, 64),
		done:             make(chan struct{}),
	}
}

func (c *CredentialProbeV2) SetKeyring(kr *secret.Keyring) {
	c.keyring = kr
}

func (c *CredentialProbeV2) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	go c.run(ctx)
	slog.Info("credential probe v2 started", "interval", c.interval)
}

func (c *CredentialProbeV2) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	<-c.done
}

func (c *CredentialProbeV2) run(ctx context.Context) {
	defer close(c.done)

	// Stagger from v1: v2 fires at minute 30 of each hour, v1 fires at minute 0.
	// On first start, wait until next :30 mark.
	if wait := time.Until(nextHalfHour()); wait > 0 && wait < c.interval {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	c.cycleAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.cycleAll(ctx)
		case credID := <-c.fastReprobeQueue:
			// P2: event-triggered fast reprobe after auth_failed/unreachable.
			go func(id int) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(c.fastReprobeDelay):
				}
				slog.Info("credential probe v2: fast reprobe triggered",
					"credential_id", id, "delay", c.fastReprobeDelay)
				c.probeOne(ctx, id)
			}(credID)
		}
	}
}

func nextHalfHour() time.Time {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 30, 0, 0, now.Location())
	if now.Minute() >= 30 {
		next = next.Add(1 * time.Hour)
	}
	return next
}

type v2Snapshot struct {
	ID                     int
	Status                 string
	LifecycleStatus        string
	ManualDisabled         bool
	QuotaState             string
	ProviderEnabled        bool
	ProviderManualDisabled bool
	BaseURL                string
	APIKey                 string
	DefaultProbeModel      string
	ProviderProtocol       string
	CatalogCode            string // P3: balance probe vendor routing
}

// cycleAll iterates active credentials, sends /v1/models + mini chat "hi",
// writes back health/availability state.
func (c *CredentialProbeV2) cycleAll(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	rows, err := c.db.Query(timeoutCtx, `
		SELECT c.id, c.status, c.lifecycle_status, COALESCE(c.manual_disabled, FALSE),
		       COALESCE(c.quota_state, 'ok'),
		       COALESCE(p.enabled, FALSE), COALESCE(p.manual_disabled, FALSE),
		       COALESCE(p.base_url, ''),
		       c.secret_ciphertext,
		       COALESCE(c.default_probe_model, ''),
		       COALESCE(p.protocol, 'openai-completions'),
		       COALESCE(p.catalog_code, '')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, FALSE) = TRUE
		  AND COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
		  AND c.availability_state <> 'suspended'
		  AND COALESCE(c.default_probe_model, '') <> ''
		ORDER BY c.id
	`)
	if err != nil {
		slog.Warn("credential probe v2: query failed", "error", err)
		return
	}
	defer rows.Close()

	checked := 0
	healthy := 0
	warn := 0
	failed := 0

	probeStart := time.Now()

	for rows.Next() {
		var s v2Snapshot
		var ciphertext []byte
		if err := rows.Scan(&s.ID, &s.Status, &s.LifecycleStatus, &s.ManualDisabled,
			&s.QuotaState, &s.ProviderEnabled, &s.ProviderManualDisabled,
			&s.BaseURL, &ciphertext, &s.DefaultProbeModel, &s.ProviderProtocol, &s.CatalogCode); err != nil {
			continue
		}

		// Decrypt API key (same pattern as credential_cycler.go)
		apiKey, decErr := decryptCiphertext(ciphertext, c.keyring, c.encKey)
		if decErr != nil {
			slog.Warn("credential probe v2: decrypt failed (will mark health=error; is_routable may go FALSE)",
				"credential_id", s.ID,
				"error", decErr,
			)
			c.writeHealth(timeoutCtx, s.ID, probeResult{
				HealthStatus: "error",
				HealthError:  "decrypt failed",
				HealthSource: "probe",
			})
			failed++
			continue
		}
		s.APIKey = apiKey

		// Verify the probe model is still routable
		ok, errMsg := c.probeCredential(timeoutCtx, s)
		checked++

		var pr probeResult
		if ok {
			pr = probeResult{
				HealthStatus:      "healthy",
				HealthLatencyMs:   int(time.Since(probeStart).Milliseconds()),
				HealthSource:      "probe",
				AvailabilityState: "ready",
			}
			healthy++
		} else {
			pr = classifyProbeFailure(errMsg)
			pr.HealthLatencyMs = int(time.Since(probeStart).Milliseconds())
			failed++
			if pr.HealthStatus == "warning" {
				warn++
			}
			if failed == 1 || failed%5 == 0 {
				slog.Warn("credential probe v2: probe failed (will write health/availability state)",
					"credential_id", s.ID,
					"health_status", pr.HealthStatus,
					"availability_state", pr.AvailabilityState,
					"error", errMsg,
					"failed_so_far", failed,
				)
			}
		}
		pr.HealthProbeModel = s.DefaultProbeModel
		c.writeHealth(timeoutCtx, s.ID, pr)

		// P3: balance probe for supported vendors (only when healthy).
		if pr.AvailabilityState == "ready" {
			if balUSD, ok := c.probeBalance(timeoutCtx, s); ok {
				if _, err := c.db.Exec(timeoutCtx,
					`UPDATE credentials SET balance_usd = $1 WHERE id = $2`,
					balUSD, s.ID,
				); err != nil {
					slog.Warn("credential probe v2: balance_usd write failed",
						"credential_id", s.ID, "error", err)
				} else {
					slog.Debug("credential probe v2: balance_usd updated",
						"credential_id", s.ID, "balance_usd", balUSD)
				}
			}
		}

		// P2: fast reprobe after auth_failed or unreachable.
		if pr.AvailabilityState == "auth_failed" || pr.AvailabilityState == "unreachable" {
			select {
			case c.fastReprobeQueue <- s.ID:
			default:
			}
		}
	}

	slog.Info("credential probe v2: cycle complete",
		"checked", checked,
		"healthy", healthy,
		"warning", warn,
		"failed", failed,
	)
}

type probeResult struct {
	HealthStatus          string
	HealthError           string
	HealthLatencyMs       int
	HealthProbeModel      string
	HealthSource          string
	AvailabilityState     string
	AvailabilityRecoverAt *time.Time
	QuotaState            string
	StateReasonCode       string
}

// probeCredential runs the 2-step probe (GET /v1/models + mini chat "hi").
func (c *CredentialProbeV2) probeCredential(ctx context.Context, s v2Snapshot) (bool, string) {
	if s.BaseURL == "" {
		return false, "empty base URL"
	}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	desc := providercap.Resolve(s.ProviderProtocol, "")

	// Step 1: GET /v1/models (skip for anthropic-messages — no /models endpoint)
	if desc.SupportsModelsEndpoint {
		modelsURLs := providercap.ModelsURLCandidates(s.BaseURL, nil, desc)
		if len(modelsURLs) == 0 {
			return true, "" // manifest-only, skip
		}
		step1OK := false
		for _, u := range modelsURLs {
			req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
			if err != nil {
				continue
			}
			providercap.ApplyAuthHeaders(req, desc, s.APIKey)
			resp, err := httpClient.Do(req)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			resp.Body.Close()
			if resp.StatusCode == 200 {
				step1OK = true
				break
			}
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return false, fmt.Sprintf("401/403: %s", truncateBody(body))
			}
		}
		if !step1OK {
			return false, "models endpoint unreachable"
		}
	}

	// Step 2: mini chat completion with "hi" — protocol-aware.
	// For anthropic-messages providers (e.g. minimax /anthropic) the openai
	// chat URL 404s, so we hit /v1/messages instead with x-api-key.
	if desc.ChatProbeEndpoint == upstreamurl.EpMessages {
		msgURL := providercap.ProbeEndpointURL(s.BaseURL, desc)
		ok, errMsg := c.miniAnthropic(ctx, httpClient, s.APIKey, s.DefaultProbeModel, msgURL, 20)
		return ok, errMsg
	}
	chatURL := providercap.ProbeEndpointURL(s.BaseURL, desc)
	ok, errMsg := c.miniChat(ctx, httpClient, s.APIKey, s.DefaultProbeModel, chatURL, 1)
	if !ok && strings.Contains(errMsg, "max_tokens") {
		// Some legacy gateways reject max_tokens < 2; retry with 2
		ok, errMsg = c.miniChat(ctx, httpClient, s.APIKey, s.DefaultProbeModel, chatURL, 2)
	}
	return ok, errMsg
}

func (c *CredentialProbeV2) miniChat(ctx context.Context, httpClient *http.Client, apiKey, model, chatURL string, maxTokens int) (bool, string) {
	chatBody := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": maxTokens,
	}
	bodyJSON, _ := json.Marshal(chatBody)
	req, err := http.NewRequestWithContext(ctx, "POST", chatURL, bytes.NewReader(bodyJSON))
	if err != nil {
		return false, "build chat request: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, "chat unreachable: " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	bodyStr := string(body)
	if resp.StatusCode == 200 {
		return true, ""
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return false, fmt.Sprintf("401/403: %s", truncateBody(body))
	}
	if resp.StatusCode == 429 {
		return false, "429 rate limited"
	}
	if resp.StatusCode == 402 {
		return false, "402 payment required"
	}
	if resp.StatusCode == 404 && probeutil.IsEndpointIDRequiredError(bodyStr) {
		// Volcano Ark and similar providers: this model needs an endpoint ID
		// (outbound_model_name) to be callable.  Use the canonical error code
		// so classifyProbeFailure can map it to a non-fatal state instead of
		// marking the credential unreachable and blocking all routing.
		return false, fmt.Sprintf("%s: %s", probeutil.EndpointIDRequiredErrCode, truncateBody(body))
	}
	return false, fmt.Sprintf("chat status %d: %s", resp.StatusCode, truncateBody(body))
}

func truncateBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// miniAnthropic sends a single-shot /v1/messages request with x-api-key +
// anthropic-version headers. Used as the probe step 2 for anthropic-messages
// providers (e.g. minimax /anthropic) which don't expose /v1/chat/completions.
//
// Returns (true, "") on 2xx; otherwise the upstream status + body preview.
func (c *CredentialProbeV2) miniAnthropic(ctx context.Context, httpClient *http.Client, apiKey, model, msgURL string, maxTokens int) (bool, string) {
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	}
	bodyJSON, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", msgURL, bytes.NewReader(bodyJSON))
	if err != nil {
		return false, "build messages request: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, "messages unreachable: " + err.Error()
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, ""
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return false, fmt.Sprintf("401/403: %s", truncateBody(respBody))
	}
	if resp.StatusCode == 429 {
		return false, "429 rate limited"
	}
	return false, fmt.Sprintf("messages status %d: %s", resp.StatusCode, truncateBody(respBody))
}

func classifyProbeFailure(errMsg string) probeResult {
	pr := probeResult{HealthSource: "probe"}
	switch {
	case strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403"):
		pr.HealthStatus = "auth_failed"
		pr.AvailabilityState = "auth_failed"
		pr.HealthError = errMsg
		pr.StateReasonCode = "auth_error"
	case strings.Contains(errMsg, "429"):
		pr.HealthStatus = "warning"
		pr.AvailabilityState = "rate_limited"
		recover := time.Now().Add(60 * time.Second)
		pr.AvailabilityRecoverAt = &recover
		pr.HealthError = errMsg
		pr.StateReasonCode = "rate_limited"
	case strings.Contains(errMsg, "402") || strings.Contains(errMsg, "balance"):
		pr.HealthStatus = "warning"
		pr.AvailabilityState = "ready"
		pr.QuotaState = "periodic_exhausted"
		pr.HealthError = errMsg
		pr.StateReasonCode = "balance_low"
	case strings.Contains(errMsg, probeutil.EndpointIDRequiredErrCode):
		// 404 InvalidEndpointOrModel — the credential itself is reachable and
		// authenticated, but the chosen default_probe_model needs an endpoint
		// ID (outbound_model_name) to be callable.  We mark health=warning so
		// admins see something is wrong, but availability_state stays "ready"
		// so routing for the OTHER models on this credential is NOT blocked.
		// admin will see state_reason_code="endpoint_id_required" and can
		// either set outbound_model_name on the binding or pick a different
		// default_probe_model.
		pr.HealthStatus = "warning"
		pr.AvailabilityState = "ready"
		pr.HealthError = errMsg
		pr.StateReasonCode = probeutil.EndpointIDRequiredErrCode
	default:
		pr.HealthStatus = "unreachable"
		pr.AvailabilityState = "unreachable"
		recover := time.Now().Add(5 * time.Minute)
		pr.AvailabilityRecoverAt = &recover
		pr.HealthError = errMsg
		pr.StateReasonCode = "network_error"
	}
	return pr
}

// writeHealth persists probe results with manual-disable guard.
// Even if the caller somehow passes a manual_disabled credential, the
// WHERE clause refuses to write.
func (c *CredentialProbeV2) writeHealth(ctx context.Context, credID int, pr probeResult) {
	execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var recoverAt *time.Time
	if pr.AvailabilityRecoverAt != nil {
		recoverAt = pr.AvailabilityRecoverAt
	}
	var quotaState *string
	if pr.QuotaState != "" {
		quotaState = &pr.QuotaState
	}
	var stateReason *string
	if pr.StateReasonCode != "" {
		stateReason = &pr.StateReasonCode
	}

	if _, err := c.db.Exec(execCtx, `
		UPDATE credentials
		SET health_status = $1,
		    health_error = $2,
		    health_checked_at = NOW(),
		    health_latency_ms = $3,
		    health_probe_model = $4,
		    health_source = $5,
		    availability_state = $6,
		    availability_recover_at = $7,
		    quota_state = COALESCE($8, quota_state),
		    state_reason_code = $9,
		    state_updated_at = NOW()
		WHERE id = $10
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND quota_state NOT IN ('permanently_exhausted', 'balance_exhausted')
	`, pr.HealthStatus, pr.HealthError, pr.HealthLatencyMs, pr.HealthProbeModel,
		pr.HealthSource, pr.AvailabilityState, recoverAt,
		quotaState, stateReason, credID); err != nil {
		slog.Warn("credential probe v2: writeHealth failed",
			"credential_id", credID, "health_status", pr.HealthStatus, "error", err)
	}
}

// probeOne re-probes a single credential. Used by fast-reprobe (P2).
func (c *CredentialProbeV2) probeOne(ctx context.Context, credID int) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var s v2Snapshot
	var ciphertext []byte
	err := c.db.QueryRow(timeoutCtx, `
		SELECT c.id, c.status, c.lifecycle_status, COALESCE(c.manual_disabled, FALSE),
		       COALESCE(c.quota_state, 'ok'),
		       COALESCE(p.enabled, FALSE), COALESCE(p.manual_disabled, FALSE),
		       COALESCE(p.base_url, ''),
		       c.secret_ciphertext,
		       COALESCE(c.default_probe_model, ''),
		       COALESCE(p.protocol, 'openai-completions'),
		       COALESCE(p.catalog_code, '')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(c.default_probe_model, '') <> ''
	`, credID).Scan(
		&s.ID, &s.Status, &s.LifecycleStatus, &s.ManualDisabled,
		&s.QuotaState, &s.ProviderEnabled, &s.ProviderManualDisabled,
		&s.BaseURL, &ciphertext, &s.DefaultProbeModel, &s.ProviderProtocol, &s.CatalogCode,
	)
	if err != nil {
		slog.Debug("credential probe v2: fast reprobe skipped",
			"credential_id", credID, "error", err)
		return
	}
	apiKey, decErr := decryptCiphertext(ciphertext, c.keyring, c.encKey)
	if decErr != nil {
		slog.Warn("credential probe v2: fast reprobe decrypt failed",
			"credential_id", credID, "error", decErr)
		return
	}
	s.APIKey = apiKey
	probeStart := time.Now()
	ok, errMsg := c.probeCredential(timeoutCtx, s)
	var pr probeResult
	if ok {
		pr = probeResult{
			HealthStatus:      "healthy",
			HealthLatencyMs:   int(time.Since(probeStart).Milliseconds()),
			HealthSource:      "fast_reprobe",
			AvailabilityState: "ready",
		}
		slog.Info("credential probe v2: fast reprobe recovered", "credential_id", credID)
	} else {
		pr = classifyProbeFailure(errMsg)
		pr.HealthLatencyMs = int(time.Since(probeStart).Milliseconds())
		pr.HealthSource = "fast_reprobe"
		slog.Info("credential probe v2: fast reprobe still failing",
			"credential_id", credID, "availability_state", pr.AvailabilityState)
	}
	pr.HealthProbeModel = s.DefaultProbeModel
	c.writeHealth(timeoutCtx, credID, pr)
}

// probeBalance fetches the account balance for supported vendors (P3).
// Returns (balanceUSD, true) on success, (0, false) otherwise.
func (c *CredentialProbeV2) probeBalance(ctx context.Context, s v2Snapshot) (float64, bool) {
	desc := providercap.Resolve(s.ProviderProtocol, s.CatalogCode)
	balURL := providercap.BalanceURL(s.BaseURL, desc)
	if balURL == "" {
		return 0, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, balURL, nil)
	if err != nil {
		return 0, false
	}
	providercap.ApplyAuthHeaders(req, desc, s.APIKey)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("credential probe v2: balance probe failed",
			"credential_id", s.ID, "url", balURL, "error", err)
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return 0, false
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, false
	}
	parts := strings.Split(desc.BalanceJSONPath, ".")
	cur := parsed
	for _, p := range parts {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[p]
		case []any:
			idx := 0
			fmt.Sscanf(p, "%d", &idx)
			if idx >= len(v) {
				return 0, false
			}
			cur = v[idx]
		default:
			return 0, false
		}
		if cur == nil {
			return 0, false
		}
	}
	switch v := cur.(type) {
	case float64:
		return v, true
	case string:
		var f float64
		if _, err2 := fmt.Sscanf(v, "%f", &f); err2 == nil {
			return f, true
		}
	}
	return 0, false
}

// decryptCiphertext attempts to decrypt with keyring first, then fallback to
// Fernet (32-byte AES).
func decryptCiphertext(ciphertext []byte, kr *secret.Keyring, encKey []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	if kr != nil {
		pt, _, err := secret.DecryptAny(string(ciphertext), kr, encKey)
		if err == nil {
			return string(pt), nil
		}
	}
	if len(encKey) == 32 {
		pt, err := secret.DecryptFernet(ciphertext, encKey)
		if err == nil {
			return pt, nil
		}
	}
	return "", fmt.Errorf("no decryption key available (keyring=%v, fernet=%d bytes)", kr != nil, len(encKey))
}
