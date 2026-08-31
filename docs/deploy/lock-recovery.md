# Lock recovery for the 245 / 154 blue-green deploys

This document describes how to recover a stuck deploy lock on the
`245` or `154` gateways, and the design rationale for why
`deploy-seamless.sh --force` does NOT verify lock age before
recovering.

If you only need the recovery procedure, jump to
[**Recovery procedure**](#recovery-procedure). If you want the
rationale for the design, read
[**Why no age-based staleness check?**](#why-no-age-based-staleness-check).

## When locks get stuck

The deploy script (`scripts/deploy-seamless.sh`) takes two locks:

* a **local lock** at `$TMPDIR/kx-llm-gateway-deploy-${TARGET}.lock`
  on the orchestrator host;
* a **remote lock** at `/var/lib/llm-gateway-go/deploy.lock` on the
  target host.

Both are released by an `EXIT` trap when the deploy script exits
cleanly. A lock gets stuck only when the deploy process is killed
*before* the trap runs — for example:

* `SIGKILL` (`kill -9`) from a wrapper script that does not allow
  the trap handler to execute.
* OOM-killer on the orchestrator host when the deploy is mid-upload.
* Power loss of the orchestrator or target mid-deploy.
* A `set -e` failure inside the trap handler itself (very rare;
  tracked in `tests/deploy_lock_test.sh::AC-L5`).

When the next `deploy-seamless.sh deploy 245` runs, it sees the
lock held and exits `75` (EX_TEMPFAIL) without doing any work. This
is correct — the lock is doing its job.

## Recovery procedure

The recommended entry point is `scripts/deploy-lib/unlock-remote.sh`.
It is *interactive*: it prints the held lock's metadata and asks
for explicit confirmation before deleting.

```
$ bash scripts/deploy-lib/unlock-remote.sh 245 --force
```

The script will:

1. Read the held remote lock's metadata file
   (`/var/lib/llm-gateway-go/deploy.lock/metadata`).
2. Print each field (`target`, `source_user`, `source_host`, `pid`,
   `started_at`, `commit`, `version`) so you can verify the lock
   is genuinely stale.
3. Refuse to proceed if the recorded `target` does not match the
   requested target, or if the recorded `pid` is still alive and
   looks like an in-flight deploy.
4. Otherwise prompt for `yes / no` before deleting the lock.

If you already know the lock is stale (e.g. the deploy process is
gone from `ps`), answer `yes`. The script removes the lock and
prints the path it removed.

For the **local** lock on the orchestrator, the equivalent is:

```
$ bash scripts/deploy-lib/unlock-local.sh 245
```

The local script is non-interactive because the local lock is less
risky: it protects only the orchestrator's checkout, never the
target's release slots.

If `unlock-remote.sh --force` insists the lock is held by a live
deploy process *and* you have independently confirmed the process
is gone (e.g. you control the orchestrator's `kill -9` history),
the escape hatch is `deploy-seamless.sh --force`. It runs
`lock_recover_remote` which performs the same PID + target checks
plus the trap-cleanup of any in-flight deploy. **Do not bypass
without first running `ps -ef | grep deploy-seamless` to confirm
the process is really gone.**

## Why no age-based staleness check?

`lock_recover_remote` validates:

* `target` matches the requested target — refused otherwise.
* `pid` is dead (or not recorded) — refused if alive.

It does **not** validate that the lock is "old enough" to be
considered stale. This is intentional and was the decision
documented in the 2026-08 audit round. The reasons:

* **Fail-closed is already enforced where it matters.** The two
  checks above are exactly the failures that an age threshold
  would catch — a deploy whose recorded target is wrong, or a
  deploy whose source process is still running. Both are
  structurally impossible to bypass with `lock_recover_remote`.
* **Age alone is unreliable in production.** A lock 24 hours old
  might be either a hung deploy OR a multi-day canary soak that
  the operator is intentionally leaving in place while monitoring.
  An automatic age threshold would happily delete the latter,
  killing the blue-green promotion it was supposed to protect.
  The canary-active rollback path (`deploy-seamless.sh:1108-1137`)
  *requires* the canary slot to survive long after the canary
  has been active.
* **The operator is the right authority.** Deciding "this PID is
  dead, the lock is safe to remove" requires human judgment: the
  deploy process might be hung in a way that `kill -0` cannot
  detect (e.g. uninterruptible `D` state on a stuck I/O), or it
  might be a parallel deploy on a different machine. The
  operator has the contextual awareness; the deploy script
  doesn't.

The remaining failure mode — a deploy that crashed hard
(`SIGKILL`, OOM kill) without running its EXIT trap, leaving a
lock with a recorded PID that's gone — is exactly what
`lock_recover_remote` is designed to fix: the recorded PID is
verified dead (`kill -0` returns nonzero), so recovery proceeds.
Age staleness would NOT have helped here: PID liveness is what
detects this case.

## Audit trail

Every `deploy-seamless.sh --force` invocation, and every
`unlock-remote.sh --force` invocation, is logged via stderr to
the deploy log with the held lock's full metadata. Keep the
deploy logs (`deploy.log`) for at least 90 days so the audit
chain can be reconstructed if a force-recover turns out to have
been applied against a still-active lock.

The behavioral test `tests/deploy_lock_test.sh::AC-L12-14` enforces
that `lock_recover_local` and `lock_recover_remote` refuse live
PIDs, refuse cross-target locks, and refuse when the source
process is identifiable as a deploy. **Do not weaken any of
those assertions without an audit round.**

## See also

* `scripts/deploy-lib/lock.sh` — implementation and header comment.
* `scripts/deploy-lib/unlock-remote.sh` — interactive operator
  entry point.
* `scripts/deploy-lib/unlock-local.sh` — local lock recovery.
* `tests/deploy_lock_test.sh` — behavioral coverage of the
  recover / release / refusal invariants.