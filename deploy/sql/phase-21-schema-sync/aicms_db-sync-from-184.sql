-- ============================================================
-- Sync SQL for database: aicms_db
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Source: 184 (production reference)
-- Tables missing in local: 5
-- ============================================================

\connect aicms_db

CREATE TABLE public.bots (
    id integer NOT NULL,
    name character varying(128) NOT NULL,
    description text NOT NULL,
    avatar character varying(512) NOT NULL,
    system_prompt text NOT NULL,
    memora_user_id character varying(128) NOT NULL,
    memora_namespace character varying(128) NOT NULL,
    small_model character varying(128) NOT NULL,
    large_model character varying(128) NOT NULL,
    temperature double precision NOT NULL,
    top_k integer NOT NULL,
    confidence_threshold double precision NOT NULL,
    enabled boolean NOT NULL,
    extra json NOT NULL,
    created_at timestamp without time zone NOT NULL,
    updated_at timestamp without time zone NOT NULL
);

CREATE TABLE public.contacts (
    id integer NOT NULL,
    name character varying(128) NOT NULL,
    channel character varying(32) NOT NULL,
    external_id character varying(128) NOT NULL,
    tags json NOT NULL,
    notes character varying(1024) NOT NULL,
    created_at timestamp without time zone NOT NULL,
    updated_at timestamp without time zone NOT NULL
);

CREATE TABLE public.intents (
    id integer NOT NULL,
    bot_id integer NOT NULL,
    name character varying(128) NOT NULL,
    description text NOT NULL,
    keywords json NOT NULL,
    pattern character varying(512) NOT NULL,
    action character varying(32) NOT NULL,
    reply_template text NOT NULL,
    priority integer NOT NULL,
    enabled boolean NOT NULL,
    created_at timestamp without time zone NOT NULL,
    updated_at timestamp without time zone NOT NULL
);

CREATE TABLE public.messages (
    id integer NOT NULL,
    session_id integer NOT NULL,
    role character varying(16) NOT NULL,
    content text NOT NULL,
    intent_matched character varying(128) NOT NULL,
    source_refs json NOT NULL,
    created_at timestamp without time zone NOT NULL
);

CREATE TABLE public.sessions (
    id integer NOT NULL,
    bot_id integer NOT NULL,
    contact_id integer,
    title character varying(256) NOT NULL,
    status character varying(32) NOT NULL,
    channel character varying(32) NOT NULL,
    created_at timestamp without time zone NOT NULL,
    updated_at timestamp without time zone NOT NULL
);

CREATE INDEX ix_contacts_external_id ON public.contacts USING btree (external_id);

CREATE INDEX ix_intents_bot_id ON public.intents USING btree (bot_id);

CREATE INDEX ix_messages_created_at ON public.messages USING btree (created_at);

CREATE INDEX ix_messages_session_id ON public.messages USING btree (session_id);

CREATE INDEX ix_sessions_bot_id ON public.sessions USING btree (bot_id);

CREATE INDEX ix_sessions_contact_id ON public.sessions USING btree (contact_id);
