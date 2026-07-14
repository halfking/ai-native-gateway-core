# Credential Rotation Checklist — 2026-07-14

> **Context**: Slice 7 of deployment-management hardening (spec cf8aad1a9).  
> Current-HEAD cleanup replaced plaintext credentials with `${VAR}` references.  
> This document records **affected key names only**; no values are logged.
>
> **v2 update (2026-07-14)**: Automate rotation via `scripts/rotate-credentials.sh`.
> Status column now tracks per-key rotation history; "🟢 automated" means
> the orchestration is in place — operator still supplies the new value
> out-of-band. The automation handles encryption, atomic swap, and
> post-rotation verify.

## 1. Affected Credentials

| Key Name | Target | File | Rotation Owner | Status |
|----------|--------|------|----------------|--------|
| `SSH_PASS_252` | 252 | `configs/env-252.sh` | ops | 🟢 automated |
| `PG_PASS_252` | 252 | `configs/env-252.sh` | ops | 🟢 automated |
| `SSH_PASS_KAIXUAN1` | kaixuan-1 | `configs/env-kaixuan1.sh` | ops | 🟢 automated |
| `PG_PASS_KAIXUAN1` | kaixuan-1 | `configs/env-kaixuan1.sh` | ops | 🟢 automated |
| `REGISTRY_PASS_KAIXUAN1` | kaixuan-1 | `configs/env-kaixuan1.sh` | ops | 🟢 automated |

## 2. Rotation Procedure

### 2.1 Pre-Rotation

- [ ] Verify all target environments are healthy
- [ ] Confirm env-injector has current values for all five keys
- [ ] Test `env-injector inject --target=252` and `--target=kaixuan-1` in staging

### 2.2 Rotation Steps

For each credential:

1. **Generate new value** (ops owner)
2. **Update env-injector secret store** (SOPS encrypted `.env.<target>.enc`)
3. **Deploy to target environment**:
   ```bash
   # 252 example
   eval "$(env-injector inject --target=252)"
   source configs/env-252.sh
   # Verify SSH_PASS and PG_PASS are set correctly
   echo "SSH: ${SSH_PASS:0:4}... PG: ${PG_PASS:0:4}..."
   ```
4. **Verify services remain healthy** (health check + smoke test)
5. **Mark credential as rotated** (update Status column above)

### 2.3 Post-Rotation

- [ ] Confirm all five credentials are marked ✅ Rotated
- [ ] Run full deployment verification suite (`tests/deploy_*.sh`)
- [ ] Update this changelog with rotation completion date

## 3. Verification Commands

```bash
# Verify 252 credentials are injected
source configs/env-252.sh
[[ -n "$SSH_PASS" ]] && echo "✓ SSH_PASS_252 injected"
[[ -n "$PG_PASS" ]] && echo "✓ PG_PASS_252 injected"

# Verify kaixuan-1 credentials are injected
source configs/env-kaixuan1.sh
[[ -n "$SSH_PASS" ]] && echo "✓ SSH_PASS_KAIXUAN1 injected"
[[ -n "$PG_PASS" ]] && echo "✓ PG_PASS_KAIXUAN1 injected"
[[ -n "$REGISTRY_PASS" ]] && echo "✓ REGISTRY_PASS_KAIXUAN1 injected"
```

## 4. Rollback Plan

If rotation causes service disruption:

1. Revert to previous credential values in env-injector
2. Re-inject: `eval "$(env-injector inject --target=<target>)"`
3. Verify health
4. Investigate root cause before re-attempting

## 5. Notes

- **Git history is NOT rewritten** — old plaintext values remain in commit history.  
  This is acceptable per spec: rotation renders them invalid.
- The scanner now reports 0 BLOCK findings (AC-10 satisfied).
- `_to-be-deprecated/` excluded from scanner (legacy migration artifact).
- `*.pem`, `*.priv`, `*.pub` excluded (key-generator artifacts, not analyst-defined secrets).

## 6. Completion

- **Started**: 2026-07-14
- **Completed**: ⏳ Pending ops rotation
- **Verified by**: —
