# MiniMax Periodic Quota Recovery

## Summary

Fixed the recovery path for periodic-quota credentials that were also marked
`lifecycle_status='disabled'` by the provider-profile automation.

## Root Cause

The periodic quota probe and `CredentialProbeV2.ProbeNow` required
`lifecycle_status='active'`. An automatically profile-disabled credential was
therefore excluded even after its upstream quota window had reset. The normal
routing view correctly kept the node out of traffic, but no probe could gather
the evidence needed to recover it.

## Change

- Periodic quota probing now includes only non-manual, automatically disabled
  credentials whose quota recovery time has arrived.
- A successful probe restores the lifecycle to `active` and clears the
  automatic-disable audit fields.
- Manual disables, non-periodic quota states, and future recovery timestamps
  remain excluded.
- Active credentials keep the existing fast reprobe behavior; the recovery
  deadline is scoped only to the disabled periodic-quota branch.

## Verification

- `go test ./... -count=1`
- `go build ./...`
- `go vet ./...`
- Targeted MiniMax periodic-quota regression tests
