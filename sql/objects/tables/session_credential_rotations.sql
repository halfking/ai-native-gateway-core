--
-- Name: session_credential_rotations; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/895_domain_chain_startup_images.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-22 补录（R107 轮 · 12h 审计）：本表此前在正典目录里没有任何条目，
--   而迁移 895 已把它建进 startup 通道 ⇒ 门
--   sql/schema/session_family_canonical_ddl_test.go::TestCanonicalObjectsCoverEveryMigratedSessionTable
--   在 f11cf03a0 上判红 ⇒ **origin/main 门套件是红的**。
--   ★ 本表由 domain 链源文件 sql/migrations/domain/331_session_state_and_rotations.sql
--     逐字镜像而来（见 895 的形状对照门 TestMigration895ShapeMatchesTheDomainChainSources）。

CREATE TABLE public.session_credential_rotations (
    id                  bigint       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id          text         NOT NULL,
    tenant_id           text         NOT NULL,
    seq                 integer      NOT NULL,
    credential_id       bigint       NOT NULL,
    credential_label    text,
    model               text,
    provider            text,
    started_at          timestamptz  NOT NULL,
    ended_at            timestamptz,
    turns               integer      NOT NULL DEFAULT 0,
    duration_sec        integer      NOT NULL DEFAULT 0,
    prompt_tokens       bigint       NOT NULL DEFAULT 0,
    completion_tokens   bigint       NOT NULL DEFAULT 0,
    cost_usd            numeric(14,8) NOT NULL DEFAULT 0,
    switch_reason       text         NOT NULL DEFAULT 'initial',
    fp_slot_index       integer,
    created_at          timestamptz  NOT NULL DEFAULT now(),

    CONSTRAINT session_credential_rotations_session_seq UNIQUE (session_id, seq)
);
