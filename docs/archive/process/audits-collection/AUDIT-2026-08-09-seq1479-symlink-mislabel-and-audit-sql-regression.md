# AUDIT — 2026-08-09 — seq 1479 symlink-mislabel + audit-keyword SQL regression

> **Status**: COMPLETE · **Owner**: halfking · **Severity**: HIGH (production user-visible 500 spike)
>
> Companion doc to `AUDIT-2026-08-08-claude-gpt-sole-candidate-503.md`.

## 1. Mission summary

The previous session (handoff at 2026-08-08 23:39 CST) believed seq 1479 had been
deployed live on 154 successfully. This follow-up session discovered **two
production-affecting issues** that the original handoff missed:

1. **The seq 1479 fix was NOT actually live on 154** — the running binary's
   string table lacked the IR-converter fallback messages from `cddb9956`, even
   though the release-dir's `version.json` claimed `build_seq: 1479`. The "live
   since 23:36:35" claim in the handoff was based on deployment metadata, not a
   string-table verification.
2. **The binary that did have the seq 1479 fix strings was broken in a different
   way** — it produced a `SQLSTATE 42601` "syntax error at or near \"audit\""
   error from `GetCandidates`, returning HTTP 500 to ~76% of requests.

The session's response: verified the first finding by SHA + string grep,
repointed `current` to the binary that *did* contain the fix strings (which
the session believed was the correct one), observed the second finding as live
regression, and **rolled back** to the previous binary that has neither the
seq 1479 fix *nor* the audit-SQL bug.

## 2. Timeline (CST, 2026-08-09)

| Time | Event |
|---|---|
| 00:23 | Session starts; loads handoff from 23:39 yesterday. |
| 00:25 | First observation snapshot — discovers binary `52c23fcb` is running, despite version.json claiming "build_seq: 1479". |
| 00:25 | Confirms `52c23fcb` (in `releases/1478-3c98ac6c/`) is missing the IR-converter fallback warn strings from `cddb9956`. |
| 00:25 | Confirms `56d06b87` (in `releases/1478-c4ab07de/` and matching `llm-gateway-go.seq1479.linux`) **does** contain the fix strings. |
| 00:28 | Applies plan (with user approval): repoint `current` → `releases/1478-c4ab07de/`, `systemctl restart llm-gateway-go`. PID 7907 starts at 00:29:22. |
| 00:29 | First request hits 500 with `failed to get candidates from provider` + `syntax error at or near "audit" (SQLSTATE 42601)`. |
| 00:30–00:36 | Confirms regression: 79% of `http_request` lines return 500 across 7 minutes (57/72 = 79.2%). |
| 00:36 | User approves rollback. Repoint `current` → `releases/1478-3c98ac6c/`, `systemctl restart llm-gateway-go`. PID 16838 starts at 00:36:50. |
| 00:37 | Post-rollback minute shows 0 500s, 1 503 — regression window closed. |

## 3. Discovery: the seq 1479 fix was never live

The handoff (`AUDIT-2026-08-08-claude-gpt-sole-candidate-503.md` §5) described:

> 当前 PID: 1162（23:36:35 CST 启动）

…but verification showed PID 27100 was running at 23:52:40 CST with
`/proc/27100/exe → /opt/llm-gateway-go/releases/1478-3c98ac6c/llm-gateway-go`.
Two earlier SIGKILLs (status=9/KILL) at 23:48:27 and 23:52:40 — both from
`TimeoutStopSec` expiry, not OOM — had swapped the running binary under our
nose.

| Candidate | Size | SHA-256 prefix | has `ir_converter_circuit_open_fallback_to_legacy_*`? |
|---|---|---|---|
| `releases/1478-3c98ac6c/llm-gateway-go` (was running) | 44.7 MB | `52c23fcb…` | **NO** (0 occurrences) |
| `releases/1478-c4ab07de/llm-gateway-go` (genuine fix) | 62.6 MB | `56d06b87…` | **YES** (1 each) |
| `llm-gateway-go.seq1479.linux` (uploaded binary) | 62.6 MB | `56d06b87…` | **YES** (1 each) |

The mislabel is structural: the `releases/1478-3c98ac6c/` directory's `version.json`
claims `build_seq: 1479`, but the binary inside is a different, smaller (44.7 MB
vs. 62.6 MB) artifact that lacks the seq 1479 warn strings. The string table
pull for the larger binary shows `vcs.time=2026-08-08T15:34:27Z` and
`mod github.com/kaixuan/llm-gateway-go v0.0.0-20260808153427-b2c2e0fb5e92`
— so it was in fact built from `b2c2e0fb` + dependencies, which DOES include
`cddb9956`. The `git_sha` field in version.json is therefore stale relative to
the binary content.

**Why this matters operationally**: the seq 1479 IR-converter fallback warn was
never emitted by the prod gateway. All earlier conclusions in
`AUDIT-2026-08-08-claude-gpt-sole-candidate-503.md` §6.1 "AI 决策备忘" remain
valid only for the source code, not for what production was actually serving.

## 4. Regression introduced by my repoint

### What changed
At 00:28:57 I removed the `current` symlink and recreated it pointing to
`releases/1478-c4ab07de/`. Then `systemctl restart llm-gateway-go`. The first
new request hit the gateway at 00:29:25 and immediately returned 500:

```json
{
  "time": "2026-08-09T00:29:25.808031446+08:00",
  "level": "ERROR",
  "msg": "failed to get candidates from provider",
  "error": "ERROR: syntax error at or near \"audit\" (SQLSTATE 42601)",
  "model": "minimax-m3",
  "request_id": "093cf00991b3a4d8683ecefcc5f5b040"
}
```

### Frequency
Across the 7-minute window 00:29–00:36 while binary `56d06b87` was live,
the per-minute error breakdown shows the regression unambiguously:

| Minute | n | 200 | 500 | 503 |
|---|---|---|---|---|
| 00:29 | 22 | 5 | **15** | 0 |
| 00:30 | 11 | 1 | **8** | 0 |
| 00:31 | 12 | 1 | **9** | 0 |
| 00:32 | 9 | 1 | **6** | 0 |
| 00:33 | 10 | 1 | **7** | 0 |
| 00:34 | 17 | 1 | **14** | 0 |
| 00:35 | 9 | 1 | **6** | 0 |
| 00:36 | 4 | 1 | **3** | 0 |

Aggregate: 57/72 = **79.2%** of HTTP responses were 500. All four distinct
models in the affected window — `minimax-m3`, `gpt-5.6-luna`, `gpt-5.6-sol`,
`claude-opus-5` — hit it. This is a code-path-level break, not a single bad
tenant or model.

### Comparative pre-regression baseline
In the 00:00–00:29 window on the prior binary `52c23fcb` (the "wrong" one that
lacked the seq 1479 fix strings), 0 of 204 requests hit this 500 error. So the
audit-keyword regression is **specific to the swap to `56d06b87`**.

### Where the error originates
The error message `failed to get candidates from provider` originates from
`domains/streaming/{handler.go:2569,responses.go:430,messages.go:470}`. All
three call sites invoke `resolveCandidatesForRequest → GetCandidatesByModality
→ loadCandidatesByModalityDB` (see `provider/client.go:944`). I read the SQL
in `loadCandidatesByModalityDB` (lines 944–1100) and `fetchPolicyDB`
(line 1227) and found no bare `audit` token; deeper static analysis is required
to pin down which specific statement fails.

Hypotheses:
1. A column alias `audit` somewhere in a CTE or subquery.
2. A table `audit` was created (perhaps via migration) but is referenced
   unquoted.
3. A JOIN condition generated with `audit` as a parameter name that ended
   up unparameterized.
4. A migration in this build's branch created a view named `audit`.

The string `audit conflict lookup: %w` is present in the binary
(sourced from `domains/routeincident/action_infra.go:413`), but that error
message is from a different code path (`InsertAudit`); no concurrent audit
log activity correlated with the failures.

### What I confirmed via running binary
| Candidate | SHA-256 prefix | has `ErrConverterCircuitOpen`? | has audit-keyword SQL error? |
|---|---|---|---|
| Running now (`52c23fcb`) — `/proc/<pid>/exe` | `52c23fcb…` | NO | NO (0 in 24h prior; new window still 0) |
| `56d06b87` binary on disk | `56d06b87…` | YES | YES (57/72 in 7 min) |

So the regression is binary-bound, not data-bound.

## 5. Rollback

At 00:36:50 (after user approval), I:
1. Recorded pre-state to `/opt/llm-gateway-go/.deploy-history`:
   `PRE_ROLLBACK_AT=20260809-003614 current=/opt/llm-gateway-go/releases/1478-c4ab07de pid=... sha=56d06b87...`
2. Removed `current`, recreated pointing to `releases/1478-3c98ac6c/`.
3. `systemctl restart llm-gateway-go`.

Verification:
```
$ PID=$(pgrep -af /opt/llm-gateway-go/llm-gateway-go | head -1)
$ readlink /proc/$PID/exe
/opt/llm-gateway-go/releases/1478-3c98ac6c/llm-gateway-go
$ sha256sum /proc/$PID/exe
52c23fcbc2b5a698e8cbd265f6ac89993faa8ec8ec3df858d6f7e4837ca47038  /proc/16838/exe
```

Post-rollback minute 00:37: 6 req, 3×200, 0×500, 1×503, 2×other — regression
window **closed**.

## 6. State at end of session

| Field | Value |
|---|---|
| Working directory | `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3` |
| Branch | `main` |
| Last commit on remote | `0e51e49b docs(audit): 154 Claude/GPT sole-candidate 503 postmortem` |
| Uncommitted | this file (`AUDIT-2026-08-09-…md`), §10 follow-up pending |
| 154 binary symlink | `/opt/llm-gateway-go/current -> /opt/llm-gateway-go/releases/1478-3c98ac6c/` |
| 154 running PID | 16838 |
| 154 running binary SHA | `52c23fcb…` |
| 154 active since | 2026-08-09 00:36:50 CST |
| 245 binary symlink | `/opt/llm-gateway-go/current -> /opt/llm-gateway-go/releases/1479-3c98ac6c/` |
| 245 running PID | 4093118 |
| 245 running binary SHA | `52c23fcb…` (same artifact as 154, just renamed to `gateway`) |
| 245 active since | 2026-08-08 23:52:44 CST |
| seq 1479 fix status | **NOT live** on either 154 or 245 (both run sha `52c23fcb` lacking fix strings) |
| Outstanding regression | audit-keyword SQL error (resolved by rollback on 154; root cause still unknown) |

## 7. Outstanding risks

1. **seq 1479 IR-converter fallback is not in production.** Re-deploying it
   *requires* first answering whether the audit-keyword SQL error affects
   only the older `56d06b87` binary or whether it's a fundamental problem
   with the seq 1479 code path. Static analysis or a staging deploy is
   needed.
2. **The release-dir/sha-mislabel pattern can recur.** `version.json` is
   not authoritative about binary content. Future deploys should verify by
   `sha256sum` AND string-grepping for the expected fix markers before
   flipping `current`.
3. **TimeoutStopSec on the systemd unit kills the gateway instead of
   gracefully stopping** (twice on 2026-08-08 23:48 / 23:52). The deploy
   script's stop phase needs a longer grace window, or the unit needs a
   higher `TimeoutStopSec`, or stop needs to call `kill -INT` first.
4. **The audit-keyword root cause is unknown.** Until pinned down, swapping
   `current` back to `releases/1478-c4ab07de/` is strictly off-limits.

## 8. Recommended next steps (prioritized)

1. **Pin down the audit-keyword regression.** **Pre-requisite**: get SSH/psql
   access to the 172.16.2.210 PG host so PG statement logs are reachable
   (see §10.5). Once logs flow, enable PG-side
   `log_min_duration_statement = 0` for the duration of a 245 maintenance
   window, deploy the `56d06b87` binary to 245, capture the offending query
   from PG stderr, fix forward in source, re-build, re-test, then re-deploy
   to 154. Do **not** attempt Stage B before the prerequisite is met
   (the 245 swap will recur the regression with no diagnostic value).
2. **Re-establish the deploy guard.** Add a pre-deploy `sha256sum` + string-grep
   check to whatever wrapper script flips `current` so we never silently
   swap a binary that lacks expected fix strings.
3. **Re-tune the systemd unit's `TimeoutStopSec`** to ≥ `30s` and add a
   pre-stop `kill -INT` so the gateway can drain gracefully.
4. **Once fix-1 lands, re-deploy seq 1479 to 154 with the corrected binary**,
   and resume the original "observe fallback warn rate over 24h" mission
   from handoff §5.

## 9. References

- `AUDIT-2026-08-08-claude-gpt-sole-candidate-503.md` — original seq 1477 +
  seq 1479 audit; the deployment claim in §5 must be read with this addendum
  in mind.
- `/opt/llm-gateway-go/.deploy-history` — recorded pre-state of every
  `current` swap and rollback in this session.
- systemd journal: `journalctl -u llm-gateway-go --since "2026-08-08 23:30"`
  documents the two SIGKILLs at 23:48:27 / 23:52:40.

## 10. Stage B repro attempt — follow-up session 2026-08-09 00:47 CST

This session also attempted Stage B (reproduce the regression on 245 preprod
with PG statement logging enabled) as a next step. Findings:

### 10.1 245 environment observed
- `/opt/llm-gateway-go/current` → `releases/1479-3c98ac6c/gateway` (binary
  name on 245 is `gateway`, not `llm-gateway-go`).
- Running binary SHA `52c23fcb…` (44.7 MB), same artifact as 154 had.
  **245 also runs the binary that lacks the seq 1479 fix strings.**
- systemd unit: `ExecStart=/opt/llm-gateway-go/gateway`,
  `Environment=LLM_GATEWAY_USE_NEW_PROBE_MODE=false`,
  `TimeoutStopSec` unset (defaults to 90s).
- PG DSN in `/opt/llm-gateway-go/.env`:
  `postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@172.16.2.210:5432/llm_gateway`
  — PG is on a remote host (172.16.2.210, NOT local on 245 or 154).

### 10.2 PG side
- PG version: `PostgreSQL 17.10 (Debian 17.10-1.pgdg13+1)`.
- `log_statement = all`, `log_min_duration_statement = -1`, but
  `logging_collector = off`. So PG is configured to log every statement to
  stderr, but stderr goes to wherever the PG process is running — which we
  cannot reach from either 245 or 154 via SSH (no credentials to the 172.16.2.210
  host in this session's env-injector pool).
- `pg_stat_statements` is loaded with `track = top`, `track_utility = on`,
  `save = on`, ~1139 queries tracked.

### 10.3 Why `pg_stat_statements` couldn't reveal the offending query
The regression triggered `syntax error at or near "audit" (SQLSTATE 42601)`,
which is a **parse-time** error. `pg_stat_statements` only tracks successfully
parsed-and-planned statements; parse errors never enter the table. Surveyed
top 50 most-called statements and 11 audit-token matches; **every audit-token
match was inside a SQL comment** (e.g. `-- audit-ir-multimodal (2026-07-13): …`,
`-- Spec: 2026-06-12-credential-availability-audit-design §3.1`). None were
bare `audit` identifiers. Therefore the offending query **is not** sitting in
`pg_stat_statements` and cannot be recovered from this side.

### 10.4 Why I did NOT cut 245 to the broken binary
245 is the shared preprod (`llmgo.kxpms.cn`). Cutting to the broken
`56d06b87` binary would mean ~79% of preprod traffic goes to 500 for ~7 min
until detected and reverted — same blast radius I just caused on 154 prod.
Without a way to capture the offending query (no PG log access, no
query-capture proxy in place), the rollback signal would be a hot second
swap with no extra diagnostic value. **Therefore Stage B was not executed.**
This is the principled non-action; do not read the absence of evidence here as
the absence of a regression.

### 10.5 Concrete next steps for whoever picks this up
1. **Get SSH / psql access to the 172.16.2.210 PG host.** Without that, every
   Stage B attempt is blindsight. The PG logs (`log_statement = all` is
   already set) will reveal the offending query the moment the binary runs
   it again.
2. **Alternative: deploy a Postgres-aware query-capture sidecar**
   (e.g. `pgcat`, `pgbouncer` with `log_connections = on` +
   `log_min_duration_statement = 0`, or a tcpdump tap with `pg-query-logger`)
   in front of the gateway. This is heavier infrastructure; only do it if (1)
   fails.
3. **Alternative: pre-deploy grep of the binary for any unquoted SQL token.**
   `strings llm-gateway-go | grep -E '\b(audit|select|insert|update|delete|where|join|group|order|having)\b'`
   to surface every `audit`/other-reserved-keyword token. Then manually
   decide which ones are inside comments and which are SQL strings with
   unquoted identifiers. This is what got me to "string grep works for the
   reverse direction"; the forward direction (bad query not in any obvious
   place) suggests the bug is in a runtime query-construction path — i.e.
   the SQL is built dynamically with a parameter or branch I haven't yet
   found.
4. **Once the query is captured, fix forward and rebuild.** The diff is
   likely a column-identifier that needs `"audit"` (quoted) escaping, or an
   audit-keyword in a CTE/alias clause that needs renaming.

### 10.6 245 release-dir identity notes
- 245's binary is named `gateway` rather than `llm-gateway-go`. The
  `releases/` directory on 245 has `1478-3c98ac6c` and `1479-3c98ac6c`
  release-dirs, both with binaries that **lack** the seq 1479 warn strings.
- The release-dir labelled `1478-c4ab07de` does NOT exist on 245. To re-deploy
  seq 1479 there for repro, the actual artifact (`sha 56d06b87`, the 62.6 MB
  binary, or its source tree at HEAD ~ `b2c2e0fb`+`cddb9956`) needs to be
  uploaded to `releases/1478-c4ab07de/gateway` first.
- 245's systemd unit config (current state): `TimeoutStopSec` is unset, so
  the same SIGKILL-on-stop behaviour I saw on 154 is also latent on 245.

- Source diff for the seq 1479 fix: `git show cddb9956` — purely an executor
  change; no SQL touched by that commit, which is why the audit-keyword bug
  is *not* cddb9956-introduced directly (it's a latent pathology exposed by
  the swap).
