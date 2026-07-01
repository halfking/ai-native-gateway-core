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
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
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
	cache            *ModelAvailabilityCache
	interval         time.Duration
	fastReprobeDelay time.Duration
	fastReprobeQueue chan int // credential IDs
	cancel           context.CancelFunc
	done             chan struct{}

	// 新增：状态管理器引用
	stateManager credentialstate.StateObserver
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

func (c *CredentialProbeV2) SetAvailabilityCache(cache *ModelAvailabilityCache) {
	c.cache = cache
}

// SetStateManager 设置状态管理器（新增）
func (c *CredentialProbeV2) SetStateManager(sm credentialstate.StateObserver) {
	c.stateManager = sm
}

// SubmitFastProbe 提交到快速探测队列（新增公共方法）
func (c *CredentialProbeV2) SubmitFastProbe(credID int) {
	select {
	case c.fastReprobeQueue <- credID:
		slog.Debug("credential probe v2: fast probe submitted", "credential_id", credID)
	default:
		slog.Warn("credential probe v2: fast probe queue full", "credential_id", credID)
	}
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
//
// 2026-06-29 audit: a single failed probe must not declare a credential
// dead. Each step is wrapped in a short-jitter retry loop (0s/2s/5s, see
// internal/probeutil.ProbeRetryDelays) that only retries on transient
// conditions (network errors, 5xx, 408/425/429). 401/403/404/400/422/402
// fail fast because retrying them is pointless and risks masking real
// configuration errors.
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
		// Outer loop: short-jitter retries.
		// Inner loop: walk the candidate URLs (e.g. base/v1/models, base/models).
		// Per attempt we accept the first URL that returns 200. A non-retryable
		// status (401/403/404/400/422) on any URL short-circuits to a final
		// failure; all-URLs-5xx-or-network triggers the next retry.
		var lastErr string
		for _, delay := range probeutil.ProbeRetryDelays {
			if !probeutil.SleepWithCtx(ctx, delay) {
				return false, "ctx canceled during step1 retry: " + ctx.Err().Error()
			}
			step1OK := false
			nonRetryableHit := ""
			for _, u := range modelsURLs {
				req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
				if err != nil {
					continue
				}
				providercap.ApplyAuthHeaders(req, desc, s.APIKey)
				resp, err := httpClient.Do(req)
				if err != nil {
					// Network error — record and try next URL within this attempt.
					lastErr = fmt.Sprintf("models endpoint network error: %s", err.Error())
					continue
				}
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
				//nolint:errcheck // best-effort close
				resp.Body.Close()
				if resp.StatusCode == 200 {
					step1OK = true
					break
				}
				if !probeutil.IsProbeRetryableStatus(resp.StatusCode) {
					nonRetryableHit = fmt.Sprintf("status %d: %s", resp.StatusCode, truncateBody(body))
					break
				}
				lastErr = fmt.Sprintf("status %d: %s", resp.StatusCode, truncateBody(body))
			}
			if step1OK {
				lastErr = ""
				break
			}
			if nonRetryableHit != "" {
				// nonRetryableHit already carries "status <code>: <body>",
				// so no need to re-list the status codes in the prefix.
				return false, nonRetryableHit
			}
		}
		if lastErr != "" {
			return false, fmt.Sprintf("models endpoint unreachable (after %d attempts): %s",
				len(probeutil.ProbeRetryDelays), lastErr)
		}
	}

	// Step 2: mini chat completion with "hi" — protocol-aware.
	// For anthropic-messages providers (e.g. minimax /anthropic) the openai
	// chat URL 404s, so we hit /v1/messages instead with x-api-key.
	runChat := func() (bool, string) {
		if desc.ChatProbeEndpoint == upstreamurl.EpMessages {
			msgURL := providercap.ProbeEndpointURL(s.BaseURL, desc)
			return c.miniAnthropic(ctx, httpClient, s.APIKey, s.DefaultProbeModel, msgURL, 20)
		}
		chatURL := providercap.ProbeEndpointURL(s.BaseURL, desc)
		ok, errMsg := c.miniChat(ctx, httpClient, s.APIKey, s.DefaultProbeModel, chatURL, 1)
		if !ok && strings.Contains(errMsg, "max_tokens") {
			// Some legacy gateways reject max_tokens < 2; retry with 2.
			// This is a parameter-compatibility fallback, NOT a transient
			// retry — runs at most once per attempt, inside the attempt.
			ok, errMsg = c.miniChat(ctx, httpClient, s.APIKey, s.DefaultProbeModel, chatURL, 2)
		}
		return ok, errMsg
	}

	var (
		errMsg  string
		lastErr string
	)
	for _, delay := range probeutil.ProbeRetryDelays {
		if !probeutil.SleepWithCtx(ctx, delay) {
			return false, "ctx canceled during step2 retry: " + ctx.Err().Error()
		}
		ok, e := runChat()
		if ok {
			return true, ""
		}
		errMsg = e
		lastErr = e
		// Network / 5xx / 408 / 425 / 429 → retry next loop.
		// Anything else (400, 401, 402, 403, 404, 422) → fail fast.
		if !shouldRetryChatErrMsg(errMsg) {
			return false, errMsg
		}
	}
	return false, fmt.Sprintf("chat unreachable (after %d attempts): %s",
		len(probeutil.ProbeRetryDelays), lastErr)
}

// shouldRetryChatErrMsg inspects the errMsg produced by miniChat / miniAnthropic
// and returns true when the underlying failure is transient (network error,
// 5xx, 408, 425, 429). Clear business failures (400/401/402/403/404/422)
// return false so the loop fails fast.
//
// miniChat / miniAnthropic do not return a structured status, so we sniff
// the errMsg. Recognised shapes:
//
//	"chat status 503: ..."  → status 503 → retryable
//	"401/403: ..."         → not retryable
//	"chat unreachable: ..." → network error → retryable
//	"messages status 429: ..." → retryable
//	"endpoint_id_required: ..." → not retryable (config issue)
func shouldRetryChatErrMsg(errMsg string) bool {
	if errMsg == "" {
		// Empty error after a failed chat is unusual; treat as retryable
		// rather than silently declaring success.
		return true
	}
	if strings.HasPrefix(errMsg, "401/403:") ||
		strings.HasPrefix(errMsg, "402 ") ||
		strings.HasPrefix(errMsg, "402:") ||
		strings.Contains(errMsg, "endpoint_id_required") {
		return false
	}
	// Look for "status NNN:" and run through the classifier.
	if code, ok := extractStatusCode(errMsg); ok {
		return probeutil.IsProbeRetryableStatus(code)
	}
	// No status code visible — assume network/transport level error
	// (chat unreachable, build request, etc.). Always retryable so long
	// as the context is still alive (the loop checks ctx itself).
	return true
}

// extractStatusCode pulls the first "<word> status NNN:" or "status NNN:"
// pattern from a probe errMsg. Returns (code, true) on success.
//
// Recognised shapes produced by miniChat / miniAnthropic:
//
//	"chat status 503: ..."      → 503
//	"messages status 429: ..."  → 429
//	"status 500: ..."           → 500  (defensive; not currently emitted)
func extractStatusCode(s string) (int, bool) {
	const statusWord = "status "
	for _, prefix := range []string{"chat ", "messages ", statusWord} {
		idx := strings.Index(s, prefix)
		if idx < 0 {
			continue
		}
		rest := s[idx+len(prefix):]
		// Parse 3+ digits.
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end < 3 {
			continue
		}
		var code int
		for i := 0; i < end; i++ {
			code = code*10 + int(rest[i]-'0')
		}
		return code, true
	}
	return 0, false
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
	//nolint:errcheck // best-effort close
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
		// marking the credential unreachable and blocking all streaming.
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
	//nolint:errcheck // best-effort close
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
		return
	}
	if c.cache != nil && c.cache.Enabled() && pr.HealthProbeModel != "" {
		available := pr.AvailabilityState == "ready"
		state := pr.HealthStatus
		if state == "healthy" {
			state = "healthy_confirmed"
		}
		if !available && pr.AvailabilityState != "" {
			state = pr.AvailabilityState
		}
		if err := c.cache.Set(execCtx, credID, pr.HealthProbeModel, modelAvailabilityFields(
			credID,
			pr.HealthProbeModel,
			state,
			available,
			pr.HealthStatus,
			0,
			0,
			recoverAt,
			pr.HealthSource,
		)); err != nil {
			slog.Warn("credential probe v2: cache write failed",
				"credential_id", credID,
				"probe_model", pr.HealthProbeModel,
				"error", err)
		}
	}

	// 新增：同步到状态管理器
	if c.stateManager != nil && pr.HealthProbeModel != "" {
		now := time.Now()
		state := &credentialstate.State{
			CredentialID:  credID,
			Model:         pr.HealthProbeModel,
			Available:     pr.AvailabilityState == "ready",
			HealthStatus:  pr.HealthStatus,
			AvgLatencyMs:  pr.HealthLatencyMs,
			LastUpdatedAt: now,
			LastError:     pr.HealthError,
			RecoverAt:     recoverAt,
			Source:        "probe_v2",
		}
		c.stateManager.UpdateFromProbe(execCtx, state)
	}
}

// ProbeNow is the unified external entry point for "fire a credential
// probe right now, on demand". It is the API surface used by the bg
// fast-reprobe path (after auth_failed/unreachable) AND reserved for
// future admin manual-trigger endpoints. Auto + manual now share one
// code path so the retry policy (2s/5s backoff) and result handling
// stay in lock-step — admin never has to know whether to retry.
//
// The probe uses probeCredential internally, which already wraps step1
// (GET /v1/models) and step2 (mini chat "hi") in the shared
// probeutil.ProbeRetryDelays loop, so transient upstream issues are
// absorbed before the credential is marked degraded.
//
// ctx should carry an outer deadline; ProbeNow itself enforces a 30s
// upper bound so a slow probe cannot trap the caller. The result is
// persisted via writeHealth (same columns the scheduled cycleAll writes
// to: health_status, availability_state, availability_recover_at, ...).
//
// The probeSource field in the resulting health row identifies the
// caller so the admin UI can distinguish "scheduled hourly probe" from
// "fast reprobe after auth_failed" from "admin manual trigger" without
// cross-referencing logs.
func (c *CredentialProbeV2) ProbeNow(ctx context.Context, credID int) {
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
		slog.Debug("credential probe v2: ProbeNow skipped (credential not active or missing)",
			"credential_id", credID, "error", err)
		return
	}
	apiKey, decErr := decryptCiphertext(ciphertext, c.keyring, c.encKey)
	if decErr != nil {
		slog.Warn("credential probe v2: ProbeNow decrypt failed",
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
			HealthSource:      "probe_now",
			AvailabilityState: "ready",
		}
		slog.Info("credential probe v2: ProbeNow ok",
			"credential_id", credID,
			"latency_ms", pr.HealthLatencyMs)
	} else {
		pr = classifyProbeFailure(errMsg)
		pr.HealthLatencyMs = int(time.Since(probeStart).Milliseconds())
		pr.HealthSource = "probe_now"
		slog.Info("credential probe v2: ProbeNow failed",
			"credential_id", credID,
			"availability_state", pr.AvailabilityState,
			"final_error", errMsg)
	}
	pr.HealthProbeModel = s.DefaultProbeModel
	c.writeHealth(timeoutCtx, credID, pr)
}

// probeOne is kept as a private alias for the internal fast-reprobe path
// so the existing call site (line 109) compiles unchanged. Prefer
// ProbeNow for any new caller — it is the public, retry-aware entry
// point. probeOne now shares the exact same code path with admin's
// future manual triggers: one unified ProbeNow.
func (c *CredentialProbeV2) probeOne(ctx context.Context, credID int) {
	c.ProbeNow(ctx, credID)
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
	//nolint:errcheck // best-effort close
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
			//nolint:errcheck // best-effort parse, non-critical
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
