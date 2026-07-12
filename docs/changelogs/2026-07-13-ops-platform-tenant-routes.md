# Ops Platform Route Repair

- Fixed the auth-hydration router deadlock that left deep-linked ops views as an empty `RouterView`.
- Added read-only tenant license and update views with tenant-scoped API endpoints.
- Added tenant ownership columns for licenses and releases.
- Verification: `npm run build`, `go test ./...`, and `git diff --check` passed.
- Full authenticated browser verification against the newly deployed build remains pending.
