# A1: A2A Protocol

> Status: `RECONSTRUCTED-DRAFT`
> OmniRoute state/event references are `SOURCE-VERIFIED` where checked; Go implementation is `NEW-DESIGN`.

## Proposed contract

Model an A2A task with explicit `submitted`, `working`, `completed`, `failed`, and `cancelled` states. Store artifacts and ordered events, expose JSON-RPC plus SSE, enforce TTL cleanup, and propagate cancellation through a narrow executor adapter. Terminal states are immutable.

The Go repository currently has no A2A package or endpoint. The implementation must be isolated from the existing Executor dependency direction: A2A may call the executor, but the executor must not import A2A.

## Persistence and security gates

`NEW-DESIGN`: define a Store interface with in-memory test and PostgreSQL implementations. PostgreSQL transactions must lock the task row during state transitions and enforce tenant isolation. Auth must use gateway principals; arguments cannot override tenant identity. Cancellation, reconnect, duplicate submission, artifact size, TTL cleanup, and SSE disconnects require tests before rollout.

No migration or public endpoint is authorized by this reconstructed document alone.
