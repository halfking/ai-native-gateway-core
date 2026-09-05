# Provider Quality And Failover Logic Points

## LP1: Preserve degraded siblings

- Candidate quality is a ranking signal, not an availability hard gate.
- Manual disable and permanent states stay excluded by the authoritative view.

## LP2: Model-level operator diagnostics

- Return every configured node for a model.
- Include routing state, block reason, request success/latency, and probe conflicts.

## LP3: Failover validation

- Verify on 245 that a degraded sibling remains selectable after primary failure.
- Verify that manual-disable remains blocked and no production state is mutated.
