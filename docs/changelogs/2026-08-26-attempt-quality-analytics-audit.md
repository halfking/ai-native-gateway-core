# 2026-08-26 Attempt-Level Supplier Quality Analytics Audit

## Result

The gateway records retry, node-switch, and model-switch attempts, but current
supplier quality aggregation does not consume those attempt records. The
requirement is therefore partially implemented: request-level traceability is
available, while attempt-level supplier quality analytics is not.

## Verified Existing Coverage

- Dispatch creates a distinct attempt identity for each upstream forward and
  emits terminal success/failure events.
- RequestJourney durably persists content-free attempt, retry, and switch
  events under one request identity.
- The request-journey API exposes recent node and request timelines for
  operational diagnosis.
- Dispatch tests cover same-node retry, node switch, and model switch event
  sequences.

## Gap

`domains/providerprofile/adapters.go` aggregates quality inputs from
`request_logs_hot`, while that table is unique by `request_id`. A final success
can therefore overwrite or represent only the final request result; it cannot
provide one quality outcome for every attempted supplier node.

`internal/handlers/quality_handler.go` also reads the existing daily profile
and final usage data, rather than RequestJourney attempt events. Current
provider quality responses consequently cannot report attempt success rate,
retry rate, node-switch conversion, or model-switch conversion.

## Follow-Up

The proposed implementation and acceptance criteria are in
`docs/followup/attempt-quality-analytics/01-design.md`.

## Audit Evidence

- `go test ./domains/dispatch ./domains/requestjourney ./internal/streamretry ./cmd/gateway`
- `bash scripts/task-stop-audit.sh verify .acc-task-stop-summary.md`

The general security-review skill advertised a runner that was not present at
its declared local path, so no security-script pass is claimed by this audit.
