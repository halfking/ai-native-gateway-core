# P1: Provider Expansion

> Status: `RECONSTRUCTED-DRAFT`
> Evidence: facts marked `SOURCE-VERIFIED`; migration and API choices are `NEW-DESIGN`.

## Source facts

- `SOURCE-VERIFIED`: OmniRoute provider data is distributed across `src/shared/constants/providers/{noauth,oauth,web-cookie,apikey/*,local,search,audio,upstream-proxy,cloud-agent,system}.ts` and related catalog files. A reliable current total is not established; the old “290 providers” claim is `MISSING-EVIDENCE`.
- `SOURCE-VERIFIED`: Go resolves candidates from the database through `provider.Client`; `provider.Candidate` already carries protocol, catalog, pricing, cache, latency, context, quota, and billing fields.
- `SOURCE-VERIFIED`: Go runtime protocol values include `openai-completions` and `anthropic-messages`; they are distinct from the domain-level provider protocol enum.

## Proposed boundary

`NEW-DESIGN`: export OmniRoute catalog data to a reviewed intermediate JSON file, validate it, and generate idempotent SQL seed statements. The database remains the single runtime source of truth. Do not generate a large Go constant file and do not place secrets in the export.

Validation must cover catalog key uniqueness, protocol mapping, URL scheme, model aliases, auth kind, and explicit omission of unsupported fields. OAuth client secrets and API keys are `MISSING-EVIDENCE` until a secret-store contract is approved.

## Acceptance gates

1. A source checkout and export command are recorded.
2. Generated SQL is deterministic and idempotent.
3. Existing candidates and routing behavior are unchanged when the seed is absent.
4. Catalog import tests cover OpenAI-shaped and Anthropic-shaped candidates.
5. Rollback is a seed disable/delete procedure approved separately from this document.
