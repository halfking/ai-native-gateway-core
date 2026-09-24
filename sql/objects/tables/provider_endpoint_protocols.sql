--
-- Name: provider_endpoint_protocols; Type: TABLE; Schema: public; Owner: -
--
-- 2026-09-24 (r0924 supplier-protocol-optimization §3.2): per-provider
-- endpoint table. The pre-r0924 model had ONE (base_url, protocol) pair
-- per providers row, forcing operators to either: (a) declare one
-- protocol as "primary" and live with silent downgrades when an
-- Ollama-native client hit a chat-compatible Ollama deployment, or
-- (b) split the supplier into multiple provider rows (one per protocol)
-- which broke the credential/balance/quota rollups.
--
-- The new model: one providers row, N provider_endpoint_protocols rows.
-- Each row carries its own (protocol, base_url, vendor_native, weight,
-- health) tuple. The dispatcher (EndpointSelector) decides per-request
-- which tuple to land on. The legacy (providers.base_url, providers.protocol)
-- pair stays as the "Stage 4" fallback for operators who haven't filled
-- in the child rows yet — backfill is opportunistic (see migration V800).
--
-- Contract (must stay in lock-step with provider/catalog/protocol.go,
-- internal/endpointselect/selector.go, internal/upstreamurl/upstreamurl.go):
--   - protocol ∈ CHECK list (openai-completions, openai-responses,
--     anthropic-messages, gemini-generate, ollama-native)
--   - Exactly one row per provider has is_primary=true (enforced by
--     the partial unique index below)
--   - weight is a tiebreaker for two enabled endpoints of the same
--     protocol; lower weight = higher priority
--   - vendor_native is the discovery-canonical family string
--     (anthropic-claude, openai-gpt, google-gemini, ollama, ...)
--     — free text but validated at write time (TODO r0924 follow-up)

CREATE TABLE public.provider_endpoint_protocols (
    id                    bigserial    PRIMARY KEY,
    provider_id           bigint       NOT NULL,
    tenant_id             text         NOT NULL DEFAULT 'default',
    protocol              text         NOT NULL,
    base_url              text         NOT NULL,
    is_primary            boolean      NOT NULL DEFAULT false,
    vendor_native         text,
    enabled               boolean      NOT NULL DEFAULT true,
    weight                smallint     NOT NULL DEFAULT 100,
    notes                 text,
    health_status         text         NOT NULL DEFAULT 'unknown',
    health_checked_at     timestamptz,
    health_latency_ms     integer,
    health_error          text,
    consecutive_failures  integer      NOT NULL DEFAULT 0,
    last_failure_kind     text,
    created_at            timestamptz  NOT NULL DEFAULT now(),
    updated_at            timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT provider_endpoint_protocols_unique
        UNIQUE (provider_id, protocol),
    CONSTRAINT provider_endpoint_protocols_provider_fk
        FOREIGN KEY (provider_id) REFERENCES public.providers(id) ON DELETE CASCADE,
    CONSTRAINT provider_endpoint_protocols_protocol_check
        CHECK (protocol = ANY (ARRAY[
            'openai-completions',
            'openai-responses',
            'anthropic-messages',
            'gemini-generate',
            'ollama-native'
        ])),
    CONSTRAINT provider_endpoint_protocols_base_url_nonempty
        CHECK (length(base_url) > 0),
    CONSTRAINT provider_endpoint_protocols_weight_positive
        CHECK (weight BETWEEN 1 AND 999)
);

-- One primary endpoint per provider: prevents the dispatcher from
-- seeing two rows with is_primary=true (would create an
-- undefined-ordering bug). Migration V800 backfills is_primary=true
-- on the legacy row.
CREATE UNIQUE INDEX provider_endpoint_protocols_one_primary
    ON public.provider_endpoint_protocols (provider_id)
    WHERE is_primary = true;

-- Hot-path indexes used by the dispatcher's endpoint-load query:
--   "give me all enabled endpoints for this provider, primary first".
CREATE INDEX provider_endpoint_protocols_provider_idx
    ON public.provider_endpoint_protocols (provider_id, protocol);

-- Family lookup (Stage 2 of EndpointSelector): when no protocol match,
-- fall back to family match. Index covers the (provider_id,
-- vendor_native) lookup. Partial index skips null vendor_native.
CREATE INDEX provider_endpoint_protocols_family_idx
    ON public.provider_endpoint_protocols (provider_id, vendor_native)
    WHERE vendor_native IS NOT NULL AND enabled = true;