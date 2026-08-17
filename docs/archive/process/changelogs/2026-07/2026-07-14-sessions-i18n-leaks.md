# Sessions i18n leaks (P2) — 2026-07-14

## Summary

- Patched `sessions.ts` Chinese leaks across 7 locales (incl. zh-TW simplified → traditional).
- Added sync scripts under `web/scripts/` for safe, repeatable locale repairs.
- Wired `SessionReplayView` at `/admin/session-replay` (requires super).
- Fixed `deploy-245.sh` arithmetic warning from multi-line `grep -c` output.

## Validation

- `npm run i18n:check` + `npm run build` (web)
- `bash scripts/deploy-245.sh`
- Browser: de-DE `/admin/sessions` → Sitzungsverwaltung; `/admin/session-replay` reachable
