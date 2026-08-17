# i18n Locale Key Parity Audit

## Changes

- Added the 618 missing `ja-JP` locale keys across the affected frontend modules.
- Updated the locale sync script so future missing-key repairs include `ja-JP`.
- Changed the parity tests to require every locale to explicitly define all `zh-CN` source leaf keys instead of relying on the English fallback.
- Fixed `i18n-audit.mjs --strict` so large JSON reports do not get truncated before strict validation.

## Verification

- `node scripts/i18n-audit.mjs --strict`
- `npm exec vitest run src/i18n/parity.test.ts src/i18n/keys_referenced.test.ts --run`
- `npm exec tsc -- --noEmit`
- `npm run build`
- `go test ./i18n ./middleware ./admin`
