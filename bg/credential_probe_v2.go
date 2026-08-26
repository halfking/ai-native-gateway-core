package bg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/probeutil"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/provider"
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
	db                 *pgxpool.Pool
	encKey             []byte
	keyring            *secret.Keyring
	cache              *ModelAvailabilityCache
	interval           time.Duration
	fastReprobeDelay   time.Duration
	fastReprobeQueue   chan int // credential IDs
	fastReprobeMu      sync.Mutex
	fastReprobePending map[int]struct{}
	cancel             context.CancelFunc
	done               chan struct{}
	started            bool
	stopped            bool
	lifecycleMu        sync.Mutex
	probeWG            sync.WaitGroup

	// 新增：状态管理器引用
	stateManager credentialstate.StateObserver

	// onQuotaRecovered is the dispatcher-facing notification fired from
	// cycleAll's healthy-ready success branch. The hook is consulted AFTER
	// writeHealth has flipped the credential back to ready (so the cache
	// invalidation can take effect immediately on the next chat request),
	// with source="cycle_all" so the metric counter and dispatcher handler
	// can distinguish this path from fast_probe and probe_queue. nil → the
	// probe path stays silent (routing layer falls back to candCache TTL).
	// 2026-08-26 quota-recovery-notify fix: closes the
	// "DB says ready but cache still excludes credential" gap.
	onQuotaRecovered func(credID int, source string)

	probeCtxMu sync.RWMutex
	probeCtx   context.Context
}

// NewCredentialProbeV2 builds the background probe-v2 worker. The cycle
// interval and fast-reprobe delay default to 1h / 5min (production) but
// can both be shortened via env for local/test environments:
//
//	LLM_GATEWAY_CRED_PROBE_V2_INTERVAL
//	LLM_GATEWAY_CRED_PROBE_V2_FAST_REPROBE_DELAY
//
// This avoids the "first probe missed mock startup, wait an hour for the
// next tick" failure mode in scenario tests.
func NewCredentialProbeV2(db *pgxpool.Pool, encKey []byte) *CredentialProbeV2 {
	interval := 1 * time.Hour
	fastDelay := 5 * time.Minute
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CRED_PROBE_V2_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		} else if err != nil {
			slog.Warn("credential probe v2: invalid LLM_GATEWAY_CRED_PROBE_V2_INTERVAL, using 1h", "value", v, "error", err)
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CRED_PROBE_V2_FAST_REPROBE_DELAY")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			fastDelay = d
		} else if err != nil {
			slog.Warn("credential probe v2: invalid LLM_GATEWAY_CRED_PROBE_V2_FAST_REPROBE_DELAY, using 5m", "value", v, "error", err)
		}
	}
	return &CredentialProbeV2{
		db:                 db,
		encKey:             encKey,
		interval:           interval,
		fastReprobeDelay:   fastDelay,
		fastReprobeQueue:   make(chan int, 64),
		fastReprobePending: make(map[int]struct{}),
		done:               make(chan struct{}),
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

// SetOnQuotaRecovered wires the dispatcher-facing notification fired from
// cycleAll's healthy-ready success branch. The (credID, source) signature
// lets the integrator route the label + invalidator from a single closure.
// Safe to call multiple times; the latest non-nil setter wins. nil → the
// probe path stays silent (the routing layer falls back to candCache TTL).
//
// 2026-08-26 quota-recovery-notify fix: without this hook the probe path
// would write healthy into the DB but the routing layer's candidate cache
// would still exclude the credential until TTL elapses, so the first chat
// request after a recharge still picks a fallback node.
func (c *CredentialProbeV2) SetOnQuotaRecovered(fn func(credID int, source string)) {
	if fn == nil {
		return
	}
	c.onQuotaRecovered = fn
}

// SubmitFastProbe queues a delayed credential probe. At most one pending
// delayed probe is allowed per credential: PeriodicQuotaProbe (5 min),
// BalanceQuotaProbe (2 min), and transient-error recovery can otherwise all
// submit the same credential faster than fastReprobeDelay (normally 5 min),
// creating an unbounded pile of sleeping goroutines after the queue consumer
// starts. The pending mark is released when the queued task finishes or when
// a full queue rejects it.
func (c *CredentialProbeV2) SubmitFastProbe(credID int) {
	if c == nil || credID <= 0 {
		return
	}
	// Atomically reject a submission once Stop has begun. Keep the lifecycle
	// lock through the non-blocking send so shutdown cannot leave a pending
	// mark in a queue whose consumer has already exited.
	c.lifecycleMu.Lock()
	if c.stopped {
		c.lifecycleMu.Unlock()
		return
	}
	c.fastReprobeMu.Lock()
	if _, pending := c.fastReprobePending[credID]; pending {
		c.fastReprobeMu.Unlock()
		c.lifecycleMu.Unlock()
		slog.Debug("credential probe v2: fast probe already pending", "credential_id", credID)
		return
	}
	c.fastReprobePending[credID] = struct{}{}
	select {
	case c.fastReprobeQueue <- credID:
		c.fastReprobeMu.Unlock()
		c.lifecycleMu.Unlock()
		slog.Debug("credential probe v2: fast probe submitted", "credential_id", credID)
	default:
		delete(c.fastReprobePending, credID)
		c.fastReprobeMu.Unlock()
		c.lifecycleMu.Unlock()
		slog.Warn("credential probe v2: fast probe queue full", "credential_id", credID)
	}
}

func (c *CredentialProbeV2) releaseFastProbe(credID int) {
	c.fastReprobeMu.Lock()
	delete(c.fastReprobePending, credID)
	c.fastReprobeMu.Unlock()
}

// fastProbeQueueLen exposes the pending fast-probe count for tests and
// diagnostics. A persistently full queue (cap 64) means submissions are
// being dropped — exactly the failure mode StartFastProbeConsumer exists
// to prevent.
func (c *CredentialProbeV2) fastProbeQueueLen() int {
	return len(c.fastReprobeQueue)
}

func (c *CredentialProbeV2) fastProbePendingLen() int {
	c.fastReprobeMu.Lock()
	defer c.fastReprobeMu.Unlock()
	return len(c.fastReprobePending)
}

func (c *CredentialProbeV2) Start(ctx context.Context) {
	c.start(ctx, true)
}

// StartFastProbeConsumer starts ONLY the fast-reprobe queue drain loop,
// skipping the legacy hourly cycleAll. LLM_GATEWAY_USE_NEW_PROBE_MODE=true
// intentionally skips Start() (the new-mode workers own the scheduled
// probe surface), but PeriodicQuotaProbe / BalanceQuotaProbe still submit
// quota-recovery probes into fastReprobeQueue. Without a consumer those
// submissions pile up until the 64-slot queue fills and every subsequent
// probe is silently dropped — observed on 154 production (2026-08-18):
// credentials stuck in permanently_exhausted/suspended for weeks while
// "credential probe v2: fast probe queue full" logged every cycle and the
// upstream had long recovered.
func (c *CredentialProbeV2) StartFastProbeConsumer(ctx context.Context) {
	c.start(ctx, false)
}

func (c *CredentialProbeV2) start(ctx context.Context, legacyCycle bool) {
	ctx, cancel := context.WithCancel(ctx)
	c.lifecycleMu.Lock()
	if c.started || c.stopped {
		c.lifecycleMu.Unlock()
		cancel()
		return
	}
	c.started = true
	c.cancel = cancel
	c.probeCtxMu.Lock()
	c.probeCtx = ctx
	c.probeCtxMu.Unlock()
	c.lifecycleMu.Unlock()
	go c.run(ctx, legacyCycle)
	slog.Info("credential probe v2 started",
		"interval", c.interval, "legacy_cycle", legacyCycle)
}

func (c *CredentialProbeV2) Stop() {
	c.lifecycleMu.Lock()
	if c.stopped {
		c.lifecycleMu.Unlock()
		return
	}
	c.stopped = true
	cancel := c.cancel
	started := c.started
	c.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		<-c.done
	}
	c.probeWG.Wait()
	// A cancelled consumer can leave queue entries that it never received.
	// The worker is terminal after Stop, but clear their marks so lifecycle
	// state remains internally consistent and tests/diagnostics cannot report
	// phantom pending probes.
	c.fastReprobeMu.Lock()
	for credID := range c.fastReprobePending {
		delete(c.fastReprobePending, credID)
	}
	c.fastReprobeMu.Unlock()
}

// ProbeNowAsync executes a credential probe immediately in the background.
// It is used after a caller has already applied its own backoff. Unlike
// SubmitFastProbe, it does not add the separate five-minute fast-reprobe delay.
func (c *CredentialProbeV2) ProbeNowAsync(credID int) {
	if c == nil {
		return
	}
	c.lifecycleMu.Lock()
	if !c.started || c.stopped {
		c.lifecycleMu.Unlock()
		return
	}
	c.probeCtxMu.RLock()
	ctx := c.probeCtx
	c.probeCtxMu.RUnlock()
	if ctx == nil {
		c.lifecycleMu.Unlock()
		return
	}
	c.probeWG.Add(1)
	c.lifecycleMu.Unlock()
	go func() {
		defer c.probeWG.Done()
		c.ProbeNow(ctx, credID)
	}()
}

func (c *CredentialProbeV2) run(ctx context.Context, legacyCycle bool) {
	defer close(c.done)

	if legacyCycle {
		// Stagger from v1: v2 fires at minute 30 of each hour, v1 fires at minute 0.
		// On first start, wait until next :30 mark.
		if wait := time.Until(nextHalfHour()); wait > 0 && wait < c.interval {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	if legacyCycle {
		c.cycleAll(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if legacyCycle {
				c.cycleAll(ctx)
			}
		case credID := <-c.fastReprobeQueue:
			// P2: event-triggered fast reprobe after auth_failed/unreachable.
			// Track the delayed goroutine so Stop waits for its cancellation
			// path and it always releases the per-credential pending mark.
			c.probeWG.Add(1)
			go func(id int) {
				defer c.probeWG.Done()
				defer c.releaseFastProbe(id)
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
			slog.Warn("credential probe v2: decrypt failed (will mark credential unreachable)",
				"credential_id", s.ID,
				"error", decErr,
			)
			recoverAt := time.Now().Add(5 * time.Minute)
			c.writeHealth(timeoutCtx, s.ID, probeResult{
				HealthStatus:          "unreachable",
				HealthError:           "decrypt failed",
				HealthSource:          "probe",
				AvailabilityState:     "unreachable",
				AvailabilityRecoverAt: &recoverAt,
				StateReasonCode:       "decrypt_failed",
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
				QuotaState:        "ok", // 探测成功时主动清除 periodic_exhausted
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

		// 2026-08-26 quota-recovery-notify fix: after writeHealth has flipped
		// the credential back to ready, notify the dispatcher so the
		// per-credential candidate cache invalidates immediately and the
		// next chat request re-plans with the recovered binding visible
		// (instead of waiting for the candCache TTL or the next 5-min
		// PeriodicQuotaProbe tick). nil hook → silent, routing layer falls
		// back to its TTL. We deliberately reuse the existing
		// AvailabilityState=="ready" check rather than threading a new flag
		// from writeHealth — the dispatcher only cares about "the credential
		// flipped back to routable", and writeHealth is the single writer
		// of availability_state (the routing layer already trusts its
		// verdict).
		if ok && pr.AvailabilityState == "ready" && c.onQuotaRecovered != nil {
			c.onQuotaRecovered(s.ID, "cycle_all")
		}

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
	// BindingOnly means the probe reached and authenticated the credential,
	// but its selected model is not callable. Only that credential-model node
	// may be removed from routing; sibling models must remain unaffected.
	BindingOnly bool
}

// loadBoundRawModels returns distinct available raw models bound to a credential.
// A probe-time lookup failure is non-fatal: callers retain the default probe model.
func (c *CredentialProbeV2) loadBoundRawModels(ctx context.Context, credID int) []string {
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	rows, err := c.db.Query(queryCtx, `
		SELECT DISTINCT pm.raw_model_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE cmb.credential_id = $1
		  AND COALESCE(cmb.available, TRUE) = TRUE
		  AND COALESCE(pm.available, TRUE) = TRUE
		  AND pm.raw_model_name <> ''
	`, credID)
	if err != nil {
		slog.Warn("credential probe v2: loadBoundRawModels query failed",
			"credential_id", credID, "error", err)
		return nil
	}
	defer rows.Close()

	models := make([]string, 0)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			slog.Warn("credential probe v2: loadBoundRawModels scan failed",
				"credential_id", credID, "error", err)
			return nil
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("credential probe v2: loadBoundRawModels rows failed",
			"credential_id", credID, "error", err)
		return nil
	}
	return models
}

func uniqueStringSet(values ...[]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, group := range values {
		for _, value := range group {
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
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

	httpClient := &http.Client{Timeout: 30 * time.Second}
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
		pr.HealthStatus = "unreachable"
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
		pr.BindingOnly = true
	case isModelBindingProbeError(errMsg):
		// An explicit model response is binding-scoped. Do not convert this
		// into credential-level unavailable state: the same credential can
		// still serve its other bound models.
		pr.HealthStatus = "warning"
		pr.AvailabilityState = "ready"
		pr.HealthError = errMsg
		pr.StateReasonCode = "model_binding_error"
		pr.BindingOnly = true
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

// isModelBindingProbeError accepts only chat-probe responses. A similarly
// worded failure from the provider's /models endpoint is provider/API evidence
// and must remain credential-scoped.
func isModelBindingProbeError(errMsg string) bool {
	message := strings.ToLower(errMsg)
	if !strings.HasPrefix(message, "chat status ") && !strings.HasPrefix(message, "messages status ") {
		return false
	}
	return strings.Contains(message, "model not found") ||
		strings.Contains(message, "model has been deprecated") ||
		strings.Contains(message, "model is deprecated")
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
		    -- 审计修正 (2026-07-06): 探测成功且 quota_state 被重置为 'ok' 时，
		    -- 同步清除残留的 quota_recover_at，避免状态不一致
		    -- (quota_state='ok' 但 quota_recover_at 指向未来时间)。
		    -- 失败分支不传 $8，COALESCE 保持旧值，这里的 CASE 也不会触发。
		    quota_recover_at = CASE 
		        WHEN COALESCE($8, quota_state) = 'ok' THEN NULL 
		        ELSE quota_recover_at 
		    END,
		    lifecycle_status = CASE
		        WHEN COALESCE($8, '') = 'ok'
		          AND lifecycle_status = 'disabled'
		          AND auto_disabled_at IS NOT NULL
		        THEN 'active'
		        ELSE lifecycle_status
		    END,
		    auto_enabled_at = CASE
		        WHEN COALESCE($8, '') = 'ok'
		          AND lifecycle_status = 'disabled'
		          AND auto_disabled_at IS NOT NULL
		        THEN NOW()
		        ELSE auto_enabled_at
		    END,
		    auto_enabled_reason = CASE
		        WHEN COALESCE($8, '') = 'ok'
		          AND lifecycle_status = 'disabled'
		          AND auto_disabled_at IS NOT NULL
		        THEN 'periodic_quota_probe_recovered'
		        ELSE auto_enabled_reason
		    END,
		    auto_disabled_at = CASE
		        WHEN COALESCE($8, '') = 'ok'
		          AND lifecycle_status = 'disabled'
		          AND auto_disabled_at IS NOT NULL
		        THEN NULL
		        ELSE auto_disabled_at
		    END,
		    auto_disabled_reason = CASE
		        WHEN COALESCE($8, '') = 'ok'
		          AND lifecycle_status = 'disabled'
		          AND auto_disabled_at IS NOT NULL
		        THEN NULL
		        ELSE auto_disabled_reason
		    END,
		    state_reason_code = $9,
		    state_updated_at = NOW()
		WHERE id = $10
		  AND (
		      lifecycle_status = 'active'
		      OR (
		          lifecycle_status = 'disabled'
		          AND auto_disabled_at IS NOT NULL
		          AND COALESCE(quota_state, 'ok') = 'periodic_exhausted'
		          AND (quota_recover_at IS NULL OR quota_recover_at <= now())
		      )
		  )
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  -- 2026-08-07 P0 死锁修复：硬配额守卫必须让"探活实测成功"通过。
		  --
		  -- 旧条件是无条件的 quota_state NOT IN ('permanently_exhausted',
		  -- 'balance_exhausted')，造成一个无法自愈的闭环：
		  --   1. 上游 429 "usage limit exceeded" 被分类为 KindQuotaPermanent
		  --      → quota_state='permanently_exhausted' + availability='suspended'
		  --   2. balance_quota_probe 会挑中它去探测（目标状态就含
		  --      permanently_exhausted），探测甚至返回 200 健康
		  --   3. 但成功分支要写的 quota_state='ok' / availability='ready'
		  --      被这条 WHERE 直接过滤掉 → 0 rows affected
		  --   4. 凭据永久卡死，只能靠 admin force_enable 人工救
		  -- 实测证据（154 生产，2026-08-07）：cred 34 (zhima-1)
		  -- health_status='healthy' 却仍是 permanently_exhausted/suspended。
		  --
		  -- 新语义：只有在"本次探测仍未恢复"时才保留硬配额守卫。
		  -- 探活成功（$8='ok'）说明上游实际已可用（额度重置或用户已充值），
		  -- 这是比历史 quota_state 更新的事实，必须允许翻回 ready。
		  AND (
		      COALESCE($8, '') = 'ok'
		      OR quota_state NOT IN ('permanently_exhausted', 'balance_exhausted')
		  )
	`, pr.HealthStatus, pr.HealthError, pr.HealthLatencyMs, pr.HealthProbeModel,
		pr.HealthSource, pr.AvailabilityState, recoverAt,
		quotaState, stateReason, credID); err != nil {
		slog.Warn("credential probe v2: writeHealth failed",
			"credential_id", credID, "health_status", pr.HealthStatus, "error", err)
		return
	}
	if pr.BindingOnly {
		c.writeBindingUnavailable(execCtx, credID, pr)
	} else if pr.AvailabilityState == "ready" {
		// 2026-08-26 self-check audit (apigpt / gpt-5.6-terra case):
		// restoreBindingOnProbeSuccess only clears bindings whose
		// unavailable_reason='auto_probe_model_binding' (the per-model
		// probe-revert ladder). For a token-billing credential that was
		// down for balance_exhausted, sibling bindings often had been
		// marked unavailable via auto_rate_limit / auto_concurrent /
		// continuous_failure during the outage — and restoreBindingOnProbeSuccess
		// would not touch them. Once the credential recovers to 'ready',
		// v_routable_credential_models still filters them out because
		// cmb.available=FALSE, so the operator sees the credential as
		// "ready" but the bound model still routes nowhere.
		//
		// User principle: "if one model under a credential is healthy,
		// all sibling models should be reachable; clear the old model
		// list and replace with the freshly observed list. If the
		// upstream /v1/models fetch fails, do NOT touch the binding
		// list."  We honor that by fanning the recovery across every
		// cmb row under the credential whose current reason is one of
		// the auto-* ladders (the upstream health evidence applies to
		// the whole credential — apigpt's account is healthy, not just
		// gpt-5.6-terra).
		c.restoreAllBindingsOnCredentialSuccess(execCtx, credID, pr.HealthProbeModel)
		c.restoreBindingOnProbeSuccess(execCtx, credID, pr.HealthProbeModel)
	}

	// 2026-08-26 self-check audit: on the healthy-ready branch, fan the
	// cache write across EVERY bound raw model (regardless of the
	// current cmb.available state) so the cache reflects the freshly
	// recovered binding set. On the failure branch, keep the existing
	// cmb.available=TRUE filter — the cache mirrors current DB state
	// when the upstream is sick.
	var writeModels []string
	if !pr.BindingOnly && pr.AvailabilityState == "ready" {
		writeModels = uniqueStringSet([]string{pr.HealthProbeModel}, c.loadBoundRawModelsAll(execCtx, credID))
	} else {
		writeModels = uniqueStringSet([]string{pr.HealthProbeModel}, c.loadBoundRawModels(execCtx, credID))
	}
	if len(writeModels) == 0 {
		writeModels = []string{pr.HealthProbeModel}
	}
	if c.cache != nil && c.cache.Enabled() && pr.HealthProbeModel != "" {
		available := pr.AvailabilityState == "ready" && !pr.BindingOnly
		state := pr.HealthStatus
		if state == "healthy" {
			state = "healthy_confirmed"
		}
		if !available && !pr.BindingOnly && pr.AvailabilityState != "" {
			state = pr.AvailabilityState
		}
		if pr.BindingOnly {
			state = "model_binding"
		}
		for _, model := range writeModels {
			if err := c.cache.Set(execCtx, credID, model, modelAvailabilityFields(
				credID, model, state, available, pr.HealthStatus, 0, 0,
				recoverAt, pr.HealthSource,
			)); err != nil {
				slog.Warn("credential probe v2: cache write failed",
					"credential_id", credID, "probe_model", model, "error", err)
			}
		}
	}

	// 新增：同步到状态管理器
	if c.stateManager != nil && pr.HealthProbeModel != "" {
		now := time.Now()
		for _, model := range writeModels {
			state := &credentialstate.State{
				CredentialID:  credID,
				Model:         model,
				Available:     pr.AvailabilityState == "ready" && !pr.BindingOnly,
				HealthStatus:  pr.HealthStatus,
				AvgLatencyMs:  pr.HealthLatencyMs,
				LastUpdatedAt: now,
				LastSuccessAt: func() *time.Time {
					if pr.AvailabilityState != "ready" {
						return nil
					}
					return &now
				}(),
				LastError: pr.HealthError,
				RecoverAt: recoverAt,
				Source:    "probe_v2",
			}
			c.stateManager.UpdateFromProbe(execCtx, state)
		}
	}
}

// writeBindingUnavailable closes a model-scoped probe failure across the
// binding table and derived node state. Credential health was already saved by
// writeHealth as reachable/ready, so this helper must never update credentials
// availability or lifecycle fields.
func (c *CredentialProbeV2) writeBindingUnavailable(ctx context.Context, credID int, pr probeResult) {
	if pr.HealthProbeModel == "" {
		return
	}
	if _, err := c.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available              = FALSE,
		    unavailable_reason     = 'auto_probe_model_binding',
		    unavailable_at         = NOW(),
		    unavailable_recover_at = NOW() + INTERVAL '7 days',
		    updated_at             = NOW()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
	`, credID, pr.HealthProbeModel); err != nil {
		slog.Warn("credential probe v2: binding unavailable write failed",
			"credential_id", credID,
			"raw_model", pr.HealthProbeModel,
			"reason", pr.StateReasonCode,
			"error", err)
		return
	}
	provider.InvalidateCandidateCacheForCredential(credID)
}

// restoreBindingOnProbeSuccess closes the binding-only self-check loop. It
// restores exactly the model this probe called and only when this probe was the
// actor that disabled it; manual and other automated holds remain untouched.
func (c *CredentialProbeV2) restoreBindingOnProbeSuccess(ctx context.Context, credID int, rawModel string) {
	if rawModel == "" {
		return
	}
	result, err := c.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available              = TRUE,
		    unavailable_reason     = NULL,
		    unavailable_at         = NULL,
		    unavailable_recover_at = NULL,
		    updated_at             = NOW()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND cmb.available = FALSE
		  AND cmb.unavailable_reason = 'auto_probe_model_binding'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
	`, credID, rawModel)
	if err != nil {
		slog.Warn("credential probe v2: binding recovery write failed",
			"credential_id", credID, "raw_model", rawModel, "error", err)
		return
	}
	if result.RowsAffected() > 0 {
		provider.InvalidateCandidateCacheForCredential(credID)
	}
}

// restoreAllBindingsOnCredentialSuccess is the 2026-08-26 self-check audit
// fix for the apigpt / gpt-5.6-terra recharge scenario. Once the
// credential-level probe comes back healthy (writeHealth sees
// AvailabilityState='ready'), this helper fans the recovery across every
// cmb row under the credential whose current unavailable_reason is one of
// the auto-* / continuous_failure ladders.
//
// Why a separate helper:
//   - restoreBindingOnProbeSuccess only clears `auto_probe_model_binding`
//     (the per-model probe-revert ladder). Other reasons
//     (auto_rate_limit, auto_concurrent, auto_stream_timeout,
//     continuous_failure) were left untouched even when the underlying
//     upstream was healthy again.
//   - The user's principle: "一旦这个模型成功了，应该先拉取凭据下所有
//     模型清单，如果成功拉到，就清空原来凭据下的模型清单，用新清单
//     替换。"  We honor that with a single credential-scoped UPDATE that
//     reflects "the account is healthy → every binding the upstream
//     accepts is routable".
//
// Hard guards (preserved):
//   - unavailable_reason NOT LIKE 'manual%' — operator pin.
//   - unavailable_reason <> 'model_probe_broken' — permanent broken flag
//     owned by bg/model_probe.go's own recovery ladder.
//   - admin_protected = FALSE — admin pin.
//   - cmb.available = FALSE — only flip rows that are currently down.
//
// Mirrors the same clearing to model_offers so /api/routing/resolve
// ("test route") and the admin UI stay in lock-step with the production
// router (v_routable_credential_models). The candidate cache is invalidated
// so the router's next read sees the fresh state.
//
// probeModel is the model the probe actually called; it's logged for
// observability but not used as a filter — the recovery applies to every
// sibling binding, not just the probe model.
func (c *CredentialProbeV2) restoreAllBindingsOnCredentialSuccess(ctx context.Context, credID int, probeModel string) {
	if c.db == nil {
		return
	}
	execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// 1. Clear cmb rows that an automated ladder had marked unavailable.
	// The predicate intentionally covers:
	//   - continuous_failure    (credentialhealth/checker.go:markDegraded)
	//   - auto_*                (domains/credential/writer.go:writeModelLevelFailureOnly)
	//   - auto_probe_model_binding (this file: writeBindingUnavailable)
	//   - probe_*               (bg/node_probe.go — rare under balance_exhausted,
	//     but defensive)
	result, err := c.db.Exec(execCtx, `
		UPDATE credential_model_bindings cmb
		SET available              = TRUE,
		    unavailable_reason     = NULL,
		    unavailable_at         = NULL,
		    unavailable_recover_at = NULL,
		    updated_at             = NOW()
		WHERE cmb.credential_id = $1
		  AND cmb.available = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.unavailable_reason, '') <> 'model_probe_broken'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
	`, credID)
	if err != nil {
		slog.Warn("credential probe v2: restoreAllBindingsOnCredentialSuccess failed",
			"credential_id", credID, "probe_model", probeModel, "error", err)
		return
	}
	cmbRows := int(result.RowsAffected())

	// 2. Mirror to model_offers so /api/routing/resolve and the admin
	// UI match the cmb side. Same guard set as the cmb UPDATE — the
	// mirrors can otherwise drift if a later change adds a guard to
	// one side but not the other.
	moResult, err := c.db.Exec(execCtx, `
		UPDATE model_offers mo
		SET available          = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at     = NULL,
		    updated_at         = NOW()
		WHERE mo.credential_id = $1
		  AND mo.available = FALSE
		  AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(mo.unavailable_reason, '') <> 'model_probe_broken'
		  AND COALESCE(mo.admin_protected, FALSE) = FALSE
	`, credID)
	if err != nil {
		slog.Warn("credential probe v2: restoreAllBindingsOnCredentialSuccess mirror failed",
			"credential_id", credID, "probe_model", probeModel, "error", err)
		// Continue — cmb side succeeded; cache invalidation still applies.
	}
	moRows := int(moResult.RowsAffected())

	if cmbRows > 0 || moRows > 0 {
		slog.Info("credential probe v2: recharge recovery fan-out cleared bindings",
			"credential_id", credID,
			"probe_model", probeModel,
			"cmb_rows", cmbRows,
			"model_offers_rows", moRows,
			"trigger", "apigpt_recharge_2026_08_26",
		)
		provider.InvalidateCandidateCacheForCredential(credID)
	}
}

// loadBoundRawModelsAll returns distinct raw_model_name values bound to a
// credential WITHOUT filtering on cmb.available. Used by the healthy probe
// branch so the cache fan-out reflects the freshly recovered binding set,
// including bindings that had been marked unavailable during the prior
// balance_exhausted window.
//
// Sibling to loadBoundRawModels (which keeps the cmb.available=TRUE filter
// for the failure branch where the cache mirrors current DB state).
//
// Errors and empty result both return nil — callers fall back to the
// probe-model-only list.
func (c *CredentialProbeV2) loadBoundRawModelsAll(ctx context.Context, credID int) []string {
	if c.db == nil {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	rows, err := c.db.Query(queryCtx, `
		SELECT DISTINCT pm.raw_model_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE cmb.credential_id = $1
		  AND COALESCE(pm.available, TRUE) = TRUE
		  AND pm.raw_model_name <> ''
	`, credID)
	if err != nil {
		slog.Warn("credential probe v2: loadBoundRawModelsAll query failed",
			"credential_id", credID, "error", err)
		return nil
	}
	defer rows.Close()

	models := make([]string, 0)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err == nil && model != "" {
			models = append(models, model)
		}
	}
	if err := rows.Err(); err != nil {
		slog.Warn("credential probe v2: loadBoundRawModelsAll rows failed",
			"credential_id", credID, "error", err)
		return nil
	}
	return models
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
		  AND c.status = 'active'
		  AND (
		      c.lifecycle_status = 'active'
		      OR (
		          c.lifecycle_status = 'disabled'
		          AND c.auto_disabled_at IS NOT NULL
		          AND COALESCE(c.quota_state, 'ok') = 'periodic_exhausted'
		          AND (c.quota_recover_at IS NULL OR c.quota_recover_at <= now())
		      )
		  )
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
			QuotaState:        "ok", // 探测成功时主动清除 periodic_exhausted
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
