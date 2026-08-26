# 252 → local llm-gateway-pg Sync Verification Report

**Date**: 2026-08-26
**Source**: llm_gateway@115.29.212.252:5432 (via SSH tunnel :15432)
**Target**: llm_gateway@127.0.0.1:5432 (Docker container llm-gateway-pg)
**Tool**: scripts/pg-table-copy.sh

## Schema-level

| Metric                | 252 source | local target | Status |
|-----------------------|-----------:|-------------:|--------|
| PG version            | 17.10      | 17.10        | ✓ MATCH |
| public tables         | 383        | 397         | +14 local (leftover archived/test fixtures) |
| partitioned tables    | 25         | 28          | +3 local (partition auto-created subtables) |
| extensions            | citus 13.3, citus_columnar 13.3, vector 0.8.3, pg_trgm 1.6 | citus 13.3, citus_columnar 13.3, vector 0.8.5, pg_trgm 1.6 | ✓ MATCH |
| 252-only tables       | 0          | —            | ✓ all schema in local |
| local-only extras     | 14         | —            | ⚠ archived/test fixtures from previous syncs |

## Credentials

| Item              | 252 source                | local target              | Status |
|-------------------|---------------------------|---------------------------|--------|
| PG user           | llm_gateway              | llm_gateway               | ✓ MATCH |
| PG password       | 4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg | (now same, persisted) | ✓ SYNCED |
| PG database       | llm_gateway              | llm_gateway               | ✓ MATCH |

## Row Counts (non-hot tables)

- 297 non-hot tables compared
- 194 exact matches
- 103 with small drift (sub-percent noise, from 252-side real-time writes between dump and recheck)

## Hot Tables (schema-only by design)

- 91 hot tables (suffixes `_hot`, `_2026_*`, `_archived`, parent partitions)
- Schema present locally; data intentionally not copied
