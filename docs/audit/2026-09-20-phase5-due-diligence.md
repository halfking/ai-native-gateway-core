# Phase 5 Due Diligence — 2026-09-20

**Status: pointer surface labels applied; due-diligence items not auto-disabled — operator review required.**

## 1. Pointer surface labeling (executed)

14 of the 19 `*-latest` canonicals had active aliases; **28 active alias rows** were
marked `surface='pointer'` in a single transaction. Pointer marker is on
`model_aliases.surface` because `models_canonical` has no `surface` column.
The 5 `*-latest` canonicals with no active aliases
(`codestral-latest`, `codex-mini-latest`, `gpt-5.2-chat-latest`,
`mistral-large-latest`, `mistral-small-latest`) keep their existing state
— pointer marker cannot be applied at the alias level when no aliases exist;
the canonical_name pattern is the only remaining signal.

Governance hook: the `model_aliases.surface='pointer'` marker is now
filterable by `WHERE surface='pointer' AND status='active'` in admin /
`/v1/models` queries. Pointer rows can be hidden from "list models" UIs
without changing their `status`.

## 2. Due-diligence checklist (operator review)

The following rows are flagged as **live but semantically suspect** and
must not be touched without operator confirmation. None of them have been
disabled or re-pointed in this audit.

### 2.1 Anthropic "claude-fable" family — 4 rows

| id | canonical_name | family | source | pm_refs | req_7d | req_all |
|---:|---|---|---|---:|---:|---:|
| 193851 | claude-fable-5 | anthropic-claude | provider_refresh | **8** | **1262** | **2273** |
| 2908408 | claude-fable-5-1 | anthropic-claude | discovery | 4 | 18 | 26 |
| 2067352 | claude-fable-5-thinking | anthropic-claude | provider_refresh | 1 | 0 | 0 |
| 2664224 | claude-fable-latest | anthropic-claude | provider_refresh | 1 | 0 | 0 |

**Findings:**
- "Fable" is **not** in Anthropic's public model catalog (as of 2026-09-20).
- `claude-fable-5` is **deeply live** (8 providers, 1262 reqs/7d, 2273 all-time).
  This rules out the "synthetic test data" hypothesis from the audit's
  Category A — it is real upstream traffic.
- Likely explanations (in order of probability):
  1. **A private Anthropic preview / staging model** the 8 providers (587,
     5990, 33, 9271, 13092, 21, 36, ...) are routing through. Anthropic has
     had "Project Fable" referenced in some preview contexts.
  2. A **vendor-side label alias** that an upstream relay is rewriting to a
     public Anthropic model — the canonical name reflects the relay's
     internal naming, not Anthropic's.
  3. A **deliberate misdirection** by a provider to make traffic look
     anthropic-shaped when it's actually a different model.

**Action required:** confirm with operators of providers 587, 5990, 33, 9271,
13092, 21, 36 which upstream model `claude-fable-5` actually maps to.
Until confirmed, **do not disable**.

### 2.2 Anthropic opus non-existent versions — 4 rows

| id | canonical_name | family | source | pm_refs | req_7d | req_all |
|---:|---|---|---|---:|---:|---:|
| 2664425 | claude-opus-4.1 | anthropic-claude | provider_refresh | 2 | 0 | 0 |
| 2664244 | claude-opus-4.7-fast | anthropic-claude | provider_refresh | 1 | 0 | 0 |
| 2664237 | claude-opus-4.8-fast | anthropic-claude | provider_refresh | 1 | 0 | 0 |
| 2664172 | claude-opus-5-fast | anthropic-claude | provider_refresh | 1 | 0 | 0 |

**Findings:**
- Anthropic's published opus progression is `opus-4 → opus-4.5 → opus-4.6
  → opus-4.7 → opus-4.8 → opus-5`. `opus-4.1` is **not** in the public
  catalog; `-fast` is **not** an Anthropic-published tier (Anthropic's
  speed tier is implicit, not in the model id).
- All 4 are `source=provider_refresh`, meaning a credential refresh
  upstream surfaced these names. There is **zero traffic** on all four
  (`req_7d=0`, `req_all=0`).
- `*-fast` may be a third-party "fast inference" tier from a specific
  upstream provider (not Anthropic's own). `opus-4.1` may be a typo or
  internal version label.

**Action required:** confirm with operators of the upstream providers
listed in `provider_models.canonical_id = <id>` whether each is a real
upstream model id. If not, mark `status='disabled'` with reason
`"not in upstream catalog; provider operator confirmed 2026-09-20"`.

### 2.3 xAI grok-4.3 — 1 row

| id | canonical_name | family | source | pm_refs | req_7d | req_all |
|---:|---|---|---|---:|---:|---:|
| 2664249 | grok-4.3 | xai-grok | provider_refresh | 2 | 0 | 0 |

**Findings:**
- xAI's published Grok progression is `grok-1 → grok-2 → grok-3 → grok-4 →
  grok-4.5 → grok-4.6` (as of 2026-09-20). `grok-4.3` is **not** in the
  public catalog.
- 2 live `provider_models` refs, 0 traffic.
- May be a vendor's internal label for a `grok-4.x` checkpoint.

**Action required:** same as 2.2 — confirm with provider operators.

### 2.4 *-latest canonicals without active aliases — 5 rows

| id | canonical_name | family | source | active_aliases | note |
|---:|---|---|---|---:|---|
| 91 | codestral-latest | mistral | db | 0 | legacy db row; both self-aliases are deprecated (`codestral-2505`, `codestral-latest`); codestral is no longer a current Mistral model line |
| 2838025 | codex-mini-latest | unknown | auto_discovered | 0 | family=unknown wrong; should be `openai-gpt`; alias discovery probably failed |
| 66718 | gpt-5.2-chat-latest | openai-gpt | provider_refresh | 0 | alias discovery hasn't caught up; self-name will resolve via canonical_name |
| 88 | mistral-large-latest | mistral | db | 0 | deprecated self-alias `mistral-large-latest` + `mistral-large-2411`; legacy db row |
| 89 | mistral-small-latest | mistral | db | 0 | deprecated self-alias `mistral-small-latest` + `mistral-small-2503`; legacy db row |

**Findings:**
- These rows have `canonical_name LIKE '%-latest'` (the spec signal) but
  no active aliases to apply `surface='pointer'`. The pattern still
  identifies them as pointer rows when admins query
  `WHERE canonical_name LIKE '%-latest'`.
- `codex-mini-latest` has `family='unknown'` — same kind of mislabel as
  the `claude-sonnet-3.5` row fixed in Phase 3; recommend
  `family='openai-gpt'`.

**Action required:**
- 91 / 88 / 89 (codestral-latest / mistral-large-latest / mistral-small-latest):
  review whether `db` rows still represent current Mistral offerings.
  Mistral's current lineup uses `-latest`/`-24XX` snapshot convention;
  if these are stale, mark `status='deprecated'`.
- 2838025 (codex-mini-latest): fix `family='unknown' → 'openai-gpt'`
  (mirrors Phase 3's claude-sonnet-3.5 fix).
- 66718 (gpt-5.2-chat-latest): no action needed if the canonical is
  intended as a pointer — admin /models listings will still surface it.

## 3. Rollback

`surface='pointer'` is metadata; reverting is a single UPDATE:

```sql
UPDATE model_aliases SET surface = NULL
WHERE canonical_id IN (SELECT id FROM models_canonical WHERE canonical_name LIKE '%-latest')
  AND status = 'active'
  AND surface = 'pointer';
```

No DB constraint depends on the surface value, so rollback is safe and
side-effect-free.
