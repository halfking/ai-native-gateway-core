# R1: Advanced Routing

> Status: `RECONSTRUCTED-DRAFT`
> Facts are `SOURCE-VERIFIED`; new strategy interfaces are `NEW-DESIGN`.

## Source facts

- `SOURCE-VERIFIED`: OmniRoute currently exposes 19 public routing strategies in `src/shared/constants/routingStrategies.ts`, plus an internal `quota-share` mode. Older references to 17 or 18 are stale.
- `SOURCE-VERIFIED`: OmniRoute auto-combo uses a default 5% random exploration path and rotator-style exploitation. This is not Thompson Sampling.
- `SOURCE-VERIFIED`: Go already has tier/billing/sticky/P2C/pressure-aware routing and URSM v2. Go Bandit scorer code exists, but its `main.go` assembly is WIP/commented out.

## Proposed boundary

`NEW-DESIGN`: add only pure score functions or a narrow strategy interface inside the existing router. Reuse availability filtering, sticky, fp-slot, circuit, limiter, and URSM ordering. A strategy must not reintroduce unavailable candidates or mutate credential health.

Potential future modes include cost, cache, context, reset-aware, and quota-headroom scoring. Required data fields and nil/unknown-cost behavior must be specified before implementation. OmniRoute's exploration behavior must not be described as a Go Bandit equivalent.

## Gates

- Table-driven score tests with nil prices and missing quota data.
- Stable tie-breaking and bounded score computation.
- Shadow comparison against existing P2C before enabling.
- Explicit default-off setting and rollback to the existing planner.
