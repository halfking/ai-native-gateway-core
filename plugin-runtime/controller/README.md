# Configuration-managed plugin runtime

`Start(ctx, Options)` connects the existing JSON-manifest supervisor to the
configuration reloader, reconciliation engine, registries, and cross-process
EventBus. Gateway wiring opts in using `LLM_GATEWAY_PLUGIN_RUNTIME_CONFIG`.
`Options.ConfigPath` is required. A zero interval defaults to 30 seconds.

```yaml
server:
  host: localhost
  port: 8080
plugins:
  directory: /opt/gateway/plugins
  enabled: [analytics, reports]
```

Only listed IDs are managed. Discovery prefers
`<directory>/<id>/current/plugin-manifest.json`, falling back to
`<directory>/<id>/plugin-manifest.json` only when `current` does not exist.
The controller loads the established JSON schema; it does not start the
experimental YAML process supervisor. Changes to server, security, or the plugin
directory require a gateway restart and are rejected during reload.

A configuration transaction changes desired state only after every operation
succeeds. Any failure restores the previous desired state and actual bundles.
Each running bundle is copied into a private runtime directory so rollback also
works when a legacy installation overwrites files in place. Superseded and failed
snapshots are pruned after the transaction. Manifest signatures are checked by
the root supervisor using `Options.SigningPubkey`.

Version upgrades stop the old process before starting and checking its
replacement. This can interrupt requests; it is **not a zero-downtime upgrade**.
A failed start or readiness check restores the prior bundle and performs
readiness again. `Manifest(id)` reports the actual started snapshot, while
`BaseURL(id)` returns a Unix route only for a ready process that is still alive.
Processes use the controller lifetime context; canceling a completed Reload or
Reconcile request does not kill its new processes. `Close` cancels ongoing work,
joins watchers and the controller loop, stops every child, and removes runtime
files. Canceling the lifetime context also closes the controller.

Each reconciliation probes the actual running health endpoint with a 500 ms
deadline. Two consecutive failures withdraw proxy and registry readiness and
trigger process recovery, even when the PID is alive. Recovery must pass the
handshake and health checks again. Health probes run in the controller loop and
never block a normal proxy request.

An invalid manifest found during a later scan is reported as an error. Health
checks and crash recovery continue against the last accepted pinned bundles, so
a rejected deployment cannot strand existing plugins. Invalid manifests during
initial startup still fail startup because no accepted state exists yet.

## Plugin event protocol

Each child receives its own credentials:

- `GATEWAY_PLUGIN_ID`
- `GATEWAY_PLUGIN_EVENT_SOCKET`
- `GATEWAY_PLUGIN_EVENT_TOKEN`

The event socket is separate from HTTP plugin sockets, in a directory with mode
0700; the socket has mode 0600. Use `ipc.NewClient(socket).Send(ctx, jsonRequest)`
for the bounded JSON transport. Each connection carries one complete JSON
request and one complete JSON response, without a length prefix. Every request
contains `type`, `plugin_id`, and `token`. `EventRequest` and `EventResponse`
define the wire contract.

```json
{"type":"event.subscribe","plugin_id":"reports","token":"<inherited token>","pattern":"inventory.*"}
{"type":"event.publish","plugin_id":"analytics","token":"<inherited token>","event_type":"inventory.changed","payload":{"item":"A"}}
{"type":"event.poll","plugin_id":"reports","token":"<inherited token>","subscription_id":"sub-1"}
{"type":"event.unsubscribe","plugin_id":"reports","token":"<inherited token>","subscription_id":"sub-1"}
```

Subscriptions support exact names, `prefix.*`, and `*`. Poll returns immediately
with up to 32 events. The server assigns event source from the authenticated
plugin ID; clients cannot set another plugin's source or access its subscription.
Supervisor lifecycle events use source `plugin-runtime`. Stop, disable, upgrade,
and crash recovery revoke the old token and its subscriptions; a restarted child
must subscribe again with its newly inherited token.

Delivery is best effort: each subscriber queue holds at most 32 events, each
plugin may own 16 subscriptions, and the transport permits 128 subscriptions in
total. JSON payloads are limited to 16 KiB. Overflow drops events; `dropped`
reports overflow at the transport poll queue since the last poll (the underlying
EventBus may also drop before dispatch). There is no durable replay guarantee.

## Verification

`controller_test.go` launches two real helper child processes, exercises their
HTTP handshake and health endpoints, and makes them send authenticated IPC
requests to one another. It also covers enable/disable, disk version upgrades,
failed transaction rollback, actual version restoration, crash recovery, atomic
config replacement, lifecycle cancellation, live processes returning 503 or
hanging health endpoints, credential isolation/revocation, queue/subscription
bounds, and process/file cleanup.

```sh
go test -race -count=1 ./plugin-runtime/controller
go vet ./plugin-runtime/controller
```
