# Incident 2026-07-09 — Request Logging Failure on host 252

**Status:** ✅ Resolved
**Severity:** High (request logging silently failed; UX errors visible to all logged-in users)
**Duration:** 2026-07-08 (frontend SystemStatusIndicator introduced) → 2026-07-10 02:25 UTC+8
**Hosts affected:** 192.168.0.252 (Postgres), 47.97.111.154 (gateway), console users on https://llm.kxpms.cn

---

## 1. Summary

Two distinct but discoverable-together failures surfaced on 2026-07-09:

1. **Backend (request_wal_hot missing primary key)** — every API request tried to
   `INSERT … ON CONFLICT (request_id, created_at) DO NOTHING` into
   `request_wal_hot`, but the table was created without a primary key constraint.
   The `ON CONFLICT` clause therefore failed with SQLSTATE 42P10 and **all**
   request logs were silently dropped.
2. **Frontend (console errors)** — A new `SystemStatusIndicator` component
   called `GET /healthz?full=true` which the backend (after NET-007 fix on
   2026-06-28) requires the static `LLM_GATEWAY_ADMIN_API_KEY`. User JWT tokens
   cannot authenticate → 401. Combined with a Vue template bug that
   destructured `{ row }` from `undefined` (after data fetch returned late),
   this produced two distinct console errors visible to every logged-in user.

The fixes were completed in three commits on `main`:

| Commit | Scope |
|---|---|
| `1567cfd5` | SQL migration `345_request_wal_hot_independence.sql` (idempotent PK ensure) |
| `956f6b7f` | `domains/hooks/observability/telemetry/client.go` (affinity_hit ambiguity) |
| `631eecc6` + `674d5fbf` | Frontend destructure / non-null-assertion cleanup + healthz fallback |

---

## 2. Timeline

| Date / Time | Event |
|---|---|
| 2026-06-28 | Backend NET-007 fix (commit `905f8da7`) — `/healthz?full=true` now requires `LLM_GATEWAY_ADMIN_API_KEY` |
| 2026-07-08 23:48 | Frontend commit `d9f4a3c0` adds `SystemStatusIndicator` calling `getHealth(true)` → `/healthz?full=true` |
| 2026-07-09 16:00 | User reports console "healthy" — host 154 still runs version ≤ 949 (no NET-007 OR no SystemStatusIndicator) |
| 2026-07-09 19:08 | Deploy version 954 to 154 (contains both NET-007 AND SystemStatusIndicator) → 401 errors start |
| 2026-07-09 23:01 | Deploy versions 955, 956 — 401 persists |
| 2026-07-10 00:45 | First 252 fix attempt — creates `request_wal_hot` table, but PK constraint missing |
| 2026-07-10 01:08 | Second 252 fix — `ALTER TABLE ADD CONSTRAINT request_wal_hot_pkey PRIMARY KEY (request_id, created_at)` — request logs flow |
| 2026-07-10 01:16 | Commit `5911f1c9` — `_core.ts` swallows `/healthz?full=true` 401 to break login-loop, but error remains visible in console |
| 2026-07-10 01:19 | Deploy version 957 |
| 2026-07-10 02:24 | Commits `1567cfd5`, `956f6b7f`, `631eecc6` produced and merged into main |
| 2026-07-10 02:25 | Audit commit `674d5fbf` — closes remaining `{ row }` destructure + non-null assertion sites |
| 2026-07-10 (this doc) | Audit + cleanup of runbook archive |

---

## 3. Root Cause Analysis

### 3.1 Backend — `request_wal_hot` missing primary key

**Symptom (log on host 154):**
```
"request_logger: CreateInitial failed"
"error":"ERROR: there is no unique or exclusion constraint matching the ON CONFLICT specification (SQLSTATE 42P10)"
```

**Why:** Postgres requires an existing unique constraint or primary key on
columns referenced by `ON CONFLICT`. The hot-table clone was created via
`LIKE request_wal INCLUDING ALL`, but `INCLUDING CONSTRAINTS` does **not**
copy primary keys (only CHECK constraints), so the PK was missing on
`request_wal_hot`.

**Fix (`1567cfd5`):** Idempotent PK check added to migration 345, plus a
standalone `sql/fixes/fix-request-wal-hot-primary-key.sql` for ad-hoc repair.

### 3.2 Backend — `affinity_hit` column ambiguity (telemetry)

**Symptom:**
```
"telemetry request db persist failed"
"error":"ERROR: column reference \"affinity_hit\" is ambiguous (SQLSTATE 42702)"
```

**Why:** `INSERT INTO request_logs_hot … ON CONFLICT DO UPDATE` and the
follow-up `UPDATE` referenced `affinity_hit` unqualified; both
`request_logs_hot` and `request_logs` have that column.

**Fix (`956f6b7f`):** Qualified with table name in `COALESCE`.

### 3.3 Frontend — Vue destructure crash

**Symptom (browser console):**
```
[Vue Error] TypeError: Cannot destructure property 'row' of 'undefined' as it is undefined.
    at index-CTolYw8f.js:156:107393
```

**Why:** Element Plus `<el-table-column>` templates used
`<template #default="{ row }">`. When the table's `:data` binding was
transiently `undefined` (during data refetch, hot-reload, or empty-state
transitions), Vue's scoped-slot prop became `undefined`, and JS destructuring
threw.

**Affected:** 32 sites across 7 files (`SessionStatsPanel.vue`,
`TopSessionsTable.vue`, `SessionPanoramaView.vue`, `TaskAnalyticsView.vue`,
`UserProfileListView.vue`, `UserProfileView.vue`, `ClientAnalyticsView.vue`).

**Fix:** Converted to `<template #default="scope">` + `scope?.row?.property`
optional-chaining pattern.

### 3.4 Frontend — non-null assertion (`!`) crash

**Symptom:** Same `Cannot destructure property 'row' of 'undefined'` or
runtime null-deref in heatmap / sankey / detail views.

**Why:** Components used `<div v-else>` followed by `data!.property`, relying
on TypeScript non-null assertion that does not survive runtime nulls.

**Affected:** `HeatmapMatrix.vue`, `RouteFlowSankey.vue`,
`RoutingDashboardView.vue` (`layer2Cache[...]!`), `SessionContextDetailView.vue`
(`messagesData!`).

**Fix:** Replaced `v-else` with `v-else-if="data && data.rows && data.cols"`
(or appropriate null check) and removed all `!.` accesses.

### 3.5 Frontend — `/healthz?full=true` 401

**Symptom:**
```
GET https://llm.kxpms.cn/healthz?full=true 401 (Unauthorized)
Failed to load system status: Error: Unauthorized
```

**Why:** Backend NET-007 (2026-06-28) made `/healthz/full` an admin-token-only
ops endpoint. The new `SystemStatusIndicator` (2026-07-08) called
`getHealth(true)` which hit the legacy `/healthz?full=true` query-string
path — same admin-token requirement. User JWT tokens cannot authenticate.

**Fix:** Frontend now uses `getHealth(false)` (basic `/healthz`) — public
endpoint. `SystemStatusIndicator` wraps `getHealth(true)` in a try/catch with
fallback to `getHealth(false)` for graceful degradation when admin info is
unavailable.

### 3.6 Auth-middleware issue (predecessor)

Before 5911f1c9, the 401 on `/healthz?full=true` triggered the global auth
middleware's auto-redirect to login, causing a **login loop** for users whose
JWT could not authenticate. The loop is fixed by 5911f1c9 (added
`isAdminProtectedPath` heuristic that doesn't redirect on healthz errors).

---

## 4. Fixes (file-level)

### Backend

| File | Commit | Change |
|---|---|---|
| `sql/migrations/startup/345_request_wal_hot_independence.sql` | `1567cfd5` | Idempotent PK creation block |
| `sql/fixes/fix-request-wal-hot-primary-key.sql` | `1567cfd5` | Standalone repair script |
| `domains/hooks/observability/telemetry/client.go` | `956f6b7f` | Qualified `affinity_hit` column refs |

### Frontend

| File | Change |
|---|---|
| `web/src/components/analytics/HeatmapMatrix.vue` | `v-else-if` guard, no `!` |
| `web/src/components/analytics/RouteFlowSankey.vue` | `v-else-if="data && data.links"` |
| `web/src/components/SystemStatusIndicator.vue` | `getHealth(true)` with try/catch fallback to `getHealth(false)` |
| `web/src/views/RoutingDashboardView.vue` | `layer2Cache[...]!` → guarded access |
| `web/src/views/session-context/SessionContextDetailView.vue` | `messagesData!` → `v-else-if="messagesData"` |
| `web/src/components/SessionStatsPanel.vue` | `{ row }` → `scope?.row?.property` (6 sites) |
| `web/src/components/analytics/TopSessionsTable.vue` | (8 sites) |
| `web/src/views/SessionPanoramaView.vue` | (3 sites) |
| `web/src/views/TaskAnalyticsView.vue` | (6 sites) |
| `web/src/views/UserProfileListView.vue` | (3 sites) |
| `web/src/views/UserProfileView.vue` | (5 sites) |
| `web/src/views/ClientAnalyticsView.vue` | (1 site) |
| `web/src/api/_core.ts` | `isAdminProtectedPath` — does not redirect on healthz 401 |

---

## 5. Verification

### Build

```bash
$ cd web && npm run build
✓ built in 7.96s
✓ no type errors
✓ no remaining template destructure issues
```

### Greps (final state)

```bash
$ grep -rn 'template #default="{ row' web/src/        # expect 0
$ grep -rn 'data!\.' web/src/                          # expect 0
$ grep -rn 'layer2Cache\[.*\]!' web/src/               # expect 0
$ grep -rn 'messagesData!\.' web/src/                  # expect 0
```

### Database (host 252)

```sql
SELECT conname FROM pg_constraint
WHERE conrelid = 'request_wal_hot'::regclass AND contype = 'p';
-- request_wal_hot_pkey  ✓

SELECT COUNT(*), MAX(created_at) FROM request_wal_hot;
-- 3+ rows, growing  ✓
```

### Browser (host llm.kxpms.cn)

- Console: no `Cannot destructure property 'row'` ✓
- Console: no `GET /healthz?full=true 401` ✓
- `/routing-v2/dashboard` analytics tab: heatmap + sankey render ✓
- Top status indicator: shows G/D/R/T pills, no error overlay ✓

---

## 6. Lessons Learned

### What went well

- Symptom → root cause traceable from user console error in <30 minutes.
- Idempotent SQL migration pattern for adding PK constraints works for
  hot-cloned tables.
- Existing audit-grade logging in `request_logger` surfaced the SQLSTATE
  42P10 error clearly.

### What went wrong

- **Silent failure mode**: `request_wal_hot` is a write-only audit log, so
  the missing-PK bug did not break request forwarding. It was only caught
  because the new `SystemStatusIndicator` *also* started calling an
  auth-protected endpoint. **Lesson**: every new observability feature
  should be paired with assertions that existing observability is intact.
- **Frontend / backend drift**: NET-007 was deployed 2026-06-28; the
  frontend feature using the legacy endpoint was added 2026-07-08 — a
  10-day gap with no integration test. **Lesson**: any backend route change
  requires an immediate frontend audit or `BREAKING.md` notice.
- **Linter re-introducing `!`**: After the initial destructure fix
  (`631eecc6`), the linter apparently re-introduced some `data!.`
  non-null assertions, requiring a second pass (`674d5fbf`).
  **Lesson**: linter config should reject `!` after `.vue` file changes.
- **5 healthz/252 docs existed simultaneously** describing overlapping
  fragments of the same incident. **Lesson**: this runbook consolidation
  is overdue; future incident write-ups must use the
  `docs/INCIDENT-YYYY-MM-DD-<slug>.md` filename pattern.

### Action items

- [ ] Add `vue/no-non-null-assertion` rule to ESLint config (TODO
      assigned — file a follow-up issue)
- [ ] Add CI step that greps for `template #default="{ row"` and fails
      the build if found
- [ ] Add a `RequestLoggingProbe` Prometheus metric exposing
      `request_wal_hot.insert_failures_total` to surface silent failures
- [ ] Frontend: any change to `api/_core.ts` requires a sibling PR adding
      test coverage for the `isAdminProtectedPath` heuristic

---

## 7. References

- Runbook archive: `docs/runbooks/2026-07-09-request-logging-252/`
- Deploy runbook: `docs/FRONTEND_FIX_FINAL.md` (preserved for deployment steps)
- i18n TODO cleanup plan: `docs/runbooks/2026-07-09-request-logging-252/root-runbooks/i18n-fix-plan.md`
- Backend NET-007 fix: commit `905f8da7`
- Frontend `SystemStatusIndicator` introduction: commit `d9f4a3c0`
- All fix commits: `5911f1c9`, `1567cfd5`, `956f6b7f`, `631eecc6`, `674d5fbf`