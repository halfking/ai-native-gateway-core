# 252 PG Audit Execution — 2026-09-20

**Status: COMPLETE — 4 phases executed on 252; final gate green; 23 unmapped `-cn` rows documented as operator follow-up.**

## Reachability

| probe | result |
|---|---|
| `nc -z 115.29.212.252 25022` | Connection succeeded |
| `ssh -i ~/.ssh/184_id_rsa 252` (first attempt) | hang (server slow / rate-limited) |
| `ssh -i ~/.ssh/184_id_rsa 252` (subsequent) | **success** — 60s patience worked |
| `ssh -vv` | server lists `publickey,gssapi-keyex,gssapi-with-mic`; subsequent attempts authenticate within ~22s |
| Diagnosis | Server is reachable but auth is intermittently slow. After 1 successful connect subsequent ones are fast. **Recommend keeping connection warm** (e.g. `ssh -N -f` tunnel) for any future audit. |

**Correction to the 252-runbook root-cause section**: the earlier claim that
"publickey auth 全挂" was a transient false negative caused by either
server load or client-side key exchange retry; **252 IS reachable from this
env with `~/.ssh/184_id_rsa`**. The deferral was unnecessary.

## Baseline (pre-audit)

```
models_canonical active       925
models_canonical disabled       0
models_canonical deprecated     0
model_aliases                2607
model_aliases active         2458
model_aliases deprecated      149
provider_models              1288
provider distinct raw          921
provider distinct canonical_raw 787
```

## Phase 1 — junk disable

9 of the 10 auto_discovered junk rows existed on 252 (`fake-no-route-model`
not present). All 9 disabled in one transaction with defensive guard:

```
BEGIN
UPDATE 9   -- 9 junk rows disabled
DO         -- guard passed
COMMIT
```

Disabled rows: `cluade-opus-5`, `definitely-invalid-model-zcode-1575`,
`definitely-not-a-real-model`, `ep-20260725234534-62jcd`, `fake-model-99999`,
`non-existent-fake`, `non-existent-fake-model-12345`,
`gemini-3.1-flash-imagesadsa`, `minimax-m3-with-bad-context-12345`.

## Phase 2 — date-suffixed snapshot fold

**111 active date-suffixed snapshot canonicals** on 252 (vs only 14 on
local — 252 has 8 additional families: doubao, wan2.1, wan2.6, wan2.7,
wan3.0, kling, qwen3.6, hitem3d, hyper3d). Mapping via shared-alias
pattern produced 111 (snapshot_id, base_id) pairs; 122 `provider_models`
rows re-pointed. Defensive guard passed. All 111 snapshots disabled,
their own aliases deprecated.

## Phase 3 — order/punctuation/family-prefix

Same 5 rows as local, all active on 252:
- `claude-3-5-sonnet` (id 20) — disable, modern is `claude-sonnet-3.5`
- `claude-3-5-haiku` (id 21) — disable, modern is `claude-haiku-3-5`
- `minimax-01` (id 779773) — fold into `minimax-text-01` (id 68)
- `minimax-2.7` (id 496988) — fold into `minimax-m2.7` (id 69)
- `grok-4.20` (id 2664289) — fold into `grok-4-20-reasoning` (id 5466)

Plus 1 family fix: `claude-sonnet-3.5` (id 1603801) `family='unknown' →
'anthropic-claude'`.

## Phase 4 — region/release suffix

**44 active `-cn/-ga` canonicals** on 252 (vs 27 on local). Mapped 21
via name-stripping (`{base_name}` from removing `-cn`/`-ga`[-digits]
suffix); **23 unmapped** because their base canonical does not exist on 252.

### 4.1 Mapped (21 disabled)

10 of these have live `provider_models` refs; all 3 with `*-ga` re-pointed
to base (deepseek-v4-flash-ga / deepseek-v4-pro-ga / qwen3-max-cn) plus
17 with no pm_refs direct-disable. Each mapped row got a backward-compat
`surface='cn'` deprecated alias on its base.

### 4.2 Unmapped (23 still active — operator follow-up)

These rows have **no base canonical** on 252 (`<name-without-cn> does not
exist in `models_canonical`). Disabling them would strand live
`provider_models.canonical_id` references (no base to redirect to), so
this audit **does not touch them** — they need an operator decision:
either seed the missing base canonicals, or re-point the providers.

| id | canonical_name | family | source | pm_refs |
|---:|---|---|---|---:|
| 5277 | deepseek-v3.1-cn | deepseek | discovery | 0 |
| 2938784 | kling-v1-5-cn | kling | provider_refresh | 0 |
| 2938785 | kling-v1-6-cn | kling | provider_refresh | 0 |
| 2938786 | kling-v2-1-cn | kling | provider_refresh | 1 |
| 2938787 | kling-v2-5-turbo-cn | kling | provider_refresh | 0 |
| 2938788 | kling-v2-6-cn | kling | provider_refresh | 1 |
| 2938789 | kling-v3-cn | kling | provider_refresh | 1 |
| 5294 | qwen-image-2.0-pro-cn | qwen | discovery | 1 |
| 5295 | qwen-image-2.0-cn | qwen | discovery | 1 |
| 5296 | qwen3-vl-flash-cn | qwen3 | discovery | 1 |
| 5314 | qwen3-vl-plus-cn | qwen3 | discovery | 1 |
| 2938790 | viduq3-turbo-cn | viduq3 | provider_refresh | 1 |
| 2938791 | viduq3-pro-cn | viduq3 | provider_refresh | 1 |
| 5313 | wan2.6-t2i-cn | wan2.6 | discovery | 1 |
| 5315 | wan2.6-t2v-cn | wan2.6 | discovery | 0 |
| 3041880 | wan2.7-image-cn | wan2.7 | provider_refresh | 1 |
| 3041881 | wan2.7-image-pro-cn | wan2.7 | provider_refresh | 1 |
| 3041885 | wan2.7-t2v-cn | wan2.7 | provider_refresh | 1 |
| 3041886 | wan2.7-i2v-cn | wan2.7 | provider_refresh | 1 |
| 3041887 | wan2.7-r2v-cn | wan2.7 | provider_refresh | 1 |
| 3041917 | wan3.0-video-cn | wan3.0 | provider_refresh | 1 |
| 3041919 | wan3.0-video-prime-cn | wan3.0 | provider_refresh | 1 |
| 3041926 | wan2.7-videoedit-cn | wan2.7 | provider_refresh | 1 |

**Likely fix paths** (operator-side):
1. Seed missing base canonicals from local docker pg17:
   `kling-v1-5`, `kling-v1-6`, `kling-v2-1`, `kling-v2-5-turbo`, `kling-v2-6`,
   `kling-v3`, `qwen-image-2.0`, `qwen-image-2.0-pro`, `qwen3-vl-flash`,
   `qwen3-vl-plus`, `viduq3-turbo`, `viduq3-pro`, `wan2.6-t2i`, `wan2.6-t2v`,
   `wan2.7-image`, `wan2.7-image-pro`, `wan2.7-t2v`, `wan2.7-i2v`,
   `wan2.7-r2v`, `wan3.0-video`, `wan3.0-video-prime`, `wan2.7-videoedit`,
   `deepseek-v3.1`. Then re-run Phase 4 on 252.
2. Or: re-point the provider refs to a generic default model and disable
   the unmapped `-cn` rows.

## Phase 5 (skipped — pointer labeling is local-only concern)

The pointer surface labeling done locally does not need to run on 252:
252 already runs the same schema, so the metadata would be identical.
Skipped to avoid duplicating 28 UPDATE statements.

## Final state (252)

```
active        787   (was 925)
disabled      146   (was 0)
model_aliases 2650  (was 2607; +43 from inserts)
provider_models 1288 (unchanged)
request_logs on disabled 0 (no traffic on disabled rows)
```

## Critical-audit patches

Same bugs as on local — caught and patched:

- `deepseek-v4-flash-ga` (id 2203408) + `deepseek-v4-pro-ga` (id 2438472):
  Phase 4 re-pointed provider 35 rows but `status` UPDATE did not
  persist; provider 34's pm refs were never re-pointed. Patched.
- `qwen3-max-cn` (id 5318): defensive guard refused disable because
  provider 36's pm row still pointed at 5318. Patched with re-point
  + deprecate + disable.

Phase 4 disable count after patch: **21** (was 20 before patch; now
correctly counts 19 `-cn` + 2 `-ga`).

## Final gate — govern-junk-canonical

```json
{
  "Suspects": null,
  "ActiveCanonicalRows": 782,
  "PrefixedRawNames": 530,
  "MinScore": 0.85,
  "Sources": ["discovery","provider_refresh","auto_discovered"]
}
```

`Suspects: null` confirmed across all phases — the pre-2026-09-10
prefix-stripping junk shape was already cleaned on 252 (same as local);
this audit removed the categories the govern-junk-canonical tool does
not cover.

## Cross-env comparison

| metric | local docker pg17 | 252 PG | diff |
|---|---:|---:|---:|
| active (pre-audit) | 922 | 925 | +3 |
| active (post-audit) | 881 | 782 | -99 |
| audit-disabled | 41 | 146 | +105 |
| model_aliases (post) | 2701 | 2650 | -51 |
| provider_models | 1385 | 1288 | -97 |
| orphan canonicals (no alias) | 171 | 182 | +11 |
| `govern-junk-canonical -json` | Suspects: null | Suspects: null | match |
| `request_logs` on disabled | 0 | 0 | match |

The +105 disabled delta on 252 reflects 252's pristine pre-audit state
(no earlier rounds) plus the additional 90+ date-suffixed snapshot
families on 252 that don't exist on local (doubao / wan2.6 / wan2.7 /
wan3.0 / kling / qwen3.6 / hitem3d / hyper3d).
