package sessionv2mirror

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// mirrorBacklogCap is the bounded capacity of the in-process backlog.
// Spec §12 GAP 2: Session V2 mirror failures had no replay queue, so a
// sustained V2 outage during the cutover window silently dropped rows
// with only a slog.Warn + monotonic counter to surface it. The backlog
// keeps the last mirrorBacklogCap failed entries in-process so an
// operator (or a future background replayer) can drain them. It is NOT
// persisted to disk — dbdegradation.RingBuffer already covers the WAL
// fallback path; this backlog is the observability/replay surface for
// the session-v2 mirror specifically.
const mirrorBacklogCap = 10000

// V2Writer is the minimal write surface PersistHook depends on.
// *v2.SessionWriterV2 satisfies it; declaring it as an interface lets
// unit tests inject a failing writer without constructing a real
// SessionWriterV2 (whose Write needs live DB sub-writers).
type V2Writer interface {
	Write(ctx context.Context, req *v2.ProcessedRequest) error
}

// BacklogItem is one failed mirror entry held in the backlog. It
// captures the request id + the converted ProcessedRequest so a future
// replayer can retry without re-parsing the telemetry entry.
type BacklogItem struct {
	RequestID string
	Req       *v2.ProcessedRequest
	// Entry is retained for diagnostics (the raw telemetry row); not
	// required for replay but useful for forensics when draining.
	Entry *telemetry.RequestLogEntry
}

// sessionV2MirrorBacklogPending is the Prometheus gauge exposing the
// current backlog depth. A rate-of-change alert on this metric (or a
// static threshold) tells operators the V2 mirror is losing rows
// faster than it can be drained — the exact "data drift during
// cutover" failure mode spec §12 GAP 2 calls out.
var sessionV2MirrorBacklogPending = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "session_v2_mirror_backlog_pending",
	Help: "Number of failed Session V2 shadow-write entries currently held in the in-process backlog (bounded by session_v2_mirror_backlog_capacity). Non-zero during a V2 mirror outage; drain via DrainBacklog or a future background replayer (spec §12 GAP 2).",
})

var (
	backlogMu sync.Mutex
	backlog   []BacklogItem
)

// shadowWriteEnabledFn is the feature-flag seam the hook consults.
// It is stored in an atomic.Value so concurrent hook invocations (the
// telemetry onPersisted path runs under load) and a test override never
// race on the function pointer. Defaulting to the settings-backed
// reader keeps production behaviour unchanged; tests override it via
// setShadowWriteEnabledForTest because settings.Global is nil in the
// unit-test binary (the reader returns false and short-circuits the
// hook before the write — and therefore the backlog — is reached).
type shadowEnabledFn func() bool

var shadowWriteEnabledPtr atomic.Value // holds shadowEnabledFn

func init() {
	shadowWriteEnabledPtr.Store(shadowEnabledFn(func() bool {
		return settings.GetPlatformBool("sessions_v2.enabled", false) &&
			settings.GetPlatformBool("sessions_v2.shadow_write", false)
	}))
}

// shadowWriteEnabled reads the current seam under the atomic load.
func shadowWriteEnabled() bool {
	fn := shadowWriteEnabledPtr.Load().(shadowEnabledFn)
	return fn()
}

// setShadowWriteEnabledForTest replaces the seam. Test-only; production
// never overrides (the settings-backed default reads the live
// settings_kv value on every call, so hot-reload is preserved).
func setShadowWriteEnabledForTest(fn shadowEnabledFn) {
	if fn == nil {
		fn = func() bool { return false }
	}
	shadowWriteEnabledPtr.Store(fn)
}

// appendBacklog adds one failed entry, evicting the oldest (FIFO) when
// the backlog is at capacity. pending must never exceed cap. The gauge
// is updated under the same lock so a scrape never observes a torn
// (stale) depth.
func appendBacklog(item BacklogItem) {
	backlogMu.Lock()
	defer backlogMu.Unlock()
	if len(backlog) >= mirrorBacklogCap {
		// Drop the oldest entry to make room. This is intentional: the
		// backlog is a bounded observability/replay buffer, not an
		// unbounded queue; under a sustained outage the oldest entries
		// are the least actionable and the monotonic
		// llm_gateway_shadow_write_failed_total counter already recorded
		// every drop.
		backlog = backlog[1:]
	}
	backlog = append(backlog, item)
	sessionV2MirrorBacklogPending.Set(float64(len(backlog)))
}

// BacklogStats returns the current backlog depth and the fixed
// capacity. Both are read under the backlog lock so the pair is
// consistent. Used by /debug endpoints and tests.
func BacklogStats() (pending, capacity int) {
	backlogMu.Lock()
	defer backlogMu.Unlock()
	return len(backlog), mirrorBacklogCap
}

// DrainBacklog removes and returns up to max entries (oldest first),
// preserving FIFO order. max <= 0 drains nothing. The gauge is
// refreshed after the drain.
func DrainBacklog(max int) []BacklogItem {
	if max <= 0 {
		return nil
	}
	backlogMu.Lock()
	defer backlogMu.Unlock()
	n := len(backlog)
	if n == 0 {
		return nil
	}
	if max > n {
		max = n
	}
	out := make([]BacklogItem, max)
	copy(out, backlog[:max])
	backlog = backlog[max:]
	sessionV2MirrorBacklogPending.Set(float64(len(backlog)))
	return out
}

// resetBacklogForTest clears the backlog and zeroes the gauge. Test-only.
func resetBacklogForTest() {
	backlogMu.Lock()
	defer backlogMu.Unlock()
	backlog = nil
	sessionV2MirrorBacklogPending.Set(0)
}
