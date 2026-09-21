# Request Detail Body Fallback Audit — 2026-08-28

## Scope

This audit covers the verified request-detail body fallback only. It does not claim completion for unrelated enhancement proposals under `docs/design/` or `docs/implementation/`.

## Evidence

The production target request `127eaaaf93a9e3ff2873a5e299d90cb6` had a metadata row and a `request_logs_bodies` row with request and outbound payloads, but its `response_body` was SQL `NULL`. The same request had a matching `session_turns` row and `session_bodies` row.

This shows the missing response in the request-detail page was persisted-data incompleteness, not a frontend JSON decoding issue. Request metadata and available persisted bodies must remain visible, while session data may fill only absent fields.

## Implemented behavior

- `pgBodyReader.ReadRequestLogsBodies` keeps request-log bodies authoritative.
- When any request-log body field is absent, it reads the matching session turn.
- Only missing request, response, or outbound fields are filled from `session_bodies`; already-present request-log fields are never overwritten.
- If no fields exist in either store, the existing metadata-only fallback is retained.

## Verification

- `go test ./admin ./domains/requestdetail -count=1` passed.
- `TestPGBodyReaderFillsPartialBodiesFromSessionTurns` confirms only the missing response is filled from the session store.
- `git diff --check` was run for the files included in this change.

## Non-goals and follow-up

- This change does not reconstruct response payloads absent from both storage locations.
- The root cause of successful stream records whose `response_body` was not written remains a telemetry/write-pipeline investigation.
- The separately untracked request-detail enhancement design and summary documents are intentionally excluded because their claimed implementation is not present in this verified change.
