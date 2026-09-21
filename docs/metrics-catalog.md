# Metrics Catalog — FreeDiscovery

This document catalogs every metric declared in `metrics/freediscovery_metrics.go`.
Each entry includes the symbol, type, labels, source of truth, and intended use.

All metric names share the prefix `freediscovery_` to keep them grouped in
Prometheus queries and Grafana dashboards. Label cardinality follows the
GW-00 low-cardinality rule: `provider`, `status`, `verdict`, and `reason`
are bounded enums — never add `model` or `tenant` dimensions.

---

## Scans

### `freediscovery_scans_total`

- **Symbol:** `FreeDiscoveryScansTotal` (CounterVec)
- **Labels:**
  - `provider` (string) — provider_code from the template; empty when the
    template could not be resolved
  - `status` (string) — `success`, `failed`, `template_disabled`, `template_not_found`
- **Where it is emitted:** `domains/freediscovery/discovery_engine.go`
  (`Run`, `failScanMetrics`)
- **Purpose:** Count scan task outcomes. `rate()` gives throughput; the
  `failed` series is the primary alert signal.

### `freediscovery_scan_duration_seconds`

- **Symbol:** `FreeDiscoveryScanDurationSeconds` (Histogram)
- **Labels:** none
- **Buckets:** Prometheus default buckets.
- **Where it is emitted:** `domains/freediscovery/discovery_engine.go`
  (success path and `failScanMetrics`)
- **Purpose:** Observe end-to-end scan latency from `running` to terminal
  state. Use `histogram_quantile()` for SLO dashboards.

### `freediscovery_active_scans`

- **Symbol:** `FreeDiscoveryActiveScans` (Gauge)
- **Labels:** none
- **Where it is emitted:** `domains/freediscovery/discovery_engine.go`
  (incremented on `pending → running`, decremented on terminal transition)
- **Purpose:** Current number of scan tasks in `running` state. Input for
  scheduling/concurrency decisions.

## Discoveries

### `freediscovery_models_discovered_total`

- **Symbol:** `FreeDiscoveryModelsDiscoveredTotal` (Histogram)
- **Labels:** none
- **Buckets:** `0, 1, 5, 10, 25, 50, 100, 250, 500`
- **Where it is emitted:** `domains/freediscovery/discovery_engine.go`
  (success path, after `saveResults`)
- **Purpose:** Distribution of models found per scan task.

### `freediscovery_resources_discovered_total`

- **Symbol:** `FreeDiscoveryResourcesDiscoveredTotal` (Counter)
- **Labels:** none
- **Where it is emitted:** `domains/freediscovery/discovery_engine.go`
  (success path, cumulative `Add`)
- **Purpose:** Cumulative count of models discovered across all scans.

### `freediscovery_tos_violations_total`

- **Symbol:** `FreeDiscoveryTosViolationsTotal` (CounterVec)
- **Labels:**
  - `provider` (string) — provider_code
  - `verdict` (string) — `avoid`, `caution`
- **Where it is emitted:** `domains/freediscovery/discovery_engine.go`
  (ToS pre-judgment loop in `Run`)
- **Purpose:** Track upstream policy risk. A spike usually means the
  provider changed model naming or the preset verdict needs review.

## Imports

### `freediscovery_import_total`

- **Symbol:** `FreeDiscoveryImportTotal` (CounterVec)
- **Labels:**
  - `status` (string) — `imported`, `skipped`, `conflicted`
- **Where it is emitted:** `domains/freediscovery/import_service.go`
  (`Import` outcome loop)
- **Purpose:** Track import outcomes feeding the route used to pull
  discovered free-tier models into the catalog. `conflicted` rate is the
  primary signal for tenant contention.

## URL Safety

### `freediscovery_url_safety_blocked_total`

- **Symbol:** `FreeDiscoveryURLSafetyBlockedTotal` (CounterVec)
- **Labels:**
  - `reason` (string) — `<field>_<class>` where field ∈ `base_url`,
    `models_endpoint` and class ∈ `required`, `too_long`, `control_chars`,
    `scheme`, `hostname`, `userinfo`, `fragment`, `blocked_ip`,
    `parser_bypass`, `whitespace`, `not_relative`, `scheme_relative`,
    `invalid_url`, `other`
- **Where it is emitted:** `domains/freediscovery/url_safety.go`
  (`isValidBaseURL` / `isValidModelsEndpoint` wrappers)
- **Purpose:** Count template-save-time URL rejections. Bursts indicate
  misconfigured upstream sources or probing attempts.

## Conventions

- Declare metrics only in `metrics/freediscovery_metrics.go`; wire them at
  the call sites listed above. Do not register duplicates.
- Histogram buckets default to Prometheus defaults; override only when the
  distribution is known (as with the models-per-scan buckets).
- Gauges reflect latest known state; do not increment.
- Label cardinality is bounded: `provider`, `status`, `verdict`, `reason`
  all come from small enumerated sets.

## Related Files

- `metrics/freediscovery_metrics.go` — symbol declarations.
- `domains/freediscovery/discovery_engine.go` — scan lifecycle emission.
- `domains/freediscovery/import_service.go` — import outcome emission.
- `domains/freediscovery/url_safety.go` — URL safety rejection emission.
