// Package bg — node_probe.go
//
// NodeProbeWorker is the new error-triggered node-probe worker mandated
// by the 2026-07-14 spec rewrite.  It supersedes
//   - bg/credential_probe_v2.go         (1h cycle)
//   - bg/model_probe.go                 (5min cycle + consensus)
//   - bg/model_probe_suspicious.go      (2min cycle)
//   - bg/active_probe_worker.go         (5s..15m backoff)
//
// Cadence
// ───────
// Error-triggered.  State machine per (credential_id, raw_model_name)
// lives in node_probe_state.  Backoff ladder:
//
//	attempt 1 → +5s     (immediate on first failure)
//	attempt 2 → +30s
//	attempt 3 → +60s
//	attempt 4 → +5m
//	attempt 5 → +1h
//	attempt 6 → +2h
//	attempt 7+→ +6h    (ladder cap since 2026-07-24, bg/probe_backoff.go;
//	                    rows keep ticking forever — "paused" was removed
//	                    from this mode, only Submit's ON CONFLICT re-arms)
//
// After attempt 7 the row keeps probing on the 6h cadence; the ladder
// resets when the NEXT REAL FAILURE for the same
// (credential, model) arrives — Submit's ON CONFLICT un-pauses it and
// resets the ladder (broken_probe_reviver only touches the legacy
// model_probe_state table, and is a no-op under this new probe mode since
// R36).
//
// Two rounds per attempt
// ──────────────────────
//  1. direct  — POST the upstream provider's base URL using the
//     decrypted credential, mirroring the "isolate_upstream"
//     call in bg/self_check_worker.go.  Verifies the
//     upstream is actually serving traffic.
//  2. gateway — POST the local gateway with the system API key.
//     Verifies the credential is wired into routing and
//     the gateway-side plugins (auth, transform, billing,
//     rate-limit) are not blocking the path.
//
// The direct round is the source of truth for node availability: a
// direct success restores the binding even when the pinned gateway round
// fails (2026-09-08 — gateway-side pin/URSM failures used to keep a healthy
// upstream red). Both rounds must succeed (HTTP 200 + a tool call echo)
// for the attempt to count as a full success; otherwise the backoff
// ladder keeps re-probing.
//
// Outbound X-LLM-Origin-* headers
// ────────────────────────────────
//
//	X-LLM-Origin-Stage : node_probe
//	X-LLM-Origin-Actor : node-probe-worker
//	X-Forwarded-For    : $LLM_GATEWAY_EGRESS_FORWARDED_FOR + egress IP
//	X-Real-IP          : $LLM_GATEWAY_EGRESS_IP
package bg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/internal/loopback"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// nodeProbeMaxAttempts matches the length of NodeProbeBackoffChain.
	nodeProbeMaxAttempts = 7

	// 30s is a safety-net scan; Submit also wakes the worker immediately.
	nodeProbeTickInterval = 30 * time.Second
	// nodeProbeQueuePumpInterval paces the unified-queue pump that feeds due
	// node_probe_state rows into credential_probe_queue (legacy picker is
	// parked in that mode; without the pump the rows starve the queue).
	nodeProbeQueuePumpInterval = 30 * time.Second
	// nodeProbeQueuePumpBatch caps how many due rows one pump tick enqueues.
	nodeProbeQueuePumpBatch = 20
	// nodeProbeQueuePumpHoldoff advances a pumped row's next_retry_at so the
	// same row is not re-enqueued on every tick while the queue drains it.
	nodeProbeQueuePumpHoldoff = 45 * time.Second
	// A bounded batch keeps a large outage from monopolizing the worker.
	nodeProbeBatchSize = 8

	// nodeProbeInFlightWindow prevents the same (cred, model) from
	// being probed concurrently by two workers / re-deploys.  Set
	// 5 minutes so a re-entry within that window is treated as
	// "still running".
	nodeProbeInFlightWindow = 5 * time.Minute

	// nodeProbeSyncFanout caps how many concurrent (cred, model)
	// direct probes ProbeSync runs in parallel. Mirrors
	// nodeProbeBatchSize so a no_candidate burst does not multiply
	// upstream pressure beyond what the background worker would
	// produce.
	nodeProbeSyncFanout = 8

	// nodeProbePerCredSyncConcurrency (Wave 3 B2③, 2026-09-22) caps
	// concurrent ProbeSync direct probes against ONE credential. The
	// global fanout above bounds worker-wide pressure but a no_candidate
	// burst naming many models on the same credential could still point
	// all 8 slots at a single upstream account; per-credential ≤2 keeps
	// the burst spread across credentials. Design §6.3 "同凭据探测并发≤2".
	nodeProbePerCredSyncConcurrency = 2

	// Flash-blip double-confirm pacing (Wave 3 B2②, 2026-09-22). After a
	// request-path error that would degrade the node, two lightweight
	// direct pings run inside the 2~5s design window; the degrade is
	// applied only when BOTH fail (design §6.3 "闪断双确认").
	nodeProbeConfirmFirstPingDelay = 2 * time.Second
	nodeProbeConfirmPingGap        = 2 * time.Second
	// nodeProbeConfirmBudget bounds the whole confirm (pings + gaps).
	nodeProbeConfirmBudget = 15 * time.Second

	// 2026-09-03 P1.3: bounded retry + persistent failure for
	// submitViaQueueSource. ProbeQueue.Enqueue can fail with transient DB
	// pressure; one-shot failures used to be silently logged (Submit) or
	// delayed until the next pump tick (pumpDueStatesToQueue), so operators
	// had no signal and Submit callers observed no recovery. We now retry
	// up to nodeProbeQueueSubmitMaxAttempts with a small exponential
	// backoff (100ms / 250ms / 500ms) bounded by the caller's ctx, and
	// persist the final error to node_probe_state so the row stays
	// inspectable. The retry budget is small on purpose: queue submission
	// is a single INSERT round-trip, so 3 attempts cover transient
	// connection blips without blocking recovery loops for seconds.
	nodeProbeQueueSubmitMaxAttempts    = 3
	nodeProbeQueueSubmitBaseBackoff    = 100 * time.Millisecond
	nodeProbeQueueSubmitBackoffMult    = 2.5
	nodeProbeQueueSubmitMaxBackoff     = 500 * time.Millisecond
	nodeProbeQueueSubmitFailureHoldoff = 30 * time.Second
	nodeProbeQueueSubmitErrCode        = "queue_submit_failed"
	nodeProbeQueueSubmitErrDetailMax   = 256
)

type NodeProbeStateSink interface {
	ApplyProbeForTenant(ctx context.Context, tenant string, credentialID int, rawModel string, success bool, latencyMs int) error
}

type nodeProbeAuditDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// NodeProbeWorker polls node_probe_state and executes the two-round
// (direct + gateway) probe for each (credential, model) whose
// next_retry_at has elapsed.
//
// probeDirect and probeGateway use separate HTTP clients:
//   - probeClient — respects the upstream HTTP_PROXY via ProxyFunc,
//     matching the path real user requests take through the gateway.
//   - client — direct (no proxy), used only for the gateway round
//     which hits the local gateway API endpoint.
//
// 2026-07-16 fix: probeDirect previously used the direct client (no proxy),
// which bypassed the proxy resolver and reported false-positive recoveries
// when the proxy was actually broken. Credentials oscillated between
// "available" (probeDirect through proxy-less client → OK) and
// "unavailable" (real requests through proxy → timeout), producing the
// "models briefly work then 5xx" pattern.
type NodeProbeWorker struct {
	db            *pgxpool.Pool
	auditDB       nodeProbeAuditDB
	encKey        []byte
	keyring       *secret.Keyring
	apiKey        string
	baseURL       string
	client        *http.Client // direct (no proxy), for probeGateway
	probeClient   *http.Client // proxy-respecting, for probeDirect
	stateObserver credentialstate.StateObserver
	stateSink     NodeProbeStateSink
	// tenantResolver looks up the tenant ID for a credential. Production
	// wires it to credentials.tenant_id via (*pgxpool.Pool).QueryRow; tests
	// can inject a stub to assert the backfill branch without spinning up
	// a full PG container.
	tenantResolver func(ctx context.Context, credID int) (string, error)
	// stateProvider (2026-07-17) is the READ-side contract used by
	// ProbeSync's reuse path: after a syncWaiter channel closes, we
	// re-check IsAvailable for that (cred,model) so the function can
	// report recovery when cycle() (or another fresh-job goroutine)
	// wrote availability=true. Without this the reuse path always
	// returns false even when the pair did recover, producing a
	// spurious 503. May be nil — in that case reuse-path callers fall
	// back to ctx-driven 503.
	stateProvider credentialstate.StateProvider
	emitter       *ActiveProbeEmitter
	// probeSink (2026-08-11) mirrors the node-probe lifecycle (submitted /
	// in-flight) to the self-check SSE stream. The terminal completed/failed
	// transition is already covered by ActiveProbeEmitter.publishSink; this
	// sink covers the earlier lifecycle stages so the 自检 tab shows the
	// pending → running → done progression. Optional — nil is a no-op.
	probeSink ProbeEventSink

	// probeQueue (2026-08-13, 需求 6 完全统一) routes Submit into the durable
	// credential_probe_queue. When non-nil the worker is in unified-queue mode:
	// Submit enqueues, the legacy loop() stops picking node_probe_state rows,
	// and a ProbeQueueWorker + ProbeService own execution. nil = legacy path.
	probeQueue *ProbeQueue
	// enqueueFn (2026-09-05 noise-reduction) is the submitViaQueueSource test
	// seam over ProbeQueue.Enqueue, following the same injectable-fn style as
	// ProbeService.automaticEligibilityFn. It exists so the deterministic-gate
	// skip (ErrProbeAutomaticIneligible / ErrProbeOutOfScope must not consume
	// the retry budget) can be unit-tested without a live database. Production
	// wiring leaves it nil and enqueue() falls through to probeQueue.Enqueue.
	enqueueFn func(ctx context.Context, task ProbeQueueTask) (int64, bool, error)

	// Candidate cache invalidation keeps a direct probe result visible to the
	// next routing decision instead of waiting for the provider cache TTL.
	invalidateCandidateCache func(credentialID int)
	// recordCircuitSuccess closes the in-memory breaker as soon as the direct
	// provider probe confirms recovery.
	recordCircuitSuccess func(providerID, credentialID int)
	// modelQualityTrigger (2026-08-11) requests an on-demand model-IQ re-test
	// for a node whose probe has failed repeatedly (suspicious-action trigger).
	// nil = disabled. Set via SetModelQualityTrigger.
	modelQualityTrigger func(credentialID int, rawModel string, consecutiveFailures int)

	stopCh   chan struct{}
	stopOnce sync.Once
	wakeCh   chan struct{}

	mu         sync.Mutex
	inFlight   map[string]struct{} // dedup key: "<credID>|<model>"
	triggers   map[string]nodeProbeTrigger
	wakeTimers map[string]*time.Timer

	// syncWaiters lets ProbeSync callers wait for an in-flight cycle()
	// run to finish instead of issuing a duplicate probe. Closed once
	// per (cred,model) at the end of runOne, AFTER the state-cache
	// writes so any waiter's subsequent re-PlanCandidates observes
	// the recovered availability.
	syncWaitersMu sync.Mutex
	syncWaiters   map[string][]chan struct{}

	// credSyncSem (Wave 3 B2③) holds one buffered channel per credential
	// seen by ProbeSync, capping same-credential concurrent direct probes
	// at nodeProbePerCredSyncConcurrency. Entries are never evicted: the
	// map is bounded by the credential count and an idle entry is one
	// empty buffered channel.
	credSyncSem sync.Map // map[int]chan struct{}

	// probeConfirmRound is the Wave 3 B2② test seam over probeDirect for
	// ProbeConfirm. Production leaves it nil.
	probeConfirmRound func(ctx context.Context, credID int, model string) nodeProbeRoundResult

	// decryptFailures / decryptTrippedAt implement the instance-level
	// decrypt circuit (2026-09-17 incident: a dev instance on 252 with a
	// mismatched LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY fired ~1400
	// decrypt failures per hour at the shared production DB). Consecutive
	// upstream-secret decrypt failures ≥ decryptTripThreshold trip the
	// circuit: drainDue stops picking new work for decryptTripCooldown,
	// then half-opens to let ONE probe through — if the operator fixed
	// the key the probe succeeds and resets the counter, otherwise the
	// circuit re-closes. Any successful resolve resets the counter.
	decryptFailures  atomic.Int64
	decryptTrippedAt atomic.Int64 // unix seconds of the last trip; 0 = never
}

// decryptTripThreshold is the consecutive decrypt-failure count that trips
// the instance-level decrypt circuit.
const decryptTripThreshold = 5

// decryptTripCooldown is how long the circuit stays fully closed before a
// half-open probe is allowed through.
const decryptTripCooldown = 15 * time.Minute

// nodeProbeGatewaySideRetryDelay paces node_probe_state retries when the
// direct round failed for a gateway-side reason (see isGatewaySideProbeError):
// the pair is not unhealthy, so the chained backoff ladder must not escalate,
// but we also do not want a misconfigured instance hammering the row every 5s.
const nodeProbeGatewaySideRetryDelay = 15 * time.Minute

// isGatewaySideProbeError reports whether a direct-round errCode describes a
// failure that happened INSIDE this gateway instance — building/resolving the
// endpoint or decrypting the credential secret — rather than an upstream
// health signal. Such failures say "this instance cannot talk to the
// upstream" (missing/mismatched key, keyring misconfig, join corruption),
// NOT "the (credential, model) pair is unhealthy", so they must never be
// written to the shared availability surfaces. The probe_-prefixed forms are
// accepted defensively because updateBindingAvailability persists
// "probe_"+errCode into unavailable_reason.
//
// 2026-09-17 incident: a dev gateway sharing the production DB with a
// different CREDENTIAL_ENCRYPTION_KEY decrypted every legacy envelope to
// "cannot decrypt: unknown format" → endpoint_build →
// credential_model_bindings.available=FALSE (probe_endpoint_build, 5min
// cooldown) on healthy credentials, fighting the production instance's
// successful probes every cycle. This classifier is the shared-state guard.
//
// R40 粒度细分（闭合 R39 §三#3）：guard 有一个被它一并压制的真信号子类——
// 单凭据密文永久损坏（加密用过的 key 已轮转掉 / 行级损伤）。这类失败跟随
// 凭据而不是实例（每个实例都解不开），绑定面却因 guard 永无不可用信号，
// 只剩真实流量 breaker 兜底。识别器见 isDecryptShapedProbeDetail +
// credentialSpecificDecryptFailure。
func isGatewaySideProbeError(errCode string) bool {
	switch errCode {
	case "endpoint_build", "request_build",
		"probe_endpoint_build", "probe_request_build":
		return true
	}
	return false
}

// isDecryptShapedProbeDetail reports whether an endpoint_build errDetail came
// from the credential-secret decrypt step. resolveDirectTarget wraps every
// decrypt error as `decrypt: %w` (ErrUnknownFormat → "decrypt: cannot
// decrypt: unknown format", ErrAADMismatch → "decrypt: secret: AAD
// mismatch…"), and no other endpoint_build path emits that prefix.
func isDecryptShapedProbeDetail(errDetail string) bool {
	return strings.Contains(errDetail, "decrypt: ")
}

// credentialSpecificDecryptFailure reports whether a decrypt-shaped
// gateway-side failure is evidence against THIS credential's envelope rather
// than the instance's key config: the instance-level decrypt circuit counts
// consecutive decrypt failures and trips at decryptTripThreshold, so a
// failure observed while the counter is still below threshold means this
// instance decrypts other credentials fine (any success resets the counter)
// — the failure follows the credential, not the instance. On a genuinely
// misconfigured instance the counter reaches the threshold and trips, which
// both re-enables this suppression and lets deescalateGatewaySideProbeState
// repair the few pre-trip writes.
func (w *NodeProbeWorker) credentialSpecificDecryptFailure(errDetail string) bool {
	if w == nil || !isDecryptShapedProbeDetail(errDetail) {
		return false
	}
	return w.decryptFailures.Load() < decryptTripThreshold
}

// decryptCircuitTripped reports whether the instance-level decrypt circuit
// is currently blocking background probe picks. Logs (rate-limited to once
// per decryptTripCooldown) while tripped. When the cooldown elapses it
// half-opens: the caller is allowed one pick so a fixed key can prove
// itself and reset the counter.
func (w *NodeProbeWorker) decryptCircuitTripped() bool {
	if w == nil {
		return false
	}
	n := w.decryptFailures.Load()
	if n < decryptTripThreshold {
		return false
	}
	now := time.Now().Unix()
	trippedAt := w.decryptTrippedAt.Load()
	if trippedAt == 0 {
		if w.decryptTrippedAt.CompareAndSwap(0, now) {
			slog.Error("node_probe_worker: decrypt circuit OPEN — this instance cannot decrypt credential secrets; background node probes paused",
				"consecutive_decrypt_failures", n,
				"cooldown", decryptTripCooldown.String(),
				"hint", "check LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY / keyring on this instance; wrong keys poison shared availability state")
			return true
		}
		// Lost the CAS race: another goroutine just tripped it; re-read.
		trippedAt = w.decryptTrippedAt.Load()
	}
	if time.Duration(now-trippedAt)*time.Second >= decryptTripCooldown {
		// Half-open: allow exactly one pick. decryptTrippedAt is bumped so
		// subsequent picks stay blocked until this probe's outcome either
		// resets decryptFailures (success) or re-trips via failure #n+1.
		w.decryptTrippedAt.Store(now)
		return false
	}
	return true
}

// recordDecryptFailure / resetDecryptFailures maintain the consecutive-decrypt
// failure counter behind the instance-level decrypt circuit.
func (w *NodeProbeWorker) recordDecryptFailure() {
	if w != nil {
		w.decryptFailures.Add(1)
	}
}

func (w *NodeProbeWorker) resetDecryptFailures() {
	if w == nil {
		return
	}
	// 2026-09-17: if the circuit was OPEN and this success just proved the key
	// config fixed, the rows this instance (or a peer) poisoned while
	// misconfigured are still parked on hours-long ladders / unavailable
	// bindings. Unwind them now instead of waiting out every backoff.
	wasTripped := w.decryptFailures.Load() >= decryptTripThreshold
	w.decryptFailures.Store(0)
	w.decryptTrippedAt.Store(0)
	if wasTripped {
		go w.deescalateGatewaySideProbeState(context.Background())
	}
}

type nodeProbeTrigger struct {
	tenantID string
	parentID string
}

// SetStateObserver wires probe results into the router's in-memory and Redis
// state caches. PostgreSQL writes alone are insufficient because routing reads
// the credentialstate cache before it reaches the database.
func (w *NodeProbeWorker) SetStateObserver(observer credentialstate.StateObserver) {
	if w != nil {
		w.stateObserver = observer
	}
}

// SetNodeStateSink wires URSM v2 probe feedback without coupling bg to the
// concrete v2 Manager. It is optional so legacy deployments retain their
// existing credentialstate-only behavior.
func (w *NodeProbeWorker) SetNodeStateSink(sink NodeProbeStateSink) {
	if w != nil {
		w.stateSink = sink
	}
}

// SetStateProvider wires the READ-side cache so ProbeSync's reuse path
// can re-check IsAvailable after a syncWaiter channel closes. The same
// *credentialstate.Manager satisfies both StateObserver and
// StateProvider, so the same instance is passed to both setters.
func (w *NodeProbeWorker) SetStateProvider(provider credentialstate.StateProvider) {
	if w != nil {
		w.stateProvider = provider
	}
}

// SetEmitter makes node-probe rounds visible in the same request stream as
// legacy active probes. The worker remains usable without telemetry.
func (w *NodeProbeWorker) SetEmitter(emitter *ActiveProbeEmitter) {
	if w != nil {
		w.emitter = emitter
	}
}

// SetProbeSink wires the self-check SSE sink so the node-probe lifecycle
// (submitted / in-flight stages) is mirrored to the 自检 tab. The terminal
// completed/failed transition is emitted by the ActiveProbeEmitter; this sink
// covers the earlier stages so the queue progression is visible. Optional.
func (w *NodeProbeWorker) SetProbeSink(sink ProbeEventSink) {
	if w != nil {
		w.probeSink = sink
	}
}

// SetInvalidateCandidateCache wires the provider cache invalidator used after
// direct probe state changes.
func (w *NodeProbeWorker) SetInvalidateCandidateCache(fn func(credentialID int)) {
	if w != nil {
		w.invalidateCandidateCache = fn
	}
}

// SetCircuitRecovery wires the request-path circuit breaker recovery hook.
func (w *NodeProbeWorker) SetCircuitRecovery(fn func(providerID, credentialID int)) {
	if w != nil {
		w.recordCircuitSuccess = fn
	}
}

// SetModelQualityTrigger wires an optional callback invoked when a node probe
// fails twice in a row (attempt >= 2, same source as the active-probe
// submitter's consecutive threshold — NOT nodeProbeMaxAttempts). The
// gateway uses this to request an on-demand model-IQ re-test for the failing
// node (a "suspicious action" trigger, see docs/model-iq/01-design.md §3.4).
// The callback receives (credentialID, rawModel, consecutiveFailures) and must
// be safe to call from the probe goroutine (best-effort; the receiver should
// run the actual test async and log/swallow errors). nil disables the hook.
func (w *NodeProbeWorker) SetModelQualityTrigger(fn func(credentialID int, rawModel string, consecutiveFailures int)) {
	if w != nil {
		w.modelQualityTrigger = fn
	}
}

// SetProbeQueue switches NodeProbeWorker into unified-queue mode (需求 6 完全统一).
// When set, Submit enqueues into the durable credential_probe_queue instead of
// UPSERT-ing node_probe_state, and the legacy loop() stops picking node_probe
// rows (the ProbeQueueWorker + ProbeService own execution). ProbeSync (the
// synchronous request-path probe) is unaffected — it never used the queue. Pass
// nil to revert to the legacy direct path (kill-switch).
func (w *NodeProbeWorker) SetProbeQueue(q *ProbeQueue) {
	if w != nil {
		w.probeQueue = q
	}
}

// UseProbeQueue reports whether Submit routes through the unified queue.
func (w *NodeProbeWorker) UseProbeQueue() bool { return w != nil && w.probeQueue != nil }

// NewNodeProbeWorker constructs a worker.  baseURL="" picks
// LLM_GATEWAY_NODE_PROBE_BASE_URL or the local gateway loopback URL.
// apiKey is the system-level API key the worker uses for the gateway
// round; it should be an is_system=true key with read+write on the
// provider model bindings.
//
// proxyFunc is optional.  When non-nil, the direct probe respects the
// upstream HTTP_PROXY configured in the environment so that the probe
// result accurately reflects what real user requests experience
// (instead of reporting a false-positive recovery by bypassing the
// proxy).  Pass upClient.Proxy().ProxyFunc() from main.go.
func NewNodeProbeWorker(db *pgxpool.Pool, encKey []byte, keyring *secret.Keyring, apiKey, baseURL string, proxyFunc func(*http.Request) (*url.URL, error)) *NodeProbeWorker {
	if baseURL == "" {
		if envURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_NODE_PROBE_BASE_URL")); envURL != "" {
			baseURL = envURL
		} else {
			baseURL = loopback.GatewayBase() + "/v1"
		}
	}
	w := &NodeProbeWorker{
		db:          db,
		auditDB:     db,
		encKey:      encKey,
		keyring:     keyring,
		apiKey:      apiKey,
		baseURL:     baseURL,
		client:      &http.Client{Timeout: 30 * time.Second},
		stopCh:      make(chan struct{}),
		wakeCh:      make(chan struct{}, 1),
		inFlight:    make(map[string]struct{}),
		triggers:    make(map[string]nodeProbeTrigger),
		wakeTimers:  make(map[string]*time.Timer),
		syncWaiters: make(map[string][]chan struct{}),
	}

	if proxyFunc != nil {
		w.probeClient = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy:                 proxyFunc,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		}
	} else {
		w.probeClient = w.client
	}

	// tenantResolver is the only path that contacts PG for tenant lookup.
	// It is wired here so production always honours credentials.tenant_id;
	// tests inject a stub via SetTenantResolver.
	if w.db != nil {
		w.tenantResolver = func(ctx context.Context, credID int) (string, error) {
			var tenant string
			err := w.db.QueryRow(ctx,
				"SELECT COALESCE(tenant_id, '') FROM credentials WHERE id = $1", credID,
			).Scan(&tenant)
			return tenant, err
		}
	}

	return w
}

// SetTenantResolver overrides the production PG-backed tenant lookup. Tests
// use it to assert the backfill branch without spinning up a database.
func (w *NodeProbeWorker) SetTenantResolver(fn func(ctx context.Context, credID int) (string, error)) {
	if w == nil {
		return
	}
	w.tenantResolver = fn
}

func (w *NodeProbeWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	w.resolveProbeAPIKey(ctx)
	go w.loop(ctx)
	// 2026-09-17 incident follow-up: when the decrypt circuit's shared-state
	// guard ships (or a corrected CREDENTIAL_ENCRYPTION_KEY deploys), rows the
	// misconfigured instance already wrote can still hold hours-future
	// next_retry_at ladders and probe_endpoint_build-poisoned bindings. Sweep
	// them once at startup so recovery does not wait out every backoff.
	go w.deescalateGatewaySideProbeState(ctx)
	slog.Info("node_probe_worker started",
		"tick_interval", nodeProbeTickInterval,
		"max_attempts", nodeProbeMaxAttempts,
		"api_key_resolved", w.apiKey != "",
	)
}

// deescalateGatewaySideProbeState resets the shared-state residue of
// gateway-side probe failures (decrypt / endpoint build). Those failures were
// never upstream health signals, so their ladder rows and unavailable
// bindings are safe to unwind from ANY instance — the err/reason codes
// themselves identify the writes (see isGatewaySideProbeError). Best-effort,
// bounded, logged; a failed sweep just leaves the rows to age out naturally.
//
// Targets (2026-09-17 05:00–09:08 decrypt storm on the shared 252 DB):
//   - node_probe_state rows parked on endpoint_build/request_build with
//     future next_retry_at → pull to now() so the pump re-verifies the pair
//     with the now-working key (observed: cf=7 ladders parking hzx-2 /
//     minimax-prod-v2 lanes for ~5h AFTER the fix deployed);
//   - credential_model_bindings flipped unavailable by those errors →
//     restore available. R42 caveat: the R40 credential-specific decrypt
//     exemption legitimately writes the same 'probe_endpoint_build' reason
//     (per-credential ciphertext corruption), and this sweep cannot tell
//     the two apart — every restart re-opens those rows until the next
//     probe rewrites them (accepted tradeoff, R40 §一; a dedicated reason
//     tag is the Phase-2 cleanup if the window ever hurts).
func (w *NodeProbeWorker) deescalateGatewaySideProbeState(ctx context.Context) {
	if w == nil || w.db == nil {
		return
	}
	sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	tag, err := w.db.Exec(sweepCtx, `
		UPDATE node_probe_state
		SET next_retry_at = now(),
		    next_retry_seconds = 5,
		    updated_at = now()
		WHERE last_err_code IN ('endpoint_build', 'request_build')
		  AND next_retry_at > now()
	`)
	if err != nil {
		slog.Warn("node_probe_worker: gateway-side ladder de-escalation failed", "error", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("node_probe_worker: de-escalated gateway-side probe ladders",
			"rows", tag.RowsAffected(),
			"hint", "these failures were instance config problems (decrypt/endpoint build), not upstream health")
	}
	tag, err = w.db.Exec(sweepCtx, `
		UPDATE credential_model_bindings cmb
		SET available = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at = NULL,
		    unavailable_recover_at = NULL,
		    updated_at = now()
		WHERE cmb.available = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') IN ('probe_endpoint_build', 'probe_request_build')
	`)
	if err != nil {
		slog.Warn("node_probe_worker: gateway-side binding de-escalation failed", "error", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("node_probe_worker: restored bindings poisoned by gateway-side probe errors",
			"rows", tag.RowsAffected())
	}
}

// resolveProbeAPIKey preserves the caller-provided data-plane key. Local
// gateway probes traverse AuthMiddleware, which validates only the static
// gateway key; substituting a database system key causes an unrelated 401.
func (w *NodeProbeWorker) resolveProbeAPIKey(ctx context.Context) {
	if w == nil {
		return
	}
	slog.DebugContext(ctx, "node_probe_worker: using configured gateway API key",
		"api_key_resolved", w.apiKey != "")
}

func (w *NodeProbeWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.mu.Lock()
		for key, timer := range w.wakeTimers {
			if timer != nil {
				timer.Stop()
			}
			delete(w.wakeTimers, key)
		}
		w.mu.Unlock()
		if w.client != nil {
			w.client.CloseIdleConnections()
		}
		if w.probeClient != nil && w.probeClient != w.client {
			w.probeClient.CloseIdleConnections()
		}
	})
}

// nodeProbeLoop 退避参数（loop 的 panic 自动重启策略）。
const (
	// R50 引入：panic 重启按 30s 起步、5min 封顶的指数退避。
	nodeProbeLoopInitialBackoff = 30 * time.Second
	nodeProbeLoopMaxBackoff     = 5 * time.Minute
	// R51 审计 P3：loopOnce 只在 panic 或 ctx/stop 时返回，"健康运行"只能
	// 用运行时长度量。上一轮存活 ≥3 个 tick（90s，真正干过活）后的 panic
	// 视作偶发故障——退避重置到起步值，不再被历史 panic 序列把重启间隔
	// 永久钉在封顶值。
	nodeProbeLoopHealthyRunAge = 3 * nodeProbeTickInterval
)

// nodeProbeWorkerRestartsTotal 统计 loop panic 自动重启次数（R51 审计 P3）。
// 只增不减：健康运行重置的是退避，不是计数——用于发现反复 panic 的 worker。
var nodeProbeWorkerRestartsTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "llmgw_node_probe_worker_restarts_total",
	Help: "Total number of node_probe_worker loop restarts after a recovered panic.",
})

// nextProbeLoopBackoff 计算 panic 重启后的下一次退避：上一轮健康运行足够久
// 则重置到起步值；否则翻倍、封顶。（原实现无健康重置，且封顶判断有误——
// 240s<5min 会翻成 8min——本函数把封顶修正为真正的 min(×2, 5min)。）
func nextProbeLoopBackoff(prevBackoff, previousRun time.Duration) time.Duration {
	if previousRun >= nodeProbeLoopHealthyRunAge {
		return nodeProbeLoopInitialBackoff
	}
	if prevBackoff < nodeProbeLoopMaxBackoff {
		if next := prevBackoff * 2; next < nodeProbeLoopMaxBackoff {
			return next
		}
	}
	return nodeProbeLoopMaxBackoff
}

// loop 是 loopOnce 的守护包装（R50 审计 P3）：原实现 recover 后直接返回，
// 探测一旦 panic 即静默停摆且无任何调度面感知。现 panic 后按 30s 起步、
// 5min 封顶的指数退避自动重启；ctx 取消 / Stop 时正常退出。
// R51 审计 P3：重启打 Prometheus 计数 + 带累计值/上轮时长的日志；健康运行
// （≥3 tick）后的 panic 重置退避。
func (w *NodeProbeWorker) loop(ctx context.Context) {
	backoff := nodeProbeLoopInitialBackoff
	restarts := 0
	for {
		started := time.Now()
		w.loopOnce(ctx)
		ran := time.Since(started)
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-time.After(backoff):
		}
		// 能走到这里说明上一轮 loopOnce 是 panic 退出（正常退出只经由
		// ctx/stop，那两个分支已在上面 return）。
		restarts++
		nodeProbeWorkerRestartsTotal.Inc()
		backoff = nextProbeLoopBackoff(backoff, ran)
		slog.Warn("node_probe_worker: loop restarted after recovered panic",
			"restarts_total", restarts,
			"previous_run", ran.Round(time.Second),
			"next_backoff", backoff)
	}
}

func (w *NodeProbeWorker) loopOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("node_probe_worker panic", "recover", r)
		}
	}()
	// Unified-queue mode: execution is owned by ProbeQueueWorker + ProbeService.
	// The legacy node_probe_state picker is disabled to avoid double execution.
	// ProbeSync (synchronous request-path probe) does not use this loop.
	if w.UseProbeQueue() {
		slog.Info("node_probe_worker: unified-queue mode, legacy picker disabled")
		// 2026-08-18 fix (glm-5.2 outage): with the picker parked, NOTHING fed
		// the 569 due node_probe_state rows into the queue — the rows sat
		// "ready" on dashboards forever while the durable queue starved
		// (no traffic → no request_failure submissions). Pump due rows into
		// the queue on a slow tick; dedup keys make double-enqueue a no-op.
		pumpTicker := time.NewTicker(nodeProbeQueuePumpInterval)
		defer pumpTicker.Stop()
		w.pumpDueStatesToQueue(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.stopCh:
				return
			case <-pumpTicker.C:
				w.pumpDueStatesToQueue(ctx)
			}
		}
	}
	ticker := time.NewTicker(nodeProbeTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.drainDue(ctx)
		case <-w.wakeCh:
			w.drainDue(ctx)
		}
	}
}

// SubmitWithSource is Submit with an explicit queue source. source 落到
// credential_probe_queue.source 与 node_probe_runs.trigger_kind，供自检流
// origin 徽章与审计归因消费，必须是 538 迁移 CHECK 约束允许的枚举值
// （request_failure/periodic/external_async/admin/integrity_probe_planner/
// selfcheck）。2026-09-08 审计：主动扫描器（today-success 等）此前走
// Submit 被静默归因为 request_failure，审计与看板全部失真。legacy 直写
// 路径（probeQueue == nil）没有 source 列，回退 Submit 语义。
func (w *NodeProbeWorker) SubmitWithSource(credID int, model, tenantID, parentReqID, source string) {
	if w == nil {
		return
	}
	if model == "" {
		return
	}
	if w.probeQueue != nil {
		if _, err := w.submitViaQueueSource(credID, model, tenantID, parentReqID, source); err != nil {
			slog.Warn("node_probe_worker: submit via queue failed",
				"credential_id", credID, "model", model, "source", source, "error", err)
		}
		return
	}
	w.Submit(credID, model, tenantID, parentReqID)
}

// Submit enqueues a (credID, model) pair for probing.  Called by the
// state manager when a real request fails.  Idempotent: if the row
// is currently in flight (in-memory `inFlight` map in cycle) the
// DB write is a no-op; pickDueAtomically's SKIP LOCKED handles
// cross-instance dedup.
func (w *NodeProbeWorker) Submit(credID int, model, tenantID, parentReqID string) {
	if w == nil {
		return
	}
	if model == "" {
		return
	}
	// Unified-queue mode (需求 6): route through credential_probe_queue instead
	// of UPSERT-ing node_probe_state. The dedup_key is the canonical node-probe
	// ID; ON CONFLICT DO NOTHING preserves any in-flight backoff (no collapse).
	// next_run_at = now+5s mirrors the legacy "first retry in 5s" arming; the
	// queue worker's completeFailure advances the 7-step chain on subsequent
	// failures. MaxAttempts caps the chain at nodeProbeMaxAttempts (7).
	if w.probeQueue != nil {
		w.submitViaQueue(credID, model, tenantID, parentReqID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 2026-07-15 P0 fix (B-series cascade follow-up): the previous
	// Submit set in_flight_until = now() + 5 minutes, which the
	// pickDueAtomically filter combined with `in_flight_until <= now()`
	// to dead-letter the row for 5 minutes after every real
	// failure. Worse, after 7 consecutive failures runOne would
	// `paused = TRUE` and never auto-resume. The combined effect
	// was the "I never see the probe after a failure" complaint
	// logged by operators on 2026-07-15 — the worker logged
	// `submit` for each failure but the next cycle's pickDue
	// skipped the row because in_flight_until was always in the
	// future.
	//
	// Fix: Submit now writes a next_retry_at 5 seconds in the future
	// and leaves in_flight_until NULL. Paused rows are unpaused
	// (with consecutive_failures reset) so any fresh real failure
	// can restart the probe cycle. The in-memory `inFlight` map
	// plus the SELECT FOR UPDATE SKIP LOCKED in pickDueAtomically
	// are the only two dedup mechanisms.
	//
	// 2026-07-16 (audit follow-up — backoff collapse): the previous
	// Submit's ON CONFLICT unconditionally took LEAST(next_retry_at,
	// now+5s) and reset consecutive_failures=0. Combined with
	// credentialstate.Manager firing Submit on every failure where
	// ConsecutiveFails>=2, the 5s/30s/60s/5m/1h/2h/24h backoff ladder
	// never advanced — each fresh user failure pulled the row back to
	// the 5s rung and reset the counter, so runOne always computed
	// attempt=1. Result: the same credential could be probed roughly
	// every (round_duration+5s) under sustained failure, producing the
	// "lots of probe tiles in the live stream" symptom operators saw.
	//
	// Fix: ON CONFLICT now only re-arms when the row is EITHER paused
	// (restart the cycle from scratch) OR has no future next_retry_at
	// (a previous Submit's 5s window has already elapsed and runOne
	// has either processed it or is about to). When the row already
	// has a future next_retry_at — i.e. runOne is mid-cycle and the
	// ladder has been legitimately escalated — we update only the
	// audit columns and leave next_retry_at / consecutive_failures /
	// in_flight_until alone, so the backoff chain
	// (5s→30s→60s→5m→1h→2h→24h) can actually advance.
	//
	// consecutive_failures is reset only for paused rows (re-arm from
	// scratch); for already-expired rows it is left at its current
	// value so runOne's increment by 1 (line 331: attempt = state+1)
	// correctly reflects "one more failed probe round on top of the
	// previous ladder position" rather than collapsing back to rung 1.
	//
	// The 2-second flash-protection in Manager.UpdateOnFailure still
	// prevents transient noise (a success within 2s) from re-arming.
	//
	// 2026-09-20 probe-volume policy (INV-2): the re-arm condition gains a
	// healthy-parked branch. Success now parks rows 30 days out
	// (MarkNodeProbeHealthy / mirrorNodeProbeState / runOne success), so a
	// future next_retry_at alone no longer implies "mid-ladder" — it is
	// usually a healthy row parked by design. A fresh REAL failure must
	// restart tracking immediately for exactly those rows. Ladder rows
	// (last_err_code set or consecutive_failures > 0) keep the 2026-07-16
	// semantics: their schedule is left alone so the chain can advance.
	_, _ = w.db.Exec(ctx, nodeProbeSubmitUpsertSQL(), credID, model)
	key := fmt.Sprintf("%d|%s", credID, model)
	w.mu.Lock()
	w.triggers[key] = nodeProbeTrigger{
		tenantID: tenantID,
		parentID: parentReqID,
	}
	if _, exists := w.wakeTimers[key]; !exists {
		w.wakeTimers[key] = time.AfterFunc(5*time.Second, func() {
			w.mu.Lock()
			delete(w.wakeTimers, key)
			w.mu.Unlock()
			nonBlockingWake(w.wakeCh)
		})
	}
	w.mu.Unlock()
	slog.Info("node_probe_worker: submit",
		"credential_id", credID, "model", model,
		"tenant_id", tenantID, "parent_request_id", parentReqID,
	)
	// 2026-08-11: mirror the "probe enqueued" transition to the 自检 SSE
	// stream so the tab shows the pending task immediately. The triggerKind
	// (request_failure / no_candidates) is carried as the Reason for context.
	w.publishProbeEvent(credID, model, "pending", "node_probe", parentReqID, 0)
}

// submitViaQueue is the unified-queue path for Submit. It enqueues a node_probe
// task into credential_probe_queue; the ProbeQueueWorker + ProbeService execute
// it. Best-effort on DB error: the source-parameterized helper
// submitViaQueueSource already retries + persists failures (P1.3), so this
// wrapper only logs at Warn when the helper ultimately fails.
func (w *NodeProbeWorker) submitViaQueue(credID int, model, tenantID, parentReqID string) {
	if _, err := w.submitViaQueueSource(credID, model, tenantID, parentReqID, "request_failure"); err != nil {
		slog.Warn("node_probe_worker: submit via queue failed",
			"credential_id", credID, "model", model, "source", "request_failure", "error", err)
	}
}

// enqueue routes submitViaQueueSource through ProbeQueue.Enqueue, or the
// injected test seam when wired (see NodeProbeWorker.enqueueFn).
func (w *NodeProbeWorker) enqueue(ctx context.Context, task ProbeQueueTask) (int64, bool, error) {
	if w.enqueueFn != nil {
		return w.enqueueFn(ctx, task)
	}
	return w.probeQueue.Enqueue(ctx, task)
}

// submitViaQueueSource is the source-parameterized enqueue used by Submit
// ("request_failure") and the scheduled pump ("periodic"). The source feeds
// the 自检 stream's origin badge and must stay inside the
// credential_probe_queue.source CHECK constraint.
//
// 2026-09-03 P1.3: bounded retry (default 3 attempts, 100ms / 250ms /
// 500ms backoff) plus persistent failure record. Behaviour:
//   - Each attempt runs under a 1s sub-context; the outer ctx
//     (caller-provided or 5s fallback) bounds total wall time.
//   - Duplicate dedup_keys are treated as success (peer or earlier attempt
//     already enqueued the same task) and recorded under outcome="duplicate".
//   - 2026-09-05 noise-reduction: deterministic gate rejections
//     (ErrProbeAutomaticIneligible / ErrProbeOutOfScope) are NOT retried.
//     They are evaluated from current credential/provider state at the queue
//     boundary and cannot flip between retries, so the old retry loop only
//     amplified one rejection into 3 WARNs + 1 ERROR per credential per pump
//     cycle (~75 ERRORs/cycle, docs 2026-09-05-pg-error-audit §5 P1). They
//     are now logged once at Info (mirroring the consumption-side branch in
//     probe_queue_worker.go::processTask), recorded under
//     outcome="skipped_ineligible" / "skipped_out_of_scope", and returned as
//     (false, nil) so no caller re-arms the row or re-logs the failure.
//     node_probe_state is deliberately left untouched (no
//     queue_submit_failed stamp) — a gate rejection is a business skip, not
//     an infrastructure failure.
//   - If all attempts fail on a transient error, the final error is persisted
//     to node_probe_state (UPDATE existing row or INSERT a placeholder row)
//     so operators have a queryable trail and Submit callers observe
//     non-nil error. We do NOT modify consecutive_failures /
//     next_retry_at / paused: those belong to the probe execution layer
//     (runOne) and must not be polluted by infrastructure flakiness.
//   - Every attempt and the final outcome are reflected in
//     llmgw_node_probe_queue_submission_total{source,outcome} and the
//     matching duration histogram so dashboards can alert on sustained
//     outcome=failed rates.
//
// Returns (inserted, err): inserted=true iff this call produced a
// new credential_probe_queue row. False on duplicate, deterministic gate
// skip, or error. The boolean is what pumpDueStatesToQueue's P1.2 holdoff
// branch consults.
func (w *NodeProbeWorker) submitViaQueueSource(credID int, model, tenantID, parentReqID, source string) (bool, error) {
	if w == nil || w.probeQueue == nil {
		err := fmt.Errorf("probe queue not initialized")
		nodeProbeQueueSubmissionTotal.WithLabelValues(source, "failed").Inc()
		return false, err
	}
	if tenantID == "" {
		tenantID = "default"
	}
	if source == "" {
		source = "request_failure"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	task := ProbeQueueTask{
		CredentialID: int64(credID),
		TenantID:     tenantID,
		RawModel:     model,
		Command:      "node_probe",
		Mode:         "multi_round",
		// 2026-08-13: 常用模型更高优先级（默认 80），抢占式先执行；非常用 60。
		Priority:    FeaturedQueuePriority(model, 60),
		MaxAttempts: nodeProbeMaxAttempts,
		NextRunAt:   time.Now().Add(5 * time.Second),
		Automatic:   source != "admin",
		Source:      source,
		ParentReqID: parentReqID,
		DedupKey:    buildNodeProbeTaskID(credID, model),
	}

	start := time.Now()
	var (
		inserted        bool
		lastErr         error
		firstAttemptErr error
	)
	for attempt := 1; attempt <= nodeProbeQueueSubmitMaxAttempts; attempt++ {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, time.Second)
		_, ins, err := w.enqueue(attemptCtx, task)
		attemptCancel()
		if err == nil {
			inserted = ins
			outcome := "success"
			if !ins {
				outcome = "duplicate"
			}
			nodeProbeQueueSubmissionTotal.WithLabelValues(source, outcome).Inc()
			nodeProbeQueueSubmissionDuration.WithLabelValues(source, outcome).Observe(time.Since(start).Seconds())
			if attempt > 1 {
				slog.Info("node_probe_worker: submit via queue succeeded after retry",
					"credential_id", credID, "model", model, "source", source,
					"attempts", attempt, "inserted", ins)
			} else {
				slog.Info("node_probe_worker: submit via queue",
					"credential_id", credID, "model", model, "source", source, "inserted", ins)
			}
			return inserted, nil
		}
		// 2026-09-05 noise-reduction: deterministic gate rejections must not
		// consume the retry budget. The eligibility gate (probe_queue.go
		// automaticTaskEligible) reads current credential/provider state and
		// the URSM scope gate is static for the process lifetime, so neither
		// can flip between two attempts milliseconds apart. Mirror the
		// consumption-side branch (probe_queue_worker.go processTask: single
		// Info + settle-as-skip) instead of 3 WARNs + 1 ERROR per credential
		// per pump cycle. No persistSubmitFailure: a disabled credential or a
		// disabled provider is a config state, not queue_submit_failed.
		if errors.Is(err, ErrProbeAutomaticIneligible) || errors.Is(err, ErrProbeOutOfScope) {
			outcome := "skipped_out_of_scope"
			if errors.Is(err, ErrProbeAutomaticIneligible) {
				outcome = "skipped_ineligible"
			}
			nodeProbeQueueSubmissionTotal.WithLabelValues(source, outcome).Inc()
			nodeProbeQueueSubmissionDuration.WithLabelValues(source, outcome).Observe(time.Since(start).Seconds())
			slog.Info("node_probe_worker: enqueue rejected by deterministic gate, skipped",
				"credential_id", credID, "model", model, "source", source,
				"outcome", outcome, "error", err)
			return false, nil
		}
		if attempt == 1 {
			firstAttemptErr = err
		}
		lastErr = err
		slog.Warn("node_probe_worker: enqueue via queue failed, retrying",
			"credential_id", credID, "model", model, "source", source,
			"attempt", attempt, "max_attempts", nodeProbeQueueSubmitMaxAttempts,
			"error", err)
		if attempt < nodeProbeQueueSubmitMaxAttempts {
			nodeProbeQueueSubmissionRetriesTotal.WithLabelValues(source).Inc()
			backoff := nodeProbeQueueSubmitBaseBackoff
			for i := 1; i < attempt; i++ {
				backoff = time.Duration(float64(backoff) * nodeProbeQueueSubmitBackoffMult)
				if backoff > nodeProbeQueueSubmitMaxBackoff {
					backoff = nodeProbeQueueSubmitMaxBackoff
					break
				}
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				nodeProbeQueueSubmissionTotal.WithLabelValues(source, "failed").Inc()
				nodeProbeQueueSubmissionDuration.WithLabelValues(source, "failed").Observe(time.Since(start).Seconds())
				return false, fmt.Errorf("enqueue probe task: ctx cancelled mid-retry: %w", ctx.Err())
			case <-timer.C:
			}
		}
	}

	// All attempts exhausted. Persist the failure so operators and the
	// next pump tick can both see it (best-effort: persistence failure
	// must not mask the original enqueue error).
	if w.db != nil {
		w.persistSubmitFailure(credID, model, firstAttemptErr, lastErr)
	}
	nodeProbeQueueSubmissionTotal.WithLabelValues(source, "failed").Inc()
	nodeProbeQueueSubmissionDuration.WithLabelValues(source, "failed").Observe(time.Since(start).Seconds())
	slog.Error("node_probe_worker: enqueue via queue exhausted retries",
		"credential_id", credID, "model", model, "source", source,
		"attempts", nodeProbeQueueSubmitMaxAttempts,
		"last_error", lastErr,
	)
	return false, fmt.Errorf("enqueue probe task: exhausted %d attempts: %w",
		nodeProbeQueueSubmitMaxAttempts, lastErr)
}

// persistSubmitFailure records the final submission error onto
// node_probe_state. It must NOT modify consecutive_failures /
// next_retry_at / next_retry_seconds / paused — those columns are owned
// by the probe-execution layer (runOne) and pollution here would skew
// the 5s→30s→60s→5m→1h→2h→6h backoff ladder for transient DB blips
// that have nothing to do with upstream health.
//
// We update last_err_code / last_err_detail / updated_at for an existing
// row, or INSERT a placeholder row (next_retry_at=now() so the next
// pump tick picks it up; paused=FALSE; in_flight_until=NULL) so the
// failure is queryable even on a credential that has not yet produced a
// real probe run. The error detail is truncated to
// nodeProbeQueueSubmitErrDetailMax bytes to stay within TEXT bounds.
func (w *NodeProbeWorker) persistSubmitFailure(credID int, model string, firstErr, lastErr error) {
	if w.db == nil || (firstErr == nil && lastErr == nil) {
		return
	}
	detail := lastErr
	if detail == nil {
		detail = firstErr
	}
	detailStr := detail.Error()
	if len(detailStr) > nodeProbeQueueSubmitErrDetailMax {
		detailStr = detailStr[:nodeProbeQueueSubmitErrDetailMax] + "…(truncated)"
	}
	firstStr := ""
	if firstErr != nil {
		firstStr = firstErr.Error()
		if len(firstStr) > nodeProbeQueueSubmitErrDetailMax {
			firstStr = firstStr[:nodeProbeQueueSubmitErrDetailMax] + "…(truncated)"
		}
	}
	// Use a short independent ctx so a caller-cancelled ctx cannot stop us
	// from recording the failure.
	persistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := w.db.Exec(persistCtx, `
		UPDATE node_probe_state
		SET last_err_code  = $3,
		    last_err_detail = $4,
		    updated_at      = now()
		WHERE credential_id = $1 AND raw_model_name = $2
	`, credID, model, nodeProbeQueueSubmitErrCode, detailStr); err != nil {
		slog.Warn("node_probe_worker: persist submit failure (update) failed",
			"credential_id", credID, "model", model, "error", err)
		return
	}
	// INSERT path uses ON CONFLICT DO NOTHING so we never overwrite a row
	// that the probe worker has already started managing. Keep the normal
	// probe backoff untouched; this timestamp only rate-limits infrastructure
	// submission failures while the queue/database is unavailable.
	if _, err := w.db.Exec(persistCtx, `
		INSERT INTO node_probe_state (
			credential_id, raw_model_name,
			consecutive_failures, consecutive_successes,
			last_attempt_at, next_retry_at, next_retry_seconds,
			paused, in_flight_until,
			last_direct_ok, last_gateway_ok,
			last_err_code, last_err_detail,
			updated_at
		) VALUES (
			$1, $2, 0, 0,
			NULL, now() + $5::interval, 5,
			FALSE, NULL,
			NULL, NULL,
			$3, $4,
			now()
		)
		ON CONFLICT (credential_id, raw_model_name) DO NOTHING
	`, credID, model, nodeProbeQueueSubmitErrCode, detailStr,
		fmt.Sprintf("%d seconds", int(nodeProbeQueueSubmitFailureHoldoff.Seconds()))); err != nil {
		slog.Warn("node_probe_worker: persist submit failure (insert) failed",
			"credential_id", credID, "model", model, "error", err)
	}
	if firstStr != "" && firstStr != detailStr {
		slog.Warn("node_probe_worker: enqueue retries did not improve the error",
			"credential_id", credID, "model", model,
			"first_error", firstStr, "last_error", detailStr)
	}
}

// pumpDueStatesToQueue feeds due node_probe_state rows into the unified
// credential_probe_queue. In unified-queue mode the legacy picker loop is
// parked, so these rows (written by failures, watchdogs, and the hourly
// healthy-mark reconciler) had no consumer at all — the dashboards kept
// counting them as "ready probes" while zero probes executed (2026-08-18
// glm-5.2 incident: 569 ready / 0 probing, oldest due since 2026-06-19).
// Pumping keeps node_probe_state as the scheduling surface and the queue as
// the execution surface. Enqueue dedup (buildNodeProbeTaskID) makes repeat
// pumps a no-op; the holdoff UPDATE stops a tight re-pump loop.
func (w *NodeProbeWorker) pumpDueStatesToQueue(ctx context.Context) {
	if w == nil || w.db == nil || w.probeQueue == nil {
		return
	}
	qCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := w.db.Query(qCtx, pumpDueStatesSQL(), nodeProbeQueuePumpBatch)
	if err != nil {
		slog.Warn("node_probe_worker: pump due states query failed", "error", err)
		return
	}
	defer rows.Close()
	type dueRow struct {
		credID int
		model  string
		tenant string
	}
	var due []dueRow
	for rows.Next() {
		var r dueRow
		if err := rows.Scan(&r.credID, &r.model, &r.tenant); err != nil {
			slog.Warn("node_probe_worker: pump due states scan failed", "error", err)
			return
		}
		due = append(due, r)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("node_probe_worker: pump due states iterate failed", "error", err)
		return
	}
	for _, r := range due {
		// 'periodic' (not 'request_failure'): these are scheduled re-probes,
		// and the CHECK constraint on credential_probe_queue.source rejects
		// anything outside its enum anyway.
		//
		// 2026-09-03 P1.2 fix: only update next_retry_at when probe submission
		// succeeds. If submission fails, keep the old next_retry_at so the next
		// recovery cycle can retry. This ensures the backoff ladder actually
		// executes probes instead of silently advancing the schedule without
		// doing any work.
		//
		// 2026-09-03 P1.3 follow-on: submitViaQueueSource returns
		// (inserted, error). The `inserted` flag tells us whether THIS call
		// produced a new row in credential_probe_queue (vs. dedup-collapsing
		// an existing one). For the holdoff branch it does not matter —
		// either way the queue now owns the task — so we keep the P1.2
		// behaviour: any non-error advances next_retry_at by
		// nodeProbeQueuePumpHoldoff so the next tick doesn't re-pump before
		// the queue can claim the row.
		if _, err := w.submitViaQueueSource(r.credID, r.model, r.tenant, "", "periodic"); err != nil {
			slog.Warn("node_probe_worker: pump submit failed, will retry in next cycle",
				"credential_id", r.credID, "model", r.model, "error", err)
			continue
		}
		// Submission succeeded (or dedup-collapsed): advance the row so the
		// next tick doesn't re-pump it before the queue has a chance to
		// execute/claim it. Real outcomes (success reset / failure backoff)
		// overwrite this via mirrorNodeProbeState.
		if _, err := w.db.Exec(qCtx, `
				UPDATE node_probe_state
				SET next_retry_at = now() + $3::interval, updated_at = now()
				WHERE credential_id = $1 AND raw_model_name = $2 AND next_retry_at <= now()`,
			r.credID, r.model, fmt.Sprintf("%d seconds", int(nodeProbeQueuePumpHoldoff.Seconds()))); err != nil {
			slog.Warn("node_probe_worker: pump holdoff update failed",
				"credential_id", r.credID, "model", r.model, "error", err)
		}
	}
	if len(due) > 0 {
		slog.Info("node_probe_worker: pumped due states to queue", "count", len(due))
	}
}

// pumpDueStatesSQL is extracted for guard tests (same pattern as
// probe_queue.go reviveExpiredReadySQL). Beyond the paused/due filters it
// applies the SAME automatic-probe eligibility gate the queue enforces at
// Enqueue (automaticProbeEligibilityExistsSQL — credential active +
// lifecycle active + not manually disabled, provider enabled + not manually
// disabled). Rows for disabled credentials/providers are rejected
// deterministically at the queue boundary (ErrProbeAutomaticIneligible), so
// pumping them could never succeed — it only produced the per-cycle
// "enqueue via queue exhausted retries" ERROR spam (3 WARNs + 1 ERROR per
// credential per 30s tick, ~75 ERRORs/cycle; docs
// 2026-09-05-pg-error-audit-and-environment §5 P1). Keeping the gate in
// WHERE (evaluated before ORDER BY/LIMIT) means ineligible rows do not
// consume the nodeProbeQueuePumpBatch slots, and they re-enter the pump
// automatically the moment their credential/provider is re-enabled — probe
// semantics are unchanged, the futile enqueue attempts are gone. The outer
// alias is `cred` so it cannot shadow the EXISTS fragment's own
// `credentials c` / `providers p` aliases.
//
// 2026-09-11 P3 (handoff 2026-09-11-selfcheck-necessity-gate 遗留项 5): the
// pump also filters rows whose binding chain is broken — no
// credential_model_bindings row, dangling cmb.provider_model_id, or
// provider_models.raw_model_name mismatch. resolveDirectTarget returns "no
// rows" for exactly these pairs, so each pumped row ran a full doomed probe
// lifecycle (claim → in-flight tile → endpoint-build failure → audit row)
// and the missing-binding short-circuit in ProbeService.Run then dropped the
// state with a best-effort DELETE — which, on failure, left the row behind
// for the next pump tick to re-enqueue: the same churn loop the necessity
// skip path had, minus the retry or the counters. The pump is the ONLY
// recurring scheduler that ignored the binding chain (every
// credential_recovery path — recoverExpiredBindings, the stale-state
// reconciler, the lookback scan — JOINs it), so filtering here closes the
// loop at its source. The predicate is deliberate EXISTENCE ONLY (no
// available/manual gates): resolveDirectTarget probes any pair whose binding
// merely exists, so a stricter filter would stop pumping probeable rows, and
// a (re)created binding lets the row re-enter the pump automatically —
// mirroring the eligibility gate's re-entry semantics.
//
// 2026-09-20 probe-volume policy (INV-1): the pump only enqueues rows with
// row-level error evidence (recorded err code, pending failure counter, or
// never confirmed healthy). Healthy-parked rows — including legacy rows whose
// 30-day-parked next_retry_at has elapsed — stay out: probing a pair that
// succeeded (business or probe) with zero error signal is exactly the
// "normal-state continuous probing" this policy removes. The first real
// failure flips the row back into this filter via Submit's re-arm.
func pumpDueStatesSQL() string {
	return `
		SELECT nps.credential_id, nps.raw_model_name, COALESCE(cred.tenant_id, 'default')
		FROM node_probe_state nps
		JOIN credentials cred ON cred.id = nps.credential_id
		WHERE nps.paused = FALSE AND nps.next_retry_at <= now()
		  AND ` + nodeProbeErrorEvidenceSQL("nps") + `
		  AND ` + automaticProbeEligibilityExistsSQL("nps.credential_id") + `
		  AND EXISTS (
			SELECT 1
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			WHERE cmb.credential_id = nps.credential_id
			  AND pm.raw_model_name = nps.raw_model_name
		  )
		ORDER BY nps.next_retry_at
		LIMIT $1`
}

// publishProbeTask TaskType uses task.Command so node_probe / integrity_verify /
// selfcheck tasks render with their own type on the 自检 stream (2026-08-13).

// publishProbeEvent is the shared hook for node-probe lifecycle events that
// are NOT the terminal completed/failed (those go through ActiveProbeEmitter).
// It is best-effort: a nil sink or a publish error never blocks the worker.
//
// The task ID is shared with ActiveProbeEmitter.publishSink via
// buildNodeProbeTaskID so the SSE tile is updated in place across the full
// pending → in-flight → completed/failed lifecycle.
func (w *NodeProbeWorker) publishProbeEvent(credID int, model, status, source, reason string, attempt int) {
	if w == nil || w.probeSink == nil {
		return
	}
	w.probeSink.PublishProbeEvent(ProbeStreamEvent{
		ID:           buildNodeProbeTaskID(credID, model),
		TaskType:     "node_probe",
		Source:       source,
		Status:       status,
		CredentialID: int64(credID),
		RawModel:     model,
		Attempt:      attempt,
		Reason:       reason,
		TimestampMs:  time.Now().UnixMilli(),
	})
}

func nonBlockingWake(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// MarkNodeProbeHealthy is the public hook for code paths outside
// NodeProbeWorker (e.g. the manual "全面探测" / TriggerAllSync flow)
// that successfully probe a (credential, model) pair and want the
// row in node_probe_state to immediately reflect healthy.
//
// Without this, the routing view v_routable_credential_models keeps
// excluding the binding until NodeProbeWorker's natural backoff
// ladder rolls over — up to the 6h ladder cap after the most recent
// failure (the ladder no longer parks rows indefinitely).
//
// The "next_retry_at = now() + 30 days" choice is the 2026-09-20 probe-volume
// policy: park, don't re-arm (docs/probe/2026-09-20-probe-volume-optimization.md).
// The pre-2026-09-20 value (+1h) meant every successful business request armed
// another probe one hour later, so healthy in-use pairs were re-probed
// indefinitely by the pump/reconcile loop. A healthy-parked row
// (last_direct_ok=TRUE, no err, no failure counter) is now invisible to every
// scheduler; the first new real failure re-arms it to now()+5s via Submit's
// ON CONFLICT healthy-parked branch.
//
// 2026-09-08: also writes through to credential_model_bindings and
// credentials (syncHealthyNodeSurfaces). The executor calls this on every
// successful business request, so both statements are guarded to be 0-row
// no-ops when the surfaces are already healthy.
func MarkNodeProbeHealthy(ctx context.Context, db *pgxpool.Pool, credentialID int, rawModel string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO node_probe_state (
			credential_id, raw_model_name,
			consecutive_failures, consecutive_successes,
			last_attempt_at, next_retry_at, next_retry_seconds,
			paused, in_flight_until,
			last_direct_ok, last_gateway_ok,
			last_err_code, last_err_detail,
			updated_at
		) VALUES ($1, $2, 0, 1, now(), now() + interval '30 days', 2592000, FALSE, NULL, TRUE, TRUE, NULL, NULL, now())
		ON CONFLICT (credential_id, raw_model_name) DO UPDATE
		SET consecutive_failures = 0,
		    consecutive_successes = node_probe_state.consecutive_successes + 1,
		    last_attempt_at = now(),
		    next_retry_at = now() + interval '30 days',
		    next_retry_seconds = 2592000,
		    paused = node_probe_state.paused,
		    in_flight_until = NULL,
		    last_direct_ok = TRUE,
		    last_gateway_ok = TRUE,
		    last_err_code = NULL,
		    last_err_detail = NULL,
		    updated_at = now()
	`, credentialID, rawModel)
	if err != nil {
		return err
	}
	return syncHealthyNodeSurfaces(ctx, db, credentialID, rawModel)
}

// finishProbe releases the inFlight slot for key AND detaches every
// ProbeSync caller that attached as a syncWaiter for key, all under a
// single critical section. It is the single completion hook used by
// both cycle() (background probe) and the fresh-job goroutine in
// ProbeSync (synchronous probe), guaranteeing a uniform
// release→detach→close order across the two completion paths.
//
// Order matters: the slot must be released before the waiters are
// detached. A waiter that wakes and immediately retries ProbeSync must
// find the slot free and start a fresh probe. If the slot were still
// held, the retry would register a new waiter whose channel can never
// be closed (this routine deletes the map entry when it fires),
// burning the caller's entire ctx budget.
//
// Closing happens AFTER the in-flight slot release, so any waiter's
// subsequent re-PlanCandidates observes the recovered availability.
func (w *NodeProbeWorker) finishProbe(key string) {
	if w == nil {
		return
	}
	// 2026-08-07 fix: drain the in-flight slot AND the waiters for the
	// round we just completed in one critical section. Doing the two
	// ops under separate locks would let a fresh ProbeSync caller
	// re-register on the same key after the slot was released but
	// before the waiter list was detached — the close loop below
	// would then close the new round's waiter and leave the new
	// round hanging forever (the map entry was deleted, so its
	// channel could never be closed again).
	//
	// The close loop runs OUTSIDE the w.mu critical section: closing
	// the channels can take arbitrarily long and we must not hold
	// w.mu while doing it. The snapshot we hand it is safe — no
	// further goroutine can race in and append to this round's
	// waiter slice because the map entry was deleted under
	// syncWaitersMu first.
	w.mu.Lock()
	delete(w.inFlight, key)
	w.syncWaitersMu.Lock()
	chs := append([]chan struct{}(nil), w.syncWaiters[key]...)
	delete(w.syncWaiters, key)
	w.syncWaitersMu.Unlock()
	w.mu.Unlock()
	for _, ch := range chs {
		close(ch)
	}
}

// ProbeSync fans out parallel direct→gateway probes for the supplied
// candidates and reports whether at least one pair recovered.
//
// Behaviour
// ─────────
//   - Total wall time bounded by ctx. The executor passes a 5s timeout
//     derived from the user's r.Context(); client disconnect cancels
//     the inner HTTP requests via the same ctx.
//   - For each (cred, model) that is NOT already in-flight (i.e. the
//     background cycle() did not start it in the last 5 minutes),
//     ProbeSync runs a new direct probe under nodeProbeSyncFanout
//     concurrent goroutines. The first direct.ok pair immediately
//     promotes to a gateway round; if the gateway round also passes
//     the function returns true.
//   - For each (cred, model) that IS in-flight, ProbeSync waits on a
//     per-key channel that cycle() closes at runOne completion. This
//     keeps concurrent no_candidate requests from issuing duplicate
//     probes against the same credential.
//   - The direct round's state-cache writes (updateObservedState +
//     invalidateCandidateCache) are issued the same way cycle() does
//     them, so the routing layer's next PlanCandidates sees the new
//     availability. ProbeSync does NOT modify node_probe_state — the
//     background backoff ladder is the worker's job, not the request's.
//   - Audit rows are written with trigger_kind="sync_request" so
//     dashboards can distinguish synchronous probes from the
//     request_failure / tick-driven ones.
//
// Returns true iff at least one (cred, model) is reachable directly from the
// supplier. The gateway round is still executed and recorded for diagnostics,
// but a gateway-side probe failure must not hide a recovered supplier node
// from the request router.
func (w *NodeProbeWorker) ProbeSync(
	ctx context.Context,
	candidates []credentialstate.NoCandidatesCandidate,
	tenantID string,
	parentReqID string,
) bool {
	start := time.Now()
	defer func() {
		nodeProbeSyncDuration.Observe(time.Since(start).Seconds())
	}()

	if w == nil || len(candidates) == 0 {
		nodeProbeSyncTotal.WithLabelValues("skipped").Inc()
		return false
	}

	type syncJob struct {
		key    string
		credID int
		model  string
		reuse  bool // wait for in-flight instead of running new
		waitCh chan struct{}
	}

	jobs := make([]syncJob, 0, len(candidates))
	freshJobs := make([]syncJob, 0, len(candidates))
	for _, c := range candidates {
		if c.CredentialID == 0 || strings.TrimSpace(c.RawModel) == "" {
			continue
		}
		key := fmt.Sprintf("%d|%s", c.CredentialID, c.RawModel)

		// Single critical section for check + (register waiter | reserve
		// slot). 2026-08-07 fix: the inFlight check and the waiter
		// registration / slot reservation used to be two separate w.mu
		// critical sections, so two concurrent ProbeSync callers for the
		// same key could BOTH observe the slot free and both launch a
		// fresh probe (duplicate direct probes), and a waiter registering
		// between the owner's release and notify would be attached to a
		// map entry that finishProbe had already deleted — its channel
		// could never close. Merging the check, the waiter registration
		// and the reservation under one w.mu critical section makes
		// "one in-flight probe per (cred,model)" hold for synchronous
		// callers too. Lock order: w.mu → syncWaitersMu.
		w.mu.Lock()
		if _, busy := w.inFlight[key]; busy {
			ch := make(chan struct{})
			w.syncWaitersMu.Lock()
			w.syncWaiters[key] = append(w.syncWaiters[key], ch)
			w.syncWaitersMu.Unlock()
			w.mu.Unlock()
			nodeProbeSyncInflightWaiters.Inc()
			jobs = append(jobs, syncJob{key: key, credID: c.CredentialID, model: c.RawModel, reuse: true, waitCh: ch})
			continue
		}
		w.inFlight[key] = struct{}{}
		w.triggers[key] = nodeProbeTrigger{tenantID: tenantID, parentID: parentReqID}
		w.mu.Unlock()

		freshJobs = append(freshJobs, syncJob{key: key, credID: c.CredentialID, model: c.RawModel})
		jobs = append(jobs, freshJobs[len(freshJobs)-1])
	}

	type freshResult struct {
		job     syncJob
		direct  nodeProbeRoundResult
		gateway nodeProbeRoundResult
	}

	results := make(chan freshResult, len(freshJobs))
	sem := make(chan struct{}, nodeProbeSyncFanout)
	var wg sync.WaitGroup
	for _, j := range freshJobs {
		j := j
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Wave 3 B2③: per-credential ≤2 on top of the global fanout.
			// R57 §三.2：获取序为 per-cred → 全局（原为全局先取、goroutine
			// 内后取 per-cred——同凭据 N 个 (cred,model) 突发可在 per-cred
			// 闸前占满全部 8 个全局槽，把其它凭据的热路径探测饿在
			// sem<- 上）。改为先取 per-cred 再取全局：落选的突发 goroutine
			// 停在 credSem<- 上不占全局槽，单凭据最多经 ≤2 闸支配 2 个全
			// 局槽。无死锁：全局槽持有者已持 per-cred 槽，完成路径不再
			// 依赖任何其它资源（锁序不变：w.mu → syncWaitersMu）。
			credSem := w.credSemaphore(j.credID)
			credSem <- struct{}{}
			defer func() { <-credSem }()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Ensure the in-flight slot for THIS key is released and any
			// concurrent ProbeSync caller that attached as a reuse waiter
			// is woken once the probe completes. cycle() would not fire
			// for this key (we own the slot), so without this the waiter
			// would burn its entire ctx budget. finishProbe releases the
			// slot BEFORE notifying, so a woken caller that immediately
			// retries finds the slot free (fresh path) instead of
			// re-registering a waiter that can never be closed.
			defer w.finishProbe(j.key)
			res := freshResult{job: j}
			res.direct = w.probeDirect(ctx, j.credID, j.model)
			if res.direct.ok {
				// CRITICAL: update state BEFORE probeGateway so the
				// routing layer sees the restored credential, not the
				// stale cooling state left by the original 5xx.
				w.updateBindingAvailability(ctx, j.credID, j.model, true, "", 0, "")
				w.updateCredentialHealth(ctx, j.credID)
				w.updateObservedState(ctx, j.credID, j.model, true, "", time.Now())
				// 2026-08-24: smart-fallback tentative restore (需求 6
				// bullet 6). A sync probe "passed in isolation" — stamp a
				// revert deadline so bg.ProbeRollback reverts the binding
				// if no confirming probe success (which clears
				// probe_revert_at via updateBindingAvailability's success
				// branch) lands within the window. Confirmation arrives
				// naturally: the tick/queue still probes this (cred,model)
				// because emitSyncAudit deliberately does NOT touch
				// node_probe_state (next_retry_at unchanged). Disabled
				// entirely when the window env is 0/off.
				if revertAfter := probeTentativeRevertAfter(); revertAfter > 0 {
					markCtx, markCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
					if err := MarkTentativeRestore(markCtx, w.db, j.credID, j.model, revertAfter); err != nil {
						slog.Warn("node_probe_worker: tentative restore stamp failed",
							"credential_id", j.credID, "model", j.model, "error", err)
					}
					markCancel()
				}
				res.gateway = w.probeGateway(ctx, j.credID, j.model)
			} else {
				// 2026-09-17: gateway-side errors (decrypt/endpoint build) must
				// not touch the shared availability surfaces — same doctrine as
				// the tick path. updateBindingAvailability also refuses these
				// errCodes internally; this branch additionally protects the
				// observed-state surface.
				if isGatewaySideProbeError(res.direct.errCode) && !w.credentialSpecificDecryptFailure(res.direct.errDetail) {
					slog.Error("node_probe_worker: gateway-side direct probe error (sync) — not updating availability",
						"credential_id", j.credID,
						"model", j.model,
						"err_code", res.direct.errCode,
						"err_detail", res.direct.errDetail)
				} else {
					// Sync probes carry no ladder attempt context; attempt=1
					// keeps the generic 5-minute cooldown for a first 404 —
					// the tick/queue paths escalate to the model-not-served
					// horizon once their attempt counts confirm it.
					w.updateBindingAvailability(ctx, j.credID, j.model, false, res.direct.errCode, 1, res.direct.errDetail)
					recoverAt := time.Now().Add(5 * time.Minute)
					w.updateObservedState(ctx, j.credID, j.model, false, res.direct.errCode, recoverAt)
				}
			}
			// 2026-08-18: the sync path also drives the URSM v2 authoritative
			// router. Without this write the probe "recovered" only the PG
			// binding while PlanCandidatesWithContext kept rejecting the node
			// for its missing/expired Redis node key, so the executor's retry
			// after ProbeSync returned 503 again (glm-5.2 outage on 154).
			// Success reflects the direct round only: the gateway round is
			// routed through this same URSM filter and can 503 circularly
			// while the key is missing.
			// R42: failures honor the gateway-side predicate (see runOne) —
			// a misconfigured instance must not poison the shared node key.
			if res.direct.ok || w.ursmFailureWritable(res.direct.errCode, res.direct.errDetail) {
				w.updateURSMv2ProbeState(ctx, tenantID, j.credID, j.model, res.direct.ok, res.direct.latencyMs)
			}
			if w.invalidateCandidateCache != nil {
				w.invalidateCandidateCache(j.credID)
			}
			if err := w.emitSyncAudit(ctx, j.credID, j.model, res.direct, res.gateway, start, parentReqID); err != nil {
				slog.Warn("node_probe_worker: sync audit persist failed",
					"credential_id", j.credID,
					"raw_model", j.model,
					"trigger_kind", "sync_request",
					"parent_request_id", parentReqID,
					"error", err)
			}
			results <- res
		}()
	}

	var (
		winnerMu     sync.Mutex
		winnerDirect nodeProbeRoundResult
		winnerGw     nodeProbeRoundResult
		winnerSet    bool
	)

	// 2026-07-27 concurrency fix: winnerSet / winnerDirect / winnerGw were
	// written under winnerMu but read unlocked (the `!winnerSet` fast-path
	// guards and the success log below). Harmless today — only this
	// goroutine writes them — but the mutex was dead weight and the
	// asymmetry invites a real race the moment a second writer appears.
	// Lock both sides consistently instead.
	winnerIsSet := func() bool {
		winnerMu.Lock()
		defer winnerMu.Unlock()
		return winnerSet
	}
	// takeWinner records the first recovered result. Returns nothing; the
	// double-check inside the lock keeps "first writer wins" semantics.
	takeWinner := func(direct, gw nodeProbeRoundResult) {
		winnerMu.Lock()
		if !winnerSet {
			winnerDirect = direct
			winnerGw = gw
			winnerSet = true
		}
		winnerMu.Unlock()
	}

	doneFresh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneFresh)
	}()

drainLoop:
	for {
		select {
		case res := <-results:
			if !winnerIsSet() && probeRecovered(res.direct) {
				takeWinner(res.direct, res.gateway)
			}
		case <-doneFresh:
			for {
				select {
				case res := <-results:
					if !winnerIsSet() && probeRecovered(res.direct) {
						takeWinner(res.direct, res.gateway)
					}
				default:
					break drainLoop
				}
			}
		case <-ctx.Done():
			break drainLoop
		}
	}

	// Wait for reuse jobs (in-flight background probes OR fresh-job
	// goroutines from another ProbeSync caller) to finish, then re-check
	// availability so we can report recovery when cycle() / the other
	// caller wrote availability=true. Without this re-check the reuse
	// path always returned false even on a genuine recovery, producing
	// a spurious 503 for every dedup-reuse request.
	for _, j := range jobs {
		if !j.reuse {
			continue
		}
		select {
		case <-j.waitCh:
			nodeProbeSyncInflightWaiters.Dec()
			// A background / sibling probe finished. Did it actually
			// mark this (cred,model) available? Re-check the cache.
			// IsAvailable does I/O — call it outside winnerMu.
			if !winnerIsSet() && w.stateProvider != nil {
				if avail, _ := w.stateProvider.IsAvailable(ctx, j.credID, j.model); avail {
					// We don't have the probe round result here;
					// synthesize a minimal winner record so the
					// success-path log line still fires.
					takeWinner(nodeProbeRoundResult{
						ok:            true,
						providerID:    j.credID,
						outboundModel: j.model,
					}, nodeProbeRoundResult{ok: true})
				}
			}
		case <-ctx.Done():
			nodeProbeSyncInflightWaiters.Dec()
		}
	}

	// Read the winner fields once under the lock, then log from the locals.
	winnerMu.Lock()
	finalSet, finalDirect, finalGw := winnerSet, winnerDirect, winnerGw
	winnerMu.Unlock()

	if finalSet {
		nodeProbeSyncTotal.WithLabelValues("recovered").Inc()
		slog.Info("node_probe_worker: sync probe recovered",
			"credential_id", finalDirect.providerID,
			"outbound_model", finalDirect.outboundModel,
			"direct_status", finalDirect.httpStatus,
			"gateway_status", finalGw.httpStatus,
			"tenant_id", tenantID,
			"parent_request_id", parentReqID,
			"elapsed_ms", time.Since(start).Milliseconds(),
		)
		return true
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			nodeProbeSyncTotal.WithLabelValues("client_cancel").Inc()
		} else {
			nodeProbeSyncTotal.WithLabelValues("timeout").Inc()
		}
	} else {
		nodeProbeSyncTotal.WithLabelValues("exhausted").Inc()
	}
	slog.Info("node_probe_worker: sync probe exhausted",
		"tenant_id", tenantID,
		"parent_request_id", parentReqID,
		"candidates", len(jobs),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)
	return false
}

// probeRecovered reports whether the supplier can serve the credential/model
// pair again. The direct round is authoritative for routing recovery because
// it isolates supplier availability from gateway-side probe failures. The
// gateway round remains an audit signal and is still emitted to diagnostics.
func probeRecovered(direct nodeProbeRoundResult) bool {
	return direct.ok
}

// credSemaphore returns the per-credential ProbeSync slot channel
// (Wave 3 B2③). Safe for concurrent use; entries are created on first use
// and never evicted (bounded by the credential count).
func (w *NodeProbeWorker) credSemaphore(credID int) chan struct{} {
	if v, ok := w.credSyncSem.Load(credID); ok {
		return v.(chan struct{})
	}
	v, _ := w.credSyncSem.LoadOrStore(credID, make(chan struct{}, nodeProbePerCredSyncConcurrency))
	return v.(chan struct{})
}

// nodeProbeConfirmSlotWait bounds how long ProbeConfirm waits for a
// per-credential direct-probe slot before each ping.
const nodeProbeConfirmSlotWait = 1500 * time.Millisecond

// acquireCredSlotForConfirm takes the shared per-credential ≤2 direct-probe
// gate with a bounded wait. R57 §三.2：ProbeConfirm 原先绕过 per-cred ≤2 闸
// （同凭据 N 模型同时确认 → 2N 并发直连），现在每次 ping 前有界等闸；闸的
// 睡眠窗口（首 ping 延迟/ping 间隔）不占槽。等待超时属容量饥饿而非节点状
// 态证据 → fail-open（false = 未确认损坏，不降级），与「确认才降级」教义
// 一致；ctx 取消仍按既有教义 fail-closed。返回释放函数与是否获得闸。
func (w *NodeProbeWorker) acquireCredSlotForConfirm(ctx context.Context, credID int) (func(), bool) {
	credSem := w.credSemaphore(credID)
	select {
	case credSem <- struct{}{}:
		return func() { <-credSem }, true
	case <-time.After(nodeProbeConfirmSlotWait):
		nodeProbeConfirmSlotStarved.Inc()
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}

// ProbeConfirm is the Wave 3 B2② flash-blip double confirmation. After a
// request-path error that would degrade the node, the caller defers the
// degrade and asks ProbeConfirm for the verdict: two lightweight direct
// pings run inside the design window (first after 2s, second after another
// 2s, whole confirm bounded by nodeProbeConfirmBudget). The node counts as
// confirmed broken — and the caller applies the deferred degrade — only when
// BOTH pings fail; any success means the error was a transient blip and the
// node keeps its healthy state.
//
// The pings go through probeDirect only: no state writes, no circuit
// recording, no node_probe_state mutation — the confirm must not stack
// breaker or probe-backoff effects on top of the request failure it
// verifies ("只影响状态写入不叠加熔断"). R57 §三.2：每次 ping 受共享的
// per-credential ≤2 直连闸约束（与 ProbeSync 同一 credSemaphore），等待
// 窗口不占槽。
//
// A nil worker or a cancelled context fails CLOSED (returns true) so a
// dead node cannot be kept routable by an unverifiable confirm.
func (w *NodeProbeWorker) ProbeConfirm(ctx context.Context, credID int, model string) bool {
	if w == nil {
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, nodeProbeConfirmBudget)
	defer cancel()
	round := w.probeConfirmRound
	if round == nil {
		round = w.probeDirect
	}
	time.Sleep(nodeProbeConfirmFirstPingDelay)
	release, ok := w.acquireCredSlotForConfirm(cctx, credID)
	if !ok {
		if cctx.Err() == nil {
			// 容量饥饿（非 ctx 取消）：无法验证 ≠ 已确认损坏 → fail-open。
			slog.Warn("node_probe_worker: confirm ping skipped on per-cred slot starvation (fail-open)",
				"credential_id", credID, "model", model)
			return false
		}
		return true
	}
	// R59 audit (S5-F3): release was inlined after round() — a round panic
	// leaked the slot and the per-cred capacity permanently dropped to ≤1.
	// defer makes the release panic-safe.
	first := func() (r nodeProbeRoundResult) {
		defer release()
		return round(cctx, credID, model)
	}()
	if first.ok {
		return false
	}
	select {
	case <-cctx.Done():
		return true
	case <-time.After(nodeProbeConfirmPingGap):
	}
	release2, ok := w.acquireCredSlotForConfirm(cctx, credID)
	if !ok {
		if cctx.Err() == nil {
			slog.Warn("node_probe_worker: confirm second ping skipped on per-cred slot starvation (fail-open)",
				"credential_id", credID, "model", model)
			return false
		}
		return true
	}
	second := func() (r nodeProbeRoundResult) {
		defer release2()
		return round(cctx, credID, model)
	}()
	return !second.ok
}

// emitSyncAudit writes a node_probe_runs row for a sync_request probe
// without touching node_probe_state. The columns mirror runOne() so
// downstream dashboards can filter by trigger_kind="sync_request".
func (w *NodeProbeWorker) emitSyncAudit(
	ctx context.Context,
	credID int,
	model string,
	direct nodeProbeRoundResult,
	gw nodeProbeRoundResult,
	startedAt time.Time,
	parentReqID string,
) error {
	auditDB := w.auditDB
	if auditDB == nil {
		auditDB = w.db
	}
	if auditDB == nil {
		return nil
	}
	now := time.Now()
	durationMs := int(now.Sub(startedAt).Milliseconds())
	// 2026-09-10: keep node_probe_runs.success semantics consistent with
	// runOne and probeRecovered — direct-round health. The gateway round
	// stays visible through the gateway_* columns.
	success := direct.ok
	cleaned := make(map[string]string, len(direct.requestHeaders))
	for k, v := range direct.requestHeaders {
		switch k {
		case "Authorization", "X-Api-Key", "x-api-key":
			continue
		}
		cleaned[k] = v
	}
	requestHeadersJSON := probeHeadersJSON(cleaned)

	_, err := auditDB.Exec(ctx, `
		INSERT INTO node_probe_runs (
			credential_id, raw_model_name, trigger_kind, attempt, next_retry_seconds,
			direct_ok, direct_http_status, direct_err_code, direct_latency_ms, direct_err_detail,
			gateway_ok, gateway_http_status, gateway_err_code, gateway_latency_ms, gateway_err_detail,
			success, started_at, completed_at, duration_ms,
			api_model, outbound_model, provider_id,
			request_url, request_headers, request_body, response_body,
			timeout_at_ms, via_proxy,
			trigger_request_id
		) VALUES (
			$1, $2, 'sync_request', 1, 0,
			$3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13, $14, $15, $16,
			$17, $18, $19,
			$20, $21::text::jsonb, $22, $23,
			$24, $25,
			$26
		)
	`,
		credID, model,
		direct.ok, direct.httpStatus, direct.errCode, direct.latencyMs, direct.errDetail,
		gw.ok, gw.httpStatus, gw.errCode, gw.latencyMs, gw.errDetail,
		success, startedAt, now, durationMs,
		model, direct.outboundModel, direct.providerID,
		direct.requestURL, requestHeadersJSON, direct.requestBody, direct.responseBody,
		direct.latencyMs, direct.viaProxy,
		parentReqID,
	)
	if err != nil {
		auditPersistFailedTotal.WithLabelValues("sync_request").Inc()
		return fmt.Errorf("insert node_probe_runs sync audit: %w", err)
	}
	return nil
}

func (w *NodeProbeWorker) drainDue(ctx context.Context) {
	// Instance-level decrypt circuit (2026-09-17): when this instance cannot
	// decrypt credential secrets there is no point picking work — every direct
	// round would fail endpoint_build and (before the shared-state guard) pollute
	// the shared availability tables. Paused here; the circuit half-opens after
	// decryptTripCooldown so a fixed key recovers automatically.
	if w.decryptCircuitTripped() {
		return
	}
	for i := 0; i < nodeProbeBatchSize; i++ {
		if !w.cycle(ctx) {
			return
		}
	}
}

// worker is in a low-traffic hot path and we want predictable DB pressure.
//
// Cross-instance isolation: pickDueAtomically issues a single
// SELECT ... FOR UPDATE SKIP LOCKED + UPDATE in_flight_until inside
// a transaction, so two gateway instances cannot both pick the same
// (cred, model) row at the same instant.  Combined with the in-memory
// dedup map this guarantees "one in-flight probe per (cred, model)
// globally".
func (w *NodeProbeWorker) cycle(ctx context.Context) bool {
	credID, model, ok, err := w.pickDueAtomically(ctx)
	if err != nil {
		slog.Warn("node_probe_worker: pick due failed", "error", err)
		return false
	}
	if !ok {
		return false
	}
	key := fmt.Sprintf("%d|%s", credID, model)
	w.mu.Lock()
	if _, busy := w.inFlight[key]; busy {
		w.mu.Unlock()
		return false
	}
	w.inFlight[key] = struct{}{}
	trigger := w.triggers[key]
	delete(w.triggers, key)
	w.mu.Unlock()

	// Release the slot and wake any ProbeSync callers that were waiting
	// on this in-flight background probe to complete (dedup reuse).
	// finishProbe releases inFlight BEFORE notifying, so a woken caller
	// that immediately retries finds the slot free (fresh path) instead
	// of re-registering a waiter whose channel could never be closed.
	// runOne's state-manager cache writes are visible to a waiter's
	// subsequent re-PlanCandidates because runOne completes before this
	// deferred call runs.
	defer w.finishProbe(key)

	if err := w.runOne(ctx, credID, model, "", trigger); err != nil {
		slog.Warn("node_probe_worker: runOne failed",
			"credential_id", credID, "model", model, "error", err)
	}

	return true
}

// pickDueAtomically selects the next-due (cred, model) row and marks
// it in-flight in a single transaction so concurrent gateway instances
// cannot pick the same row.  Uses SELECT ... FOR UPDATE SKIP LOCKED:
//
//	BEGIN;
//	  SELECT credential_id, raw_model_name
//	    FROM node_probe_state
//	   WHERE paused = FALSE AND next_retry_at <= now()
//	   ORDER BY next_retry_at ASC
//	   LIMIT 1
//	   FOR UPDATE SKIP LOCKED;
//	  UPDATE node_probe_state SET in_flight_until = now() + $1 WHERE ...;
//	COMMIT;
//
// Returns (credID, model, true, nil) on success, (0, "", false, nil)
// when nothing is due.
//
// 2026-07-16: in_flight_until is a post-pick lease, not a submit-time delay.
// Submit leaves it NULL, so filtering it here prevents cross-instance duplicate
// probes without delaying a newly submitted row.
//
// 2026-08-11: the previous 24h activity filter (last_attempt_at/updated_at >=
// now()-24h) silently dropped any node whose probe row had been idle for more
// than a day, so a credential that failed, cooled, then was forgotten would
// never be picked again — recovery depended entirely on the 60s
// credential_recovery tick re-submitting it. Widened to 7 days so a due
// (next_retry_at <= now()) row is always eligible; the bound remains only to
// keep centuries-old orphan rows out of the worker.
//
// 2026-09-08 self-check audit: the scan now applies the same
// automaticProbeEligibilityExistsSQL gate as pumpDueStatesSQL / ProbeQueue
// enqueue. The legacy tick path (LLM_GATEWAY_PROBE_QUEUE_ENABLED=false
// kill-switch) previously probed manually-disabled / retired credentials
// forever — spending probe calls against a credential the operator retired
// and letting the failure path flip its cmb state. Gating here (evaluated
// before ORDER BY/LIMIT) also keeps ineligible rows from consuming the
// pick; they re-enter automatically when the credential is re-enabled.
func (w *NodeProbeWorker) pickDueAtomically(ctx context.Context) (int, string, bool, error) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return 0, "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck

	var credID int
	var model string
	err = tx.QueryRow(ctx, `
			SELECT credential_id, raw_model_name
			FROM node_probe_state
			WHERE paused = FALSE
			  AND next_retry_at <= now()
			  AND (in_flight_until IS NULL OR in_flight_until <= now())
			  AND (last_direct_ok IS DISTINCT FROM TRUE
			       OR last_gateway_ok IS DISTINCT FROM TRUE)
			  AND (last_attempt_at >= now() - interval '7 days'
			       OR updated_at >= now() - interval '7 days')
			  AND `+automaticProbeEligibilityExistsSQL("node_probe_state.credential_id")+`
		ORDER BY next_retry_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`).Scan(&credID, &model)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, "", false, nil
		}
		return 0, "", false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE node_probe_state
		   SET in_flight_until = now() + $1::interval
		 WHERE credential_id = $2 AND raw_model_name = $3
	`, fmt.Sprintf("%d seconds", int(nodeProbeInFlightWindow.Seconds())), credID, model); err != nil {
		return 0, "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, "", false, err
	}
	return credID, model, true, nil
}

// clampAuditAttempt constrains the attempt number written to node_probe_runs
// to the table's CHECK domain (node_probe_runs_attempt_check: 1..7).
//
// R12 P2 (2026-09-11): since the 2026-07-15 P0 fix removed the pause at
// nodeProbeMaxAttempts, the legacy runOne path keeps incrementing
// node_probe_state.consecutive_failures past 7, and attempt = cf+1 grows
// unbounded — every probe of a long-failing (cred, model) pair then died on
// the audit INSERT with SQLSTATE 23514, losing the audit row after the probe
// had already run. The audit ladder only has 7 rounds, so clamp the audit
// metadata; the true failure count stays authoritative in node_probe_state.
// The unified-queue path clamps task.Attempt to [1, max_attempts] already,
// so this is a no-op there.
func clampAuditAttempt(a int) int {
	if a < 1 {
		return 1
	}
	if a > nodeProbeMaxAttempts {
		return nodeProbeMaxAttempts
	}
	return a
}

// runOne executes the two-round probe and updates the state row +
// audit log accordingly.  triggerKind is recorded on node_probe_runs;
// "" defaults to "request_failure".
func (w *NodeProbeWorker) runOne(ctx context.Context, credID int, model, triggerKind string, trigger nodeProbeTrigger) error {
	if triggerKind == "" {
		triggerKind = "request_failure"
	}
	startedAt := time.Now()

	state, err := w.loadState(ctx, credID, model)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	attempt := state.ConsecutiveFailures + 1
	// 2026-07-15 P0 fix: previously the worker paused the row forever
	// after nodeProbeMaxAttempts (7) consecutive failures, with no
	// way to unpause except a manual UPDATE. That meant a cred that
	// genuinely recovered (e.g. upstream transient) stayed hidden
	// from the router for hours/days, contributing to the
	// `no_candidates` cascade seen on 2026-07-15 14:00~15:35. We now
	// let the chained backoff in the else branch below stretch the
	// retry interval indefinitely (1h, 4h, 12h, 24h) so the worker
	// naturally throttles itself without permanently abandoning the
	// pair. Submit() below unpauses + resets failure count on any
	// fresh real failure, so the next user-visible 503 will restart
	// the cycle.
	if attempt > nodeProbeMaxAttempts {
		slog.Info("node_probe_worker: attempt cap reached, applying long backoff without pausing",
			"credential_id", credID, "model", model,
			"consecutive_failures", state.ConsecutiveFailures,
			"max_attempts", nodeProbeMaxAttempts)
	}

	// 2026-08-11: mirror the "probe now running" transition. This fires
	// after pickDueAtomically leased the row (the lease is what makes the
	// probe exclusive across instances), so subscribers see pending→in-flight.
	w.publishProbeEvent(credID, model, "in-flight", "node_probe", triggerKind, attempt)

	// Round 1: direct upstream
	direct := w.probeDirect(ctx, credID, model)
	// CRITICAL: update state BEFORE probeGateway so the routing layer
	// sees the restored credential, not the stale cooling state from
	// the original 5xx or transient failure.
	if direct.ok {
		w.updateBindingAvailability(ctx, credID, model, true, "", 0, "")
		w.updateCredentialHealth(ctx, credID)
		w.updateObservedState(ctx, credID, model, true, "", time.Now())
		if w.recordCircuitSuccess != nil {
			w.recordCircuitSuccess(direct.providerID, credID)
		}
	}
	// 2026-07-24 P0 fix: if direct probe returned endpoint_build with
	// "no rows in result set", it means the (cred, model) pair has no
	// row in credential_model_bindings — this is a configuration
	// problem (a node_probe_state row was created for a model that
	// the credential does not actually serve), NOT an upstream health
	// problem. Counting it as a failure and applying backoff only
	// pollutes the worker with rows that can never succeed and starve
	// the worker from probing other (cred, model) pairs that actually
	// matter. We:
	//   1. write a single audit row to node_probe_runs (for forensics)
	//   2. drop the node_probe_state row (worker stops re-picking it)
	//   3. log a one-shot warning so the operator sees the misconfig
	// Returns success=true so the calling cycle() loop does not retry.
	if isMissingBindingErr(direct) {
		slog.Warn("node_probe_worker: dropping probe for (cred, model) with no credential_model_bindings row",
			"credential_id", credID, "model", model,
			"reason", "endpoint_build + no rows in result set",
			"hint", "check provider_models.raw_model_name ↔ credential_model_bindings.raw_model_name join")
		w.emitProbe(ctx, credID, 0, model, model, "direct", attempt, trigger, direct)
		if _, err := w.db.Exec(ctx, `
			DELETE FROM node_probe_state
			 WHERE credential_id = $1 AND raw_model_name = $2
		`, credID, model); err != nil {
			slog.Warn("node_probe_worker: failed to drop orphan state row",
				"credential_id", credID, "model", model, "error", err)
		}
		// R12 audit closure (2026-09-11): the branch comment promises one
		// node_probe_runs forensic row, but the code never wrote it — the
		// unified ProbeService.Run missing-binding branch does (direct passed
		// for both rounds, success=false, nextSec=0), so dashboards lost the
		// signal on legacy-path drops. Mirror it. The DELETE above already
		// stopped the re-pick churn, so a failed audit write must not undo
		// the cleanup — surface it like the normal legacy path instead.
		now := time.Now()
		if err := w.insertNodeProbeRun(ctx, credID, model, triggerKind, attempt, 0,
			direct, direct, false, startedAt, now, int(now.Sub(startedAt).Milliseconds())); err != nil {
			auditPersistFailedTotal.WithLabelValues(triggerKind).Inc()
			slog.Error("node_probe: node_probe_runs audit insert failed (missing-binding drop)",
				"credential_id", credID, "model", model, "trigger_kind", triggerKind, "error", err)
			return fmt.Errorf("audit insert: %w", err)
		}
		return nil
	}
	// Round 2: gateway — now sees the restored state from the direct round
	gw := w.probeGateway(ctx, credID, model)

	// 2026-09-10 hzx-2/minimax-prod-v2 incident: the failure ladder and
	// success bookkeeping must reflect the DIRECT round only. The gateway
	// round is a composite E2E request routed through this gateway by model
	// name, so its failure is not always attributable to the probed node:
	// cred 42 (hzx-2) MiniMax-M2.7-highspeed 2026-09-10 12:56–13:01 recorded
	// direct 200 ×5 against the node's own decrypted key while every gateway
	// round returned upstream 401 invalid_key — the healthy node laddered to
	// consecutive_failures=5 within minutes of a manual force-enable and
	// showed as degraded. Same doctrine as probeRecovered (direct-only) and
	// the 2026-08-18 URSM direct-only fix below. The gateway anomaly stays
	// observable: gateway_* columns in node_probe_runs plus last_gateway_ok /
	// last_err_code / last_err_detail now carry the actual gateway outcome in
	// the success branch instead of hardcoded TRUE/NULL.
	success := direct.ok
	// 2026-08-18: URSM availability reflects the direct (upstream) round only.
	// The gateway round is itself routed through the URSM v2 filter; when the
	// node key is missing/expired the round 503s circularly and writing that
	// composite result back as available=0 turned a transient key expiry into
	// a persistent "confirmed unavailable" lockout (glm-5.2 outage on 154).
	// The composite still drives the backoff ladder and audit below.
	// R42 (2026-09-18 audit): the URSM v2 node key lives in Redis shared
	// cluster-wide; writing a failure there for a gateway-side error (this
	// instance's config, not the pair's health) locks the node out of routing
	// on every well-configured instance — the R39 P1-1 poisoning channel via
	// a second surface. Success always writes (it is the recovery signal);
	// failures follow the same predicate as the binding guards, R40
	// credential-specific decrypt exemption included.
	if direct.ok || w.ursmFailureWritable(direct.errCode, direct.errDetail) {
		w.updateURSMv2ProbeState(ctx, trigger.tenantID, credID, model, direct.ok, direct.latencyMs)
	}
	w.emitProbe(ctx, credID, direct.providerID, model, direct.outboundModel, "direct", attempt, trigger, direct)
	w.emitProbe(ctx, credID, direct.providerID, model, direct.outboundModel, "gateway", attempt, trigger, gw)
	if !direct.ok {
		if isGatewaySideProbeError(direct.errCode) && !w.credentialSpecificDecryptFailure(direct.errDetail) {
			// 2026-09-17: gateway-side error (endpoint/request build, decrypt).
			// updateBindingAvailability refuses the shared-state write by itself,
			// and the observed-state surface (updateObservedState below plus the
			// URSM v2 write above — shared Redis) stays untouched via the same
			// predicate — the pair is not unhealthy, this instance is
			// misconfigured.
			// R40 豁免：decrypt 形且熔断未跳闸 = 单凭据密文损坏（跟随凭据）：
			// 走 else 分支真实写绑定/观测面；失败 ladder 仍按 gateway-side
			// 冻结 consecutive_failures（固定 15m backoff，见下方 mirror
			// CASE WHEN）——否则该凭据在绑定面永无不可用信号、只剩真实流量
			// breaker 兜底（R39 §三#3）。
			slog.Error("node_probe_worker: gateway-side direct probe error — not updating availability",
				"credential_id", credID,
				"model", model,
				"err_code", direct.errCode,
				"err_detail", direct.errDetail)
		} else {
			w.updateBindingAvailability(ctx, credID, model, false, direct.errCode, attempt, direct.errDetail)
			_, horizon := unavailableBindingHorizon(direct.errCode, attempt)
			recoverAt := time.Now().Add(horizon)
			w.updateObservedState(ctx, credID, model, false, direct.errCode, recoverAt)
		}
	}
	if w.invalidateCandidateCache != nil {
		w.invalidateCandidateCache(credID)
	}
	now := time.Now()
	durationMs := int(now.Sub(startedAt).Milliseconds())

	if success {
		// 2026-09-20 probe-volume policy: success parks the row 30 days out
		// instead of re-arming +1h. The 2026-07-22 BUG #6 fix chose +1h so a
		// silently-changed upstream (key rotation, quota, deprecation) would
		// be re-checked within an hour; in practice it turned every probe
		// success into another probe an hour later, indefinitely, for
		// healthy pairs. Under the error-gated policy the failure detectors
		// (active_probe submitter consecutive_threshold=2, request-path
		// Submit, credential_recovery ticker) own change detection: the first
		// real failure re-arms this row to now()+5s immediately.
		if _, err := w.db.Exec(ctx, `
				UPDATE node_probe_state SET
					consecutive_failures = 0,
					consecutive_successes = consecutive_successes + 1,
					last_attempt_at = now(),
					next_retry_at = now() + interval '30 days',
					next_retry_seconds = 2592000,
					paused = FALSE,
					last_run_id = NULL,
					last_direct_ok = TRUE,
					last_gateway_ok = $3,
					last_err_code = $4,
					last_err_detail = $5,
					in_flight_until = NULL,
					updated_at = now()
				WHERE credential_id = $1 AND raw_model_name = $2
			`, credID, model, gw.ok, firstErrCode(direct, gw), firstErrDetail(direct, gw)); err != nil {

			w.logNodeProbeStateUpdateWarning("success", direct.providerID, credID, model, trigger.parentID, err)
		}

		// 2026-07-25 SPEC §3.1.2: success must immediately drop
		// node_probe_failed: invalidate URSM v2 candCache + pg_notify
		if w.invalidateCandidateCache != nil {
			w.invalidateCandidateCache(credID)
		}
		if w.db != nil {
			bgCtx, bgCancel := context.WithTimeout(context.Background(), 2*time.Second)
			if _, err := w.db.Exec(bgCtx, "SELECT pg_notify('auto_route_refresh', $1)", fmt.Sprintf("credentials:UPDATE:%d", credID)); err != nil {
				slog.Warn("node_probe_worker: pg_notify auto_route_refresh failed",
					"credential_id", credID, "model", model, "error", err)
			}
			bgCancel()
		}
	} else {
		// 2026-07-15 P0 fix (B-series cascade follow-up): the previous
		// implementation set in_flight_until = now() + 5min on the
		// failure path, which combined with pickDueAtomically's
		// `in_flight_until <= now()` clause meant a 5-minute dead
		// window after every probe — the worker logged `submit` but
		// the next cycle kept skipping it because in_flight_until was
		// still in the future. Worse, when the cycle finally did fire
		// 5 minutes later, runOne's "attempt > maxAttempts" branch set
		// paused=TRUE with no automatic un-pause, so subsequent
		// Submit() calls accumulated more `submit` log lines without
		// ever producing a probe run. The result was silent stuck
		// credentials: cred 19 (NVIDIA) and cred 21 (minimaxi.com
		// direct) stayed paused=TRUE with consecutive_failures=7 for
		// 6+ hours, masking the actual no_candidates outage.
		//
		// Fix: rely on the in-memory `inFlight` map (cycle()) and
		// SELECT ... FOR UPDATE SKIP LOCKED in pickDueAtomically for
		// cross-instance dedup, instead of an in_flight_until column
		// that bypasses both. The DB column now stays NULL after
		// runOne, and the chained backoff in next_retry_at naturally
		// paces retries (30s → 60s → 120s → ...).
		// 2026-09-17: a gateway-side error (decrypt/endpoint build — see
		// isGatewaySideProbeError) is this instance's config problem. The pair
		// is not unhealthy, so the chained ladder must not escalate and
		// consecutive_failures must not advance; pace retries at the fixed
		// gateway-side delay instead of the 5s..24h chain so a misconfigured
		// instance also stops hammering the shared row.
		gatewaySide := isGatewaySideProbeError(direct.errCode)
		// 2026-09-17: share the queue path's err-code-aware policy (404 →
		// model-not-served long horizon on confirmed attempts; network/timeout
		// → short chain; everything else → the 7-step ladder) so the legacy
		// cycle and the queue don't ladder the same pair at different speeds.
		backoff := ProbeBackoffForErrCode(direct.errCode, attempt)
		if gatewaySide {
			backoff = nodeProbeGatewaySideRetryDelay
		}
		nextRetryAt := now.Add(backoff)
		nextSec := int(backoff.Seconds())
		if _, err := w.db.Exec(ctx, `
				UPDATE node_probe_state SET
					consecutive_failures = CASE WHEN $10::boolean THEN node_probe_state.consecutive_failures ELSE $3 END,
					consecutive_successes = 0,
					last_attempt_at = now(),
					next_retry_at = $4,
					next_retry_seconds = $5,
					last_direct_ok = $6,
					last_gateway_ok = $7,
					last_err_code = $8,
					last_err_detail = $9,
					in_flight_until = NULL,
					updated_at = now()
				WHERE credential_id = $1 AND raw_model_name = $2
			`, credID, model, attempt, nextRetryAt, nextSec,
			direct.ok, gw.ok, firstErrCode(direct, gw), firstErrDetail(direct, gw), gatewaySide); err != nil {

			w.logNodeProbeStateUpdateWarning("failure", direct.providerID, credID, model, trigger.parentID, err)
		}

		// 2026-08-11: suspicious-action hook. When a node has failed 2+ times
		// in a row (the same consecutive_threshold the active_probe submitter
		// uses), request an on-demand model-IQ re-test so the latest IQ value
		// reflects the degraded node. Best-effort, fire-and-forget; the
		// receiver runs the test async and swallows errors. See
		// docs/model-iq/01-design.md §3.4.
		if w.modelQualityTrigger != nil && attempt >= 2 {
			w.modelQualityTrigger(credID, model, attempt)
		}
	}

	// Persist audit row.
	nextSec := int(ChainBackoffIndex(attempt, NodeProbeBackoffChain).Seconds())

	// 准备新字段数据
	var requestHeadersJSON string
	if len(direct.requestHeaders) > 0 {
		requestHeadersJSON = probeHeadersJSON(direct.requestHeaders)
	}

	timeoutAtMs := 0
	if direct.errCode == "network_error" && direct.latencyMs >= 14900 {
		timeoutAtMs = direct.latencyMs
	}

	_, err = w.db.Exec(ctx, `
		INSERT INTO node_probe_runs (
			credential_id, raw_model_name, trigger_kind, attempt, next_retry_seconds,
			direct_ok, direct_http_status, direct_err_code, direct_latency_ms, direct_err_detail,
			gateway_ok, gateway_http_status, gateway_err_code, gateway_latency_ms, gateway_err_detail,
			success, started_at, completed_at, duration_ms,
			api_model, outbound_model, provider_id,
			request_url, request_headers, request_body, response_body,
			timeout_at_ms, via_proxy
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $17, $18, $19,
			$20, $21, $22,
			$23, $24::text::jsonb, $25, $26,
			$27, $28
		)`,
		credID, model, triggerKind, clampAuditAttempt(attempt), nextSec,
		direct.ok, direct.httpStatus, direct.errCode, direct.latencyMs, direct.errDetail,
		gw.ok, gw.httpStatus, gw.errCode, gw.latencyMs, gw.errDetail,
		success, startedAt, now, durationMs,
		model, direct.outboundModel, direct.providerID,
		direct.requestURL, requestHeadersJSON, direct.requestBody, direct.responseBody,
		timeoutAtMs, direct.viaProxy,
	)
	if err != nil {
		// 2026-08-18 Agent B: the legacy runOne path used to swallow this
		// error with _, _ = ... — combined with the trigger_kind CHECK gap
		// that is exactly how node_probe_runs froze. Surface it so the
		// operator sees a gap and can replay the row out-of-band.
		auditPersistFailedTotal.WithLabelValues(triggerKind).Inc()
		slog.Error("node_probe: node_probe_runs audit insert failed (legacy runOne)",
			"credential_id", credID, "model", model, "trigger_kind", triggerKind, "error", err)
		return fmt.Errorf("audit insert: %w", err)
	}
	return nil
}

// isMissingBindingErr reports whether a direct probe round failed
// with the (cred, model)-pair-has-no-credential_model_bindings
// pattern. This happens when a node_probe_state row exists for a
// (cred, model) pair but the underlying JOIN in resolveDirectTarget
// returns zero rows (because credential_model_bindings has no entry
// for that pair). The probe will never succeed without a config fix
// and would otherwise consume the worker's retry budget indefinitely.
//
// Detection: probeDirect sets errCode="endpoint_build" + errDetail
// starting with "build endpoint failed: " whenever resolveDirectTarget
// returns an error. The actual SQL "no rows in result set" sentinel
// bubbles up unchanged from pgx (now wrapped with the
// "no rows in result set: credential_id=... has no enabled+unlocked
// credential_model_bindings for raw_model_name=..." prefix added in
// 2026-07-24). We match either form so a future refactor of the
// error wrapper does not silently re-introduce the bug.
func isMissingBindingErr(r nodeProbeRoundResult) bool {
	if r.errCode != "endpoint_build" {
		return false
	}
	return strings.Contains(r.errDetail, "no rows in result set")
}

type nodeProbeStateRow struct {
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	Paused               bool
}

func (w *NodeProbeWorker) loadState(ctx context.Context, credID int, model string) (nodeProbeStateRow, error) {
	var s nodeProbeStateRow
	err := w.db.QueryRow(ctx, `
		SELECT consecutive_failures, consecutive_successes, paused
		FROM node_probe_state
		WHERE credential_id = $1 AND raw_model_name = $2
	`, credID, model).Scan(&s.ConsecutiveFailures, &s.ConsecutiveSuccesses, &s.Paused)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return s, nil // never-submitted pair: not an error
		}
		return s, err
	}
	return s, nil
}

type nodeProbeRoundResult struct {
	ok            bool
	providerID    int
	outboundModel string
	httpStatus    int
	errCode       string
	errDetail     string
	latencyMs     int
	timedOut      bool // true if context deadline or client timeout triggered
	// 2026-07-16: 新增详细字段用于完整记录probe过程
	requestURL     string
	requestHeaders map[string]string // 已脱敏（不含Authorization/x-api-key）
	requestBody    string
	responseBody   string // 前512字节
	viaProxy       bool   // probeDirect是否通过代理
}

func probeHeadersJSON(headers map[string]string) string {
	if len(headers) == 0 {
		return "{}"
	}
	encoded, err := json.Marshal(headers)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// probeDirect issues a chat-completion ping directly to the upstream
// provider using the decrypted credential.  Mirrors the structure of
// bg/active_probe_executor.go:Run.
//
// `model` is the raw_model_name (vendor form, e.g. "z-ai/glm-5.2") used to
// resolve the credential via the provider_models JOIN. The actual name sent
// to the upstream in the request body is the outbound_model_name (which may
// differ from raw_model_name for some providers), selected from the DB by
// resolveDirectTarget — mirroring active_probe_executor.go:188-192. Without
// this split, a provider whose outbound_model_name differs from
// raw_model_name would fail the JOIN (since node_probe_state stores
// COALESCE(outbound,raw), the outbound name) and report endpoint_build
// failures for a healthy credential.
func (w *NodeProbeWorker) probeDirect(ctx context.Context, credID int, model string) nodeProbeRoundResult {
	r := nodeProbeRoundResult{errCode: "none"}
	plain, outboundModel, baseURL, protocol, providerID, err := w.resolveDirectTarget(ctx, credID, model)
	if err != nil {
		r.errCode = "endpoint_build"
		r.errDetail = fmt.Sprintf("build endpoint failed: %s (cred_id=%d, model=%s)", err.Error(), credID, model)
		return r
	}
	r.providerID = providerID
	r.outboundModel = outboundModel
	// Use the outbound name for the upstream body; fall back to the raw name
	// passed in (matches active_probe_executor.go's OutboundModel→RawModel
	// fallback) so providers without a distinct outbound_model_name still work.
	bodyModel := outboundModel
	if bodyModel == "" {
		bodyModel = model
	}
	endpoint := directProbeEndpoint(baseURL, protocol)
	body := directProbeBody(bodyModel, protocol)

	// 2026-07-16: 记录请求详情
	r.requestURL = endpoint
	r.requestBody = body
	r.viaProxy = (w.probeClient != w.client) // 如果probeClient与client不同，说明使用了代理

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		r.errCode = "request_build"
		r.errDetail = fmt.Sprintf("build request failed: %s (url=%s)", err.Error(), endpoint)
		return r
	}
	if strings.HasPrefix(protocol, "anthropic") {
		req.Header.Set("x-api-key", plain)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+plain)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LLM-Origin-Stage", "node_probe")
	req.Header.Set("X-LLM-Origin-Actor", "node-probe-worker")
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_IP")); v != "" {
		req.Header.Set("X-Real-IP", v)
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_FORWARDED_FOR")); v != "" {
		req.Header.Set("X-Forwarded-For", v)
	}

	// 记录请求头（脱敏）
	r.requestHeaders = make(map[string]string)
	for k, v := range req.Header {
		// 脱敏：不记录Authorization和x-api-key
		if k != "Authorization" && k != "X-Api-Key" && k != "x-api-key" {
			r.requestHeaders[k] = strings.Join(v, ", ")
		}
	}

	start := time.Now()
	// 2026-07-16 fix: use probeClient (proxy-respecting) instead of client
	// (direct). probeDirect previously bypassed the proxy, reporting
	// false-positive recoveries when the proxy was broken — the credential
	// oscillated between "available" (probe OK, no proxy) and "unavailable"
	// (real requests through proxy → timeout), causing the "models briefly
	// work then 5xx" pattern for non-domestic providers like apiclaude.cc
	// and integrate.api.nvidia.com.
	resp, err := w.probeClient.Do(req)
	r.latencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		code, timedOut := classifyProbeNetworkError(err)
		r.errCode = code
		r.timedOut = timedOut
		if timedOut {
			r.errDetail = fmt.Sprintf("upstream timeout after %ds (cred_id=%d, url=%s, model=%s)", int(w.probeClient.Timeout/time.Second), credID, endpoint, bodyModel)
		} else {
			r.errDetail = fmt.Sprintf("upstream %s: %s (cred_id=%d, url=%s, model=%s)", code, err.Error(), credID, endpoint, bodyModel)
		}
		return r
	}
	defer resp.Body.Close()
	r.httpStatus = resp.StatusCode

	// 读取响应body（前512字节）
	respBuf := make([]byte, 512)
	n, _ := resp.Body.Read(respBuf)
	r.responseBody = string(respBuf[:n])

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.ok = true
		return r
	}
	r.errCode = fmt.Sprintf("http_%d", resp.StatusCode)
	statusText := strings.TrimSpace(string(respBuf[:n]))
	// Trim trailing newlines/whitespace from status text
	if idx := strings.IndexAny(statusText, "\n\r"); idx >= 0 {
		statusText = statusText[:idx]
	}
	r.errDetail = fmt.Sprintf("upstream returned HTTP %d %s (cred_id=%d, url=%s, model=%s)", resp.StatusCode, statusText, credID, endpoint, bodyModel)
	return r
}

func (w *NodeProbeWorker) emitProbe(ctx context.Context, credID, providerID int, model, outboundModel, origin string, attempt int, trigger nodeProbeTrigger, result nodeProbeRoundResult) {
	if w == nil || w.emitter == nil {
		return
	}
	// 2026-07-17 (audit fix): recover the precise ProbeStatus from the
	// round result instead of collapsing every failure to
	// ProbeStatusFailed. Without this, a 429/503/network error on the
	// node_probe path was classified as probe_direct_internal_error /
	// failure_stage="gateway" — i.e. the exact misattribution this whole
	// change set was meant to eliminate. The errCode produced by
	// probeDirect/probeGateway encodes the failure kind:
	//   endpoint_build  -> gateway-side (decrypt/resolve) -> Failed
	//   network_error   -> Network (request_build/transport)
	//   http_<status>   -> mapped from the upstream status code
	status := nodeProbeResultToStatus(result)
	// 2026-07-17: forward the diagnostic detail already collected by
	// probeDirect/probeGateway so the emitter can surface the real
	// request/response context in auto_decision + synthetic traces.
	// Previously these fields were dropped here, which is why probe rows
	// showed only headers on the dashboard.
	w.emitter.Emit(ctx, credID, providerID, trigger.tenantID, model, outboundModel, origin, trigger.parentID, attempt, &ProbeResult{
		Status:       status,
		HTTPStatus:   result.httpStatus,
		ErrCode:      result.errCode,
		ErrMsg:       result.errDetail,
		LatencyMs:    result.latencyMs,
		RespPreview:  result.responseBody,
		ResponseBody: result.responseBody,
		RequestURL:   result.requestURL,
		RequestBody:  result.requestBody,
		ViaProxy:     result.viaProxy,
		StartedAt:    time.Now().Add(-time.Duration(result.latencyMs) * time.Millisecond),
		CompletedAt:  time.Now(),
		// Explicit origin attribution: the node-probe worker bypasses
		// OriginMiddleware, so without these labels the emitted request_logs
		// row would inherit a stale/empty origin and the dashboard's probe
		// filter (origin_stage = 'node_probe') would miss it. The actor name
		// matches the goroutine label used in logs for cross-referencing.
		OriginStage: "node_probe",
		OriginActor: "node-probe-worker",
	})
}

// nodeProbeResultToStatus maps a nodeProbeRoundResult (ok + httpStatus +
// errCode) onto the fine-grained ProbeStatus taxonomy that
// classifyProbeErrorKind / classifyProbeFailureStage consume. Mirrors the
// HTTP-status mapping in ActiveProbeExecutor.Run so both probe paths emit
// the same error_kind for the same upstream behaviour (e.g. a 429 is
// probe_direct_rate_limited on either path).
func nodeProbeResultToStatus(r nodeProbeRoundResult) ProbeStatus {
	if r.ok {
		return ProbeStatusSuccess
	}
	switch r.errCode {
	case "endpoint_build":
		return ProbeStatusFailed // gateway-side build error (decrypt/resolve)
	case "timeout":
		// Transport deadline: context cancelled, client Timeout, or a dial
		// timeout. Classified by classifyProbeNetworkError.
		return ProbeStatusTimeout
	case "dns_error", "connection_error", "network_error":
		// Non-deadline transport failures. errCode carries the finer
		// distinction (DNS resolver vs refused dial vs everything else);
		// all map to ProbeStatusNetwork so dashboards/alerts that grouped
		// on the old single "network_error" keep working unchanged.
		return ProbeStatusNetwork
	}
	// errCode is "http_<status>" for non-200 upstream responses.
	if r.httpStatus == 401 || r.httpStatus == 403 {
		return ProbeStatusAuth
	}
	if r.httpStatus == 429 {
		return ProbeStatusRate
	}
	if r.httpStatus >= 500 {
		return ProbeStatusHTTP5xx
	}
	if r.httpStatus >= 400 {
		return ProbeStatusHTTP4xx
	}
	return ProbeStatusFailed
}

// classifyProbeNetworkError maps a transport-layer error returned by
// http.Client.Do into a fine-grained errCode for node_probe_runs.
//
// Before this existed, every non-2xx transport failure collapsed to a single
// "network_error" string, so an operator reading node_probe_runs could not
// tell a DNS resolver outage from a refused dial or a slow upstream — three
// failures that need completely different responses (fix egress DNS vs the
// upstream is down vs upstream is slow). probeGateway was worse: it did not
// even detect timeouts, so a 30s gateway hang was filed as "network_error".
//
// Classification priority (first match wins):
//   - context.DeadlineExceeded → "timeout"     (whole probe budget exhausted)
//   - *url.Error with Timeout()==true → "timeout"
//   - *url.Error wrapping *net.DNSError → "dns_error"
//   - *url.Error wrapping *net.OpError{Op:"dial"} → "connection_error"
//   - bare *net.DNSError → "dns_error" (honoring DNSError.IsTimeout)
//   - bare *net.OpError → "timeout" if Timeout() else "connection_error" if dial
//   - anything else → "network_error"        (TLS, mid-stream reset, body read, …)
//
// Returns (errCode, timedOut). timedOut is redundant with errCode=="timeout"
// but is kept because nodeProbeRoundResult.timedOut is read elsewhere
// (emitProbe / probe_direct_timeout state:* branch in router.go) and the
// audit row still records it as a separate boolean for quick filtering.
//
// This is a pure function on purpose: it has no *NodeProbeWorker receiver so
// it can be unit-tested with synthetic errors instead of a real HTTP hop.
func classifyProbeNetworkError(err error) (errCode string, timedOut bool) {
	if err == nil {
		return "none", false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", true
	}
	// http.Client.Do always wraps transport errors in *url.Error.
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "timeout", true
		}
		var dnsErr *net.DNSError
		if errors.As(ue.Err, &dnsErr) {
			if dnsErr.IsTimeout {
				return "timeout", true
			}
			return "dns_error", false
		}
		var opErr *net.OpError
		if errors.As(ue.Err, &opErr) {
			if opErr.Timeout() {
				return "timeout", true
			}
			if opErr.Op == "dial" {
				return "connection_error", false
			}
		}
	}
	// Bare errors (custom Transport, direct dialer use, future callers).
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTimeout {
			return "timeout", true
		}
		return "dns_error", false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return "timeout", true
		}
		if opErr.Op == "dial" {
			return "connection_error", false
		}
	}
	return "network_error", false
}

func (w *NodeProbeWorker) resolveDirectTarget(ctx context.Context, credID int, model string) (string, string, string, string, int, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var (
		ciphertext    []byte
		outboundModel string
		baseURL       string
		protocol      string
		providerID    int
	)
	err := w.db.QueryRow(queryCtx, `
		SELECT c.secret_ciphertext,
		       COALESCE(NULLIF(pm.outbound_model_name, ''), pm.raw_model_name, ''),
		       p.base_url, COALESCE(p.protocol, 'openai-completions'), p.id
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE c.id = $1 AND pm.raw_model_name = $2
		  AND c.status IN ('active', 'cooling', 'degraded')
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE AND p.manual_disabled = FALSE
		LIMIT 1
	`, credID, model).Scan(&ciphertext, &outboundModel, &baseURL, &protocol, &providerID)
	if err != nil {
		// 2026-08-18 fix (glm-5.2 outage): the strict gates above conflate
		// "no binding row" (a config problem) with "credential currently
		// status/lifecycle-disabled" (runtime state a probe exists to gather
		// evidence about). Score automation flips lifecycle_status='disabled'
		// on stale data; the probe then fails endpoint-build with "no rows",
		// isMissingBindingErr short-circuits to a fake success, deletes
		// node_probe_state and never writes URSM — the credential becomes
		// unprobeable and unmonitorable. Retry once with only the
		// human-intent gates (credential/provider not manual_disabled +
		// provider enabled) so direct evidence can still be collected for
		// such credentials. Credential-level manual_disabled stays a hard
		// gate in BOTH rounds (2026-09-08 self-check audit): it is exactly
		// the human-intent signal this fallback promises to honour.
		var looseErr error
		looseErr = w.db.QueryRow(queryCtx, `
			SELECT c.secret_ciphertext,
			       COALESCE(NULLIF(pm.outbound_model_name, ''), pm.raw_model_name, ''),
			       p.base_url, COALESCE(p.protocol, 'openai-completions'), p.id
			FROM credentials c
			JOIN providers p ON p.id = c.provider_id
			JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			WHERE c.id = $1 AND pm.raw_model_name = $2
			  AND COALESCE(c.manual_disabled, FALSE) = FALSE
			  AND p.enabled = TRUE AND p.manual_disabled = FALSE
			LIMIT 1
		`, credID, model).Scan(&ciphertext, &outboundModel, &baseURL, &protocol, &providerID)
		if looseErr == nil {
			slog.Info("node_probe_worker: probing credential despite non-active status flags",
				"credential_id", credID, "model", model,
				"note", "status/lifecycle gates skipped to collect direct evidence")
			err = nil
		}
	}
	if err != nil {
		// 2026-07-24 P0 fix: surface the missing-binding case explicitly.
		// resolveDirectTarget requires (cred, raw_model_name) to have a
		// row in credential_model_bindings AND a matching provider_models
		// row AND the provider to be enabled+not manual_disabled AND the
		// credential to be active. Any of those conditions failing
		// yields pgx's "no rows in result set" sentinel — without
		// context, the operator sees the same generic string for five
		// different misconfigurations. We rewrap with the original
		// cred/model so the worker (and runOne's isMissingBindingErr
		// short-circuit) can disambiguate from real upstream errors.
		if err.Error() == "no rows in result set" {
			return "", "", "", "", 0, fmt.Errorf("no rows in result set: credential_id=%d has no enabled+unlocked credential_model_bindings for raw_model_name=%q (check cmb.available, p.enabled, p.manual_disabled, c.status, c.lifecycle_status)", credID, model)
		}
		return "", "", "", "", 0, err
	}
	s := string(ciphertext)
	if !secret.IsV1Envelope(s) {
		return "", "", "", "", 0, fmt.Errorf("unsupported secret format")
	}
	if w.keyring == nil {
		return "", "", "", "", 0, fmt.Errorf("keyring not configured")
	}
	// 2026-07-15 P0 fix: previously this called DecryptAESGCM directly,
	// which only handles v1:<kid>:<b64> envelopes with a non-legacy kid.
	// v1:legacy:<b64> envelopes (the historical default for credentials
	// created before the v1 multi-kid keyring landed) decode to
	// "unsupported secret format" or to "AES-GCM decryption failed",
	// both surfaced as `endpoint_build` in node_probe_state.last_err_code.
	// The result: every probe of those credentials failed at the
	// endpoint-build step, consecutive_failures climbed to 7, the row
	// got paused (the previous design), and the router lost a
	// perfectly valid minimaxi.com direct candidate. DecryptAny
	// tries AES-GCM first, then falls back to Fernet with the legacy
	// v1:legacy: prefix, matching what the production request path
	// already does via secret.DecryptAny.
	pt, _, err := secret.DecryptAny(s, w.keyring, w.encKey)
	if err != nil {
		// Feed the instance-level decrypt circuit (2026-09-17): consecutive
		// decrypt failures are a strong signal THIS instance's key config is
		// wrong (e.g. dev instance sharing the production DB with a different
		// CREDENTIAL_ENCRYPTION_KEY). See decryptCircuitTripped.
		w.recordDecryptFailure()
		// 2026-07-15 P0 fix: surface the raw error to operator logs.
		// endpoint_build was the only signal in node_probe_state,
		// but the upstream cause (keyring nil? unknown kid? Fernet
		// signature mismatch?) was swallowed. We log the full chain
		// here so the next no_candidates outage is root-causable
		// from a single grep on "node_probe_worker: decrypt failed".
		// 2026-09-08 audit: log only the key id and payload length — the old
		// "envelope_prefix" put ciphertext bytes ("v1:<kid>:<b64>") into the
		// log stream, violating the key-material-never-in-logs baseline.
		slog.Error("node_probe_worker: decrypt failed",
			"credential_id", credID, "model", model,
			"keyring_nil", w.keyring == nil,
			"enc_key_len", len(w.encKey),
			"envelope_kid", decryptEnvelopeKid(s),
			"envelope_len", len(s),
			"error", err.Error())
		return "", "", "", "", 0, fmt.Errorf("decrypt: %w", err)
	}
	// Decrypt succeeded — this instance's key config is coherent with the
	// DB's envelopes, so clear the instance-level decrypt circuit.
	w.resetDecryptFailures()
	return string(pt), outboundModel, baseURL, protocol, providerID, nil
}

// directProbeEndpoint/directProbeBody 按 nodelist 直连探针的协议分发。
// 2026-09-25：改用 probeDescriptorFor（归一 + providercap.Resolve）统一入口，
// openai-responses 凭据（vapeur/hxt-local）走原生 /v1/responses
// {"input","max_output_tokens"}——其 chat 端点对小 max_tokens 探针 400。
func directProbeEndpoint(baseURL, protocol string) string {
	return upstreamurl.Build(baseURL, probeDescriptorFor(protocol).ChatProbeEndpoint)
}

func directProbeBody(model, protocol string) string {
	desc := probeDescriptorFor(protocol)
	switch desc.ChatProbeEndpoint {
	case upstreamurl.EpMessages:
		body, _ := json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 10,
			"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		})
		return string(body)
	case upstreamurl.EpResponses:
		body, _ := json.Marshal(map[string]any{
			"model":             model,
			"input":             "ping",
			"max_output_tokens": providercap.ResponsesProbeMaxOutputTokens,
		})
		return string(body)
	default:
		body, _ := json.Marshal(map[string]any{
			"model":      model,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			"max_tokens": 10,
		})
		return string(body)
	}
}

func (w *NodeProbeWorker) logNodeProbeStateUpdateWarning(phase string, providerID, credID int, model, parentRequestID string, err error) {
	if err == nil {
		return
	}
	slog.Warn("node_probe_worker: node_probe_state update failed",
		"phase", phase,
		"provider_id", providerID,
		"credential_id", credID,
		"raw_model", model,
		"parent_request_id", parentRequestID,
		"queue", w != nil && w.probeQueue != nil,
		"error", err,
	)
}

// modelNotServedRecheckInterval is the binding cooldown applied when the
// DIRECT probe round returns HTTP 404 on attempt >= 2 of the current failure
// episode: the upstream told us it does not serve this model on this
// credential. That is a catalog mismatch, not a health transient — re-checking
// it every 5 minutes (the generic cooldown) produced the eternal 404 churn
// observed on 2026-09-17 (apigpt "gpt key": 14 models × 7 attempts/24h of
// doomed request_failure probes, every cycle flipping the binding unavailable
// again right after recovery cleared it). 6h matches the ladder's long-tail
// cap so a relay that later adds the model is picked up the same day.
//
// 口径（R40 决议，闭合 R39 §三#2）：attempt 是本轮失败 episode 的探测序号
// （成功/Submit 重置），不是"连续 404 计数"——2026-09-17 事故文档写的
// "连续两次 404"与实现有偏差，按实现口径修文档而非给 node_probe_state 加
// prev_err_code 列：episode 内 404 被瞬时错误间隔后仍升级是更保守的方向
// （6h 自愈重查封顶），不值得为叙事精确性动 schema。
const modelNotServedRecheckInterval = 6 * time.Hour

// isModelNotServedProbeError reports whether a direct-round error code is the
// upstream's "model not found" verdict. Only the DIRECT round may set it — a
// gateway-round 404 usually means "no routable candidates in this gateway",
// which is downstream of the very binding state being written.
func isModelNotServedProbeError(errCode string) bool {
	switch errCode {
	case "http_404", "probe_http_404":
		return true
	}
	return false
}

// unavailableBindingHorizon returns the cooldown horizon for one unavailable
// write. A 404 at attempt >= 2 of the episode escalates to the model-not-served
// horizon; everything else keeps the historical 5 minutes.
func unavailableBindingHorizon(errCode string, attempt int) (reason string, horizon time.Duration) {
	if isModelNotServedProbeError(errCode) && attempt >= 2 {
		return "model_not_served_404", modelNotServedRecheckInterval
	}
	return errCode, 5 * time.Minute
}

func (w *NodeProbeWorker) updateBindingAvailability(ctx context.Context, credID int, model string, available bool, reason string, attempt int, errDetail string) {
	if w == nil || w.db == nil {
		return
	}
	// 2026-09-17 shared-state guard: a gateway-side probe error (endpoint
	// build / request build — most commonly "cannot decrypt: unknown format"
	// from a mismatched CREDENTIAL_ENCRYPTION_KEY) describes THIS instance's
	// configuration, not the (credential, model) pair's upstream health.
	// Multiple gateway instances share credential_model_bindings, so writing
	// available=FALSE from a gateway-side error lets one misconfigured
	// instance (observed: 252 dev vs production, ~1400 decrypt failures/hour)
	// repeatedly flip healthy production bindings into 5-minute cooldowns and
	// fight the well-configured instances' successful probes. Refuse the
	// write; the audit trail (node_probe_runs) still records the failure.
	//
	// R40 豁免：decrypt 形且实例解密熔断未跳闸的失败是单凭据密文损坏的证据
	// （跟随凭据而非实例），按真实不可用写下去——见
	// credentialSpecificDecryptFailure。
	if !available && isGatewaySideProbeError(reason) && !w.credentialSpecificDecryptFailure(errDetail) {
		slog.Error("node_probe_worker: refusing to mark binding unavailable from a gateway-side probe error",
			"credential_id", credID,
			"model", model,
			"reason", reason,
			"hint", "gateway-side build/decrypt errors are this instance's config problem, not an upstream health signal")
		return
	}
	if available {
		_, _ = w.db.Exec(ctx, healthyBindingSQL(), credID, model)
		return
	}
	reasonTag, horizon := unavailableBindingHorizon(reason, attempt)
	_, _ = w.db.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available = FALSE,
		    unavailable_reason = $3,
		    unavailable_at = now(),
		    unavailable_recover_at = now() + $4::interval,
		    updated_at = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		`, credID, model, "probe_"+reasonTag, fmt.Sprintf("%d seconds", int(horizon.Seconds())))
}

func (w *NodeProbeWorker) updateCredentialHealth(ctx context.Context, credID int) {
	_, _ = w.db.Exec(ctx, healthyCredentialSQL(), credID)
}

// ursmFailureWritable reports whether a FAILED direct probe round may write
// its unavailable state to the shared URSM v2 surface (R42, 2026-09-18
// audit). Gateway-side errors (decrypt/endpoint/request build) describe this
// instance, not the pair — except the R40 exemption (credential-specific
// decrypt failure while the instance circuit is not tripped), which follows
// the credential and must surface. Same predicate as the
// updateBindingAvailability/applyOutcome guards; the success path is always
// writable and is checked by the callers before consulting this.
func (w *NodeProbeWorker) ursmFailureWritable(errCode, errDetail string) bool {
	return !isGatewaySideProbeError(errCode) || w.credentialSpecificDecryptFailure(errDetail)
}

func (w *NodeProbeWorker) updateURSMv2ProbeState(ctx context.Context, tenantID string, credID int, model string, success bool, latencyMs int) {
	if w == nil || w.stateSink == nil {
		return
	}
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if tenantID == "" && w.tenantResolver != nil {
		resolved, err := w.tenantResolver(probeCtx, credID)
		if err != nil {
			slog.Warn("node_probe_worker: resolve URSM v2 tenant failed",
				"credential_id", credID, "model", model, "error", err)
			return
		}
		tenantID = resolved
	}
	if err := w.stateSink.ApplyProbeForTenant(probeCtx, tenantID, credID, model, success, latencyMs); err != nil {
		slog.Warn("node_probe_worker: URSM v2 probe state write failed",
			"credential_id", credID, "model", model, "tenant_id", tenantID, "error", err)
	}
}

func (w *NodeProbeWorker) updateObservedState(ctx context.Context, credID int, model string, available bool, lastError string, recoverAt time.Time) {
	if w == nil || w.stateObserver == nil {
		return
	}
	state := &credentialstate.State{
		CredentialID:  credID,
		Model:         model,
		Available:     available,
		HealthStatus:  "healthy",
		LastUpdatedAt: time.Now(),
		LastError:     lastError,
		Source:        "node_probe",
	}
	if available {
		now := time.Now()
		state.LastSuccessAt = &now
	} else {
		state.HealthStatus = "unreachable"
		state.RecoverAt = &recoverAt
	}
	w.stateObserver.UpdateFromProbe(ctx, state)
}

// probeGateway issues a chat-completion ping through the local gateway
// using the system API key.  This catches routing, auth, transform,
// rate-limit, and compression regressions that a direct call cannot.
func (w *NodeProbeWorker) probeGateway(ctx context.Context, credID int, model string) nodeProbeRoundResult {
	r := nodeProbeRoundResult{errCode: "none"}
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":10}`, model)
	endpoint := w.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		r.errCode = "request_build"
		r.errDetail = err.Error()
		return r
	}
	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LLM-Origin-Stage", "node_probe")
	req.Header.Set("X-LLM-Origin-Actor", "node-probe-worker")
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_IP")); v != "" {
		req.Header.Set("X-Real-IP", v)
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_FORWARDED_FOR")); v != "" {
		req.Header.Set("X-Forwarded-For", v)
	}
	// 2026-07-17: record request context for diagnostics (mirrors probeDirect).
	r.requestURL = endpoint
	r.requestBody = body
	r.viaProxy = false // gateway probe uses the direct (non-proxy) client
	start := time.Now()
	resp, err := w.client.Do(req)
	r.latencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		// 2026-08-12: probeGateway previously filed every transport error as
		// "network_error", including 30s hangs. Reuse the direct-round
		// classifier so a gateway timeout is distinguishable from a refused
		// connection in node_probe_runs.gateway_err_code.
		code, timedOut := classifyProbeNetworkError(err)
		r.errCode = code
		r.timedOut = timedOut
		r.errDetail = err.Error()
		return r
	}
	defer resp.Body.Close()
	r.httpStatus = resp.StatusCode
	// Read the body for both success and failure so success-path usage
	// parsing (emitter.parseProbeUsage) has something to work with,
	// instead of reporting hardcoded token counts.
	respBuf := make([]byte, 512)
	n, _ := resp.Body.Read(respBuf)
	r.responseBody = string(respBuf[:n])
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.ok = true
		return r
	}
	r.errCode = fmt.Sprintf("http_%d", resp.StatusCode)
	if n > 0 {
		r.errDetail = r.responseBody
		if len(r.errDetail) > 256 {
			r.errDetail = r.errDetail[:256]
		}
	}
	return r
}

func firstErrCode(a, b nodeProbeRoundResult) *string {
	if !a.ok {
		s := a.errCode
		return &s
	}
	if !b.ok {
		s := b.errCode
		return &s
	}
	return nil
}
func firstErrDetail(a, b nodeProbeRoundResult) *string {
	if !a.ok {
		s := truncateStrSC(a.errDetail, 256)
		return &s
	}
	if !b.ok {
		s := truncateStrSC(b.errDetail, 256)
		return &s
	}
	return nil
}

// Used by tests to compute a deterministic dedup hash of (cred, model).
func nodeProbeDedupHash(credID int, model string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%s", credID, model)))
	return hex.EncodeToString(h[:8])
}

// decryptEnvelopeKid extracts the key id from a "v1:<kid>:<ciphertext>"
// envelope for operator diagnostics without emitting ciphertext bytes.
func decryptEnvelopeKid(envelope string) string {
	parts := strings.SplitN(envelope, ":", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}
