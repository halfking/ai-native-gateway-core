# ADR-0001: handoff_pending_confirmations.goal_state at-rest encryption / field narrowing

- **Status**: Accepted (deferred implementation)
- **Date**: 2026-08-17
- **Authors**: GoalHandoff hardening round (`d032f5bb8..c2027514e`)
- **Supersedes**: —
- **Related**:
  - Implementation commits: `75c714b67 feat(handoff): durable GoalState restore`, `78a0cade5 fix(handoff): close durable restore audit gaps`
  - This round: `d032f5bb8 fix(goal/handoff): per-request completion threshold + terminal-state CAS`, `c2027514e docs(adr): defer at-rest encryption of handoff_pending_confirmations.goal_state`
  - Schema: `db/migrations/362_handoff_durable_goal_state.sql`, `sql/migrations/startup/527_handoff_durable_goal_state.sql`
  - Module contract: `docs/modules/handoff.md`, `docs/会话优化v4/12-GoalHandoff契约.md`

## Context

The durable handoff flow persists a per-proposal `goal_state JSONB` snapshot in
`handoff_pending_confirmations` so that a follow-up session can resume the
task description, remaining work, completed steps, and current model after
the user confirms a handoff. The snapshot is currently protected by:

1. **Versioned envelope**: `marshalPersistedGoalState` clamps the payload to
   `GoalStateVersion = 1`, trims fields to fixed upper bounds
   (`task_description` ≤ 4096 runes, `remaining_work` ≤ 4096 runes,
   `current_model` ≤ 256 runes, `completed_steps` ≤ 64 entries × 512 runes
   each), and rejects payloads larger than 16 KiB.
2. **Heuristic redaction**: `redactResumeSensitive` runs regex-based
   replacements (`Bearer `, `sk-…`, etc.) over string fields before
   serialisation.
3. **Short TTL**: confirmations expire within minutes; the forensic retention
   floor is 1 day (default 30) via `bg/HandoffPendingTrimmer`.

This is **not encryption**. A backup, a replica, an operator with read-only
SQL access, or anyone who later exfiltrates the table can read
`task_description` and `remaining_work` in clear. The redaction layer is
best-effort: new credential prefixes, base64 tokens, JWT secrets, and future
LLM internal fields may not be matched. The schema does not currently use
`pgcrypto`, no KMS is integrated into the gateway, and PG TDE is not
configured on the deployment.

## Decision

**Do not implement encryption or field narrowing in this iteration.**
Document the residual risk explicitly and require KMS integration as a
prerequisite for any future encryption work.

## Options considered

| Option | Effort (engineering-days) | Query impact | Rollback path | Notes |
|---|---|---|---|---|
| **A. pgcrypto column encryption** (`pgp_sym_encrypt(goal_state, $kms_key)`) | 1 d schema + 1 d code change (marshal/unmarshal) + 1 d key rotation script | JSONB indexes on `goal_state` are invalidated. All queries that filter or join on snapshot fields must move to a deterministic-encryption companion column or be dropped. | Transparent: keep the plaintext column for one release so the migration is reversible. | Requires KMS/secret manager to hold the symmetric key. No KMS is integrated today. |
| **B. Envelope encryption (AES-GCM with KMS-wrapped DEK)** | 3 d KMS integration + 2 d application change + 1 d test | Same JSONB-index invalidation as A. | Same as A — keep plaintext column as fallback. | Best long-term option; needs KMS first. |
| **C. Field narrowing** (drop `task_description`/`remaining_work` plain text; persist only hashed task + step counts + `current_model`) | 1 d change to `marshalPersistedGoalState` + 1 d change to `unmarshalPersistedGoalState` + 1 d validation that the restore path no longer needs the plain text | Restore path becomes lossy: the new session must re-derive the task description from the conversation history. Already supported via `HistoryStore`, but quality regressions are possible for handoffs that happen early in a session. | Breaks `goal_state_version = 1` deserialisation — requires a version bump and a data migration step (drop or rewrite) for any existing proposals. | Lowest blast radius for the schema, but breaks the contract documented in `docs/会话优化v4/12-GoalHandoff契约.md`. |
| **D. Status quo (defer)** | 0 d | 0 | 0 | Current acceptance: heuristic redaction + size cap + short TTL. |

## Rationale for deferring (D)

1. **No KMS in scope.** Both A and B require a key management surface that
   this gateway does not yet integrate. Building encryption on top of an
   application-held symmetric key would be strictly worse than the current
   heuristic redaction — the application key would be present in every
   deploy artifact and every memory dump.
2. **Restore-quality contract.** Option C breaks the documented resume
   experience: the user has just confirmed a handoff, and the new session is
   expected to continue from the same `task_description` /
   `remaining_work`. Forcing a hash-only resume is a user-visible regression
   that requires product sign-off before engineering work.
3. **Encryption is not the highest-value hardening left.** The remaining
   goals in the goal-handoff audit cycle are concurrency safety (terminal
   state CAS — implemented in this round) and integration coverage (sqlmock
   PG tests — implemented in this round). Both reduce operator-visible
   failures and audit-context loss more directly than encryption would.
4. **Risk is contained.** The snapshot table is small, the TTL is short
   (minutes to a day), the data is recoverable from `handoff_logs` and the
   original session's `HistoryStore` once the handoff is acknowledged. A
   backup exfiltration is a PG-backup-rotation problem, not a gateway
   feature.

## Consequences

- **Known vulnerability (residual risk):** A holder of a PG backup, replica,
  or read-replica can read the most recent (≤ forensic retention) goal
  descriptions and remaining-work strings for every handoff that has not
  been physically trimmed yet. Mitigations in place: heuristic redaction,
  16 KiB payload cap, short TTL, per-tenant logical isolation. No
  application-layer encryption.
- **Future prerequisite:** Any move toward A or B requires first integrating
  a KMS or secret manager (Vault, AWS KMS, Aliyun KMS) and adding a key
  rotation runbook. Until then, the heuristic redaction remains the
  strongest protection this gateway can offer without external dependencies.
- **Future prerequisite (option C):** Validating that the restore path can
  re-derive `task_description` from `HistoryStore` for handoffs that occur
  early in a session, and getting product sign-off on the resume-quality
  trade-off.
- **Monitoring:** Operators should treat the PG backup of this gateway as
  containing user task descriptions and treat its rotation, off-host
  transfer, and access control with that in mind — the gateway itself does
  not enforce backup confidentiality.

## Follow-up actions

1. Track KMS integration as a separate initiative. Until KMS lands, this
   ADR stays in force and encryption is not implemented.
2. Re-evaluate option C only after the HistoryStore-backed resume path has
   been A/B-tested against the current plain-text path with a representative
   sample of real handoffs.
3. If a concrete backup-exfiltration incident occurs, revisit this ADR and
   accelerate KMS + envelope encryption (option B) as the response.
