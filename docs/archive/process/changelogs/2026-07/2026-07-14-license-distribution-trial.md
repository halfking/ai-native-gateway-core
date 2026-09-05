# License Distribution and Trial Activation

Date: 2026-07-14

## Changes

- Added `POST /api/v1/license/trial` to License Authority.
- Added email validation, IP/email rate limiting, configurable trial duration, and `TRIAL-` keys.
- Added customer gateway proxy at `/api/system/license/trial`.
- Added trial entry to `ActivationWizard.vue`.
- Added distribution/activation audit and v2 implementation documents.

## Verification

- `go test ./... -count=1`
- `npm run build` in `web/`
- `git diff --check`
- Browser opened local `/activate`; existing login modal prevented completing the unauthenticated flow.

## Follow-up

Persist trial issuance limits across License Authority replicas and complete browser verification with a configured test environment.
