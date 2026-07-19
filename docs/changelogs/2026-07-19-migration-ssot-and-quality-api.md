# Migration SSOT and Quality API Changes

Date: 2026-07-19

## Changes

- Consolidated the remaining hot-fix SQL migrations under `sql/migrations/startup/441-447`.
- Removed duplicate or superseded files from `deploy/sql/migrations/`.
- Added rollback scripts for the newly numbered migrations where a safe rollback is defined.
- Made the tenant wallet primary-key repair safe to retry when the constraint already exists.
- Fixed the Volcano alias migration so canonical model rows exist before alias inserts run.
- Updated the local routing migration script to use the startup migration SSOT.
- Added quality API route-boundary tests covering the 245 hot-fix failure modes.

## Verification

- `git diff --check`
- `bash -n scripts/local-r112-migrate.sh`
- `go test ./internal/handlers ./safety ./circuit ./pool/scheduler ./internal/quality`
- `go build ./...`
- `go vet ./...`

Database execution against 245/252 is intentionally a separate deployment gate and is not represented as passed by this local verification record.
