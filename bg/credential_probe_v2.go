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
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// CredentialProbeV2 runs every 1 hour to probe credentials with a real
// mini chat completion. Writes health_* + availability_state. Skips
// credentials with manual_disabled=true or lifecycle_status<>'active'
// (the UPDATE WHERE guards enforce this last-resort).
//
// Spec: docs/superpowers/specs/2026-06-12-credential-availability-audit-design.md §5
type CredentialProbeV2 struct {
	db       *pgxpool.Pool
	encKey   []byte
	keyring  *secret.Keyring
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewCredentialProbeV2(db *pgxpool.Pool, encKey []byte) *CredentialProbeV2 {
	return &CredentialProbeV2{
		db:       db,
		encKey:   encKey,
		interval: 1 * time.Hour,
		done:     make(chan struct{}),
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
		       COALESCE(c.default_probe_model, '')
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
			&s.BaseURL, &ciphertext, &s.DefaultProbeModel); err != nil {
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

	// Step 1: GET /v1/models
	modelsURLs := upstreamurl.ModelsURLCandidates(s.BaseURL)
	if len(modelsURLs) == 0 {
		return true, "" // manifest-only, skip
	}
	httpClient := &http.Client{Timeout: 15 * time.Second}
	step1OK := false
	for _, u := range modelsURLs {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			continue
		}
		if s.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+s.APIKey)
		}
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

	// Step 2: mini chat completion with "hi"
	chatURL := upstreamurl.ChatCompletionsURL(s.BaseURL)
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
	return false, fmt.Sprintf("chat status %d: %s", resp.StatusCode, truncateBody(body))
}

func truncateBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200]
	}
	return s
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

	c.db.Exec(execCtx, `
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
		quotaState, stateReason, credID)
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

