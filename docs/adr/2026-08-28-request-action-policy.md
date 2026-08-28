# ADR: Unified request action policy

- **Status:** Proposed / incremental implementation
- **Date:** 2026-08-28

## Context

The gateway already has a broad `errorsx.ErrorKind` taxonomy, but each layer
answers a different question with a different policy: `IsRetryable`, streaming
candidate loops, dispatch failover, survival aggregation, durable settlement,
and credential state writers. This made an empty response or a short network
flash look like either a generic retry, a node switch, or a terminal response
depending on the path.

A request action must be decided from both diagnosis and facts about the
request: correlation IDs, tenant/session, protocol, phase, attempt and
same-node budgets, selected provider/credential/model, alternate candidates,
and whether semantic output has reached the client. The complete history is
not synonymous with one log: it is the bounded union of actual attempt facts,
decision journal entries, monotonic tried-credential/model sets, and (for a
cross-process durable worker) the persisted attempt/settlement state. The
request-local history is the live decision authority; RequestJourney is an
ordered observation projection and must not be used to guess missing history.

## Decision

Keep `ErrorKind` as diagnosis and make `errorsx.DecideNextAction` the pure
source of truth for the next action. It returns an `ActionDecision` containing
stable correlation fields, the reason code, retry delay, and side-effect
intents. It never sleeps, writes a client response, changes routing, or writes
provider state.

The action precedence is:

1. Client cancellation/disconnect: stop without retry, failover, probe, or
   provider punishment.
2. Committed semantic output plus a recoverable error: `resume_blocked`; never
   transparently replay bytes already seen by the client.
3. Empty response and transport interruptions: while uncommitted, perform a
   bounded same-node retry to absorb a transient network flash; once that local
   budget is exhausted, switch to a sibling node if one exists; otherwise end
   with a terminal recovery-exhausted result.
4. Credential auth/quota failures: do not hot-loop the same credential; switch
   node or enter the quota recovery state according to the owning executor.
5. Rate limit, concurrency, overload, upstream-down, and no-channel outcomes:
   wait using the provider/header hint or bounded default, with optional probe
   intent. The executor may switch candidates when its route ladder requires it.
6. Request/model/content/capability and unknown conditions are terminal or
   fail-closed; they are never guessed into a retry.

## Ownership boundaries

- Bridges classify protocol facts and report `ErrorKind` plus commit facts.
- `errorsx.DecideNextAction` interprets those facts without side effects.
- Dispatch executes same-node retry, candidate switching, model fallback, and
  queue waits while enforcing route permissions and attempt budgets.
- Survival/durable workers aggregate candidates, enforce commit gates and
  deadlines, and map the central action to their existing task state machine.
- RequestJourney, dispatch journal, metrics, and audit logs consume the same
  action/reason fields; raw upstream bodies remain restricted to audit storage.

`errorsx.IsRetryable` remains for compatibility but means only “safe for a
generic same-error retry loop”. It is not the request-level action authority.

## Empty response contract

An empty response is a validly opened upstream response with no text, thinking,
or tool output. Before semantic commit, the client connection remains open while
the owning retry loop retries the current node. A node switch occurs only after
the local retry budget is exhausted. After semantic commit, no transparent retry
is allowed. If the client itself disconnects, the result is cancellation and
must not trigger provider failover or credential punishment.

## Compatibility and rollout

The first rollout adds the policy and adapts dispatch planning and survival
aggregation while preserving their existing public result types. Subsequent
changes should remove duplicated kind-to-action tables only after parity tests
cover all error kinds, route scopes, commit states, and client lifecycle paths.
