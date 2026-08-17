# Body-Table Split Design (request_logs → + request_logs_bodies)

> **Status**: Design doc (2026-07-02). Migration 328 + Go changes planned.
> **Goal**: Decompose `request_logs` 92-column monolith into a metadata table
>          (columnar-eligible) and a sibling body table (columnar also).
> **Audience**: Backend maintainers. Read once before changing Go code.

## 1. Problem statement

`request_logs` is huge because of three large JSONB columns:

| column | rows | avg size | total |
|---|---:|---:|---:|
| `request_body` (jsonb) | 6 581 | 170 KB | 1 092 MB |
| `outbound_body` (jsonb) | 1 993 | 51 KB | 99 MB |
| `response_body` (jsonb) | 5 742 | 902 B | 5 MB |

`request_body` alone is 99 % of the storage. **It overflows Citus columnar's
1 GB serialization buffer** when serialized for the stripe writer — that's
why `request_logs_archive_*` is forced to remain heap (migration 318b).

Indexes also reference these:

- `idx_request_logs_session_outbound` — partial WHERE `outbound_body IS NOT NULL`
- `idx_request_logs_provider_tool_calls` — partial WHERE `tool_calls IS NOT NULL`
- `idx_request_logs_has_attachments` — partial WHERE `attachments IS NOT NULL`
- `idx_request_logs_tool_calls` — gin(`tool_calls`)

Decision: **three of the four large JSONB columns move out**, leaving only
`tool_calls` (small, indexed, routinely queried) inline.

## 2. Target schema

### 2.1 `request_logs` (metadata-only, columnar-eligible)

Drop: `request_body`, `outbound_body`, `response_body`

Keep: everything else (89 columns), including `tool_calls`, `attachments`,
`compression_meta`, `quality_fix_actions`, etc.

### 2.2 `request_logs_bodies` (new sibling, columnar)

```sql
CREATE TABLE public.request_logs_bodies (
    request_id   text                     NOT NULL,
    ts           timestamp with time zone NOT NULL DEFAULT now(),
    request_body jsonb,
    outbound_body jsonb,
    response_body jsonb,
    PRIMARY KEY (request_id, ts)
) PARTITION BY RANGE (ts);

-- Each monthly partition inherits the same RANGE(ts) shape as request_logs
-- so admin can JOIN by (request_id, ts) with no rebroadcast.
```

### 2.3 Partition parity

Existing `bg.PartitionManager.ensure_*_partition()` ensures monthly partitions
for `request_logs`. We add a paired ensure for `request_logs_bodies`:

```sql
CREATE OR REPLACE FUNCTION ensure_request_logs_bodies_partition(target_ts timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    month_start date := date_trunc('month', target_ts)::date;
    month_end   date := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name   text := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = part_name AND relnamespace='public'::regnamespace) THEN
        EXECUTE format(
          'CREATE TABLE %I PARTITION OF request_logs_bodies
             FOR VALUES FROM (%L) TO (%L) USING columnar', part_name, month_start, month_end);
    END IF;
END;
$$;
```

And register in `bg/partition_manager.go::ensureSpecs()`.

### 2.4 Foreign-key-like assurance

We do **not** add a real FK (the partition parent would push it down to every
partition). Instead, a deferred integrity check `assert_request_logs_bodies_consistent()`
runs daily from the cron: counts must match between the parent and the body table.

## 3. Migration phases (atomic, reversible)

### 3.1 Migration 328a — Schema + backfill (no Go change yet)

```sql
BEGIN;
  CREATE TABLE request_logs_bodies (...);
  -- create monthly partitions for current + previous month
  SELECT ensure_request_logs_bodies_partition(now());
  SELECT ensure_request_logs_bodies_partition(now() - interval '1 month');
  SELECT ensure_request_logs_bodies_partition(now() + interval '1 month');

  -- Backfill existing rows (chunked by id)
  INSERT INTO request_logs_bodies
      (request_id, ts, request_body, outbound_body, response_body)
  SELECT request_id, ts, request_body, outbound_body, response_body
  FROM request_logs
  WHERE request_body IS NOT NULL OR outbound_body IS NOT NULL OR response_body IS NOT NULL;

  -- Verify counts
  CREATE TEMP TABLE _body_check AS
    SELECT 'metadata' src,
           count(*) FILTER (WHERE request_body IS NOT NULL OR outbound_body IS NOT NULL OR response_body IS NOT NULL) AS rows_with_body
      FROM request_logs
    UNION ALL
    SELECT 'bodies', count(*) FROM request_logs_bodies;

  -- If they're equal, COMMIT. If not, ROLLBACK.
COMMIT;
```

Once migration 328a completes, the bodies live in both places (temporarily).

### 3.2 Code change 1 — Dual-write INSERT (Go)

In `domains/hooks/observability/telemetry/client.go::PersistRequestLog`:

Before (single INSERT):
```sql
INSERT INTO request_logs (..., request_body, outbound_body, response_body, ...)
VALUES (..., $35, $36, ..., $24, $25, $26, ...)
ON CONFLICT (request_id, ts) DO UPDATE SET ...
```

After (two statements, single transaction):
```sql
INSERT INTO request_logs (..., tool_calls, attachments, ...)
VALUES (..., $35, $36, ...)   -- body columns dropped
ON CONFLICT (request_id, ts) DO UPDATE SET ...;

INSERT INTO request_logs_bodies (request_id, ts, request_body, outbound_body, response_body)
VALUES ($1, $ts, $35, $36, $37)
ON CONFLICT (request_id, ts) DO NOTHING;
```

The single-transaction guarantee preserves row consistency: if either fails,
both fail.

### 3.3 Code change 2 — Read paths

`admin/logs.go::requestLogRow` already structures reads; only `outbound_body`
and `response_body` are read into the row. Add a follow-up SELECT against
`request_logs_bodies` for the detail drawer:

```go
// existing main SELECT → fills requestLogRow (no body columns now)
// in same transaction:
SELECT request_body, outbound_body, response_body
  FROM request_logs_bodies
  WHERE request_id=$1 AND ts=$2
```

`data_lifecycle_blobs.go::SET request_body = NULL, outbound_body = NULL`
becomes `DELETE FROM request_logs_bodies WHERE request_id = $1 AND ts = $2`
(or kept as NULL-then-PRUNE two-step).

### 3.4 Migration 328b — Drop columns

After dual-write is verified for ≥ 7 days:

```sql
BEGIN;
  -- Final assertion: zero rows have request_body still populated
  SELECT count(*) FROM request_logs
    WHERE request_body IS NOT NULL OR outbound_body IS NOT NULL OR response_body IS NOT NULL;
  -- assert result = 0

  ALTER TABLE public.request_logs DROP COLUMN request_body;
  ALTER TABLE public.request_logs DROP COLUMN outbound_body;
  ALTER TABLE public.request_logs DROP COLUMN response_body;

  -- Recreate affected indexes that referenced the dropped columns
  -- (idx_request_logs_session_outbound referenced outbound_body IS NOT NULL)
  DROP INDEX IF EXISTS idx_request_logs_session_outbound;
  -- (a session_outbound index against bodies is rebuilt below)

  -- Once request_logs is metadata-only, convert every partition to columnar
  PERFORM columnar_heal();
COMMIT;
```

### 3.5 After migration 328b

- `request_logs_default` 1.2 GB → ~ 60 MB (columnar on 89 small columns)
- `request_logs_archive_2026_06` 2.5 GB → ~ 100 MB
- Body partitions (columnar): 1.2 GB → ~ 100 MB total (3 columns, huge JSONB)

## 4. Reversal strategy

- 328a (table + backfill): `DROP TABLE request_logs_bodies CASCADE;`
- 328b (drop columns + conversion): `ALTER TABLE request_logs ADD COLUMN ...`
  and `ALTER TABLE ... SET ACCESS METHOD heap;` — Postgres tracks the
  underlying data inside columnar metadata too; reverting 328b from
  columnar back to heap just rewrites the rows on first SELECT, and
  re-adding the columns is straightforward as long as the bodies table
  still has them.

## 5. Operational concerns

- **Backfill duration**: ~ 1 GB copied via INSERT … SELECT; expect
  30–90 s on 184. Schedule during low traffic.
- **Disk space**: temporarily 2× for the body columns during 328a.
  Migration must pre-check `pg_tablespace_size('pg_default')` > 2 GB free.
- **Replication lag**: 328b alters the parent table, which acquires
  AccessExclusive. With 31 pods this is brief; acceptable.
- **RLS**: `request_logs_bodies` should mirror `request_logs` RLS so
  admin UI rows match.

## 6. Status checklist

- [ ] 328a migration file authored
- [ ] `ensure_request_logs_bodies_partition()` installed on 184
- [ ] `bg/partition_manager.go` extended to call it
- [ ] Backfill done; counts match
- [ ] Go dual-write in `client.go::PersistRequestLog`
- [ ] Go dual-read in `admin/logs.go` detail drawer
- [ ] Go cleanup in `data_lifecycle_blobs.go` and `data_lifecycle_attachments.go`
- [ ] Ship to 184, monitor ≥ 7 days with both columns populated
- [ ] 328b migration file authored
- [ ] Run 328b on 184 during maintenance window
- [ ] Run `columnar_heal()` to convert new metadata partitions

## 7. References

- Phase 23 columnar invariant: `sql/scripts/phase-23-columnar-invariant/README.md`
- 184 audit (2026-07-02): see `docs/STORAGE_MIGRATION.md`
- Citus columnar 1 GB buffer: migration 318b comment
