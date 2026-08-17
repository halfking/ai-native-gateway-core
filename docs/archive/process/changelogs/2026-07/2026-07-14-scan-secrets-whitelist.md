# 2026-07-14 — Scanner Whitelist Extension + Loadtest Artifact Hygiene

**Slug**: scan-secrets-whitelist
**Author**: ACC Agent
**Branch**: feature/deploy-ops-license-v2
**Phase**: 3B-5 (follow-up to Phase 3B-4 baseline governance, commit 68b20c627)

---

## What changed

Two small post-Phase-3B-4 hygiene fixes that were on disk in the worktree
but never made it into a commit before `349e6e532 docs: Phase 3B final
summary + CHANGELOG + audit update` closed the umbrella commit.

| File | Diff | Purpose |
|------|------|---------|
| `scripts/scan-secrets.sh` | +6 lines, WHITELIST_PATTERNS pt.1+pt.2 | Allow scanner to pass on legitimate placeholder patterns |
| `.gitignore` | +4 lines, `docs/**/results/*.json` | Stop loadtest runtime JSONs from polluting `git status` |

---

## Why

### 1. WHITELIST_PATTERNS extension

After commit `68b20c627 ci(scanner): Phase 3B-4 — unblock pre-push hook + baseline governance`,
scanner reports `0 BLOCK / 459 WARN / rc=0` — so the push gate is already green
via the `58` baseline entries. The +6 line extension tightens the **scanner
intelligence** further by making the matcher reject *placeholder* literals
instead of relying on a baseline exemption that hides them.

Two follow-up patterns were added:

```bash
# Phase 3B-4 cleanup: also skip lines with shell/env-var placeholders
# or bare REDACTED (not just angle-bracket <REDACTED>)
'REDACTED' '\$\{[A-Z_][A-Z0-9_]*\}' '\${[A-Z_][A-Z0-9_]*}'

# Phase 3B-4 cleanup pt.2: skip generic user:pass@host examples in docs
'user:pass@host' 'user:password@host' ':pass@' ':password@'
'username:password@' 'dbuser:dbpass@'
```

Design decision (carried over from handoff `cf8aad1a9`):

- Whitelist = "legitimate placeholders the scanner should trust"
- Baseline = "known false-positives the scanner should suppress"
- Spec AC-10 wants **baseline empty**. Whitelist may grow freely.

So this commit shrinks the *need* for future baseline entries. If a later
docs refactor introduces a new placeholder shape (e.g. `<SECRET>`),
the fix lands here in `_FREE_` patterns rather than in the baseline file.

### 2. .gitignore / loadtest JSONs

`docs/全方面测试/05-执行流程.md` describes a loadtest suite that writes
per-scenario JSON outputs into `docs/全方面测试/results/`. Six files
(`S03_concurrency.json`, `S10_long.json`, `S11_quota_w{1,2}.json`,
`S15_cross_group.json`, `S16_before_recharge.json`) showed up in
`git status --porcelain` as `??` lines every time anyone ran the suite.

These are **runtime artifacts**, equivalent to a test-results folder.
Adding `docs/**/results/*.json` to `.gitignore` is the standard pattern.

---

## Verification

### Scanner

```bash
cd <worktree>
bash scripts/scan-secrets.sh --tracked-only --baseline=scripts/scan-secrets.baseline --mode=normal
```

Expected output (matches the actual run on 2026-07-14):

```
Files scanned: 5312
Total findings: 458
  By severity:
    WARN         458
⚠️  WARNING
```

`rc=$?` = 0 (not 1). The Phase 3B-5 whitelist extension is **defensive** —
it shrinks the future surface area where the baseline file would need
to grow, but the current 0 BLOCK state is achievable without it.

### git status hygiene

Before:
```
 M scripts/scan-secrets.sh
?? "docs/\345\205\250\346\226\271\351\235\242\346\265\213\350\257\225/results/S03_concurrency.json"
?? ... (5 more)
```

After:
```
 M .gitignore
 M scripts/scan-secrets.sh
```

6 spurious `??` lines disappear.

---

## Out of scope

- **Pt.3 whitelist** (`@localhost:` / `@127\.0\.0\.1:`) was described in
  the handoff as a third cleanup round. It was *not* in the working tree
  diff and is left out of this commit; can land separately if production
  re-introduces those patterns in third-party fixtures.
- **Real baseline cleanup** (the 57-entry `Phase 3B-4` allowlist block
  with `rotate-credentials.sh → scrub docs → drop section`): that's owned
  by the rotation checklist under `docs/changelogs/2026-07-14-credential-rotation-checklist.md`
  and out of scope for this two-line follow-up.

---

## Files in this commit

```
.gitignore                                | 4 ++
scripts/scan-secrets.sh                   | 6 +
docs/changelogs/2026-07-14-scan-secrets-whitelist.md | (new, ~110 lines)
CHANGELOG.md                              | +26 (Phase 3B-5 entry)
```

3 files changed.

---

## Test command (operator runbook)

```bash
git log --oneline -3 scripts/scan-secrets.sh
# should show: <this-commit> ... 948519323 feat(deploy): Phase 1

bash scripts/scan-secrets.sh --tracked-only --baseline=scripts/scan-secrets.baseline --mode=normal \
  | grep -E "^Files scanned|^Total findings|^    WARN|^    BLOCK"
# expected: WARN=458, BLOCK=0

git status --porcelain
# expected: clean
```
