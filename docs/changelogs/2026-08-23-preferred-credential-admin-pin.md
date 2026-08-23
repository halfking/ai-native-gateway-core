# 2026-08-23 preferred-credential admin-pin wiring + Bandit weight-nudge (build 1685-1687)

## Scope
Two-line feature chain shipped to 245 (pre-prod) then 154 (prod):

- **`8e71eb4e1`** `feat(streaming): wire admin API key into ChatHandler for credential override`
- **`62fb7a56c`** `fix(credential): wire weight-nudge into BanditScorer.Sample`

Build evidence: 245 seq=1686, 154 seq=1687 (commits 87fa9814 / 443ae01d1 for the version bump chain).

## What changed

### Preferred-credential admin override (8e71eb4e1)

ChatHandler now honours a second pin path gated by the static admin token
(`LLM_GATEWAY_ADMIN_API_KEY` / `cfg.AdminAPIKey`), in addition to the existing
user-level `parsePinCredentialHeader`:

```
POST /v1/chat/completions
Authorization: Bearer <client-api-key>
X-LLMGW-Admin-Token: <cfg.AdminAPIKey>
X-LLMGW-Preferred-Credential: 42
```

The header value `42` is parsed as a credential id and propagated into the
dispatcher's `PinCredentialID`, overriding the bandit selection. The same
mechanism exists for the body field `metadata.preferred_credential` (see
`domains/streaming/preferred_credential.go:ExtractPreferredCredential`).

Auth model: `adminAuthorized` does a `crypto/subtle.ConstantTimeCompare`
between the request's `X-LLMGW-Admin-Token` header and `cfg.AdminAPIKey`.
Empty `cfg.AdminAPIKey` disables the override path entirely — production
deployments must set it before the header can do anything.

### Weight-nudge bandit wiring (62fb7a56c)

Fixes the broken `BanditScorer.Sample` from the previous WIP branch
(referencing a non-existent `b.weightNudge.GetWeightNudge()` field). New
behaviour:

- `BanditScorer` now carries `recentKinds map[string]KindWindow`,
  `weightNudgeFactors WeightNudgeFactors`, `weightNudgeEnabled bool` — all
  read inside the existing RLock window inside `Sample`.
- `ObserveError(credID, kind)` records the kind into the window using an
  allow-list (`kindToWeightDelta`) so non-actionable error categories stay
  silent.
- `SetWeightNudge(factors, enabled)` wires the env-driven config in.
- `Reset(credID)` / `ResetAll()` now also clear `recentKinds`.
- `WeightNudge(window, factors, enabled)` is the pure function; when
  `enabled==false` or no observations exist it returns `1.0` (no-op).

## Production status

- **Live on 154 (build 1687, commit 87fa9814)** — verified end-to-end
  with `curl /v1/chat/completions` against `llm.kxpms.cn` with
  `X-LLMGW-Preferred-Credential: 42`. Audit log evidence:
  `request_id=70f9e08b53f82f910d1add519056f155`, `top_credential_id=42`,
  `top_provider_id=14`, `upstream_status=200`, `latency_ms=947`.
- **Live on 245 (build 1686, commit 4fe32e14)** — same evidence chain
  (`request_id=74007cca531ed3e9f2aa4de59207dc4a`).

## Follow-up (handoff)

The weight-nudge factor is currently inert in production:

1. **`main.go` Bandit block still commented out** (line ~1125). Until that
   path is uncommented, `SetWeightNudge` is never called and
   `weightNudgeEnabled` stays `false` (Bandit itself is disabled, so
   WeightNudge is a no-op anyway — `WeightNudge` returns `1.0` when
   disabled).
2. **`MessagesHandler` and `ResponsesHandler` are still on the
   user-level `parsePinCredentialHeader` only**; the admin-pin path is
   only wired into `ChatHandler` (`domains/streaming/handler.go:3900`).
3. **v2 pipeline preflight** still reads the preferred-credential
   override from a hardcoded default path rather than cfg.

These three items are scoped for the next session — see the `handoff/`
doc generated at session end.