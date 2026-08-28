package requestdetail

import (
	"errors"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Counter exported by the request-detail hot-content store. These counters
// exist so dashboards can quantify the "sensitive file residue rate" and
// detect path-related incidents (read-only mount, permission drift, NFS
// stale handle) that previously only showed up as silent log lines.
//
// 2026-08-28 (audit follow-up): Clear used to log a slog.Warn when
// os.Remove failed but had no counter. Operators could not alert on a
// persistent residue problem — the only signal was grepping logs. The
// counters below surface that signal as a Prom metric and as a counter
// observable from admin diagnostics.
var (
	// storeClearFailuresTotal counts Clear() calls whose os.Remove failed
	// for reasons other than ErrNotExist. Each increment covers one
	// request_id that the store forgot to unlink on disk, which is a
	// sensitive-file residue event. result label distinguishes the two
	// failure classes that drive distinct incident responses.
	storeClearFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "requestdetail_store_clear_failures_total",
		Help: "request-detail Store.Clear os.Remove failures, labelled by result (permission, other).",
	}, []string{"result"})

	// storeEvictionFailuresTotal counts TTL/LRU eviction os.Remove failures.
	// Same residue semantics as Clear, but driven by background eviction
	// rather than the explicit Clear() path.
	storeEvictionFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "requestdetail_store_eviction_failures_total",
		Help: "request-detail Store eviction os.Remove failures, labelled by trigger (ttl, lru, cleanup) and result.",
	}, []string{"trigger", "result"})

	// storeClearSuccessTotal counts Clear() calls that fully removed both
	// the file and the .tmp sidecar. Together with ClearFailures this gives
	// a residue-rate = failures / (failures + success) gauge.
	storeClearSuccessTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "requestdetail_store_clear_success_total",
		Help: "request-detail Store.Clear calls that successfully removed the file.",
	})

	// storeForwarderDroppedOversizeTotal counts forwarder entries that
	// exceeded MaxBodyFileSize at emit time and were dropped before they
	// could enqueue. Without this guard, a single 100MB body would sit
	// in the queue buffer for the entire 2048-entry capacity (=200GB).
	storeForwarderDroppedOversizeTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "requestdetail_forwarder_dropped_oversize_total",
		Help: "request-detail capture forwarder entries dropped because their body size exceeded MaxBodyFileSize.",
	})

	// storeMalformedSnapshotTotal counts GetFile calls that found a JSON
	// file but could not unmarshal it. The previous code only slog.Warn'd;
	// adding a counter makes the rate observable on dashboards.
	storeMalformedSnapshotTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "requestdetail_malformed_snapshot_total",
		Help: "request-detail Store.GetFile calls that hit a local file with invalid JSON.",
	})

	// locatorDBRetryTotal counts read-your-writes retry outcomes on the L3
	// DB fallback path (方案 D, 短期). hit = retry recovered a row that the
	// first attempt missed; miss = retry exhausted without finding the row.
	// Together they expose the "in-flight vs persisted" race window in
	// production dashboards.
	locatorDBRetryTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "requestdetail_locator_db_retry_total",
		Help: "request-detail Locator.Get L3 DB read-your-writes retry outcomes.",
	}, []string{"outcome"})
)

// normalizeRemoveErr maps an os.Remove error into the {permission, other}
// label space. ErrNotExist is intentionally excluded — it is the normal
// case for idempotent Clear and does not count as a residue event.
func normalizeRemoveErr(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission"
	}
	return "other"
}
