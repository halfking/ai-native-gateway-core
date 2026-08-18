# Stream Request Lifecycle State Machine (plan C)

> Date: 2026-08-19 · Plan C, post-SP-01..04 worktree.
> Source of truth: `domains/streaming/state/{request_state,request_context,runtime}.go`
> (HEAD `7bd6783b0` on `stream-state-machine-sp05`).
> Consumers: SP-02 handler wiring (`domains/streaming/handler_state_init.go`,
> `messages.go`, `responses.go`), SP-03 hooks
> (`domains/streaming/executors/executor_chat.go`,
> `domains/hooks/compression/session_compressor.go`,
> `internal/streamretry/{retry,wrapper}.go`).

## 1. State diagrams

The state machine is split into 4 self-contained views. The terminal
states (`Completed`, `Failed`, `Cancelled`) admit no further transitions.

### 1.1 Initial state

```
                +---------------+
                |  StateReceived|
                |  (entry)       |
                +-------+-------+
                        |
              EventAuthed
                        |
                        v
                +---------------+
                |  StateAuthed   |
                +-------+-------+
                        |
        +---------------+--------------------+
        |               |                    |
 EventRouted     EventCompressing     EventCompressingSkipped
        |               |                    |
        v               v                    v
+----------+   +-----------------+   +-------------------+
| StateRouted| | StateCompressing|   | StateDispatching  |
+----------+   +-----------------+   +-------------------+
```

`EventDispatching` from `StateAuthed` is also legal (the executor's
"I am now live" signal is interchangeable with `EventCompressingSkipped`
from the matrix point of view, see `request_state.go:106-108`).

### 1.2 Normal flow: Dispatching → Streaming → Completed

```
+-------------------+   EventFirstByte   +-----------------+
|  StateDispatching | -----------------> |  StateStreaming |
|  (no first byte)  |                    |  (bytes flowing) |
+---------+---------+                    +--------+--------+
          |                                       |
          | EventFailed (any non-terminal state) | EventStreamEnded
          v                                       v
  +-------------+                          +---------------+
  | StateFailed |                          | StateCompleted |
  +-------------+                          +---------------+
```

The compression branch (StateCompressing) is optional and converges
into the same Dispatching node:

```
+------------------+  EventCompressingDone  +-------------------+
| StateCompressing | ---------------------> |  StateDispatching  |
| (body working)   |                        |  (executor live)   |
+------------------+                        +-------------------+
```

### 1.3 Cancelled state

Cancellation is **universal** — `EventCancelled` is accepted at any
non-terminal state, including the initial `StateReceived`. The runtime
serialises the cancel signal via a dedicated channel and always
preempts `EventFailed` when both arrive concurrently (see
`request_state.go:114-120`).

```
    +--------------+   +-----------+   +-----------+   +----------------+
    | StateReceived|   |StateAuthed|   |StateRouted|   | StateCompressing|
    +------+-------+   +-----+-----+   +-----+-----+   +-------+--------+
           |                |               |                  |
           |                |               |                  |
           +----------------+---------------+------------------+
                            |
                            |  EventCancelled (any non-terminal)
                            |  (client close, r.Context() cancel,
                            |   upstream timeout, watchdog)
                            v
                    +-----------------+
                    | StateCancelled  |  (terminal)
                    +-----------------+
```

### 1.4 Failed state

```
    +--------------+   +-----------+   +-----------+   +----------------+
    | StateReceived|   |StateAuthed|   |StateRouted|   | StateCompressing|
    +------+-------+   +-----+-----+   +-----+-----+   +-------+--------+
           |                |               |                  |
           |                |               |                  |
           +----------------+---------------+------------------+
                            |
                            |  EventFailed
                            |  (illegal transition → falls back to
                            |   StateFailed; terminal err populated
                            |   via apply(), request_state.go:177-184)
                            v
                    +-----------------+
                    |  StateFailed    |  (terminal)
                    +-----------------+
```

`EventCancelled` always preempts `EventFailed` when both arrive
concurrently — see `runtime.go:144-161` for the priority order in
`eventLoop` and `request_state.go:114-120` for the universal
transition.

## 2. Event table

`Source states` lists every non-terminal position from which the event
is accepted. `Target state` is the state the runtime drives into; `ok`
in the test file (`request_state_test.go:18-75`) matches this column
exactly.

| Event | Source states | Target state | Notes |
|-------|---------------|--------------|-------|
| `EventAuthed` | `Received` | `Authed` | Only legal out of `Received`. |
| `EventRouted` | `Authed` | `Routed` | Candidate resolution succeeded. |
| `EventCompressing` | `Authed`, `Routed` | `Compressing` | Body compression started. |
| `EventCompressingSkipped` | `Authed`, `Routed` | `Dispatching` | Below-threshold body, skip compression. |
| `EventCompressingDone` | `Compressing` | `Dispatching` | Only legal out of `Compressing`. |
| `EventDispatching` | `Authed`, `Routed` | `Dispatching` | Executor's own "I am live" signal. |
| `EventFirstByte` | `Dispatching` | `Streaming` | Upstream returned its first byte. |
| `EventStreamEnded` | `Streaming` | `Completed` | Upstream EOF, success. |
| `EventFailed` | **all non-terminal** | `Failed` | Universal; per spec §2, illegal transitions also fall back here (`request_state.go:177-184`). |
| `EventCancelled` | **all non-terminal** | `Cancelled` | Universal; preempts `EventFailed`. |

**Rejections / no-ops** (illegal transitions drop to `StateFailed` per
`request_state.go:177-184`):

| Source state | Event | Behaviour |
|--------------|-------|-----------|
| `Received` | anything except `Authed` | illegal → `StateFailed` |
| `Compressing` | `FirstByte` | illegal → `StateFailed` |
| `Dispatching` | `CompressingDone` | illegal → `StateFailed` |
| `Streaming` | `FirstByte` | illegal → `StateFailed` |
| any terminal | any event | rejected (no-op), `runtime.go:172-174` |

The transition table is the single source of truth — see
`request_state_test.go:18-75` for the exhaustive 30-case matrix.

## 3. RequestContext field table

All fields are private; access is via the `Set*/` setters (write lock)
and the `*()` accessors (read lock). The `mu` field itself is the lock
that guards every access — see `request_context.go:88-117`.

| Field | Type | Set by (owner) | Read by (consumer) | Purpose |
|-------|------|----------------|--------------------|---------|
| `requestID` | `string` | `NewRequestContext` (constructor) | every accessor; imm. | Stable correlation id; returned by `RequestID()`. |
| `tenantID` | `string` | `NewRequestContext` | every accessor; imm. | Tenant partition key for the request. |
| `keyInfo` | `*KeyInfo` | handler auth layer (`SetKeyInfo`) | runtime event-log accessors | Resolved API-key identity (id / tenant / scopes). |
| `bodyBytes` | `[]byte` | handler body capture (`SetBody`) | runtime / hooks; snapshot copy | Captured request body; `Body()` returns a defensive copy. |
| `model` | `string` | handler (`SetModel`) | runtime / observers | Canonicalised client model. |
| `candidates` | `[]Candidate` | routing layer (`SetCandidates`) | runtime | Final candidate set selected by routing. |
| `policy` | `*Policy` | routing layer (`SetPolicy`) | runtime | Routing policy override for this request. |
| `retryCtx` | `*RetryContext` | streamretry wrapper (`SetRetryCtx`) | runtime | Retry chain summary (attempt / max / last err). |
| `preStream` | `PreStream` | handler (`SetPreStream`) | runtime (terminal hook → `Stop()`) | Keep-alive ticker interface. |
| `compressor` | `Compressor` | handler (`SetCompressor`) | runtime (`Done()` channel) | Body compression worker. |
| `executor` | `Executor` | handler (`SetExecutor`) | runtime | Upstream dispatcher (marker interface; concrete impls in executor domain). |
| `armor` | `Armor` | handler (`SetArmor`) | runtime | Response wrapping stage (tool-call reconstruction). |
| `streamWriter` | `StreamWriter` | handler (`SetStreamWriter`) | runtime / terminal hook | SSE writer boundary; `Closed()` guards against post-cancel writes. |
| `cancelOnce` | `sync.Once` | n/a (embedded in `cancelled`) | `performCancel` | Idempotency guard for cancellation. |
| `cancelled` | `chan struct{}` | `NewRequestContext`; closed by `performCancel` | every observer via `Cancelled()` | Cancellation broadcast. |
| `cancelErr` | `error` | `performCancel` (under lock) | `CancelErr()` | Reason of first cancellation. |
| `eventLog` | `[]EventLogEntry` | runtime `appendEventLog` | `EventLog()` (snapshot) | Append-only transition audit trail. |

`performCancel` is the only writer that mutates the cancellation
fields; it is invoked from `Runtime.Cancel` under the runtime's own
write lock — see `request_context.go:307-313`.

## 4. Cancellation propagation timeline

The pre-stream keepalive is the first observer; the HTTP request
goroutine is the second. Order matters because the keepalive ticker
must stop **before** the SSE writer is closed (otherwise a keepalive
frame leaks past the cancel).

```
   1. Client closes / upstream returns 5xx / watchdog fires
                   |
                   v
   2. http.Server  ── ctx cancel ──>  r.Context().Done()
                   |
                   +-- goroutine A: HTTP request handler
                   |   (running through ChatHandler.ServeHTTP)
                   |
                   +-- goroutine B: pre-stream keepalive ticker
                       (started by startPreStreamKeepalive in handler.go)
                   |
                   +-- goroutine C: upstream reader
                       (executor_chat.go Execute → executeOpenAI)
                   |
                   +-- goroutine D: state-machine event loop
                       (initRequestStateMachine in handler_state_init.go)
                   |
                   v
   3. eventLoop sees <-parent.Done() BEFORE <-r.cancel
      (parent = r.Context(); both ultimately close via the same
       http.Server trigger, but parent.Done() wins the select race
       because it has no buffer delay)
                   |
                   v
   4. eventLoop calls apply(EventCancelled, parent.Err())
      runtime.apply():
        - exit hook (StateCompressing / Dispatching) — no-op
        - r.current = StateCancelled
        - appendEventLog
        - terminal hook: preStream.Stop()    ← goroutine B exits
        - return from eventLoop
                   |
                   v
   5. eventLoop's deferred shutdown closes r.done
      goroutine D (event loop) returns
                   |
                   v
   6. RequestContext.Cancelled() also closes here (via performCancel
      from r.cancel branch — only if Cancel was called explicitly;
      in the parent.Done() path performCancel runs from the rt.Cancel
      deferred at handler return).
                   |
                   v
   7. streamretry wrapper sees both:
      - bindStateCancel() (SP-03) created a derived ctx that is
        canceled when the state cancel channel closes
      - RetryContext.StateCancelCh is wired
      → Sleep() returns context.Canceled in microseconds, not after
        the full backoff window (internal/streamretry/retry.go:415-428)
                   |
                   v
   8. session_compressor tryLLMSummaryWithFallback (SP-03) bails out
      on the next ctx.Err() check, before issuing a network request
      to the LLM summarizer (session_compressor.go:709-738)
                   |
                   v
   9. executor_chat executeOpenAI: if upstream read is in flight,
      r.Context() cancel propagates through the executor's own ctx
      (params.R.Context()), the io.ReadAll returns ctx.Err, and
      Execute returns the same error.
                   |
                   v
  10. handler's defer cancelRequestStateMachine is a no-op because
      state is already terminal.
                   |
                   v
  11. Response is finalised — handler writes any pre-cancel frames
      buffered in StreamSession, then the preStream ticker is gone
      so no further frames can be written. Runtime's terminal hook
      closes StreamWriter so post-cancel writes from any other
      goroutine (e.g. responseInterceptor) return an error rather
      than corrupting the SSE stream.
```

Net effect: every goroutine observes the cancel within microseconds
of the trigger, the SSE writer cannot leak post-cancel, and
retry-backoff windows collapse immediately instead of waiting for
the full timer to fire.

## 5. Old slog.Info → new state-transition hook mapping

The state machine does **not** delete existing slog calls — they
remain for backwards-compatible operational logging. The new
transitions are added alongside them and become the **authoritative
lifecycle signal** that the rest of the request pipeline observes.
Where a slog call is the only thing that used to mark a stage, the
table below marks it as "retained" so the audit trail is preserved.

| Old call site (file:line) | slog message | New hook / event | Status |
|---------------------------|--------------|------------------|--------|
| `domains/streaming/handler.go:2877` (and `:2889`) | `slog.Info("candidates_resolved", ...)` | `rt.Emit(state.EventRouted)` in `messages.go:515` / `responses.go:475` | retained for elapsed_ms; state machine is the authoritative lifecycle signal |
| `domains/streaming/messages.go:202` (and `responses.go:230`) | (no slog at auth-success path) | `rt.Emit(state.EventAuthed)` | **new**; no pre-existing slog at this code point — state machine introduces explicit Authed event |
| `domains/streaming/messages.go:708` (and `responses.go:686`) | (no slog at dispatch entry) | `rt.Emit(state.EventDispatching)` | **new**; pre-stream keepalive logs (see below) remain operational |
| `domains/streaming/messages.go:518` (and `responses.go:478`) | `slog.Error("failed to get candidates from provider", ...)` on candErr | `rt.Emit(state.EventFailed)` followed by `slog.Error(...)` | retained; state machine drives the terminal transition, slog.Error preserved for log search |
| `domains/streaming/messages.go:717` (and `responses.go:694`) | (no slog at executor failure) | `rt.Emit(state.EventFailed)` guarded by `!errors.Is(r.Context().Err(), context.Canceled)` | **new**; explicit dedup so client-cancel doesn't double-fire Failed + Cancelled |
| `domains/streaming/handler.go:3171` | `slog.Info("pre_stream_keepalive_started_early", ...)` | none (operational log) | retained — pre-stream keepalive is orthogonal to the lifecycle state machine |
| `domains/streaming/handler.go:3218` | `slog.Info("session_compressor_prepare_done", ...)` | none | retained — captures elapsed_ms; the state machine emits `EventCompressing` separately at the entry to Prepare |
| `domains/streaming/executors/executor_chat.go:546` | `slog.Info("upstream_call_starting", ...)` | none (operational log) | retained — records upstream URL + rctx_err + timeout_remaining |
| `domains/streaming/executors/executor_chat.go:1040` | `slog.Info("executor: client disconnected during stream", ...)` | `runtime.eventLoop` `case <-parent.Done()` → `apply(EventCancelled, ...)` | retained as observability backup; state machine is now the authoritative source of cancellation reason |
| `domains/hooks/compression/session_compressor.go:507` | `slog.Info("session_compressor: LLM summary failed/no-op, falling back to mechanical trim", ...)` | none (compression-internal log) | retained — compression observability is independent of the request lifecycle |
| `domains/hooks/compression/session_compressor.go:709-738` (new SP-03) | (no slog at ctx bail) | `tryLLMSummaryWithFallback` checks `ctx.Err()` before/after `tryLLMSummary` | new SP-03 path; replaces what would otherwise have been a wasted network call after cancel |

## 6. Concurrency hazards + guards

### 6.1 Serialised writer / mutex strategy

The runtime serialises every state transition through a single
goroutine. `eventLoop` is the **only** goroutine that ever mutates
`r.current`; everything else (emitters, cancel signals, parent
context) goes through channels into the loop (`runtime.go:141-162`).

`apply()` itself takes the runtime mutex (line 169), so even though
the event loop is a single goroutine, the read-side helpers
(`State()`, `Err()`, `Done()`) are safe to call from any goroutine
because they also take the same mutex (`runtime.go:71-87`).

The SSE writer is **not** mutex-serialised. Instead, the state
machine's terminal hook (`apply()` line 218-222) calls
`preStream.Stop()` and the production handler's
`OnEnter(StateCancelled)` / `OnEnter(StateFailed)` hooks
(`request_state_test.go:304` mirrors this contract) call
`StreamWriter.Close()`. The writer's own `Closed()` flag then
rejects any post-cancel `WriteFrame` / `WriteDone` call with an
error — see `request_state_test.go:521-528`. The `onWriteAfterClosed`
counter is how the race test asserts that no frames leak past
cancellation.

The `RequestContext` setters and accessors use a separate `sync.RWMutex`
(`request_context.go:90`) so collaborators (handler, executor,
compressor) can populate identity / body / candidates concurrently
without taking the runtime's write lock. The runtime reads
RequestContext fields **under its own write lock** during `apply()`
(line 169), so setters and `apply` cannot interleave.

The terminal-error field (`finalErr`) is also a single-writer
field: only `apply()` writes it, and only when `r.finalErr == nil`
(runtime.go:196-198), so a second error after a terminal error is
silently dropped instead of overwriting the first reason.

### 6.2 Context propagation correctness

`parent context.Context` flows as follows:

- The HTTP server's request context (`r.Context()`) is passed by
  the handler into `initRequestStateMachine(r.Context(), ...)` which
  starts the event loop on a child goroutine — see
  `handler_state_init.go:30-39`. The runtime's
  `eventLoop` selects on `<-parent.Done()` directly
  (`runtime.go:146`), so HTTP request cancellation is observed by
  the state machine **at the same moment** any other consumer of
  `r.Context()` (executor, compressor, streamretry) sees it.
- The handler derives a child context for the executor by
  `WithExecuteHooks(params.R.Context(), &ExecuteHooks{...})`
  (`executor_chat.go:SP-03 context plumbing`). Hooks fire inside
  `executeOpenAI`'s `defer` so they observe the executor's terminal
  error regardless of return path.
- `streamretry.Wrapper.WithStateCancelCh(ch)` (SP-03) wires
  `RequestContext.Cancelled()` into the wrapper. `bindStateCancel`
  returns a derived `context.Context` that fires when EITHER the
  parent ctx is canceled OR the state channel closes
  (`wrapper.go:530-540`). This means retry-backoff `Sleep()` can
  return in microseconds on cancel instead of waiting for the
  timer — see `retry.go:415-428`.
- `session_compressor.tryLLMSummaryWithFallback` (SP-03) checks
  `ctx.Err()` both **before** and **after** the LLM call, so a
  cancel that arrives mid-summary causes the (potentially
  successful) result to be discarded instead of being applied to
  a body the client is no longer waiting for — see
  `session_compressor.go:709-738`.
- The state machine does not OWN the parent context. It only
  selects on `<-parent.Done()`. The HTTP server retains
  authority over the parent context's lifetime; the runtime
  cannot accidentally extend a cancelled parent.

### 6.3 Error attribution

Error attribution is split across three layers; the state machine
sits in the middle as the **terminal-error keeper** while the
envelope errors live at the handler / executor boundary.

- **Terminal error (state machine)**: `runtime.Err()` returns
  `finalErr`. This is set by `apply()` when the transition
  produces a non-nil error (either an explicit
  `EventFailed`-with-reason or an illegal transition that falls
  back to `StateFailed`). It is set **exactly once** (the
  `r.finalErr == nil` guard on line 196), so concurrent failure
  events from upstream reader + cancel signal cannot overwrite the
  first reason.
- **Retry-classification error (executor)**: `executor_chat.go`
  classifies upstream failures via `isRetriableError` (transient
  vs permanent) and emits a `*executors.ExecuteError` whose
  `LastKind` carries the errorsx.Kind enum. This decision is made
  **before** the state machine's `EventFailed` is emitted, so the
  state machine only records "the request failed" — it does not
  re-classify. The classification is preserved through the
  ExecuteError's `LastKind` field.
- **Envelope error (handler)**: the handler still owns the
  user-facing error envelope (status code, body). It reads
  `execErr` to decide between 400/502/503/overloaded_error, and it
  uses the state machine only for the "is this a client cancel?"
  dedup (`!errors.Is(r.Context().Err(), context.Canceled)` guard
  in `messages.go:716-718` / `responses.go:693-695`). The handler
  never reads `runtime.Err()`; the runtime's terminal error is
  for observability, not for envelope shaping.

The state machine's `finalErr` and the handler's envelope error can
disagree (e.g. `finalErr = "illegal transition"` while the handler
returns 400 with a different message). This is intentional: the
runtime is the audit log of what the lifecycle observed, while the
envelope is the user-facing contract. The two are correlated but
not synchronised — a deliberate separation that lets the envelope
be optimised for clients without losing the internal attribution
that operators need.
