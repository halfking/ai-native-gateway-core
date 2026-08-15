package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Durable-task worker observability (SR-W3, docs/修订0811/18 §12/§15).
//
// The durable_* series are the worker/projection view of the survival
// observability contract: gateway_survival_* (survival_metrics.go) covers the
// per-request coordinator path, durable_* covers the PostgreSQL recovery
// worker and the PendingStore projection outbox. Declarations ship before the
// remaining producers are wired so dashboards/alerts can target stable names;
// unexported series stay at zero until then.
//
// Cardinality note (GW-00): durable_tasks_active carries a raw tenant label
// deliberately — task volume per tenant is bounded by the per-tenant active
// task cap, so the series count is bounded by tenants × 1.

var (
	// DurableTasksActive gauges non-terminal durable tasks per tenant,
	// refreshed by the recovery worker from the authoritative
	// ActiveTaskCounts query. Alert when it approaches the tenant cap.
	DurableTasksActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "durable_tasks_active",
		Help: "Active (non-terminal) durable tasks per tenant, from the authoritative PostgreSQL count.",
	}, []string{"tenant"})
)

// durableActiveTenants remembers every tenant series ever published so a
// tenant that drained to zero (absent from the latest GROUP BY) gets its
// gauge reset instead of freezing at the last non-zero value.
var durableActiveTenants sync.Map

// SetDurableTasksActive publishes one authoritative per-tenant snapshot.
// Tenants absent from counts (no active tasks left) are reset to zero.
func SetDurableTasksActive(counts map[string]int64) {
	seen := make(map[string]struct{}, len(counts))
	for tenant, n := range counts {
		DurableTasksActive.WithLabelValues(tenant).Set(float64(n))
		seen[tenant] = struct{}{}
		durableActiveTenants.Store(tenant, struct{}{})
	}
	durableActiveTenants.Range(func(key, _ any) bool {
		tenant, _ := key.(string)
		if _, ok := seen[tenant]; !ok {
			DurableTasksActive.WithLabelValues(tenant).Set(0)
		}
		return true
	})
}
