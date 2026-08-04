--
-- Name: request_attachments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_attachments (
    id bigint NOT NULL,
    request_id text NOT NULL,
    attachment_type text DEFAULT 'image'::text NOT NULL,
    content_type text,
    size_bytes bigint DEFAULT 0 NOT NULL,
    storage_path text,
    hash text,
    original_url text,
    message_index integer DEFAULT 0 NOT NULL,
    block_index integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'detected'::text NOT NULL,
    error_code text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT request_attachments_status_check CHECK ((status = ANY (ARRAY['detected'::text, 'storing'::text, 'stored'::text, 'manifest_ready'::text, 'sent'::text, 'store_failed'::text])))
);


--
-- Name: TABLE request_attachments; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.request_attachments IS 'Per-request attachment metadata extracted from inbound bodies (base64/data-URI).
Mirrors request_logs.attachments JSONB (migration 325) in relational form.
Attachment bytes are persisted by the gateway under
LLM_GATEWAY_ATTACHMENT_DIR (SHA256 two-level sharding from migration 58f31d74d);
this table stores metadata only.

Status lifecycle:
  detected      — Extractor found the attachment
  storing       — Storage.SaveBase64Image is running
  stored        — File is on disk and dedup-checked
  manifest_ready — Captured in failover manifest (reusable on retry)
  sent          — Forwarded to upstream provider
  store_failed  — Storage failure (strict mode → 503)

Migration 401 (2026-07-15) — Phase 2C relational upgrade.';

