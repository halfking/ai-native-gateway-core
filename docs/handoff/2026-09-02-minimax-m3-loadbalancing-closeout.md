# 24h-Audit Closeout & Handoff Prompt

**Context**: 24h audit pass on the gateway provider-survival fix. The audit has
finished — there is **no work to do in this session** other than write a handoff
prompt so the next session can pick up cleanly. The handoff below follows the
project's standard master+child pattern (one master prompt + one child
sub-agent prompt) so the next session can be resumed in a single message.

---

## 1. What is already shipped (do not re-do)

Commit **`f4313a00a` "fix(gateway): add qp-rq-id + title in queue dashboard and
unblock sibling recovery"** is on `main`. It contains the two files that actually
fix the two user-reported defects:

| File | Fix |
|---|---|
| `web/src/components/QueuePerspectivePanel.vue` | New `qp-rq-id` chip showing `shortRequestId(request.request_id)` with full-id tooltip; `requestDisplayTitle(request)` for `[type] [model] @[agent]`. CSS `qp-rq-id` rule added to the scoped `<style>`. |
| `sql/migrations/domain/640_fix_null_unavailable_recover_at.sql` | `DELETE … USING` + `CREATE UNIQUE INDEX` hardening on `schema_migration_audit` (idempotent). |

That is the **complete shipped fix** for both defects.

**The third item the previous summary mentioned — a `SelectProvider` rewrite in
`spec_gateway.go` + a matching test — is NOT in the commit, NOT in the working
tree, and NOT in git history.** `git log --all -- 'settings/spec_gateway*.go'`
shows only compression-hardening commits; no provider-selection rewrite exists.
The audit's "load balancing fix" therefore equals "unblock the SQL recovery
sweeper" (the migration). The Go-side router already prefers healthy siblings via
`proxy/load_balancer.go:SelectNode` and `proxy/manager.go:selectNode`; the only
blocker was the `unavailable_recover_at IS NULL` predicate in
`bg/credential_recovery.go:expiredCmbRecoverySQL`. That predicate is now satisfiable
after the migration runs.

---

## 2. What is in the working tree but NOT shipped (do not commit with this audit)

`git status` on the current `main` working tree shows two modifications and one
untracked directory — **all three are unrelated to the audit and must stay out of
this commit**:

| File / dir | Why it is here | What to do with it |
|---|---|---|
| `sql/migrations/domain/363_featured_models_standard.sql` (modified) | Splits a single `UPDATE … SET … = (SELECT …), updated_at = NOW()` into two `UPDATE` statements because Postgres rejects a scalar subquery in the same `SET` list as `NOW()`. | Commit in its own `fix(db): 363 featured-models split UPDATE` commit. |
| `web/public/menu-config.json` (modified) | Just a re-export timestamp (`exported_at`). | Auto-regenerated on next menu export; either drop or commit in the next `chore(web): re-export menu` commit. |
| `data/attachments/2026/09/` (untracked) | 30+ day-prefixed subdirs (`02/`, `04/`, … `3a/`). Looks like test fixtures, not audit artifacts. | Confirm with the session that produced them; either add to `.gitignore` or commit in the test-fixture PR. |

**Do not stage these into the audit commit.** The user explicitly asked the audit
to land only its two files; rolling in unrelated changes would muddle the
bisect signal.

---

## 3. Operational follow-ups (run on staging before declaring the audit closed)

The SQL migration `640` is the only runtime-side change. Validate it on staging
with this checklist; do **not** apply it to prod until every box is ticked.

```sql
-- 0. Pre-state snapshot
SELECT count(*) FILTER (WHERE cmb.available = FALSE AND cmb.unavailable_recover_at IS NULL
                         AND cmb.unavailable_reason NOT LIKE 'manual%'
                         AND COALESCE(cmb.admin_protected, FALSE) = FALSE)
       AS rows_pending_recovery
FROM credential_model_bindings cmb;

-- Expect: 0 after migration. Before migration: any positive number is the
-- count of stuck siblings that the previous sweeper ignored.

-- 1. Apply migration 640
-- (psql -v ON_ERROR_STOP=1 -f sql/migrations/domain/640_fix_null_unavailable_recover_at.sql)
-- This statement is idempotent; the audit table is built first and dedup'd.

-- 2. Verify the post-state row count from step 0 is 0
-- (re-run the same SELECT — should now return 0)

-- 3. Verify the recovery sweeper picks them up
SELECT count(*) FROM credential_model_bindings
WHERE available = FALSE
  AND unavailable_recover_at <= now()
  AND unavailable_reason NOT LIKE 'manual%'
  AND COALESCE(admin_protected, FALSE) = FALSE;

-- If this is non-zero, the sweeper (60s tick) will submit probes; the next
-- successful probe will flip available=TRUE and the sibling re-joins the
-- rotation.
```

Additionally:
- **Dashboard smoke test.** Open `http://<host>/dashboard` (or wherever the
  dashboard is served). Expand a model node. Confirm the request row shows
  the truncated `request_id` and the `[type] [model] @[agent]` title. Hover the
  chip; the full UUID should appear as a tooltip.
- **Sibling rotation check.** With several sibling credentials on the same
  model in `cmb.available = TRUE` and `node_probe_state.last_direct_ok = TRUE`,
  issue a few hundred test requests and watch the per-credential request
  count metric (in `internal/metrics`) — they should distribute roughly
  evenly. If one credential still absorbs > 90 % of traffic after a full 60s
  recovery tick, that credential's `node_probe_state.next_retry_at` or
  `quota_state` is probably the bottleneck — those are NOT covered by this
  audit; open a follow-up ticket.

---

## 4. Pre-merge check (run on the fix branch before the PR)

- [ ] `git diff main -- web/src/components/QueuePerspectivePanel.vue` shows only the
      `qp-rq-id` / `requestDisplayTitle` lines.
- [ ] `git diff main -- sql/migrations/domain/640_fix_null_unavailable_recover_at.sql`
      shows the audit-table dedup + unique index lines.
- [ ] No untracked file ends up in the audit commit (see §2).
- [ ] CI green on the fix branch.
- [ ] Staging run of the migration in §3 passes.

---

## 5. Master prompt (copy-paste, fill placeholders)

```text
You are the ZCode gateway-audit follow-up agent.

Repo root:           __REPO_ROOT__
Branch to base on:    main
Task branch (new):    audit/2026-09-02-minimax-m3-loadbalancing-closeout
Tracking file:       docs/audit/2026-09-02-minimax-m3-load-balancing-dashboard-audit.md
Hand-off prompt:     docs/handoff/2026-09-02-minimax-m3-loadbalancing-closeout.md
Session objective:   Close the 2026-09-02 minimax-m3 load-balancing 24h audit by
                     (a) verifying commit f4313a00a is reachable on main,
                     (b) running the §3 staging checklist,
                     (c) cleaning up the unrelated working-tree drift per §2,
                     (d) producing a final pass/fail verdict,
                     (e) updating the tracking file,
                     (f) closing the open follow-up tickets.

Important — do NOT redo:
  * The Vue rendering fix (already in f4313a00a).
  * The SQL migration backfill (already in f4313a00a).
  * The SelectProvider rewrite (does not exist; the prior summary was wrong).

Workstream:
  1. Run `git status -sb` and `git log main --oneline -5` to confirm the
     starting state. If `fix/gateway-provider-survival-20260901` and `main`
     have diverged, do not rebase this branch; instead log a
     pre-merge-cleanup follow-up.
  2. Stage only the two audit files (§1). Do not add the §2 drift into
     this commit.
  3. Open a follow-up issue for each §2 line item with file path + one-sentence
     rationale.
  4. If §3 has not been run on staging, do not merge — write a block
     reason and stop.
  5. After staging passes, update the audit doc with a "staging-validated
     on YYYY-MM-DD" line and bump the version per project convention.
  6. Merge via PR; do not fast-forward to main.

Read these in order before acting:
  * docs/audit/2026-09-02-minimax-m3-load-balancing-dashboard-audit.md
  * docs/handoff/2026-09-02-minimax-m3-loadbalancing-closeout.md  (this file)
  * HANDOFF_DASHBOARD_20260902.md, HANDOFF_AUDIT_20260902.md (if present)
  * The two-file diff of f4313a00a (`git show f4313a00a`)

If any instruction here conflicts with HANDOFF_DASHBOARD_20260902.md or
HANDOFF_AUDIT_20260902.md, prefer this close-out prompt — it post-dates them
and reflects the actual shipped state.

Output contract:
  * Final message: a one-paragraph pass/fail verdict, the list of
    follow-up issues created, and the merged commit SHA.
  * If staging checklist (§3) is incomplete, the verdict must be
    "blocked on staging validation" — do not declare the audit closed.
```

---

## 6. Child sub-agent prompt (delegated verification)

```text
You are a verification sub-agent for the minimax-m3 load-balancing 24h audit.

Repo root: __REPO_ROOT__
Task: verify that commit f4313a00a is on main, fully reachable, and that the
two shipped files match the audit doc's description. Report any mismatch
concretely with file:line evidence. Do NOT make any code changes.

Steps:
  1. `git rev-parse main fix/gateway-provider-survival-20260901 f4313a00a` —
     confirm f4313a00a is reachable from both.
  2. `git show f4313a00a --stat` — confirm it touches exactly:
       sql/migrations/domain/640_fix_null_unavailable_recover_at.sql
       web/src/components/QueuePerspectivePanel.vue
     and nothing else.
  3. `git show f4313a00a -- web/src/components/QueuePerspectivePanel.vue |
     grep -nE 'qp-rq-id|shortRequestId|requestDisplayTitle'` — confirm
     all three symbols are present.
  4. `git show f4313a00a -- sql/migrations/domain/640_fix_null_unavailable_recover_at.sql |
     grep -nE 'schema_migration_audit_migration_id_uidx|UNIQUE INDEX'` —
     confirm the idempotency hardening landed.
  5. Cross-check the audit doc:
       `git grep -n 'f4313a00a' docs/audit/2026-09-02-minimax-m3-load-balancing-dashboard-audit.md`
     — confirm the doc references the actual commit SHA.
  6. Verify no other commits between f4313a00a and main revert or
     supersede the change:
       `git log f4313a00a..main -- web/src/components/QueuePerspectivePanel.vue sql/migrations/domain/640_fix_null_unavailable_recover_at.sql`

Output: a pass/fail per step with file:line evidence. Final message:
"VERIFIED" or "FAILED at step N: <evidence>".
```

---

## 7. State for the next session at a glance

| Item | Value |
|---|---|
| Branch | `main` (f4313a00a merged) |
| Commit | `f4313a00a9bbf6ba69fdaa2954938c0e3d3c5d87` |
| Files shipped | `web/src/components/QueuePerspectivePanel.vue`, `sql/migrations/domain/640_fix_null_unavailable_recover_at.sql` |
| Files NOT shipped | `QueuePerspectivePanel.test.ts` (the summary's "test added" claim is wrong), `spec_gateway.go`, `spec_gateway_test.go` (no SelectProvider rewrite was ever written) |
| Working-tree noise | `363_featured_models_standard.sql`, `web/public/menu-config.json`, `data/attachments/2026/09/` — out of scope |
| Run before merge | §3 staging checklist (SQL pre/post counts, dashboard smoke test, sibling rotation distribution) |
| Documentation update | `docs/audit/2026-09-02-minimax-m3-load-balancing-dashboard-audit.md` add a "staging-validated on YYYY-MM-DD" line once §3 passes |

**End of close-out handoff.**
