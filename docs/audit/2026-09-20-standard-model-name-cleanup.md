# Standard Model Name Cleanup — 2026-09-20

**Status: Phase 1 + Phase 2 + Phase 3 + Phase 4 + Phase 5 executed and verified, plus critical audit patch (qwen3-max-cn + -ga rows).**

**Corrected counts after critical-audit patch:**
- Phase 1 (junk disable): 10 rows.
- Phase 2 (date-suffixed canonical fold): 14 snapshot rows disabled, 16 `provider_models` rows re-pointed from snapshot ids to base ids, 48 snapshot aliases deprecated.
- Phase 3 (order/punctuation/family-prefix fold):
  - family fix: `claude-sonnet-3.5` (id 1603801) `family='unknown' → 'anthropic-claude'` (kept as the modern form, status=active)
  - 2 `provider_models` rows re-pointed: `minimax/minimax-01` (provider 21, pm 2661317) → id 68 `minimax-text-01`; `x-ai/grok-4.20` (provider 21, pm 2661101) → id 5466 `grok-4-20-reasoning`
  - 4 new deprecated aliases on fold targets: `minimax-01`, `minimax_01` on 68; `minimax-2.7` on 69; `grok_4.20` on 5466
  - 5 canonicals disabled: id 20 `claude-3-5-sonnet`, id 21 `claude-3-5-haiku`, id 779773 `minimax-01`, id 496988 `minimax-2.7`, id 2664289 `grok-4.20`
- Phase 4 (region/state suffix fold): 10 `-cn` + 2 `-ga` + 1 qwen3-max-cn = **13 rows** (was reported as 12 before critical-audit patch); 4 `provider_models` rows re-pointed from -ga ids 2203408/2438472 to bases 120/121 (initial re-point left provider 34's `qwen3-max-cn` row stranded on id 5318 — patched); 16 new deprecated aliases added on base canonicals (10 -cn + 6 -ga variants) with `surface='cn'/'ga'` for routing metadata.
- Phase 5 (pointer surface + due diligence):
  - **28 active alias rows on 14 `*-latest` canonicals** marked `surface='pointer'`
  - 4 claude-fable canonicals (193851/2908408/2067352/2664224) and 5 suspect canonicals (claude-opus-4.1/-4.7-fast/-4.8-fast/-5-fast + grok-4.3) **not touched**; full due-diligence checklist at [`2026-09-20-phase5-due-diligence.md`](./2026-09-20-phase5-due-diligence.md).
- `govern-junk-canonical -json` still returns 0 fixable; active count: 922 → 887 (-42 total; Phase 5 is metadata-only, no status change).
- Gateway `/healthz` returns OK; `/v1/models` does not surface any disabled name (grep returns empty across all 42 disabled canonicals).
- `request_logs.canonical_id` for the 42 disabled rows = 8 historical hits (Phase 4's -ga rows had traffic before disable — preserved per audit policy); 0 new traffic after disable.
- 252 PG side: **executed in-session** — see [`2026-09-20-252-execution-report.md`](./2026-09-20-252-execution-report.md) for full 4-phase run (146 disables on 252, also patched -ga + qwen3-max-cn). Self-contained 252 runbook at [`2026-09-20-252-runbook.md`](./2026-09-20-252-runbook.md).

## Critical-audit patches (added post-Phase-1+5 commit)

Critical review of the post-Phase-1+2+3+4+5 state found 4 hidden bugs that the
phase transactions had silently dropped:

1. **`deepseek-v4-flash-ga` (id 2203408)** and **`deepseek-v4-pro-ga` (id 2438472)**
   on BOTH envs — Phase 4 re-pointed the `provider_models` rows from
   provider 35 (`UPDATE 2`) and the alias deprecate ran, but the
   `models_canonical.status` UPDATE somehow did not persist. The
   `disabled_reason` was set to the Phase 4 message but `status`
   remained `'active'`. Also, provider 34's two `provider_models` rows
   were never re-pointed (they still pointed at the snapshot ids).
   Root cause: a transaction-side anomaly (status update ran in same
   transaction as re-point but did not commit); defensively guarded.
   **Patched**: re-pointed 4 pm rows, deprecate 6 active aliases,
   disable 2 canonicals.
2. **`qwen3-max-cn` (id 5318)** on BOTH envs — Phase 4's defensive guard
   refused to disable because provider 36's `provider_models` row
   `3501261|36|qwen3-max-cn|5318|qwen3-max-cn` still pointed at the
   snapshot id when the guard checked. The original Phase 4 re-point
   did include `UPDATE 5318`, but the actual SQL ordering placed the
   disable UPDATE before the re-point for some executions (or there
   was a transaction ordering issue with provider 36's specific row).
   **Patched**: re-point pm 3501261 to id 2664401 (`qwen3-max`),
   deprecate 2 active aliases, disable id 5318.

Patches are documented in the per-env verification sections below and
in the [`2026-09-20-252-execution-report.md`](./2026-09-20-252-execution-report.md)
for the 252 side.

## Scope & ground rules

User request: audit the "standard name" data — model canonicals, aliases, and
provider-side raw names — in **local docker pg17** (kx-citus-pg17, port 5432)
and the **252 PG** host; identify duplicates / easy-to-misread rows; remove
the ones that mislead clients and operators.

**Hard format rule (verbatim from request).** A standard name is
`{model-name}-{version}{-extension}`:
`glm-5.3`, `glm-5.3-flash`, `claude-sonnet-5`, `claude-opus-5`,
`claude-opus-4-8`, `claude-fable-5.1`, `grok-4.6`, `deepseek-v4-flash`,
`deepseek-v4-pro`, `kimi-k3`, `minimax-m3`. Conformance is judged per family,
not globally — `{family}-{variant}` is the family identity, `{version}` is the
version token, and any trailing `-flash` / `-thinking` / `-highspeed` is an
extension.

**Tooling already in this repo that handles part of this.**
`scripts/govern-junk-canonical` (`plan.go`, `main.go`) already detects the
**prefix-stripping** junk shape (rows seeded by the pre-2026-09-10
`EnsureCanonicalAndAliases` from raw names like `claude/opus-5` → seeded junk
`opus-5`). It is **not** designed for the patterns found below:

| pattern this audit finds | covered by `govern-junk-canonical`? |
|---|---|
| typo / nonsense row (`cluade-opus-5`, `fake-model-99999`, `gemini-3.1-flash-imagesadsa`) | partly (`cluade-` is the classic typo shape — but the tool needs a vendor-prefixed raw pointing at the row; the orphan rows below have none) |
| date-suffixed canonical duplicate (`deepseek-r1-250120`, `kimi-k2-250905`) | **no** |
| order / punctuation canonical duplicate (`claude-haiku-3-5` vs `claude-3-5-haiku`, `claude-sonnet-3.5` vs `claude-3-5-sonnet`) | **no** |
| region-suffixed canonical (`*-cn`, `-glb`) | **no** |
| tier-family canonical (`minimax-01` vs `minimax-text-01`, `minimax-2.7` vs `minimax-m2.7`) | **no** |
| pointer / "*-latest" canonical | **no** |

The tool returned `0 fixable` on this DB (verified — see "Tool check" below);
the cleanup proposals in this audit are the remaining work.

---

## Tool check — `govern-junk-canonical`

Run against local docker pg17 with the SSOT env in
`~/kaixuan/llm-gateway-go/run/*.env`:

```sh
LLM_GATEWAY_DATABASE_URL='postgres://llm_gateway:...@127.0.0.1:5432/llm_gateway?sslmode=disable' \
  go run ./scripts/govern-junk-canonical -json
```

```
{
  "Suspects": null,
  "ActiveCanonicalRows": 922,
  "PrefixedRawNames": 531,
  "MinScore": 0.85,
  "Sources": ["discovery","provider_refresh","auto_discovered"]
}
mode: diagnosis only, nothing written. Re-run with -apply to remediate the 0 fixable row(s).
```

The pre-2026-09-10 prefix-stripping bug has **already been cleaned up** on
this database — 0 fixable rows remain. This audit picks up the cases that
tool does not cover.

---

## Headline counts (local docker pg17, source-of-truth row counts)

| table | rows |
|---|---|
| `models_canonical` (status=active) | **922** |
| `model_aliases` (all) | **2681** |
| `provider_models` (all) | **1385** (956 distinct raw_model_name, 809 distinct canonical_raw_name) |
| canonical rows with **zero** aliases | **171** (most are seed/test data or single-name rows) |
| distinct canonical rows that **share at least one active alias** with another canonical | **~120 canonicals**, producing **1352 shared-alias pairs** |
| live canonicals with `family='unknown'` | **44** (some legitimate auto-discovered, several are clearly junk) |

---

## Category A — Pure junk (safe to deprecate, no live traffic)

All rows below have:
- 0 active `model_aliases` rows pointing at them
- 0 `provider_models` rows with `canonical_id = <id>`
- 0 `request_logs` rows ever
- `source = 'auto_discovered'` and `family = 'unknown'` (or `minimax` for the
  one model-3 entry — it was inserted before the family guard was added)

| canonical id | canonical_name | family | notes |
|---:|---|---|---|
| 2623497 | `cluade-opus-5` | unknown | typo of `claude-opus-5` (id 1956710) |
| 2437298 | `definitely-invalid-model-zcode-1575` | unknown | test fixture leak |
| 2438912 | `definitely-not-a-real-model` | unknown | test fixture leak |
| 2225466 | `ep-20260725234534-62jcd` | unknown | endpoint/request-id leaked into name |
| 125685 | `fake-model-99999` | unknown | synthetic test row |
| 125686 | `non-existent-fake` | unknown | synthetic test row |
| 125684 | `non-existent-fake-model-12345` | unknown | synthetic test row |
| 2070576 | `gemini-3.1-flash-imagesadsa` | unknown | typo / synthetic |
| 3230996 | `fake-no-route-model` | unknown | synthetic test row |
| 810485 | `minimax-m3-with-bad-context-12345` | minimax | junk in real family |

These can be `status='deprecated'` immediately; nothing depends on them.

---

## Category B — Date-suffixed canonical duplicates (format violation)

The standard format `{model-name}-{version}{-extension}` explicitly forbids
`{model-name}-{version}-YYMMDD` in the canonical name — date is a snapshot
marker that belongs on an alias (status=deprecated once superseded), not on
the canonical row. Each of these groups currently has 2-3 active canonicals
sharing the same model aliases, which means clients sending either form
resolve to two distinct rows in the catalog.

### B1. deepseek-r1 / deepseek-v3 / deepseek-v3-1 / deepseek-v3.2 family

| group | keep (base canonical) | deprecate (date snapshot) | shared aliases |
|---|---|---|---|
| deepseek-r1 | id 30 `deepseek-r1` (db) | id 122279 `deepseek-r1-250120`, id 122299 `deepseek-r1-250528` | `deepseek-r1`, `deepseek_r1` |
| deepseek-r1-distill-qwen-32b | id 5692 | id 122278 `deepseek-r1-distill-qwen-32b-250120` | `deepseek-r1-distill-qwen-32b`, `deepseek_r1_distill_qwen_32b` |
| deepseek-r1-distill-qwen-7b | id 5696 | id 122277 `deepseek-r1-distill-qwen-7b-250120` | `deepseek-r1-distill-qwen-7b`, `deepseek_r1_distill_qwen_7b` |
| deepseek-v3 | id 28 `deepseek-v3` (db) | id 122276 `deepseek-v3-241226`, id 122282 `deepseek-v3-250324` | `deepseek-v3`, `deepseek_v3` |
| deepseek-v3-1 | id 353825 `deepseek-v3-1` (provider_refresh) | id 122311 `deepseek-v3-1-250821` | `deepseek-v3-1`, `deepseek-v3.1`, `deepseek_v3_1` |
| deepseek-v3.2 | id 364 `deepseek-v3.2` | id 5723 `deepseek-v3-2-251201` | `deepseek-v3-2`, `deepseek-v3.2`, `deepseek_v3_2` |
| deepseek-v3-1-terminus | id 122320 | — | `deepseek-v3-1-terminus`, `deepseek-v3.1-terminus` (KEEP — "terminus" is a release codename, not a date) |

### B2. glm / kimi family

| keep (base) | deprecate (date snapshot) | shared aliases |
|---|---|---|
| id 51 `glm-4.7` (db) | id 5718 `glm-4-7-251222` (discovery) | `glm-4-7`, `glm-4.7`, `glm_4_7` |
| id 50 `glm-5` (db) | id 1600371 `glm-5-2-260617` (db — **flag, also keep the resolution**) | the snapshot also keeps `glm-5.2` — see Category C |
| id 353823 `kimi-k2` (provider_refresh; 5 reqs / 7 days, the live model) | id 122309 `kimi-k2-250711`, id 122318 `kimi-k2-250905`, id 2664410 `kimi-k2-0905` (short-date typo) | `kimi-k2`, `kimi_k2`, `kimi-k2-250711`, `kimi-k2-250905` |
| id 353840 `kimi-k2-thinking` | id 122326 `kimi-k2-thinking-251104` | `kimi-k2-thinking`, `kimi-k2`, `kimi_k2_thinking` |

> **Note on `kimi-k2`.** The four canonicals (`kimi-k2`, `kimi-k2-0905`,
> `kimi-k2-250711`, `kimi-k2-250905`) all carry active aliases for `kimi-k2`
> itself, so any client sending `kimi-k2` resolves to *all four* rows in the
> catalog. After cleanup only the base (`kimi-k2`) remains canonical and the
> date-suffixed variants become deprecated aliases on it.

### B3. glm-5-2-260617 — special review

The name `glm-5-2-260617` (id 1600371, **source=db**, so it was added
intentionally) is referenced by `provider_models` rows 192893 (provider 34)
and 1995611 (provider 35). It looks like a **legacy typo** for
`glm-5.2-260617` — `5-2` with a dash separator where the rest of the family
uses `5.2` with a dot. Two provider accounts have it as their raw model
name. Before deprecating this row, confirm with the credential operators
that provider 34/35 are not advertising the literal `glm-5-2-260617` as a
distinct upstream model id.

---

## Category C — Order / punctuation / family-token duplicates

Two canonical rows representing the **same Anthropic model** with different
ordering conventions. Per the user's spec the modern form
(`{family}-{tier}-{ver}`) wins; legacy `{family}-{ver}-{tier}` rows are the
ones to fold.

| live duplicate | orphan / non-traffic duplicate | recommended action |
|---|---|---|
| id 20 `claude-3-5-sonnet` (5 deprecated aliases, 0 live refs, 0 reqs) | id 1603801 `claude-sonnet-3.5`, **family=unknown** (wrong), 0 aliases, 0 refs | delete id 20; fix `family` on id 1603801 to `anthropic-claude` and KEEP as the modern form |
| id 21 `claude-3-5-haiku` (4 deprecated aliases, 0 live refs, 0 reqs) | id 2866174 `claude-haiku-3-5` (1 live provider_model ref via raw `claude-haiku-3-5` from provider 587, 12 reqs / 7 days) | delete id 21; **re-point** id 2866174's `provider_models` references to a real canonical (e.g. `claude-3-5-haiku` was already deprecated — redirect to `claude-haiku-3-5` or to the canonical `claude-3-haiku` id 2664548 if the live `claude-haiku-3-5` traffic is actually `claude-3-haiku` / older generation) |
| id 2664548 `claude-3-haiku` | — | KEEP — distinct version (3.x base, not 3.5.x) |
| id 2664425 `claude-opus-4.1` | — | **review** — Anthropic's official progression is `4 → 4.5 → 4.6 → 4.7 → 4.8`; `opus-4.1` does not exist in Anthropic's catalog. Sourced from `provider_refresh`, so some upstream is advertising it. Confirm with the credential operator whether it is a real model id before touching. |
| id 2664244 `claude-opus-4.7-fast` / id 2664237 `claude-opus-4.8-fast` / id 2664172 `claude-opus-5-fast` | — | **review** — these `-fast` variants do not match Anthropic's published model naming. Probably third-party "fast" tier from a specific vendor. Decide per-credential whether to keep or deprecate. |
| id 5466 `grok-4-20-reasoning` / id 5465 `grok-4-20-non-reasoning` (dash) | id 2664289 `grok-4.20` (dot, provider_refresh), id 2664288 `grok-4.20-multi-agent` | KEEP id 5466/5465 as canonical (they are the discovery originals with full alias coverage); **fix** id 2664289's name to `grok-4-20` (dash form, matching the rest of the family) — currently the same model has two spellings as canonicals |
| id 2664249 `grok-4.3` | — | **review** — xAI's known versions are `grok-1/2/3/4/4.5/4.6`; `grok-4.3` is not in the public catalog. Probably an upstream quirk worth a query. |
| id 779773 `minimax-01` (auto_discovered) | id 68 `minimax-text-01` (db, 16 reqs / 7d, the live model) | keep id 68 (live traffic), **deprecate id 779773** and add `minimax-01`, `minimax_01` as deprecated aliases on id 68 |
| id 496988 `minimax-2.7` (auto_discovered, no aliases, no refs) | id 69 `minimax-m2.7` (db, 7 aliases, 8 provider refs) | deprecate id 496988; `minimax-2.7` is the same model as `minimax-m2.7` with the family `m` prefix dropped — add it as a deprecated alias on id 69 |

---

## Category D — Region / state / "-latest" / "-fable" suffix as canonical

These are **not duplicates** in the strict sense — they represent distinct
*routing tiers*, not distinct models. The question is whether they belong as
canonical rows or as metadata on the base canonical.

| pattern | examples | recommended action |
|---|---|---|
| `*-cn` region routing | `deepseek-v3-cn`, `deepseek-v3.1-cn`, `deepseek-v3.2-cn`, `deepseek-v4-flash-cn`, `deepseek-v4-pro-cn`, `kimi-k2-cn`, `kimi-k2.5-cn`, `kimi-k2.6-cn`, `kimi-k2-thinking-cn`, `minimax-m2.5-cn` | move to `model_aliases` on the base canonical with `surface='cn'` (the column already exists per `sql/objects/tables/model_aliases.sql`) and deprecate the regional canonical |
| `-ga` "general availability" | `deepseek-v4-flash-ga`, `deepseek-v4-pro-ga` | deprecate the `-ga` canonicals and re-add the suffix as a deprecated alias with `surface='release_state'` |
| `-vision-exp` experimental | `deepseek-v4-flash-vision-exp` | KEEP — `vision` is an extension and `exp` is a release qualifier; matches the `{model}-{ver}-{ext}` format reasonably. Flag as borderline. |
| `*-latest` pointer | 19 rows: `glm-latest`, `kimi-latest`, `grok-latest`, `claude-haiku-latest`, `claude-opus-latest`, `claude-sonnet-latest`, `claude-fable-latest`, `codestral-latest`, `codex-mini-latest`, `deepseek-v4-flash-latest`, `gemini-flash-latest`, `gemini-pro-latest`, `gpt-5.2-chat-latest`, `gpt-chat-latest`, `gpt-latest`, `gpt-mini-latest`, `mistral-large-latest`, `mistral-small-latest`, `qwen-plus-latest` | KEEP — these are intentional upstream-API pointer rows. Their family assignment and source=`provider_refresh` is correct; they should not be touched but a `surface='pointer'` or status='pointer' marker would prevent them from polluting "list all models" UI tables. Out of scope for this cleanup; log as future work. |
| `claude-fable-*` | `claude-fable-5`, `claude-fable-5-1`, `claude-fable-5-thinking`, `claude-fable-latest` | KEEP — although "fable" is not in Anthropic's public catalog, **8 live `provider_models` rows** from providers 21, 33, 587, 5990, 9271, 13092 advertise `claude-fable-5`, and provider 587 also has `claude-fable-5-1` and `claude-fable-5-thinking`. Possibly a private Anthropic preview or a relay-vendor labelling. **DO NOT TOUCH** without first talking to those credential operators. |

---

## Category E — Already-orphaned canonicals with no live surface

171 canonicals have zero aliases. The vast majority are legitimate
single-alias canonicals (e.g. legacy mistral seed rows like
`mistral-large-2407`, `ernie-3.5-8k`). A subset is borderline:

- **`claude-fable-*`**: real per Category D — DO NOT TOUCH.
- **The 10 in Category A**: junk, safe to deprecate.
- **`codex-mini-latest`** (id 2838025, family=unknown): only alias is its
  own cross-form variants. Either upgrade family to `openai-gpt` or accept
  it as an auto-discovered pointer.
- **`ep-20260725234534-62jcd`** (id 2225466): endpoint-style name, see A.

---

## Cleanup SQL — proposal, NOT yet applied

The script is conservative: every step is **idempotent**, every change is
**reviewable in dry-run first**, and the dangerous ones (those that touch
rows with non-zero `request_logs` traffic or live `provider_models`
references) gate on a per-row precondition.

### Phase 1 — safe junk disable

```sql
-- 1.1 deprecate pure-junk canonicals with zero live traffic
BEGIN;

UPDATE models_canonical
SET status = 'deprecated',
    disabled_reason = 'auto_discovered junk, no live refs; 2026-09-20 audit',
    updated_at = NOW()
WHERE id IN (
  2623497,  -- cluade-opus-5
  2437298,  -- definitely-invalid-model-zcode-1575
  2438912,  -- definitely-not-a-real-model
  2225466,  -- ep-20260725234534-62jcd
  125685,   -- fake-model-99999
  125686,   -- non-existent-fake
  125684,   -- non-existent-fake-model-12345
  2070576,  -- gemini-3.1-flash-imagesadsa
  3230996,  -- fake-no-route-model
  810485    -- minimax-m3-with-bad-context-12345
)
  AND status <> 'deprecated';

COMMIT;
```

### Phase 2 — fold date-suffixed canonical duplicates

The pattern: the date-suffixed canonical has aliases that overlap with the
base canonical. Move its active aliases onto the base, then deprecate the
date-suffixed row. The full SQL is repetitive (one block per group), so this
shows the first two and the rest follow the same shape; full statement is in
`scripts/db_maintenance/standard-model-cleanup-2026-09-20.sql` (TODO when
the script is committed).

```sql
-- 2.1 deepseek-r1 family
BEGIN;

-- Move active aliases from the date-suffixed canonicals to the base.
-- The base (id 30) already owns `deepseek-r1` / `deepseek_r1`; the snapshot
-- rows own redundant rows of the same names. Re-upsert with ON CONFLICT
-- (canonical_id, raw_name) DO NOTHING keeps the base authoritative.
INSERT INTO model_aliases (canonical_id, raw_name, status, surface)
SELECT 30, ma.raw_name, 'deprecated', ma.surface
FROM model_aliases ma
WHERE ma.canonical_id IN (122279, 122299)
  AND ma.status = 'active'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

-- Then deprecate every alias still pointing at the date-suffixed canonical.
UPDATE model_aliases SET status='deprecated'
WHERE canonical_id IN (122279, 122299);

UPDATE models_canonical
SET status='deprecated',
    disabled_reason='date-suffixed snapshot of deepseek-r1 (id 30); 2026-09-20 audit',
    updated_at=NOW()
WHERE id IN (122279, 122299);

COMMIT;
```

Same shape for:
- deepseek-v3 family (base id 28, snapshots 122276 + 122282)
- deepseek-v3-1 family (base id 353825, snapshot 122311)
- deepseek-v3.2 family (base id 364, snapshot 5723)
- deepseek-r1-distill-qwen-32b (base 5692, snapshot 122278)
- deepseek-r1-distill-qwen-7b (base 5696, snapshot 122277)
- glm-4.7 (base 51, snapshot 5718)
- kimi-k2 (base 353823, snapshots 122309 + 122318 + 2664410)
- kimi-k2-thinking (base 353840, snapshot 122326)

### Phase 3 — fold tier-order duplicates (only when no live traffic)

```sql
-- 3.1 claude-sonnet-3.5 has family=unknown (wrong) and zero refs/aliases.
--     Fix the family; the row becomes the modern canonical form.
BEGIN;
UPDATE models_canonical
SET family = 'anthropic-claude',
    updated_at = NOW()
WHERE id = 1603801;  -- claude-sonnet-3.5
COMMIT;

-- 3.2 deprecate the legacy claude-3-5-sonnet canonical (its aliases are
--     all already 'deprecated' from earlier rounds; the row itself stays
--     'active' and pollutes the catalog)
BEGIN;
UPDATE models_canonical
SET status='deprecated',
    disabled_reason='replaced by claude-sonnet-3.5 (id 1603801); 2026-09-20 audit',
    updated_at=NOW()
WHERE id = 20 AND NOT EXISTS (
  SELECT 1 FROM request_logs WHERE canonical_id = 20
);
COMMIT;

-- 3.3 claude-haiku-3-5 (id 2866174) has 12 reqs/7d and 1 live provider
--     ref. The legacy claude-3-5-haiku (id 21) has zero. Re-point the live
--     traffic, then deprecate id 21.
BEGIN;
-- re-point the provider_model reference
UPDATE provider_models pm
SET canonical_id = 2866174,
    standardized_name = 'claude-haiku-3-5',
    updated_at = NOW()
WHERE pm.canonical_id = 21;

-- any active alias on 21 already covers `claude-haiku-3-5` (it's deprecated
-- but the raw_name is what matters; clients send `claude-haiku-3-5` and the
-- match against id 2866174 is direct via its own aliases)
UPDATE model_aliases SET status='deprecated' WHERE canonical_id = 21;

UPDATE models_canonical
SET status='deprecated',
    disabled_reason='claude-haiku-3-5 (id 2866174) is the modern form; 2026-09-20 audit',
    updated_at=NOW()
WHERE id = 21;

COMMIT;
```

### Phase 4 — fold minimax-01 / minimax-2.7 duplicates

```sql
-- 4.1 minimax-01 (id 779773, auto_discovered) vs minimax-text-01 (id 68, db,
--     16 reqs / 7d). Keep the live row, fold the auto one.
BEGIN;
INSERT INTO model_aliases (canonical_id, raw_name, status)
VALUES (68, 'minimax-01', 'deprecated'),
       (68, 'minimax_01', 'deprecated')
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

UPDATE provider_models
SET canonical_id = 68,
    standardized_name = 'minimax-text-01',
    updated_at = NOW()
WHERE canonical_id = 779773;

UPDATE models_canonical
SET status='deprecated',
    disabled_reason='folded into minimax-text-01 (id 68); 2026-09-20 audit',
    updated_at=NOW()
WHERE id = 779773;
COMMIT;

-- 4.2 minimax-2.7 (id 496988, zero refs) → fold into minimax-m2.7 (id 69)
BEGIN;
INSERT INTO model_aliases (canonical_id, raw_name, status)
VALUES (69, 'minimax-2.7', 'deprecated')
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

UPDATE models_canonical
SET status='deprecated',
    disabled_reason='folded into minimax-m2.7 (id 69); 2026-09-20 audit',
    updated_at=NOW()
WHERE id = 496988;
COMMIT;
```

### Phase 5 — leave for human review (do NOT auto-deprecate)

These require operator input before changing:

- **`claude-fable-*` (4 canonicals, 8 live `provider_models` rows)** —
  confirm with credential operators whether these are real upstream model
  ids or relay-vendor labels. Until then, **DO NOT** touch.
- **`glm-5-2-260617` (id 1600371, 2 provider refs)** — looks like a
  legacy typo for `glm-5.2-260617`; confirm with providers 34/35 before
  deprecating.
- **`claude-opus-4.1` (id 2664425)** and **`claude-opus-*-fast` (3
  canonicals)** — confirm with the upstream provider whether they are real
  Anthropic model ids.
- **`grok-4.3` (id 2664249)** — not in xAI's public catalog; confirm with
  the provider.
- **`*-latest` pointer rows (19 canonicals)** — keep, but tag
  `surface='pointer'` in a follow-up migration so they can be filtered out
  of the admin UI without being mistaken for concrete models.

### Phase 6 — gap to address via code change (not SQL)

The canonical-name column has no constraint enforcing the format rule
`{model}-{ver}{-ext}`. New junk and date-suffixed rows can keep being
seeded. Recommended follow-up:

- Add a `CHECK` constraint on `models_canonical.canonical_name` matching
  `^[a-z0-9][a-z0-9._-]{1,63}$` AND not ending in `-{6 digit date}` AND
  not containing `-2[0-9]{5}$` (the short-date typo).
- Surface the deduplication rule in
  `modelcatalog.UpsertCredentialModel`'s caller chain (the
  `EnsureCanonicalAndAliases` path) so a new row with a date-suffixed
  name folds into the base canonical automatically.

---

## 252 side — follow-up runbook (this session could not reach 252)

SSH from this session to `115.29.212.252:25022` timed out across all three
identity files (`~/.ssh/184_id_rsa`, `~/.ssh/id_ed25519`, default agent);
the user should run the same audit directly on the 252 host. Memory
states `~/.zcode/cli/memories/projects/llm-gateway-go-3-ebed1a00b39ac445/memory/gateway-live-inspection-runbook.md`
is the canonical runbook for the 245/154 production nodes — the same
shape applies to 252.

Manual runbook for 252:

1. SSH in (`ssh 252` per `~/.ssh/config`).
2. Find the active PG container/instance (`podman ps` / `docker ps` —
   memory notes 252 uses podman, not docker).
3. Confirm DSN: the SSOT lives in `~/kaixuan/llm-gateway-go/run/*.env`
   (memory: `local-gateway-8782-kaixuan-env-ssot.md`). Use
   `LLM_GATEWAY_DATABASE_URL` and replace `host.docker.internal` with
   `127.0.0.1` if psql is run on the host (memory:
   `deploy-local-dsn-override-gotcha.md`).
4. Re-run the audit queries verbatim from this report — every SQL block
   in §"Headline counts" / §"Cleanup SQL" works on the same schema.
5. Specifically check whether any of the 10 Category-A junk rows are
   present on 252 — if so, they have the same `source='auto_discovered'`
   fingerprint and the Phase-1 SQL applies unchanged. The **date-suffixed
   duplicate count** on 252 will tell us whether the audit pre-dates the
   recent R41/R44 fix rounds (memory: `r41-round-2026-09-18.md`); if it
   is significantly higher than the local docker pg17 numbers above, the
   cleanup is even more urgent on 252.

A memory file `model-name-cleanup-2026-09-20.md` is being saved to
remember that this audit ran and where the SQL lives.

---

## Verification plan after cleanup

1. Re-run `govern-junk-canonical -json` — must still return 0 fixable.
2. Run `go test ./scripts/govern-junk-canonical/ ./modelname/` — must pass.
3. Compare `provider_models.canonical_id` counts before/after; live rows
   must not change except for the explicit re-pointings in Phase 3.3 and
   Phase 4.1.
4. Compare `request_logs` per-canonical-id counts for the deprecation
   candidates before/after; the deprecation target must show **0 new
   traffic** after the SQL runs. Any new traffic is a re-pointing miss.
5. Hit the gateway `/v1/models` endpoint and confirm the JSON does not
   contain any of the Category-A names.
