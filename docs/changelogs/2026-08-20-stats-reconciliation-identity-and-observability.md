# stats: reconciliation identity, observability, and approval concurrency hardening

## Background

A follow-up audit found four gaps after the first reconciliation hardening
series:

- the row-cap split protection could still collapse legitimate daily or
  cross-tenant diffs under the migration 546 identity;
- reconciling every projection dimension against a provider-model source
  aggregation produced false `phantom_open` rows;
- `phantom_open` was persisted but indistinguishable from normal `open`
  diffs in Prometheus;
- concurrent overlapping approval batches could read the same unresolved
  diff before either transaction changed its resolution.

## Changes

- `domains/stats/reconciliation.go`: reconciliation now compares only
  `stats_usage_daily.dimension_type='provider_model'`, the dimension for
  which it has a source-fact aggregation. Derived tenant/provider/
  credential/person/error projections remain reportable but are not
  falsely classified as source-less.
- `domains/stats/reconciliation.go`: diff keys include tenant and UTC day.
  The writer uses the five-column conflict identity required by migration
  548: `(run_id, tenant_id, dimension_type, dimension_key, metric)`.
- `domains/stats/reconciliation.go`: `phantom_open` rows are counted
  separately from normal unresolved `open` rows, including through
  recursive reconciliation windows.
- `domains/stats/reconciliation.go`: restored the small missing-projection
  repair case (`source > 0`, `projected = 0`, absolute delta below the
  configured bound) while keeping projection-only phantoms excluded.
- `admin/stats.go`: approval batches sort diff IDs before locking and read
  each diff with `FOR UPDATE OF d`, preventing overlapping batches from
  holding the same rows in opposite order or producing duplicate
  adjustments.
- `domains/stats/event.go`: person hashes use a versioned length-prefixed
  input and normalize tenant whitespace before hashing, so colon-containing
  tenant/user values cannot collide before SHA-256.
- `metrics/stats_reconciliation_metrics.go` and docs: expose/document
  `phantom_open` and `panicked` labels.
- reconciliation integration fixtures now create the production
  `schema_migrations` ledger before applying 544/545 and apply migration
  548. A new PostgreSQL test verifies 548 accepts different tenant/day
  identities and rejects an exact duplicate.

## Deployment

Migration 548 is required before a gateway binary with the five-column
`ON CONFLICT` target is deployed. At the time of this changelog, 154 has
recorded 544-546 as applied; 548 is intentionally not marked applied here.

Recommended order:

1. Back up `stats_reconciliation_diffs`.
2. Apply 548 after 545 and 546.
3. Verify `idx_stats_reconciliation_diffs_run_tenant_dim_metric` exists.
4. Deploy the matching gateway image.
5. Watch `llm_gateway_stats_reconciliation_diffs_total` split by `open`,
   `phantom_open`, and `auto_repaired`, plus
   `llm_gateway_stats_reconciliation_runs_total{status='panicked'}`.

## Deferred work

`/api/v1/approvals/{id}/resume` needs its own P1 change: a transactional
resume claim/state machine so concurrent resume calls invoke the upstream
LLM exactly once. That work spans `approval_queue`, session resume code,
and HTTP response semantics and is intentionally kept separate from this
stats release.
