# OmniRoute -> Go Implementation Roadmap (Reconstructed)

> Status: `RECONSTRUCTED-DRAFT`
> Evidence labels: `SOURCE-VERIFIED`, `AUDIT-INFERRED`, `NEW-DESIGN`, `MISSING-EVIDENCE`.
> This file reconstructs an implementation baseline; it is not the missing original phase plan.

## 1. Baseline and provenance

| System | Evidence | Baseline |
|---|---|---|
| llm-gateway-go | `SOURCE-VERIFIED` | Current working-tree HEAD at planning time: `052dbd02034d1e04a92c4e216b87e5b66e12dcee` (`test(plugins): synchronize session manifest fixture`) |
| OmniRoute package metadata | `SOURCE-VERIFIED` | `/Users/xutaohuang/workspace/ai/OmniRoute/package.json` declares `3.8.49` |
| OmniRoute source snapshot | `SOURCE-VERIFIED` | Reference checkout previously inspected as `v3.8.48-1336-gc8f1d62de`, commit `c8f1d62de5d223aa209c7b0f7e09bb35382a4d2e`; this is not evidence of a published `v3.8.49` commit |
| Original phase documents | `MISSING-EVIDENCE` | The current Go checkout does not contain the original `docs/omniroute-ref` files, and their original text cannot be recovered from the available refs |

The existing `docs/omni-ref` documents are an audit and translation guide, not proof that the missing phase documents existed in this checkout.

## 2. Delivery order and gates

| Phase | Scope | Gate |
|---|---|---|
| Phase 1 | Provider catalog, advanced routing, Lite compression | Source inventory, DB/schema review, golden fixtures, default-off rollout |
| Phase 2 | Deep compression and MCP | Protocol/quality corpus, tenant/auth review, race tests, explicit migration approval |
| Phase 3 | A2A and Fusion | State-machine contract, cancellation/TTL tests, executor isolation, staged streaming policy |

Dependencies are `NEW-DESIGN`: provider catalog data is needed before routing fixtures; compression must define ownership before MCP/tool payload work; A2A and Fusion must not add dependencies from the existing Executor into new packages.

## 3. Evidence and rollout rules

- Preserve the Go database-driven provider model. Do not create a static provider registry as a substitute for `providers` data.
- Every new production capability is default-off or shadow-only until behavior, latency, and failure side effects are measured.
- New state must have an owner, TTL/cleanup rule, tenant boundary, and rollback switch before implementation.
- Changes to request execution must retain existing circuit, limiter, sticky, fp-slot, URSM, retry, and async-fallback ordering unless a separate design is approved.
- Any claim not backed by a current source path remains `MISSING-EVIDENCE`.

## 4. Explicitly deferred

O-5 sliding-window RPM, Lite/Deep compression production stages, MCP transport, A2A, Fusion, and Bandit activation are not implemented by this baseline. The current Go Bandit wiring is WIP/commented out; OmniRoute's auto-combo exploration is not Thompson Sampling.

## 5. Recovery and rollback

Documentation rollback means removing only this reconstructed tree and its navigation links. Feature rollback must be a runtime switch or removal of the newly injected component; no phase may require a destructive migration to disable. Migration numbers are intentionally not reserved by this document.

## 6. Phase index

- [P1 Provider expansion](phase1/01-provider-expansion.md)
- [R1 Advanced routing](phase1/02-advanced-routing.md)
- [C1 Lite compression](phase1/03-lite-compression.md)
- [C2-C4 Deep compression](phase2/04-deep-compression.md)
- [M1 MCP server](phase2/05-mcp-server.md)
- [A1 A2A protocol](phase3/06-a2a-protocol.md)
- [R3 Fusion routing](phase3/07-fusion-routing.md)
