// Package distlock provides a leader-follower distributed lock primitive
// backed by Redis (preferred) or an in-process sync.Map fallback.
//
// Why this exists (2026-08-19):
//
// The session title pipeline auto-fires on every first successful turn of a
// session. Two concurrent first-turn requests (client retry, racing sessions,
// cross-process triggers in a multi-replica deployment) used to both spawn a
// goroutine, both invoke the LLM, and both attempt the same INSERT — wasting
// N-1 upstream LLM calls per duplicate. The session_titles ON CONFLICT DO
// NOTHING guard kept the database consistent but did nothing to suppress the
// upstream chatter.
//
// This module is the one-stop helper for "I have a request-scoped idempotent
// operation, make sure only one of me actually executes it across the cluster".
//
// Semantics:
//
//   - Acquire returns either a leader handle or a follower handle.
//   - The leader MUST call Release once on completion (success or failure).
//     Release publishes a wake-up event on a Redis pub/sub channel, then
//     removes the lock entry via a Lua compare-and-delete so a TTL-expired
//     leader never deletes a follower's freshly-installed replacement.
//   - Followers call Wait to block until the leader releases (or until the
//     lock's TTL expires, whichever comes first). They MUST also call
//     Release, but for followers it is a no-op.
//   - If the Redis client is nil or any Redis call fails, Acquire returns
//     an error and the caller falls back to the DB-layer ON CONFLICT guard.
//     We deliberately do not block on Redis — locks are an optimization,
//     not a correctness primitive.
//
// Reuse:
//   - Auto title generation uses key llmgw:distlock:title:auto:<taskID>:<sid>
//   - Manual title regenerate/PUT/DELETE use
//     llmgw:distlock:title:manual:<taskID>:<sid>
//
// The two prefixes are independent so the user can hit "regenerate" while
// the auto pipeline is mid-run without blocking it (and vice-versa).
package distlock
