# 2026-07-13 Deployment Management Hardening (Design)

Spec: `docs/superpowers/specs/2026-07-13-deployment-management-hardening-design.md`.

This entry records design approval and the planned implementation slices.
No code or value rotation is part of this commit.

## Scope

- Single public deployment entrypoint (`./deploy.sh`) and a hardened
  `scripts/deploy.sh` that only orchestrates.
- First canonical targets: 154 (deploy/verify only) and 245
  (deploy/verify/rollback with versioned release bundles).
- 186 retired, 252/184/kaixuan deferred pending live topology
  verification.
- Local-global plus remote-per-target double lock.
- SOPS envelope coverage extended to `.env.252.enc` and
  `.env.kaixuan-1.enc` (existing age recipient).
- Tracked plaintext credentials removed from current HEAD with
  rotation owners recorded here.

## Affected Key Names (no values, rotation list)

- `SSH_ROOT_PASSWORD` — rotation owner: infra; targets: 154, 245.
- `SSH_KEY_154` — rotation owner: infra; target: 154.
- `SSH_KEY_245` — rotation owner: infra; target: 245.
- `SSH_KEY_252` — rotation owner: infra; target: 252.
- `SSH_KEY_KAIXUAN_1` — rotation owner: infra; target: kaixuan-1.
- `KAIYUAN_SSH_PASSWORD` — rotation owner: infra; target: kaixuan-1.
- `PG_LLM_GATEWAY_USER` / `PG_LLM_GATEWAY_PASS` — rotation owner:
  platform-dba; targets: 154, 245, 252.
- `LLM_GATEWAY_API_KEY` — rotation owner: gateway; targets: 154, 245,
  252, kaixuan-1.
- `LLM_GATEWAY_ADMIN_PASSWORD` — rotation owner: gateway; targets:
  154, 245.
- `LLM_GATEWAY_SECRET_KEY` — rotation owner: gateway; targets: 154,
  245, 252.
- `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` — rotation owner: gateway;
  targets: 154, 245, 252.
- `LLM_GATEWAY_DATABASE_URL` — rotation owner: platform-dba; targets:
  154, 245, 252.
- `LLM_GATEWAY_ADMIN_API_KEY` — rotation owner: gateway; targets: 154,
  245.
- `REGISTRY_USER` / `REGISTRY_PASSWORD` — rotation owner: infra;
  target: 245 (registry host).

Verification status: pending implementation. Actual value rotation is
performed by infra/gateway owners through env-injector after the
matching code change merges, with health validation on each target.
Git history is not rewritten.

## References

- Design: `docs/superpowers/specs/2026-07-13-deployment-management-hardening-design.md`
- Inventory: `acc-toolkit/manifests/secrets/server-inventory.yaml`
- Rotation tooling: `acc-toolkit/scripts/env-injector.sh`,
  `acc-toolkit/scripts/secrets/load.sh`
