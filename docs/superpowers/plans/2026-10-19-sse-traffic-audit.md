# Live-stream repeated state traffic Implementation Plan

> For agentic workers: execute this plan task-by-task with regression tests and independent code review before publication.

**Goal:** Stop retransmitting unchanged queue/node state every two seconds while preserving changed state, new-client delivery, request events, auth scopes and existing keepalive.

**Architecture:** Keep state digests on each SSE client, protected by its existing write mutex. Only queue_snapshot and node_update envelopes participate; compute digests without transport timestamps/read-only sample versions and record them only after successful writes. Ordinary successful events invalidate the cache to repair late initial replay; the next tick may resend one frame per kind. The initial replay remains authoritative and a newly registered client has no previous digest.

**Tech Stack:** Go1.27.2, net/http SSE, SHA256, httptest; existing frontend wire schema remains unchanged.

## Evidence and boundaries

252 completed access logs identified328.02GB live-stream responses over seven days; this is public egress, not measured airport billing. Current server sends full queue/node frames every2s without comparing content; the desktop discards hidden-page messages only after receiving them. Candidate hypotheses are repeated periodic state, hidden-page retention, and oversized initial/refresh snapshots. This slice reproduces and fixes the first; do not claim production byte savings before deployment/measurement.

## Tasks

- [x] Add real fan-out regression tests in admin/live_stream_periodic_dedup_test.go: repeated content, changed/empty state, newly joined client, request frames and concurrent delivery.
- [x] Run CGO_ENABLED=0 go test ./admin -run TestPeriodicState -count=1 against baseline39ff587b2; confirm duplicate frame failures before patch.
- [x] Modify admin/live_stream_sse.go: per-client bounded digest map; compare under writeMu, write successful state before committing digest. Keep initial data, other envelope types and25s comments unchanged.
- [x] Re-run targeted tests, broad live-stream tests, race, build and vet. Test the digest-disabled mutation to ensure new assertions fail.
- [x] Quantify fixture wire bytes with repeated large node snapshots; label measurements as fixture, never as supplier billing.
- [x] Review final source commit with generic and Go reviewers. Save full Task Stop Summary and exact reviewed SHA; commit in slices below200 lines, merge --no-ff from freshly fetched remote main and push without touching the original shared checkout.
- [x] Update audit/handoff and next prompt, record rollout and production measurement status. Roll back via git revert of this slice.

No dependency installation, DB/schema change, auth change, frontend schema change, or production rollout is part of this code slice. The existing deliberate negative admin retirement gate must remain unchanged and be reported separately if exercised.
