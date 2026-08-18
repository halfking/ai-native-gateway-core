# Design: Persistent Scope Revision for Reorder

Status: **DRAFT** (2026-08-19)
Owner: llm-gateway-go maintainers
Replaces (partial): computed SHA-256 hash in `admin/routing.go:764 candidateReorderRevision`

---

## 1. Motivation

Today the reorder API guards against stale writes with an opaque token computed
at response time from the live scope rows:

```go
// admin/routing.go:764
func candidateReorderRevision(rows []reorderScopeRow) (string, error) {
    sorted := make([]reorderScopeRow, len(rows))
    copy(sorted, rows)
    sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
    hasher := sha256.New()
    for _, r := range sorted {
        if _, err := fmt.Fprintf(hasher, "%d|%d|%d|%s\n",
            r.ID, r.CredentialID, r.ManualPriority,
            r.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
            return "", err
        }
    }
    return hex.EncodeToString(hasher.Sum(nil)), nil
}
```

This token is correct **as long as every priority write path touches
`updated_at`**. The reorder handler does (`applyReorderUpdate` sets
`updated_at = NOW()`), so it stays self-consistent. But there are two
recurring pain points:

1. **Same-token-equality forces refetch on every concurrent write.** Two
   super_admins dragging the same list a millisecond apart each compute the
   same hash pre-write and both lose: the second arrives, sees the same hash
   it just computed locally in the gap between its own SELECT and UPDATE,
   commits, and the first's hash is now invalid. The frontend then reports
   "排序已过期" — accurate, but noisy when admins are iterating quickly.
2. **Audit / forensic story is hard.** If a customer asks "which super_admin
   actually wrote this order at time T?", the SHA-256 hash gives no
   monotonic clue; the auditor has to correlate `routing_audit_log` rows
   against the binding update timestamps. A monotonic counter would let us
   point at "reorder #N" directly.

The fix is a **persistent, monotonically-increasing version number** stored
per `(raw_model)` row, bumped atomically by every priority write path. The
contract stays the same from the client's perspective (`expected_revision`
echoed in every reorder response), but the value becomes
`<scope_version>:<hex_hash>` so existing UI consumers keep working while the
backend gains a sortable, auditable sequence number.

## 2. Goals & Non-Goals

**Goals**

- One monotonic counter per `provider_models.raw_model_name` that monotonically
  increases on every priority / membership change.
- Bump is **atomic with the priority write** so the version always reflects
  the committed state and never lags behind it.
- The frontend contract is preserved: `expected_revision` is still a string
  echoed verbatim in the reorder response; the UI keeps parsing it as
  opaque, with no client change.
- The reorder handler continues to lock the scope with `FOR UPDATE OF cmb`
  and use the SERIALIZABLE transaction. The version table is **read once at
  the start of the transaction and compared** against `expected_revision` —
  no extra round-trips inside the critical section.
- Schema change is **additive** (new table + trigger) so the migration is
  zero-downtime: old code keeps computing hashes; new code starts reading
  versions the moment the migration commits.

**Non-Goals (deferred)**

- Multiplexing by `tenant_id`. Today there is one scope per `raw_model` and
  it is global. If we ever add per-tenant scopes the primary key needs to
  grow a tenant column; we explicitly do **not** redesign around that now.
- Moving the audit log write off `routing_audit_log`. The reorder handler
  keeps writing there inside the same transaction; we just gain the ability
  to correlate against `scope_version`.
- Replacing the opaque-token frontend contract. See §3.3 for why this is
  worth keeping.

## 3. Design

### 3.1 New table: `candidate_binding_scope_revision`

```sql
CREATE TABLE public.candidate_binding_scope_revision (
    raw_model       text PRIMARY KEY
                     REFERENCES public.provider_models(raw_model_name)
                     ON DELETE CASCADE,
    scope_version   bigint NOT NULL DEFAULT 1,
    scope_hash      char(64) NOT NULL,                 -- SHA-256 of (id,credential_id,priority,updated_at) sorted by id
    last_bumped_at  timestamptz NOT NULL DEFAULT now(),
    last_bumped_by  text                              -- requestActor of the winning transaction, nullable
);
```

- **Primary key is `raw_model`** (one row per scope). It is exactly the scope
  the reorder endpoint already targets (`req.RawModel`), so no extra index is
  needed.
- **`scope_version`** is the monotonic counter. `bigint` gives us 9.2e18
  writes before overflow; at any plausible rate that's effectively forever.
- **`scope_hash`** is the current SHA-256 of the scope, kept for diagnostics
  and as a defense-in-depth secondary check. The revision string the API
  returns is `<scope_version>:<scope_hash>`.
- **`last_bumped_at`** is for observability: "when did this scope last
  change?" mirrors the URSM v2 per-scope observability we already have.
- **`last_bumped_by`** is the `requestActor` string from the winning
  transaction. It lets the audit story skip the `routing_audit_log` join
  in 95% of cases. Nullable so the legacy / direct-SQL backfill can leave
  it null without forcing NOT NULL constraints.

### 3.2 Bump trigger

```sql
CREATE OR REPLACE FUNCTION bump_scope_revision()
RETURNS TRIGGER AS $$
DECLARE
    new_version bigint;
    new_hash    char(64);
    raw_model   text;
BEGIN
    -- Only bump on priority / membership changes. updated_at-only updates
    -- (e.g. caching refreshes) must not advance the counter, otherwise
    -- direct PATCH on unaffected columns would force a refetch.
    IF TG_OP = 'UPDATE'
       AND OLD.manual_priority = NEW.manual_priority
       AND OLD.provider_model_id = NEW.provider_model_id
       AND OLD.credential_id     = NEW.credential_id THEN
        RETURN COALESCE(NEW, OLD);
    END IF;

    -- Resolve raw_model_name once. Use NEW for INSERT/UPDATE, OLD for DELETE.
    raw_model := (
        SELECT pm.raw_model_name
        FROM provider_models pm
        WHERE pm.id = COALESCE(NEW.provider_model_id, OLD.provider_model_id)
    );
    IF raw_model IS NULL THEN
        -- Orphan row or provider_model deleted mid-flight; nothing to bump.
        RETURN COALESCE(NEW, OLD);
    END IF;

    -- Recompute hash + bump version inside the row's own statement.
    INSERT INTO candidate_binding_scope_revision (raw_model, scope_version, scope_hash, last_bumped_at)
    VALUES (
        raw_model,
        1,
        encode(digest(
            concat_ws('|',
                COALESCE(NEW.id::text, OLD.id::text),
                COALESCE(NEW.credential_id::text, OLD.credential_id::text),
                COALESCE(NEW.manual_priority::text, OLD.manual_priority::text),
                extract(epoch from COALESCE(NEW.updated_at, OLD.updated_at))::text
            ), 'sha256'),
        now()
    )
    ON CONFLICT (raw_model) DO UPDATE
        SET scope_version  = candidate_binding_scope_revision.scope_version + 1,
            scope_hash     = encode(digest(
                                concat_ws('|',
                                    NEW.id::text, NEW.credential_id::text,
                                    NEW.manual_priority::text,
                                    extract(epoch from NEW.updated_at)::text
                                ), 'sha256'), 'sha256'),
            last_bumped_at = now();

    -- Trigger fires per row in FOR UPDATE OF cmb, so we must update
    -- last_bumped_by outside the INSERT…ON CONFLICT (which can't read
    -- session-local state).
    UPDATE candidate_binding_scope_revision
       SET last_bumped_by = current_setting('app.actor', true)
     WHERE raw_model = $1$raw_model$1$;
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;
```

The trigger fires on `INSERT`, `UPDATE OF (manual_priority, provider_model_id,
credential_id)`, and `DELETE` against `credential_model_bindings`. Because the
reorder transaction already takes `FOR UPDATE OF cmb` on every scope row
**before** any UPDATE, the trigger is implicitly serialised: only one writer
holds the row at a time, so the `INSERT … ON CONFLICT DO UPDATE` is
race-free without an advisory lock.

> Note: `current_setting('app.actor', true)` requires the Go transaction to
> `SET LOCAL app.actor = $1` before issuing the UPDATE. This is the same
> pattern we use for the audit `actor` column and is already plumbed in
> `admin/handler.go`.

### 3.3 API contract (unchanged from the client's perspective)

```http
GET /api/routing/resolve?model=gpt-4
{
  "client_model":   "gpt-4",
  "canonical_name": "gpt-4",
  "candidates":     [...],
  "reorder_revision": "12:9f86d081…ad9"   // <-- format change: <version>:<hash>
}

PATCH /api/routing/candidate-bindings/reorder
{
  "raw_model":         "gpt-4",
  "expected_revision": "12:9f86d081…ad9",  // unchanged shape
  "items":             [...]
}

→ 200 OK
{
  "message":           "updated",
  "raw_model":         "gpt-4",
  "expected_revision": "13:5d49c1d7…",   // new version + new hash
  "items":             [...]
}

→ 409 Conflict (still 409; the UI's `error.status === 409` branch keeps working)
{
  "error": "stale candidate binding set, refetch and retry",
  "current_revision": "13:5d49c1d7…"
}
```

The frontend contract test in `web/src/api/_core.ts` does not need to
change — `ApiError(409, …)` is the sole signal the
`QueuePerspectivePanel.onDrop` branch uses. The string format only matters
inside `RoutingDashboardView`'s `expected_revision` field, which is opaque
text already.

### 3.4 Go-side changes

```go
// admin/routing.go (new helper)
type scopeRevision struct {
    Version int64
    Hash    string  // 64-char hex
    Raw     string  // "<version>:<hash>", formatted for transport
}

func loadScopeRevision(ctx context.Context, q pgxQueryRower, rawModel string) (scopeRevision, error) {
    var sr scopeRevision
    var hash string
    err := q.QueryRow(ctx, `
        SELECT scope_version, scope_hash
          FROM candidate_binding_scope_revision
         WHERE raw_model = $1
    `, rawModel).Scan(&sr.Version, &hash)
    if errors.Is(err, pgx.ErrNoRows) {
        return scopeRevision{Version: 0, Hash: "", Raw: ""}, nil
    }
    if err != nil {
        return scopeRevision{}, err
    }
    sr.Hash = hash
    sr.Raw = fmt.Sprintf("%d:%s", sr.Version, hash)
    return sr, nil
}

func parseScopeRevision(token string) (scopeRevision, error) {
    parts := strings.SplitN(token, ":", 2)
    if len(parts) != 2 { return scopeRevision{}, errors.New("malformed revision") }
    v, err := strconv.ParseInt(parts[0], 10, 64)
    if err != nil { return scopeRevision{}, errors.New("malformed version") }
    return scopeRevision{Version: v, Hash: parts[1], Raw: token}, nil
}
```

`fetchReorderScope` becomes:

```go
func fetchReorderScope(ctx context.Context, q pgxQueryRower, rawModel string, lock bool) ([]reorderScopeRow, scopeRevision, error) {
    sqlText := reorderScopeSQL
    if lock { sqlText += "\nFOR UPDATE OF cmb" }
    rows, err := q.Query(ctx, sqlText, rawModel)
    // … unchanged …
    rev, revErr := loadScopeRevision(ctx, q, rawModel)
    if revErr != nil { return nil, scopeRevision{}, revErr }
    return out, rev, nil
}
```

`handleRoutingCandidateBindingReorder` becomes:

```go
scope, currentRev, err := fetchReorderScope(ctx, tx, req.RawModel, true)
// …
expectedRev, parseErr := parseScopeRevision(req.ExpectedRevision)
if parseErr != nil || expectedRev.Version != currentRev.Version {
    writeError(w, http.StatusConflict, "stale candidate binding set, refetch and retry")
    return
}
// Defence-in-depth: also compare hash to catch scope contents drifting
// without a version bump (should not happen, but cheap to check).
if !secureEqualString(expectedRev.Hash, currentRev.Hash) {
    writeError(w, http.StatusConflict, "stale candidate binding set, refetch and retry")
    return
}
```

`handleRoutingResolve` likewise replaces the inline `candidateReorderRevision`
call with `loadScopeRevision(ctx, h.db, firstRaw, false)`. The fallback
behaviour (return `""` on error and `slog.Warn`) is preserved.

### 3.5 Migration backfill

The migration seeds every existing `provider_models.raw_model_name` into
`candidate_binding_scope_revision` with `scope_version = 1` and the
current `scope_hash`:

```sql
INSERT INTO candidate_binding_scope_revision (raw_model, scope_version, scope_hash)
SELECT
    pm.raw_model_name,
    1,
    encode(digest(string_agg(
        format('%s|%s|%s|%s',
            b.id,
            b.credential_id,
            b.manual_priority,
            extract(epoch from b.updated_at)
        ),
        '|' ORDER BY b.id
    ), 'sha256'), 'sha256')
FROM provider_models pm
LEFT JOIN credential_model_bindings b ON b.provider_model_id = pm.id
GROUP BY pm.raw_model_name;
```

Empty scopes (a `raw_model` with zero bindings) get an empty-string hash,
which is what `loadScopeRevision` already handles for missing rows.

## 4. Deploy order

| Step | Action | Notes |
|------|--------|-------|
| 1 | Merge migration `541_candidate_binding_scope_revision.{sql,down.sql}` | Adds table + trigger; backfills every existing raw_model. |
| 2 | Merge Go change `routing.go` (`scopeRevision` + `parseScopeRevision` + `loadScopeRevision`) | Reads from the new table; falls back to `""` on `pgx.ErrNoRows` for any race-window where the trigger hasn't run yet. |
| 3 | Bump `expected_revision` format from `hex` to `version:hash` | Single-deploy change; old clients receiving `version:hash` simply treat it as opaque text and store it; on their next refetch they get the new format. |
| 4 | Observe `routing_audit_log` + new `candidate_binding_scope_revision` for one release cycle | Confirm the trigger fires exactly once per reorder write and zero times on no-op UPDATEs. |
| 5 | Add integration test `TestRoutingCandidateBindingReorder_IntegrationBumpMonotonic` | Two consecutive reorders must yield `scope_version = 2`, then `3`. |

## 5. Risk analysis

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Trigger recursion (UPDATE on `credential_model_bindings` triggers another UPDATE on the same row via cascade) | Low | Infinite loop | The trigger only touches `candidate_binding_scope_revision`, never `credential_model_bindings`. Verified by static review. |
| Trigger fires during the `INSERT` of the backfill, double-counting `scope_version` | Medium | Backfill reports `version = 2` instead of `1` | The backfill runs **before** the trigger is created; the migration script must explicitly `CREATE TRIGGER` after the `INSERT … SELECT`. See §3.5. |
| Concurrent reorder writes both see `version = N` and one wins, the other gets spurious 409 | Low | UX noise (exactly the problem we're fixing) | The new `FOR UPDATE OF cmb` already serialises scope rows; the version read happens **inside** the locked transaction. Only one writer observes a given `(scope_version, scope_hash)` pair, so 409s now only fire when the client's view is genuinely stale (≥1 reorder happened between their SELECT and their PATCH). |
| `app.actor` GUC not set by legacy writers | Medium | `last_bumped_by` stays NULL for those rows | `current_setting('app.actor', true)` returns NULL rather than erroring; the audit story still works via `routing_audit_log`. |
| Hash format drift on `extract(epoch from updated_at)` | Low | Spurious 409 after each reorder even when only `updated_at` changed | The Go-side fallback already computes hash from `RFC3339Nano`. If we want strict parity we move the hash to a Go-side value (the trigger stays, but the API returns the Go-computed hash, not the SQL one). See §6 open question. |
| Long-running read replica skew | Low | Read replica returns stale `scope_version` | The reorder handler reads through `h.db` (the same pool that the writer uses). If we ever split read/write, the handler must use the writer pool for both reads. |

## 6. Open questions

1. **Hash computed where?** The trigger computes the hash in SQL with
   `digest(..., 'sha256')` from `pgcrypto`. The Go side currently computes
   the same hash inline. Two implementations of the same hash function is a
   recipe for drift.

   **Decision (2026-08-19, implementation):** Keep **both** implementations
   and use the hash as defence-in-depth. The trigger recomputes
   `scope_hash` from `pgcrypto` (`digest(..., 'sha256')`) and persists it
   on `candidate_binding_scope_revision`. The Go `loadScopeRevision`
   reads the stored value verbatim and surfaces it as the `<hash>` part
   of the wire token. `scopeRevisionsEqual` compares version first
   (source of truth) and hash second (catches a silent trigger break).
   The frontend never sees the hash except as opaque text it echoes back.

   Rationale: (a) `pgcrypto` is already enabled in `llm_gateway` per
   `sql/scripts/phase-22-extension-and-role-sync/00-extensions.sql`, so
   there is no extension-prep cost; (b) keeping the hash gives us a
   second signal if the trigger is ever disabled by a future migration
   and silent inconsistency leaks in; (c) the wire format stays stable
   across future field additions (the parser uses `SplitN(_, 2)` so any
   new suffix is preserved verbatim in `Raw`).

   The "drop hash, ship just `<scope_version>`" alternative remains on
   the table if the hash ever proves to be a maintenance liability; the
   current decision errs on the side of observability.
2. **Lock contention under heavy traffic.** `FOR UPDATE OF cmb` on every
   scope row serialises all reorder writers. Today this is fine because
   super_admin writes are rare (sub-Hz). If the dashboard ever sees
   auto-route writes (it doesn't), we'd need an advisory lock instead.
   Out of scope for this design.
3. **Backwards compatibility for clients caching the old hex-only
   revision.** They'd store a 64-char string; the new format
   (`<int>:<hash>`) starts with a digit and `:` which is not valid hex
   `0-9a-f`. The Go parser rejects it and the handler returns 400
   "expected_revision is required" — same UX as if the field were empty.
   This is acceptable; the fix is "refetch / refresh dashboard". We could
   detect this and return 409 instead, but the difference is cosmetic.

## 7. Test plan

- **Unit (no PG):**
  - `parseScopeRevision("12:abc…")` round-trips to `Version=12, Hash="abc…"`.
  - `parseScopeRevision("malformed")` returns `malformed revision`.
  - `parseScopeRevision("12")` (missing colon) returns `malformed revision`.
- **Integration (`LLM_GATEWAY_PG_URL`):**
  - `TestRoutingCandidateBindingReorder_IntegrationBumpMonotonic`: two
    consecutive reorders yield `scope_version = 2`, then `3`.
  - `TestRoutingCandidateBindingReorder_IntegrationResolveEcho`: a resolve
    followed by a reorder followed by another resolve shows the second
    resolve's `expected_revision` is `version+1`.
  - `TestRoutingCandidateBindingReorder_IntegrationNoOpBump`: an
    `UPDATE credential_model_bindings SET updated_at = NOW()` that does
    not change `manual_priority`, `provider_model_id`, or
    `credential_id` does **not** bump `scope_version`.
- **Manual GUI (`scripts/local-up.sh`):**
  - Log in as super_admin, drag binding 1 to position 3 → 200, version=2.
  - Refresh dashboard, drag binding 3 back to position 1 → 200, version=3.
  - With two browser tabs open, drag the same binding in both: tab A
    succeeds, tab B's PATCH returns 409, UI shows "排序已过期" and
    auto-refetches.
  - With `tenant_admin` role, `curl -X PATCH` returns 403.

## 8. Out of scope (recorded for posterity)

- Per-tenant scoping. If `provider_models.raw_model_name` ever stops being
  globally unique, the PK becomes `(tenant_id, raw_model)` and the
  trigger needs the tenant from `provider_models.tenant_id`.
- A "soft revision" that survives reorder writes but advances only on
  membership changes (binding INSERT/DELETE). Useful if we ever support
  partial reorder. Defer until needed.
- Removing the computed hash entirely. Keeping it as a secondary check is
  cheap (8 bytes / row) and gives us a defence-in-depth path if the
  trigger ever silently breaks.

---

**Status:** Ready for review. Migration draft in
`admin/reorder_scope_revision/migration_541_draft.sql`.
