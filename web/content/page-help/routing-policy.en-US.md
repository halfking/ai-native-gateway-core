# Routing Strategy Page

Route selection parameters: hierarchy order, candidate limit, and fallback rules.

## Definitions

The strategy determines “how to select”; candidate data determines “what to select.”

## How to Self-Check When Numbers Do Not Match

- 1. If changing the strategy does not change traffic: the strategy only affects new requests; in-flight requests complete under the old strategy.
- If behavior is abnormal after changing the hierarchy order: the hierarchy levels have dependencies; after making changes, first validate the calculation on the Decision page.
