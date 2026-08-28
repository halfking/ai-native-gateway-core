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
