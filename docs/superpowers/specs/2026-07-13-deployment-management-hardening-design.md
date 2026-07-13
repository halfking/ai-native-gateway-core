# Deployment Management Hardening Design

Date: 2026-07-13
Status: Approved
Scope: First deployment-management slice

## Goal

Make repository deployments deterministic and concurrency-safe through one public CLI, add reliable rollback for the 245 pre-production host, extend SOPS policy to active configuration names, and remove plaintext credentials from the current tree.

## Context

The root `deploy.sh` already delegates to `scripts/deploy.sh`, but the implementation has broken action parsing, mutating dry-run behavior, incomplete backups, incompatible target assumptions, and several independent target scripts. The repository also exposes plaintext credentials in tracked scripts and config files.

This slice deliberately establishes safe foundations before License and operations UI work.

## Decisions

1. `./deploy.sh` is the only public deployment entrypoint.
2. Implementation remains Bash and is hardened incrementally.
3. `scripts/deploy.sh` parses and orchestrates; target contracts, locks, and host operations live in focused shell libraries.
4. The first canonical targets are 154 and 245.
5. 186 is retired and must fail without performing mutations.
6. 252 is classified as deferred because repository evidence conflicts between native systemd and K3s. The independent `deploy-to-252.sh` remains the temporary target-of-record, but the canonical CLI must not invoke it until live topology, service manager, binary path, environment path, and health URL are verified.
7. Kaixuan deployment is not unified in this slice. Server inventory identifies kaixuan-1 as macOS launchd while the current canonical script calls it K3s; the canonical CLI must not touch it until live topology is verified. Only kaixuan-1 receives SOPS policy coverage because it is the only local host confirmed to run the gateway.
8. Deployment consumes existing version metadata. Version bumping and Git commits are separate release-preparation operations.
9. Locking is local-global plus remote-per-target and fails fast on contention.
10. 245 uses the tracked `llmgo-245` systemd contract.
11. Rollback restores application artifacts, not environment files or database migrations.
12. New encrypted environment files reuse the existing age recipient.
13. Plaintext credentials are removed from current HEAD and marked for operational rotation. Git history is not rewritten.

## Scope

### Included

- Correct CLI parsing for deploy, verify, rollback, plan, and force-unlock actions.
- A no-side-effect dry-run contract.
- Target contracts for 154 and 245.
- Explicit retirement behavior for 186.
- Explicit deferral behavior for 252 and non-kaixuan-1 local hosts.
- Local and remote deployment locking.
- Versioned application backups and rollback for 245.
- Automatic rollback after failed post-deploy verification.
- SOPS rules for `.env.252.enc` and `.env.kaixuan-1.enc`.
- Plaintext environment ignore rules and encrypted-file scanner handling.
- Removal of tracked plaintext credential assignments from current HEAD.
- Offline shell integration tests.

### Excluded

- Live deployment or credential rotation.
- 252 deployment topology migration.
- Kaixuan deployment unification.
- 186 support.
- Database migration rollback.
- Environment-file rollback.
- Blue-green or canary deployment.
- License dashboards, device UI, expiry alerts, and operations status UI.

## Architecture

```text
./deploy.sh
  -> scripts/deploy.sh
       -> parse command and target
       -> load target contract
       -> render plan or execute
       -> local lock
       -> remote target lock
       -> build/stage
       -> backup
       -> upload/switch/restart
       -> verify
       -> rollback on failure
       -> release locks
```

Focused shell modules under `scripts/deploy-lib/` expose stable functions rather than reading positional parameters directly:

- `targets.sh`: service name, binary path, web path, version paths, health URL, and SSH variable names.
- `lock.sh`: local acquire/release and remote acquire/release/force-unlock.
- `host.sh`: shared 154/245 staging, restart, and verification seams; this slice adds versioned backup and rollback only for 245.

Target-specific scripts become compatibility wrappers only when listed below and covered by the canonical CLI. Specialized setup, certificate, migration, and partial static-asset scripts remain separate and are not claimed as canonical.

## CLI Contract

```text
./deploy.sh --help
./deploy.sh plan <target>
./deploy.sh deploy <target> [--dry-run]
./deploy.sh verify <target>
./deploy.sh rollback <target> [--to <version>]
./deploy.sh force-unlock <target>
```

The existing shorthand `./deploy.sh <target>` remains a compatibility alias for `deploy <target>`.

`plan <target>` prints the normalized target, support state, service manager, service name, binary path, web path, health URL, source version, build sequence, planned local steps, planned remote steps, and rollback policy. The output format is the JSON document pinned by `tests/fixtures/plan_schema.json`; field order, key set, and types are fixed by the fixture and validated in test 1. It performs no connectivity check and never prints credential values.

The offline test harness stubs `env-injector`, `id`, and `hostname` through `PATH` so that env-injector supplies a deterministic fixture key set, `id -un` returns a per-process value, and `hostname` returns a per-process value. Without these stubs, AC-3 and AC-9 cannot be reproduced offline.

### Compatibility disposition

| Existing command or alias | Disposition |
|---|---|
| `71` | Preserve as a warning alias to 154 |
| `184` | Reject as deferred because it maps to unresolved 252 |
| `both` | Remove with usage error; sequencing 252 and 154 is unsafe while 252 is unresolved |
| `build` | Preserve as a non-deployment command delegated to the existing build path |
| `migrate <target>` | Remove from deployment CLI; database migration remains an explicit standalone operation |
| `verify <target>` | Preserve for canonical 154/245 targets |
| `rollback 245 [--to <version>]` | Canonical versioned rollback |
| `rollback 154` | Reject with guidance to the existing 154 rollback runbook in this slice |
| root `deploy-154.sh` and `scripts/deploy-154.sh` | Convert to thin wrappers only after canonical 154 parity tests pass |
| `deploy-to-252.sh` | Keep independent and noncanonical during deferral |
| `deploy-kaixuan1.sh` and `scripts/deploy-kaixuan1.sh` | Keep independent and noncanonical during deferral |
| `deploy/deploy.sh` and `deploy/rollback.sh` | Make fail-closed deprecation wrappers pointing to `./deploy.sh` |

Other specialized scripts are unchanged and are explicitly outside the unified-entrypoint guarantee.

The deploy path must not call `commit_build_seq`, `bump-version.sh`, or `git commit`. Version mutation code is removed from this CLI or moved behind the preserved standalone `build`/release-preparation path.

Exit codes remain stable for usage, precheck, build, deployment, verification, and rollback failures. Retired or deferred targets return a usage/configuration error before any lock, build, SSH, file write, or Git operation.

## Target Matrix

| Target | Status | Runtime contract |
|---|---|---|
| 154 | Canonical deploy/verify | `llm-gateway-go.service`, stable executable `/opt/llm-gateway-go/llm-gateway-go`, existing versioned binary naming, `/opt/llm-gateway-go/web`, `http://127.0.0.1:8781/healthz`; 71 remains a warning alias; rollback remains on the existing runbook in this slice |
| 245 | Canonical deploy/verify/rollback | `llmgo-245.service`, stable executable `/opt/llm-gateway-go/gateway`, release bundles under `/opt/llm-gateway-go/releases`, `/opt/llm-gateway-go/.env`, `http://127.0.0.1:8781/healthz` |
| 186 | Retired | Reject with retirement guidance |
| 252/184 | Deferred | `deploy-to-252.sh` remains temporary target-of-record; canonical CLI refuses to guess systemd versus K3s |
| kaixuan-1 | SOPS only | Inventory says macOS launchd while canonical script says K3s; both deployment scripts remain noncanonical until live verification |
| kaixuan-2/3 | Unsupported | No gateway deployment evidence |

## Dry-Run Contract

Dry-run performs parsing, target validation, read-only local file validation, and plan rendering only. Given an isolated empty `TMPDIR`, it must leave both the repository tree and `TMPDIR` byte-for-byte unchanged and invoke none of the fake mutating commands in the integration harness. It must not:

- modify version files or Git state;
- build artifacts;
- create local or remote locks;
- call SSH or SCP;
- create backups;
- restart services;
- write deployment records or temporary files.

## Locking

### Local lock

A repository-scoped global lock protects shared build artifacts and local deployment state. The implementation probes `command -v flock`; when available it acquires a nonblocking exclusive file-descriptor lock. Otherwise it acquires a lock with atomic `mkdir`, writes metadata only after successful creation, and removes the directory in a trap. There is no retry or implicit stale-lock cleanup in this slice.

### Remote lock

Each target uses an atomic `mkdir` lock directory on the target host. Metadata contains target, source OS user from `id -un`, source hostname, local PID, UTC start time, commit SHA, and version. It contains no secrets.

Lock contention fails fast. The local test launches two concurrent `deploy 245` processes against one fake remote filesystem; the second must exit nonzero before backup/upload and print the first lock's non-secret owner metadata. Locks are released by traps on normal and error exits. Age alone never authorizes stale-lock deletion; only `force-unlock <target>` removes a remote lock after an explicit operator action.

## Backup and Rollback

Before switching 245, the deployment stores one immutable release bundle under `/opt/llm-gateway-go/releases/${VERSION}` where `VERSION` is `version.json`'s `version` field, producing a directory name like `releases/2.4.2-45b592c5-20260713-991/`. Each bundle contains:

- executable named `gateway`;
- web static assets;
- `VERSION` and `version.json`;
- SHA-256 checksums;
- deployment metadata with `verified=false`.

`/opt/llm-gateway-go/gateway`, `/opt/llm-gateway-go/web`, and `/opt/llm-gateway-go/version.json` are stable symlinks that always resolve to `current/<...>`. The release boundary is the single `ln -sfn` of `current`; the symlink chain makes the kernel-visible binary path unchanged across deploys, so systemd restart sees only a post-restart state and the "atomic switch" claim holds without a separate rename race. After systemd restart and a successful `GET http://127.0.0.1:8781/healthz`, metadata is atomically replaced with `verified=true` and a verification timestamp.

Rollback without `--to` selects the newest retained `verified=true` release whose `VERSION` is not the active `current`. `--to` accepts only an exact retained version whose checksum manifest validates; a missing, active, unverified, or corrupt version fails before switching. If no `verified=true` nonactive release exists, the deploy exits nonzero with code 4 (`no_rollback_target`) and leaves the service in its current state; the operator must remove the failed bundle manually before retrying. Rollback switches `current`, restarts the tracked service, and verifies `/healthz`.

After a successful deploy or rollback, prune to the five newest verified releases plus the active release. Failed/unverified bundles are retained until the next successful operation, then only the newest failed bundle is kept as evidence. Existing `*.bak*` files are read-only legacy artifacts: they are neither selected nor deleted by the new CLI and require manual recovery through the old runbook.

Environment files, systemd units, Nginx configuration, and database migrations are not restored automatically. Any change to them requires a separate explicit operation.

## SOPS and Credentials

`.sops.yaml` uses the exact compatibility rule `^\.env\.(71|184|252|kaixuan-1)(\.enc)?$` with the existing age recipient. Plaintext `.env.252` and `.env.kaixuan-1` are explicitly ignored; encrypted forms remain trackable.

`.env.252.enc` and `.env.kaixuan-1.enc` are generated only from key names and values loaded through env-injector/SSOT. If the complete required key set cannot be injected, implementation stops and creates no placeholder encrypted file. A file is accepted as encrypted only when SOPS can parse its metadata and decrypt it with an authorized key; filename alone never bypasses secret scanning.

Decryption uses a permission-restricted temporary file and an exit trap that removes it. Decrypted values are never printed, committed, or persisted in repository paths.

Current-HEAD cleanup covers every tracked finding reported by the repository secret scanner, with a minimum inventory of the audited deployment/config scripts, `configs/env-252.sh`, `configs/env-kaixuan1.sh`, migration/sync helpers, generated audit output, and documentation examples. Executable configuration uses `${KEY}` references with fail-closed required-variable checks; documentation uses `<env:KEY>` placeholders.

`docs/changelogs/2026-07-13-deployment-management-hardening.md` records only affected key names, rotation owners, target environments, and verification status. It contains no values. Actual value rotation is a separate operational action after code verification and requires env-injector plus target health validation; Git history is not rewritten.

## Error Handling

- Invalid, retired, or deferred targets fail before side effects.
- Missing target configuration fails closed.
- Lock contention reports lock metadata without secrets.
- Backup failure aborts before switching artifacts.
- Restart or health failure triggers rollback while the remote lock is still held.
- Rollback verification failure preserves evidence and returns a distinct failure.
- Cleanup traps do not hide the original exit status.

## Testing

Offline Bash integration tests in `tests/deploy_cli_test.sh` inject fake `ssh`, `scp`, `systemctl`, build, and Git commands through `PATH`. Tests cover:

1. help, plan output, and command parsing;
2. shorthand and listed legacy compatibility;
3. 186 retirement plus 252/184/kaixuan deferral;
4. repository/TMPDIR/fake-command no-side-effect dry-run;
5. local and remote lock acquisition, contention, cleanup, and force-unlock;
6. the exact 245 executable, service, and health contract;
7. verified-release selection and retention;
8. exact-version rollback and missing/corrupt-version rejection;
9. failed-health automatic rollback before lock release;
10. the exact SOPS regex and plaintext ignore rules;
11. SOPS-envelope validation before scanner exemption;
12. absence of tracked plaintext credential findings under an empty `scripts/scan-secrets.baseline`.

Required verification commands are:

```text
bash -n deploy.sh scripts/deploy.sh scripts/deploy-lib/*.sh tests/deploy_cli_test.sh
shellcheck deploy.sh scripts/deploy.sh scripts/deploy-lib/*.sh tests/deploy_cli_test.sh
bash tests/deploy_cli_test.sh
bash scripts/scan-secrets.sh
bash scripts/pre-commit-check.sh
make build
make test
make lint
```

ShellCheck is a required gate when the executable is installed; if unavailable, the final report marks it unavailable rather than claiming it passed. This slice has no UI change, so browser verification is not required.

## Acceptance Criteria

- AC-1: Given any documented CLI form, when it is invoked against the fake-command harness, then parsing succeeds or returns the documented usage error and `plan` contains every normalized contract field without secret values.
- AC-2: Given an empty isolated `TMPDIR` and clean fixture repository, when `deploy 245 --dry-run` runs, then repository and `TMPDIR` hashes are unchanged and the fake mutating-command log is empty.
- AC-3: Given one fake `deploy 245` process holds the remote lock, when a second source-host process starts, then it exits nonzero before backup/upload and reports the first source user/host/PID metadata.
- AC-4: Given target 186, when any mutating action runs, then it returns a retirement error before local lock creation; 252, 184, and kaixuan targets similarly return a deferred/unsupported error.
- AC-5: Given two verified 245 bundles and one active bundle, when `rollback 245` runs, then it selects the newest nonactive verified bundle, validates checksums, atomically switches `current`, restarts the service, and passes `/healthz`.
- AC-6: Given `rollback 245 --to <version>`, when the version is missing, active, unverified, or corrupt, then no switch/restart occurs and the command exits nonzero.
- AC-7: Given a failed post-deploy `/healthz` response, when deployment verification runs, then the previous verified release is restored before the remote lock is released.
- AC-8: Given `.sops.yaml`, when its creation rules are inspected, then `^\.env\.(71|184|252|kaixuan-1)(\.enc)?$` uses the existing recipient; plaintext active files are ignored and filename-only scanner bypass is impossible.
- AC-9: Given env-injector can provide the complete required key set, when encrypted artifacts are generated, then SOPS decrypts both artifacts successfully without values appearing in output or Git plaintext; otherwise no artifact is created.
- AC-10: Given the current tracked tree, when `bash scripts/scan-secrets.sh` runs, then no blocking plaintext credential finding remains and the changelog lists rotation key names without values.
- AC-11: All required verification commands pass, except an unavailable ShellCheck executable is reported explicitly as unavailable.

## Delivery Slices

1. CLI parser, target contracts, target contracts sourced from `targets.sh`, and the tracked 154 unit `deploy/llm-gateway-go.service` (mirroring `deploy/llmgo-245.service`).
2. Offline deployment test harness with stub `env-injector`, `id`, `hostname`, `ssh`, `scp`, `systemctl`, build, and Git on `PATH`; a `tests/fixtures/plan_schema.json` pins `plan` field order and required keys.
3. Double-layer lock module with the stubbed `id`/`hostname` contract for AC-3.
4. 245 backup, deployment, verification, and rollback.
5. 154 canonical deploy/verify and a thin `deploy.sh` 154 wrapper replacing `scripts/deploy-154.sh`; 154 rollback is rejected with runbook guidance in this slice.
6. SOPS policy, ignore/scanner rules, encrypted artifacts, and the empty-baseline rewrite of `scripts/scan-secrets.baseline`.
7. Current-HEAD credential cleanup, rotation checklist, and changelog entry.
8. Compatibility wrappers for `deploy/deploy.sh`, `deploy/rollback.sh`, and any remaining legacy `deploy-*.sh` consumed by CI.

Each slice is independently testable and should remain below the repository incremental-commit thresholds.
