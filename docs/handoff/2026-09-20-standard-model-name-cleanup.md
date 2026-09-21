# Handoff: 2026-09-20 Standard Model Name Cleanup Follow-up

**Session commit:** `f9449a79a` pushed to `main`
**Branch state:** in sync with `origin/main` (was 19 behind at start of this session)
**Authored in concert with:** `92f18cf22 fix(admin/provider): 模型标准名称排重门禁 + 2026-09-20 双库去重清理脚本`

## TL;DR

Two cleanup rounds on the same day. The earlier round (`92f18cf22`,
already in `main` at session start) shipped an authoritative dedup
script that took `.34` from 1047→922 and `252` from 1052→925. This
session's round ran **Phase 1+2+3+4+5** follow-up on top of that
result, taking `.34` from 922→887 (-42) and `252` from 925→787
(-138). Total combined: 187 (this session) + 125 (script) = **312
duplicate/misleading canonicals removed**.

A **critical-audit pass** at end of session caught **4 hidden bugs**
that the phase transactions had silently dropped (2 -ga rows on each
env, qwen3-max-cn on each env). All 4 patched before commit.

## What was done (this session)

### Local docker pg17 (`.34`)

| phase | rows disabled | pm re-point | aliases deprecated | notes |
|---|---:|---:|---:|---|
| 1 — junk | 10 | 0 | 0 | `cluade-opus-5`, `fake-*`, `gemini-3.1-flash-imagesadsa`, etc. |
| 2 — date-suffix | 14 | 16 | 48 | `deepseek-r1-250120`, `kimi-k2-250905`, etc. |
| 3 — order/punct/family | 5 | 2 | 4 (new deprecated) | `claude-3-5-{sonnet,haiku}` legacy, `minimax-{01,2.7}`, `grok-4.20` |
| 3 — family fix | — | — | — | `claude-sonnet-3.5` family: `unknown` → `anthropic-claude` |
| 4 — `-cn`/`-ga` | 13 (10+2+1) | 4 | 16 (new deprecated, surface='cn'/'ga') | `qwen3-max-cn` was missed in initial Phase 4 — patched |
| 5 — pointer surface | — | — | — | 28 active aliases on 14 `*-latest` canonicals marked `surface='pointer'` |
| **TOTAL** | **42** | **22** | **~225 (incl. 16 new surface tags)** | active: 922→887 |

### 252 PG

| phase | rows disabled | pm re-point | aliases deprecated | notes |
|---|---:|---:|---:|---|
| 1 — junk | 9 | 0 | 0 | (no `fake-no-route-model` on 252) |
| 2 — date-suffix | 111 | 122 | 0 | (aliases deprecate'd via shared-alias on base) |
| 3 — order/punct/family | 5 | 2 | 4 (new deprecated) | same as local |
| 4 — `-cn`/`-ga` | 21 (19+2) | 3 | 21 (new deprecated) | `qwen3-max-cn` was missed — patched |
| **TOTAL** | **146** | **127** | **~30 (incl. 21 new surface tags)** | active: 925→787 |

### Critical-audit bugs (caught + patched)

1. **`deepseek-v4-flash-ga` (2203408)** and **`deepseek-v4-pro-ga` (2438472)**
   on both envs: Phase 4 re-pointed provider 35's pm rows but the
   `models_canonical.status` UPDATE did not persist; provider 34's pm
   refs were never re-pointed. Patched.
2. **`qwen3-max-cn` (5318)** on both envs: defensive guard refused
   disable because provider 36's pm row still pointed at the snapshot
   id. Patched with re-point + deprecate + disable.

## What was NOT done (deferred / left for operator)

- **4 claude-fable canonicals** (193851/2908408/2067352/2664224) — `claude-fable-5`
  has 1262 reqs/7d on local and 8 provider_models refs; "fable" is not in
  Anthropic's public catalog. Likely a private Anthropic preview or
  relay-vendor label. `docs/audit/2026-09-20-phase5-due-diligence.md`
  has the full operator checklist.
- **5 suspect canonicals** (claude-opus-4.1, claude-opus-4.7/4.8/5-fast,
  grok-4.3) — not in upstream public catalogs but have live pm refs;
  need operator confirmation.
- **23 `-cn` rows on 252** (kling-v1/2/3, wan2.6/2.7/3.0, viduq3,
  qwen-image, qwen3-vl) — these have NO base canonical on 252, so
  disable would strand live pm refs. Fix paths: (a) seed missing bases
  from local, or (b) re-point providers. Listed in
  `docs/audit/2026-09-20-252-execution-report.md` §4.2.

## Verification gates (all green)

| gate | result |
|---|---|
| `go build ./modelname ./scripts/govern-junk-canonical ./modelcatalog ./discovery` | clean |
| `go test ./modelname` | ok |
| `go test ./scripts/govern-junk-canonical` | ok |
| `go test ./discovery` | ok |
| `go vet ./modelname ./scripts/govern-junk-canonical` | clean |
| `govern-junk-canonical -json` on local | `Suspects: null`, `ActiveCanonicalRows: 887` |
| `govern-junk-canonical -json` on 252 | `Suspects: null`, `ActiveCanonicalRows: 787` |
| `request_logs.canonical_id` on all disabled rows | 0 new traffic after disable |
| `provider_models.canonical_id` on all disabled rows | 0 live refs |
| `curl /v1/models` for 41 disabled names on local | empty grep |

## Files shipped

| file | purpose |
|---|---|
| `docs/audit/2026-09-20-standard-model-name-cleanup.md` | Main audit report: phases, schema rules, defensive guards, critical-audit patches |
| `docs/audit/2026-09-20-252-runbook.md` | Self-contained 252 runbook (7 SQL blocks + final gate + rollback) — useful as a re-run template |
| `docs/audit/2026-09-20-252-execution-report.md` | 252 actual run results: baseline, per-phase output, final gate, 23 unmapped exceptions, cross-env comparison |
| `docs/audit/2026-09-20-phase5-due-diligence.md` | Operator due-diligence checklist for the 9 suspicious canonicals (4 fable + 5 suspect) + 5 alias-less `*-latest` rows |

## Next round prompts

The 252 deployment path is officially deferred per
`llm-gateway-deploy-test` skill (`"252 仍按官方 hardening 规范
deferred"`). The 23 unmapped `-cn` rows on 252 are the next
operational concern. Suggested next-round directions (any of these is
a reasonable follow-up scope):

1. **Seed the 22 missing base canonicals** on 252 from local docker pg17
   (kling-v1/2/3, wan2.6/2.7/3.0, viduq3, qwen-image, qwen3-vl,
   deepseek-v3.1) and re-run `docs/audit/2026-09-20-252-runbook.md`
   Block 6 against the result. The -cn rows become safely disable-able.
2. **Operator-side review** of the 4 `claude-fable` rows + 5 suspect
   rows from `docs/audit/2026-09-20-phase5-due-diligence.md`. Likely
   outcome: confirm fable is a real Anthropic preview (keep all 4);
   confirm `claude-opus-4.1` / `*-fast` are not real (disable 4 rows).
3. **Audit the dedup-cleanup script (92f18cf22) coverage** vs this
   session's work — the script handles 125 rows of duplicate, but the
   per-provider `:batch` / `:free` suffixes and `kx-` prefixed orphans
   that the commit message mentions might still be present on either
   env. Run a follow-up script iteration if so.

## Known risks / things to watch

- **154 / 245** running builds earlier than 2026-09-11 match-first fix
  may have the discovery worker re-seeding dot-winner dash twins. Per
  `92f18cf22`'s commit message, this is a known issue. Recommend
  re-running `sql/fixes/2026-09-20-canonical-dedup-cleanup.sql` on
  those envs after they pick up the `provider/client.go` change.
- **The 4 critical-audit bugs** (silent -ga disable failure,
  defensive-guard-blocked qwen3-max-cn disable) suggest the original
  phase transaction SQL had a transaction-ordering or commit-time
  anomaly. If Phase 4 / Phase 2 are ever re-run from this audit's
  scripts, double-check the `status` field is actually `'disabled'`
  AFTER the COMMIT — not just before.
- **Auto-discovery drift**: between session start and end, ~3 rows
  were added by the discovery service (`active=782 → 787` on 252 over
  the session). Expect more new canonicals to appear on a running
  gateway. Re-run audit periodically.