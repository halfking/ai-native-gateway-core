--
-- Name: session_summary_layers; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/999_session_summary_layers.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-11-23 补录（L20 修红轮）：迁移 999 创建的表在正典目录里【没有任何条目】
--   ⇒ 「按正典文件读会话族」的人会读出一张**不存在的表**
--   （sql/schema/session_family_canonical_ddl_test.go 的判据原话：
--   「正典目录是静态 jsonb lint 的列映射来源，也是下一轮审计读形状的地方；
--   缺条目 = 读出一张不存在的表」）。

CREATE TABLE public.session_summary_layers (
    id                bigint NOT NULL,
    tenant_id         character varying(255) NOT NULL,
    gw_session_id     character varying(128) NOT NULL,
    layer_type        character varying(20) NOT NULL,
    layer_sequence    integer NOT NULL,
    from_turn         integer,
    to_turn           integer,
    from_request_id   character varying(128),
    to_request_id     character varying(128),
    summary           text NOT NULL,
    key_points        text[],
    parent_layer_ids  bigint[],
    turn_count        integer,
    message_count     integer,
    token_count       integer,
    created_at        timestamp with time zone DEFAULT now() NOT NULL,
    content_hash      character varying(64) NOT NULL,
    CONSTRAINT session_summary_layers_pkey PRIMARY KEY (id),
    CONSTRAINT session_summary_layers_type_check CHECK ((layer_type = ANY (ARRAY['window'::character varying, 'segment'::character varying, 'meta'::character varying]))),
    CONSTRAINT uq_session_layer UNIQUE (tenant_id, gw_session_id, layer_type, layer_sequence)
);
