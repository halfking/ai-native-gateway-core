# Request Action Timeline Page

Each action performed within the gateway for a request is arranged chronologically (at a finer action-level granularity than the stage tree).

## Definition

Action = a specific internal operation (reading or writing to the cache, applying rules, or sending an upstream request); multiple actions can run concurrently at the same time.

## How to Self-Check When the Numbers Do Not Match

- 1. The action count differs from the stage count: one stage contains multiple actions, so the granularity differs.
- The timestamp interval is 0: sub-millisecond actions are displayed as a group rather than omitted.
