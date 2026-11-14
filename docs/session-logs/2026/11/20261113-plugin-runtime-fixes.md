# Plugin runtime audit remediation

Initial base: `938366f3e`; synchronized first to `3fd9c26c4`, then to `bf7f18f52` during the authorized security expansion. The latter incorporated the original runtime modules and independent upstream fixes; all were preserved during three-way resolution. Branch: `codex/plugin-runtime-audit-fixes`.
Scope: adopt the four previously untracked runtime modules and fix audit R1–R9.
The original checkout and its concurrent plugin API work are preserved.

## Implementation and verification plan

1. R4–R8: bounded complete JSON IPC messages; cancelable requests; closed client/server lifecycle; valid benchmark inputs. Regressions exercise fragmented requests/responses, cancellation, shutdown, 10MiB messages and 100 concurrent successful requests.
2. R3: serialize reloads, apply handlers before publishing configuration, undo applied handlers in reverse on failure. Verify old configuration and applied state survive a failed reload, including concurrent/manual and watched reloads.
3. R2/R9: observe actual process versions and aggregate action errors. Verify upgrades through public Reconcile and continued processing when one plugin fails.
4. R1: an explicitly configured controller adapts the existing verified JSON-manifest/HTTP-Unix supervisor to the new reconciler, config reloader, event bus and IPC. Preserve the legacy startup path when the new configuration is absent; reject invalid explicit configuration. Only one controller owns process lifecycle.
5. Integrate real helper subprocess tests for startup, version changes, config disable/enable, failed application rollback, inter-plugin event delivery and shutdown. Add root/CI coverage for nested modules.
6. Run module race tests, relevant root integration tests, vet, build and repository verification. Independent review must resolve findings before commits and push. Record any environmental or pre-existing failures honestly.

## Rollback

Disable the explicit runtime configuration to restore the legacy startup path. Code rollback after merge: `git revert -m 1 <merge-commit>` (the final merge SHA will be recorded in the completion record).

## Evidence

The nine audit findings have regression coverage:

| Audit | Correction | Evidence |
| --- | --- | --- |
| R1 / P1 | Explicit gateway opt-in, one existing JSON Supervisor, controller adapters, dynamic ready routing, authenticated event IPC, CI invocation | Real child-process controller suite and production initializer tests |
| R2 / P1 | Record and compare the version actually launched | Public Reconcile upgrades to 2.0.0, then remains stable |
| R3 / P1 | Publish config only after callbacks succeed; reverse compensation on failure | Failed callback retains old config and applied state |
| R4 / P1 | Bounded complete JSON framing | Fragmented requests/responses and 10 MiB round trips |
| R5 / P2 | Cancellation interrupts connection I/O and reaches handlers | Cancellation and deadline race regressions |
| R6 / P2 | Client Close rejects new requests and interrupts active connections | Closed-client and in-flight Close tests |
| R7 / P2 | Server Stop cancels handlers, joins workers, and shares completion | Concurrent Stop, never-started Stop, socket handoff regressions |
| R8 / P2 | Valid benchmark JSON, checked errors and successful request counts | Seven benchmarks pass, including 10 MiB and 100-worker batches |
| R9 / P2 | Aggregate lifecycle errors without dropping other actions | Disable/Stop errors and continued reconciliation regressions |

Independent review also fixed strict production readiness, live-but-unhealthy recovery, invalid later manifest isolation, and retention of accepted snapshots after failed recovery/rollback. Config and IPC shutdown use sync.Once to satisfy both concurrent completion semantics and the repository stopguard.

Validation integration includes the root Makefile, Codeup workflow and official verify.sh. Nested module tests would otherwise be omitted by root `go test ./...`. Vendor is regenerated using `go mod vendor`. At the initial runtime-only checkpoint, existing dependency versions were preserved; the later authorized security expansion upgrades them as recorded below. Two extra upstream goacos documentation/notice files are mechanical vendor output.

The first official gate exposed an existing false positive: SQL lint treated a legal `UPDATE ... SET data = $1` as an illegal PostgreSQL SET placeholder. The checker now recognizes statement/string boundaries, block-comment endings and PL/pgSQL control boundaries. Its 16 positive/negative contracts run from the actual pre-commit gate; no gate is skipped or bypassed.

Config race/vet (84.6%), eventbus race (85.9%), IPC race/vet (81.5%), reconciler race/vet (91.5%), controller real-process race/vet (82.0% coverage), gateway focused tests, SQL contracts and stopguard pass. An earlier full run exposed an overly short 2-second test context and the manual Stop completion pattern. Both were corrected and reviewed. The final BaseURL getter holds the ready-state read lock through the process snapshot lookup, preventing cross-generation routing; its fresh controller race run passed in 7.966s.

## Operational boundaries

- Upgrades stop the old process before starting its replacement; requests can be interrupted.
- Event delivery is bounded best effort, with no durable replay.
- Config-managed administrative activation/install/uninstall endpoints return 409; update the enabled list or installed bundle instead.
- Static web assets still use the existing plugin directory serving path. Bundle rollback covers executable/manifest/runtime state, not atomic rollback of these existing static assets.
- The experimental nested YAML supervisor is not selected in production; its DTOs are adapted to the existing root supervisor.

## Session persistence

The sessionstore CLI could not connect using its configured native credentials (`Native credential file: cannot read valid JSON`). Required remote session append/pin/archive remain unavailable; architecture decisions were saved through agentmemory's explicitly reported local fallback. No successful memora archive is claimed.


## Verification ledger maintenance

The full gate required refreshing generated route/source-line evidence and registering six plugin helper environment variables as fixture components (none selects a database). It also found four existing F3 script entries absent from the pipefail inventory. Only their factual ledger entries were added; the unrelated production script was not executed or modified.

Local macOS fixtures on 2026-11-14 showed: BSD `head -n-1` rejects even a 9-byte response and aborts the assignment under `set -e`; explicit request-ID extraction fallback retained the first ID for 1 and 10,000 records in five trials each; grep-q matching succeeded in all five small-output trials (537/522 bytes) but falsely failed in all five large-output trials (1,048,601/1,048,586 bytes) despite a first-line match. The ledger records these existing risks, not a false claim that the F3 script is safe. Cross-platform thresholds and production behavior were not inferred.

The next official attempt exposed host dependence in the existing monitor gauge fixture: low local free disk space legitimately produced a warning (exit 1), while the test expected only unavailable DB gauges (exit 3). The fixture now uses the monitor's existing injectable df/docker commands, a healthy synthetic disk and an unavailable synthetic container; configuration, notification and state are isolated inside its temporary directory. Production monitoring and all result assertions remain unchanged. The original failure and isolated passing run are retained in /tmp/plugin-runtime-monitor-rerun.log and /tmp/plugin-runtime-monitor-green.log.


## Final validation checkpoint (2026-11-14)

All plugin findings R1–R9 are fixed and independently reviewed. The full root Go suite, nested runtime modules, IR mutation/coverage gates, shell contracts and deployment state-machine tests passed in the official `GOFLAGS=-p=4 VERIFY_CGO_ENABLED=1 bash verify.sh` run. That run stopped at an existing storage fixture defect; after repairing it, the exact storage gate and its `--race` variant passed against disposable loopback PostgreSQL/Redis. No production services were contacted.

Two additional baseline blockers were minimally repaired and independently approved by generic and Go reviewers:

- `internal/sessionv2mirror/ingest_schema_realdb_test.go`: explicitly enables recording consent using the existing test override, after checking the isolated database, and restores the prior value during cleanup. Without this fixture setting, the production default correctly declined body writes; the migration assertions never reached their intended INSERT. Production privacy defaults are unchanged.
- `installer/cmd/llm-gw-installer/main.go`: removes 14 identical duplicate migration map entries. All 428 unique key/value pairs, including migration 964 up/down, remain identical. Installer tests and the complete build-tag matrix now pass.

Additional completed checks: privacy tests, root `go vet ./...`, gateway build, six local-module/source vendor parity, staged/unstaged whitespace checks. The complete official script has **not** returned PASS: the final vulnerability check returns exit 3 for 11 findings in existing dependency/toolchain versions, which this task has not changed.

| Dependency | Current baseline | Scanner remediation |
| --- | --- | --- |
| Echo | v4.15.4 | Six findings list v4.16.0; GO-2026-6875 and GO-2026-6872 list no fixed v4 version |
| klauspost/compress | v1.18.7 | GO-2026-6865 lists v1.20.2 |
| Go standard library | go1.27.2 | GO-2026-6859 requires go1.27.4; GO-2026-6809 requires go1.27.3 |

Primary advisory sources: https://pkg.go.dev/vuln/GO-2026-6875 and https://pkg.go.dev/vuln/GO-2026-6872. Scanner reachability is not a demonstrated exploit; no offensive reproduction was performed. In particular, the v4 advisories lack a known fixed v4 release, so simply upgrading a patch dependency cannot honestly make the gate pass. The user authorized the security expansion on 2026-11-14. This historical failure is superseded by the upgraded scan below; no scanner suppression was introduced.

Detailed logs and a per-file scope inventory are retained in the local audit artifact `plugin-runtime-audit/fix-evidence`. The earlier official failure log is retained separately from each subsequent passing targeted check, so it is not represented as a complete official PASS. Existing platform-dependent skips (for example unavailable flock tests on macOS) remain visible in the logs.


## Authorized security expansion and upstream integration

User instruction: upgrade dependencies and the Go toolchain, implement necessary Echo migration, pass all validation, then commit and push.

- Minimum Go patch is now `1.27.4` in all ten maintained modules; executable CI pins and maintained service Dockerfiles use the patched toolchain. The primary private base image remains the published `1.27.1-alpine` tag with explicit `GOTOOLCHAIN=go1.27.4` because registry inspection proved a private `1.27.4-alpine` tag does not exist. Public service build bases pin `1.27.4-alpine`.
- `klauspost/compress` moves from `1.18.7` to `1.20.2`, and Echo from `v4.15.4` to `v5.4.5`. `go mod tidy` removes unused v4 dependencies; vendor is regenerated. Only trailing whitespace in two upstream Echo Markdown files is normalized to satisfy the repository whitespace gate; vendored Go sources remain byte-identical to their canonical module/local sources.
- All 47 Echo consumer files move to the v5 context API. Customer logs resolve the actual response/error status. Gateway and License Authority use the existing trusted-proxy CIDR policy instead of unconditionally trusting request headers. Authority explicitly configures the extractor so trusted nginx clients retain independent trial rate-limit buckets. Default trusted proxies remain loopback; additional proxies use the existing `LLM_GATEWAY_TRUSTED_PROXY_CIDRS` setting. Direct peers retain the safe v5 default. Framework logs use the configured slog logger, and Authority retains graceful shutdown through an explicit HTTP server.
- Service Docker dependency layers copy vendor before compiling, so local runtime-module replacements do not fail during a premature `go mod download` with their directories absent.
- New ordinary compatibility tests cover trusted proxy/default direct identity, customer status logging, Authority IPv4/IPv6/configured proxies and independent client quotas. Two overlay negative controls caught deliberate context/logging regressions without editing shared source.

Upstream main advanced to `bf7f18f52` while verification ran. A named stash preserved the complete candidate before fast-forward and three-way restoration. The upstream discovery new-directory watcher and supervisor single-Wait/restart/environment fixes remain. The candidate preserves upstream IPC owner-only socket permissions and non-closing pool channels; a new 0600 regression failed before the permission block was restored and passes afterward. Runtime source/vendor parity is checked again after resolution.

Expanding race coverage to all six runtime modules exposed three existing nested supervisor races: shared health ticker ownership, premature wait completion, and reading `exec.Cmd.ProcessState` concurrently with Wait. The ticker is now local to its owner; completion is published after terminal state/events/map removal and before any restart backoff; health reads the completion signal. Five consecutive supervisor race runs and vet pass. The production controller continues using the mature root supervisor.

Independent generic and Go review approved the migration, trusted-proxy compatibility, baseline gate fixes and merge resolution. The upgraded `govulncheck` scan returns exit 0: no reachable vulnerabilities found (three non-reachable module advisories are informational). The complete official gate passed under Go 1.27.4 with GOFLAGS=-p=2 and VERIFY_CGO_ENABLED=1, ending `[verify] PASS` (exit 0). All six runtime modules and the final affected production packages passed race/vet checks. Root and installer vulnerability scans report zero reachable vulnerabilities. This validates the runtime/security candidate on bf7f18f52; later upstream integration is recorded separately.


The final official rerun also required registering one upstream `GET /api/v1/plugins` route missing from the POST surface ledger (702 routes versus 701 rows). The actual static classifier reports `unknown / func-not-indexed`; the added row retains empty observation fields and `not-probed-unresolved`. The existing 701 rows and verdict rules remain unchanged. Focused coverage/self-consistency checks and independent review pass.


Helper-module validation uses their actual structure: `scripts/injection-test` has three separate documented `go run <file>` commands in one directory, so `go test ./...` is not a valid package invocation (multiple main declarations). Each entrypoint instead passed `go build -o /dev/null <file>` and `go vet <file>` under Go 1.27.4; none was executed against a provider. `tests/local/gomapper` contains no Go source and passed dependency verification. No source was changed to disguise those structures.


A subsequent complete root run passed the audit package (244.485s) but hit host loopback ephemeral-port exhaustion in two sanitize tests: local HTTP/Redis dials returned `can't assign requested address`. Read-only host evidence showed 16,193 TIME_WAIT sockets against ports 49152–65535 (16,384 slots), with TCP MSL 15,000ms. A retry while the pool was still exhausted also failed; after reclamation the count fell to 466. No assertions, production TCP settings or scanner rules were changed. The final official invocation uses `GOFLAGS=-p=2` to reduce concurrent socket pressure; the failure logs are retained independently from the later result.


## Runtime/security candidate commit checkpoint

Full official verification passed: root Go tests (including ursmcheck), all six runtime modules and production integration, four IR mutation harnesses and coverage, shell gates and 14-case deployment state machine, disposable PostgreSQL/Redis coherence and ingest/settings tests, privacy, vet, 16-configuration build-tag matrix, gateway build and vulnerability scan. Existing macOS/opt-in branches are visible in the log (flock-unavailable cases, optional live migration fixture and frontend checks with no frontend diff); no required gate was disabled. Root scan reports 0 reachable findings, 3 non-reachable module advisories; installer reports 0 reachable, 1 non-reachable module advisory.

A separate active checkout contains concurrent user ingress changes, so final trunk integration/push will use this clean integration checkout. Remote main e18a446b3 introduces additional largebody/auth scaffolding; its compatibility review found two body-cache/dispatch regressions that must be corrected before the final merge is published. The coherent runtime/security commit includes the regenerated vendor tree and all dependent type/call-site changes together; most changed lines are generated third-party replacement or mechanical migration.


## e18 upstream integration validation

Integrated the largebody/auth upstream changes without replacing the original shared checkout. Corrected and reviewed: oversized L1 entries no longer replace a valid Redis body or return a partial local pair; local reads reload from DB or miss; all strict state/body budgets are checked even with empty bodies; dispatch cap errors are typed, return HTTP 413 with the actual limit and stop fallback; empty sniffed bodies return the documented zero value. Native JWT tests isolate the inherited secret setting. Existing L1 budget tests observe locked residency directly while retaining their original budget/eviction expectations; new public-read tests validate complete pairs.

Affected/transitive full package suites passed (gateway, streaming, compression, largebody, sessioncontext, sanitize and authplugin), as did root vet and the vulnerability gate. Full dispatch-bodycap tagged suite, strict-cache original reproduction, compression race/vet and independent Go/generic review pass. Full merged source audit found only four generated route line numbers shifted by the pipeline correction; the official generator refreshed those lines, and the snapshot/ledger/request-surface/PG-env gates passed afterward. All other audit cases passed in that full run. Detailed failed and corrected logs are retained rather than described as an uninterrupted full audit pass.

Additional remote commits through 792b0e373 will be retained in the final trunk merge and their new packages/routing compatibility validated before publication.


## Final trunk compatibility checkpoint

Through main 792b0e373, newly added intent/native/manifest API packages pass complete tests and race checks; gateway full tests, all-package vet, dependency tidy and the reachable-vulnerability gate pass. Focused source-fact/route/ledger/environment checks pass. Public plugin-list prefix routing is byte-identical to the previously reviewed b61983481 main: all non-GET requests enter the administrator settings handler; valid activation/deactivation retains its more specific protected lifecycle route. This intentionally excludes a new public suffix-based write dispatch while retaining the upstream API library and its tests. Independent static review approved that security-preserving integration.

Main 5e0d838e9 adds an independent native-password-policy/token-revocation fix; it is retained as the final merge base and validated next.
