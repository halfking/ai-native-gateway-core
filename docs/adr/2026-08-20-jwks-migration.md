# ADR-0020: Migrate identity-go from HS256 shared secret to EdDSA + JWKS

- **Status:** Proposed
- **Date:** 2026-08-20
- **Scope:** All 6 repos vendoring `identity-go` (`memora`, `openpocket`, `llm-gateway-go`, `RedClaw`, `agent-control-center` (`acc`), `ai-session-manager` (`asm`))
- **Supersedes / affects:** the current `token.Allowlist` + `token.VerifyMultiIssuer` HS256 trust model

> Numbering note: `ADR-0020` is the fleet-wide identity-series number shared verbatim across all 6 repos. It is intentionally distinct from each repo's local ADR sequence (e.g. RedClaw is locally at `0004`, llm-gateway-go at `ADR-0002`), so that this cross-repo decision has one stable identifier everywhere.

---

## Context

### Current scheme: one HS256 secret for the whole fleet

Six repos share a vendored `identity-go` module for JWT minting and verification. Five of them are **issuing** services; one is a **consumer** only.

| Repo | Role | Issuer (`iss`) |
| --- | --- | --- |
| `RedClaw` | issuer + verifier | `redclaw` |
| `memora` | issuer + verifier | `memora` |
| `llm-gateway-go` | issuer + verifier | `llm-gateway` |
| `openpocket` | issuer + verifier | `pocket` |
| `agent-control-center` (`acc`) | issuer + verifier | `acc` |
| `ai-session-manager` (`asm`) | **verifier / consumer only** | *none — forbidden as issuer* |

Today the scheme is:

- **Algorithm:** HS256 (symmetric HMAC-SHA256), enforced at parse time. `alg=none` and algorithm-substitution attacks are correctly rejected via an explicit signing-method check plus `jwt.WithValidMethods([]string{"HS256"})`.
- **Key material:** a **single shared secret**, loaded from the `IDENTITY_SHARED_SECRET` environment variable (validated at `>= 32` bytes).
- **Issuer allowlist:** `redclaw,memora,llm-gateway,pocket,acc`, parsed from env into a list of issuers. Critically, `token.Allowlist(envValue, sharedSecret)` assigns **the same `sharedSecret` to every issuer entry** — the symmetric shared-key mode.
- **Audience:** `aud` is required and explicitly matched against the resource server's expected audience (single value, `[]string`, and `[]any` forms all handled).
- **`asm` is forbidden as an issuer:** `ai-session-manager` shares `llm-gateway-go`'s user issuer, tenant, and scope; it owns no independent user identity issuer.

The verification path itself is sound and well covered: the per-repo `*_identity_test.go` suites (40+ cases across `memora/internal/middleware`, `openpocket/backend/internal/auth`, `acc-go/internal/auth`, `llm-gateway-go/admin`, `ai-session-manager/internal/auth`, plus `identity-go/token`) assert allowlist enforcement, audience matching, `asm` rejection, expiry, cross-issuer isolation, short/empty secret rejection, and gated shadow mapping. **This ADR is not motivated by a verification bug.** It is motivated by the shape of the key material.

### The problem: a global trust boundary with a single point of failure

Because every issuer **signs** and **verifies** with the *same* HS256 secret, the secret is simultaneously a signing capability and a verification capability, fleet-wide. HMAC has no public/private split: whatever can check a signature can also produce one.

Therefore a compromise of **any single repo** — leaked source, poisoned build, exfiltrated runtime env var, a debug log line, a CI secret dump, or one over-permissioned container — hands the attacker the ability to **forge valid tokens for all 5 issuers**, impersonating any user or service anywhere in the fleet.

#### Blast radius: what one repo compromise can impersonate

| Compromised repo | Secret obtained | Issuers it can forge | Services that will accept the forgery |
| --- | --- | --- | --- |
| `RedClaw` | `IDENTITY_SHARED_SECRET` | `redclaw`, `memora`, `llm-gateway`, `pocket`, `acc` — **all 5** | all 6 (incl. `asm`) |
| `memora` | `IDENTITY_SHARED_SECRET` | **all 5** | all 6 (incl. `asm`) |
| `llm-gateway-go` | `IDENTITY_SHARED_SECRET` | **all 5** | all 6 (incl. `asm`) |
| `openpocket` | `IDENTITY_SHARED_SECRET` | **all 5** | all 6 (incl. `asm`) |
| `agent-control-center` (`acc`) | `IDENTITY_SHARED_SECRET` | **all 5** | all 6 (incl. `asm`) |
| `ai-session-manager` (`asm`) | `IDENTITY_SHARED_SECRET` | **all 5** *(despite `asm` being forbidden as an issuer — it still holds the signing key it needs only for verification)* | all 6 |

The `asm` row is the sharpest illustration of the flaw: `asm` is deliberately **forbidden from issuing tokens**, yet under HS256 it must hold the full signing key merely to *verify* them. The issuer allowlist is a policy control, and policy controls do not survive key possession.

Consequences of this shape:

- **No per-service key isolation.** The trust boundary is the union of all 6 repos, their build pipelines, and their runtime environments. Security is bounded by the *weakest* of the six, not the strongest.
- **Rotation is a synchronized fleet outage.** Replacing the secret requires coordinated redeploy of all 6 repos; there is no way to distrust one service's key while keeping others valid.
- **No surgical revocation.** There is no key identifier to revoke — only one secret for everything, so the only remedy is rotating the single global secret.
- **Weak forensics.** A forged token is byte-for-byte indistinguishable from a legitimate one; nothing in the token attributes it to a signer.

---

## Decision

**Adopt EdDSA (Ed25519) asymmetric signing for all inter-service identity tokens, with per-issuer public key publication via JWKS.**

Concretely:

1. **Per-issuer private keys.** Each of the 5 issuing services (`redclaw`, `memora`, `llm-gateway`, `pocket`, `acc`) holds **its own Ed25519 private key**, never shared with any other service. Ed25519 is chosen over RSA for small keys/signatures, fast verification, and no parameter-choice footguns (no exponent or padding decisions).
2. **Every key gets a `kid`.** Tokens carry a `kid` in the JWT header identifying the exact signing key, enabling per-key rotation and per-key revocation.
3. **JWKS publication.** Each issuer publishes its **public** keys at `GET /.well-known/jwks.json` (a shared JWKS aggregator fronting all issuers is an acceptable alternative, provided its content is derived from each issuer's own published set and it is not itself a signing authority). The endpoint is unauthenticated and cacheable — it contains only public material.
4. **Verification.** Verifiers resolve the signing key by `iss` → JWKS URI → `kid`, then validate, in order: signing algorithm is `EdDSA` (reject `none`, reject HMAC, reject algorithm substitution), signature, `iss` against the existing allowlist, `aud` against the resource server's expected audience, and the existing temporal claims (`exp`, `nbf`, `iat`).
5. **JWKS caching and rotation.** Verifiers cache JWKS with a TTL, refresh on unknown-`kid` (rate-limited to prevent a forced-fetch amplification vector), tolerate publishing overlap during rotation (old and new `kid` both present), and fail closed when a key cannot be resolved.
6. **Preserve existing, correct policy.** The issuer allowlist stays. The explicit `aud` check stays. **`asm` remains forbidden as an issuer** — and under EdDSA this becomes structurally enforced rather than merely policy-enforced: `asm` holds no private key at all, so it is cryptographically incapable of minting a token for any issuer.

---

## Consequences

### Positive

- **Per-issuer key isolation — the primary win.** Compromising one repo's private key forges **only that issuer's** tokens. The blast radius shrinks from "all 5 issuers, all 6 services" to "1 issuer". The table above collapses from 5 columns of impersonation to 1.
- **Verifiers hold no signing power.** A leak from a consumer — most importantly `asm`, which needs verification only — grants **zero** forging ability, because verifiers hold public keys exclusively. This closes the `asm` row of the blast-radius table entirely.
- **Surgical revocation without fleet redeploy.** A single compromised `kid` can be removed from that issuer's JWKS; verifiers drop it on next refresh. No shared secret to redistribute, no synchronized 6-repo deploy.
- **Clean, independent key rotation.** Each issuer rotates on its own schedule by publishing a new `kid` alongside the old, switching minting, then retiring the old key after the overlap window.
- **Better forensics and attribution.** `kid` ties every token to a specific key of a specific issuer.
- **Standard, interoperable surface.** `/.well-known/jwks.json` is the OIDC-compatible convention; future non-Go consumers or an eventual real OIDC provider integrate without bespoke work.

### Negative / costs

- **New infrastructure to build and operate:** JWKS publication endpoints on 5 services, plus a fetch/refresh/cache client in all 6. This is net-new failure surface (a verifier now has a network dependency on issuers for key resolution).
- **Cache and clock consistency.** JWKS TTL must be reconciled with token TTL and rotation overlap. Too-long TTLs delay revocation; too-short TTLs add load and fragility. Clock skew handling for `exp`/`nbf` becomes more visible once keys can change mid-window.
- **Availability coupling.** If an issuer's JWKS endpoint is unreachable and the cache is cold or expired, verification fails closed — correct for security, but a new operational dependency requiring cache-stale-serve policy and monitoring.
- **Private key management.** 5 private keys now need secure generation, storage, injection, and rotation tooling — replacing "one secret everywhere" with real key management. This is more work, and it is the point.
- **A dual-verify migration window.** Both HS256 and EdDSA must be accepted simultaneously for a period (see below). During that window the HS256 global-trust weakness **still exists in full** — the migration is not security-positive until step 5 completes.
- **All 6 repos change**, including the shadow-mapping path and the provider-contract verification path, plus the 40+ existing identity test cases, which need EdDSA and dual-verify coverage added (existing assertions should be preserved, not replaced).
- **Larger tokens** (Ed25519 signatures are 64 bytes vs. HMAC's 32) and a `kid` header — negligible, noted for completeness.

---

## Migration plan (phased)

The ordering is deliberate: **verification capability is deployed everywhere before any token is minted with the new algorithm.**

**Phase 1 — Add EdDSA signing + JWKS publication (no behavior change).**
Generate an Ed25519 keypair per issuing service. Add an EdDSA signer and a `GET /.well-known/jwks.json` endpoint to each of the 5 issuers. **Keep HS256 as the only verifier and the only minting path.** Nothing accepts or emits EdDSA yet; this phase is purely additive and independently deployable.

**Phase 2 — Dual-verify behind a flag.**
Add a JWKS-fetching EdDSA verifier to all 6 repos and accept **either** HS256 or EdDSA, gated by a feature flag. Per-token algorithm dispatch must stay strict: an EdDSA token is verified *only* against JWKS public keys and an HS256 token *only* against the shared secret — never a fallback chain that lets an attacker downgrade. Roll out to all 6 and confirm JWKS fetch/refresh/cache works in every consumer, including `asm`.

**Phase 3 — Flip minting to EdDSA.**
Switch new token minting to EdDSA, issuer by issuer, so a single issuer can be rolled back independently. Existing HS256 tokens remain valid until natural expiry; dual-verify covers the drain window.

**Phase 4 — Deprecate the HS256 verifier.**
Once all 5 issuers publish JWKS, all 6 verifiers fetch successfully, and all HS256 tokens have expired, remove the HS256 verification path and drop `HS256` from the accepted-algorithms list, leaving `EdDSA` only.

**Phase 5 — Remove the shared secret.**
Delete `IDENTITY_SHARED_SECRET` from all environments, secret stores, CI configuration, and compose/deployment files, and remove `LoadSharedSecret` usage from the code. **The global-trust vulnerability is not closed until this phase completes** — until the secret is gone from every environment, it remains forgeable material.

### Per-repo scope

- **`redclaw`, `memora`, `llm-gateway`, `pocket`, `acc`** (5 issuers): full scope — private key, EdDSA signer, JWKS endpoint, **and** the verifier/JWKS-fetch side.
- **`ai-session-manager` (`asm`)** (consumer only): **only the verifier / JWKS-fetch side.** It needs no private key, no signer, and no JWKS endpoint. `asm` must remain forbidden as an issuer, and it should be the first repo to shed the shared secret in Phase 5, since it has no legitimate need for signing material.

---

## Security note

**The HS256 global-trust compromise impact is the primary motivator for this ADR.** One shared secret used for both signing and verification across 5 issuers and 6 services means a single repo compromise — at source, build, or runtime — yields forged tokens for every issuer in the fleet, with no per-service isolation and no surgical revocation. That, not any defect in the allowlist, audience, or `asm`-rejection logic, is what this change addresses.

Two honest limits on what this buys:

1. **JWKS does not, by itself, fix a compromised *private key*.** If an attacker steals an issuer's Ed25519 private key, they can forge that issuer's tokens, and verifiers will correctly accept them — the published JWKS will happily validate the forgery. What asymmetric keys buy is **containment**: the damage stops at one issuer instead of all five, and verifiers (including `asm`) can no longer leak signing power at all. **Rotation plus `kid` revocation is the mechanism that actually bounds blast radius in time**, and it must be built and rehearsed, not merely designed. Without working rotation, this migration trades one static long-lived secret for five static long-lived keys and gains isolation but not recoverability.
2. **The migration window is a period of unchanged risk, not reduced risk.** During Phases 1–4 the shared secret still exists and still verifies. Phases must not be allowed to stall midway; a permanently half-migrated fleet carries the full HS256 weakness *plus* the added complexity of JWKS.

Additional controls to carry into implementation: keep the strict algorithm allowlist (`EdDSA` only, post-migration) so `alg` confusion and `none` remain impossible; fail closed on unresolvable `kid`; rate-limit JWKS refresh triggered by unknown `kid`; serve JWKS over TLS; and never let a JWKS response influence which *algorithm* is accepted.

---

## References

Located in `deploy/local-multi-stack/` (repo-root-relative within the `ai-native-tools` workspace):

- **`deploy/local-multi-stack/REPORT-2026-08-19-IDENTITY-GO.md`** — identity audit report for the vendored `identity-go` module; documents the HS256 shared-secret model, the issuer allowlist, and the audit findings that motivate this ADR. *(Confirmed present.)*
- **`deploy/local-multi-stack/REPORT-2026-08-19-6PROJ-TRUST-E2E.md`** — 6-project cross-trust end-to-end verification report; records the passing allowlist / `aud` / `asm`-rejection / shadow-gating behavior that this migration must preserve. *(Present under this filename; a `REPORT-2026-08-19-6PROJ-TRUST-E2E-COMPLETE.md` variant was **not** found in the workspace — treat this file as the authoritative E2E reference.)*
- Related context in the same directory: `REPORT-2026-08-18-6PROJ-LOCAL-DOCKER.md`, `state-A.md`, `docker-compose.yml` (the last is where `IDENTITY_SHARED_SECRET` must be removed in Phase 5).

Code touchpoints for implementers (vendored `identity-go`, one copy per repo):

- `third_party/identity-go/token/issuer.go` — `Allowlist()` (assigns the shared secret to every issuer), `LoadSharedSecret()`
- `third_party/identity-go/token/verify.go` — `VerifyMultiIssuer()` (HS256-forced parse, allowlist check, `aud` check)
- `third_party/identity-go/token/sign.go` — `SignHS256()`
- `third_party/identity-go/shadow/` — shadow mapping path (gated; must be updated alongside the verifier)
- Per-repo suites: `memora/internal/middleware/gateway_identity_test.go`, `openpocket/backend/internal/auth/auth_identity_test.go`, `agent-control-center/acc-go/internal/auth/auth_identity_test.go`, `llm-gateway-go/admin/auth_identity_test.go`, `llm-gateway/ai-session-manager/internal/auth/auth_identity_test.go`
