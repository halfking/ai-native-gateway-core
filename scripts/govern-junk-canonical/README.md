# govern-junk-canonical

One-shot, idempotent remediation for the junk standard-model rows the
pre-2026-09-10 discovery/refresh seeder left in `models_canonical`: raw
names had their vendor prefix stripped before seeding, so
`claude/opus-5` seeded a canonical row `opus-5` and `grok/4.6` seeded `4.6`
even though `claude-opus-5` / `grok-4.6` already existed. Junk rows carry
`source` `discovery` **or** `provider_refresh` (both paths shared
`EnsureCanonicalAndAliases`).

The forward matching bug is fixed in `modelname/match.go`
(`MatchStandardModels`); this tool cleans up the surviving rows and the
`provider_models.standardized_name` values that still hold truncated names.

## Usage

```sh
export LLM_GATEWAY_DATABASE_URL='postgres://...'

# phase 1 — diagnosis only, writes nothing (run this first, always)
go run ./scripts/govern-junk-canonical

# machine-readable diagnosis
go run ./scripts/govern-junk-canonical -json

# phase 2 — remediate, single transaction
go run ./scripts/govern-junk-canonical -apply
```

Connection: `-dsn` flag, else `$LLM_GATEWAY_DATABASE_URL`, else
`$DATABASE_URL`. Unlike `cmd/fetch-standard-iq`, the tool opens a plain pgx
pool and does **not** run schema migrations — a governance tool must not
mutate schema as a side effect of a diagnosis read.

Flags:

| flag | default | meaning |
|---|---|---|
| `-dsn` | env | postgres DSN |
| `-apply` | off | execute the remediation (default is diagnosis only) |
| `-min-score` | `0.85` (`modelname.AutoLinkThreshold`) | confidence gate for targets and redirects |
| `-sources` | `discovery,provider_refresh,auto_discovered` | `models_canonical.source` values treated as auto-seeded |
| `-json` | off | print the diagnosis as JSON |
| `-timeout` | `5m` | overall context timeout |

## How detection works (why there are no false positives)

Detection is a **pair test**, not a shape heuristic. A canonical row `c` is
a junk suspect when there is a prefixed raw name `r` (whose stripped base
equals `c`, or has `c` as a strict token suffix — the truncation shape) and
another standard row `t` such that:

1. `score(r → t | c removed) ≥ 0.85` — the re-joined name lands on `t`
   confidently. Scoring with `c` **removed** defeats the matcher's Rule 2:
   with the junk row in the catalog, `claude/opus-5` resolves to the junk
   `opus-5`; with it removed, `claude-opus-5` scores its honest 1.0.
2. `c` is a strict token suffix of `t` — `t` strictly *contains* `c`, i.e.
   the vendor prefix carried family information the stripped name lost.
3. `t` outscores `c`'s own hold on the raw (`0.99` base-exact beats the
   `0.9` containment the junk row gets from a vendor-qualified raw like
   `anthropic/claude-opus-5`, so the real row always wins its own name).
4. `t` is not just a punctuation twin (`claude-opus-4-6` vs
   `claude-opus-4.6` are spelling variants, not junk).

Verdicts:

- **fixable** — a single target clears all four conditions. `-apply`
  remediates it.
- **review** — junk evidence exists, but the top score is tied between
  different targets. Never auto-remediated.
- **withheld-foreign-alias** — junk evidence exists, but an ACTIVE alias
  spelling something *other than the row's own name* routes here (observed:
  client-facing `v4`, `3.8`, `5-3` landing on the junk `v4-flash`,
  `3.8-flash`, `5.3-flash` rows). Those aliases are live routing state an
  operator should decide about; never auto-remediated.
- **whitelisted** — the row matches the junk shape but is on the operator
  whitelist (below): an operator examined it and decided to KEEP it. Still
  reported (with the recorded rationale) so diagnosis stays transparent;
  never auto-remediated and never eligible as a redirect target.

## Operator whitelist

`OperatorWhitelist` in `plan.go` holds canonical names that match the
detection shape but are kept **on purpose**, each with the rationale that
drove the decision. Add an entry only after examining the row's traffic and
reference surface; entries must never be removed without re-running the
diagnosis and re-examining the row.

**`free` (kept 2026-09-12).** Seeded by provider 21 (OpenRouter) from the
raw name `openrouter/free`. It is OpenRouter's free-pool pseudo-model: it
has no single model identity — OpenRouter's per-model free variants
(`z-ai/glm-5.2:free`, `minimax/minimax-m3:free`, …) each already have their
own canonical row — so the pair test ties at 0.99 across five different
`:free` targets and re-pointing the row at any one of them would be wrong.
Evidence behind the keep decision: zero requests ever routed here
(`request_logs`: 0 rows with `canonical_id = 2664333` and 0 rows for
`provider_id = 21`, all time), one reference (`provider_models` 2661145),
one alias (its own name). If OpenRouter traffic ever becomes relevant, the
row should be revisited as a whole — not resolved via a redirect.

### Adding a whitelist entry

The map in `plan.go` is the whitelist's system of record — there is no
`-whitelist` flag and no DB table, on purpose (rationale below). To keep a
row the diagnosis flags as junk-shaped:

1. **Diagnose.** Run `go run ./scripts/govern-junk-canonical -json` against
   the target database and collect the row's evidence: seeding path
   (provider + raw name), references and aliases pointing at it, the tied
   targets the pair test produces, and its full request history
   (`request_logs` by `canonical_id`, all time).
2. **Decide.** Whitelisting is for rows with no single correct identity
   (like `free`); if one target is actually right, fix the data instead
   (`-apply` or the SQL template) rather than pinning the junk shape.
3. **Edit `plan.go`.** Add the entry to `OperatorWhitelist`: key = the
   lowercase canonical name, value = the rationale — seeding path, why
   re-pointing is semantically wrong, traffic evidence with dates. Keep it
   3–6 lines; a future operator must be able to re-derive the decision from
   the string alone.
4. **Mirror the story here.** Add one README paragraph under the example
   above, operator-facing.
5. **Pin it with a test.** If the entry exercises a new invariant, extend
   `plan_test.go` (the `free` entry has a test pinning its
   `VerdictWhitelisted` verdict and its exclusion from the gate catalog).
6. **Commit code + README together** and re-run the diagnosis: the row must
   now report `whitelisted` (with the reason) and stop blocking or
   misleading the rest of the plan.

Removal runs the same path in reverse — but per the map's contract, never
remove an entry without re-running the diagnosis and re-examining the row
against current evidence.

**Why no `-whitelist` flag and no DB table.** The whitelist is a *decision
ledger*, not runtime configuration: every entry needs evidence, a written
rationale, a reviewable diff and a test. A CLI flag would let entries bypass
that trail and vanish with the shell history; a DB table would spread the
ledger across environments whose junk populations differ (a local-only keep
could silently mask a production row or vice versa) and would forfeit the
compile-time invariant tests that pin gate behavior. The tool is one-shot
and operator-run with an entry frequency of roughly one per junk remediation
cycle, so recompiling costs nothing; and the tool's own rule of never
mutating schema as a side effect would otherwise force bootstrap/migration
handling in every environment the DSN can point at.

Rows whose aliases are only the old pipeline's self-variants (`opus_5`,
`4-6`) do **not** count as foreign — they are part of the junk and get
re-pointed by apply.

## What `-apply` does

Per fixable suspect, inside ONE transaction:

1. **Redirect references.** Every `provider_models` row referencing the
   junk row (`canonical_id`, or a stale `standardized_name` with a
   different/absent `canonical_id`) is re-pointed to *its own* best
   standard row, gated at `-min-score` against the catalog minus the
   suspects. Rows whose `canonical_id` is already correct only get their
   `standardized_name` rewritten. Gate failures are skipped and **block
   deprecation**, so no live reference is stranded on a deprecated row.
2. **Re-point routing.** `model_aliases` is upserted so the junk name and
   every alias that landed on the junk row resolve to the target
   (`ON CONFLICT (canonical_id, raw_name) DO UPDATE`); then all aliases
   still pointing at the junk row are set to `deprecated`.
3. **Deprecate, never delete.** `models_canonical.status` → `'deprecated'`
   — and only after an in-transaction re-check that no `provider_models`
   row references it anymore.

Every statement is idempotent; re-running (or re-running after a partial
manual cleanup) converges to the same state, and a post-apply diagnosis
pass verifies zero fixable rows remain (exit code 1 otherwise).

Historical data (`request_logs.canonical_model` etc.) is intentionally left
alone.

## Companion SQL

`sql/fixes/fix-discovery-junk-canonical-rows.sql` mirrors the diagnosis for
pssql-only triage and documents the manual DML template. The Go tool is
authoritative: the scoring gate (`modelname.BestStandardModelMatch`) cannot
be reproduced in SQL.

## Tests

```sh
go test ./scripts/govern-junk-canonical/ ./modelname/
```

Covers the classic junk cases (`opus-5`, `4.6`), the Rule-2 trap, the
punctuation-twin guard, vendor-qualified raws keeping legit rows safe, the
consensus guard, the alias-gate, plan building, gate-blocked deprecation
and idempotency on an already-remediated dataset.
