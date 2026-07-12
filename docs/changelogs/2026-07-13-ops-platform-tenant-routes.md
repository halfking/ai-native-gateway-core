# Ops Platform Route Repair

- Fixed the auth-hydration router deadlock that left deep-linked ops views as an empty `RouterView`.
- Added read-only tenant license and update views with tenant-scoped API endpoints.
- Added tenant ownership columns for licenses and releases.
- Fixed unsafe Element Plus table-slot destructuring in the five ops views and two tenant views.
- Verification: `npm run build`, `go test ./...`, and `git diff --check` passed.
- Browser-use verified all five ops views mount locally for a super-admin session, tenant-admin ops access resolves to Forbidden, and tenant views mount locally for a tenant-admin session.
