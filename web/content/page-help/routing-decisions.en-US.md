# Routing Decision Page

A log of the final routing decision for each request (who was selected and why).

## Definition

One row = one decision; includes the matched rule chain and final candidate.

## How to Check When the Numbers Do Not Match

- 1. Decision count ≠ request count: requests blocked by pre-checks do not enter routing and have no decision records.
- Two decisions for the same request: the primary call and the retry each generate one decision.
