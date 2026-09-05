# Popular Models Follow-up Drift Check

- `tenant_id` is verified in `sql/objects/tables/request_logs_hot.sql:9` and migration 341's tenant/time index.
- Tenant identity is sourced from existing `admin/context.go`, not a query parameter.
- Existing picker response shape and policy-first ordering are retained.
- The global cache is replaced with tenant-keyed entries because a single cached response would defeat SQL/ZSET isolation.
- Metrics use only terminal outcome labels; tenant IDs are excluded to avoid cardinality and disclosure risks.
