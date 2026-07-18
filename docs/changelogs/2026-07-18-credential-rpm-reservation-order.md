# Credential RPM Reservation Ordering

## Change

Move the per-credential RPM reservation in `Limiter.AcquireAll` until after
the global, provider, and credential semaphores are acquired.

## Why

The previous order recorded an RPM hit before concurrency acquisition. A
request that timed out while waiting for a semaphore therefore consumed RPM
capacity even though it never reached the upstream request path.

## Verification

- `go test ./domains/credential -run 'TestAcquireAll_RPM'`
- `go test -race ./domains/credential -run 'TestAcquireAll_RPM'`

## Rollback

Revert the implementation commit. The change is isolated to the limiter and
its regression test.
