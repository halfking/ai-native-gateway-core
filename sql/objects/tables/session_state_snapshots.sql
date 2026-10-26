--
-- Name: session_state_snapshots; Type: TABLE; Schema: public; Owner: -
--
-- ⚠ 快照来源：sql/migrations/startup/895_domain_chain_startup_images.sql
--   本目录是**快照**（静态 jsonb lint 的列映射来源），**sql/migrations/startup/ 为权威**。
--   2026-10-22 补录（R107 轮 · 12h 审计）：本表此前在正典目录里没有任何条目，
--   而迁移 895 已把它建进 startup 通道 ⇒ 门
--   sql/schema/session_family_canonical_ddl_test.go::TestCanonicalObjectsCoverEveryMigratedSessionTable
--   在 f11cf03a0 上判红 ⇒ **origin/main 门套件是红的**。
--   ★ 本表由 domain 链源文件 sql/migrations/domain/331_session_state_and_rotations.sql
--     逐字镜像而来（见 895 的形状对照门 TestMigration895ShapeMatchesTheDomainChainSources）。

CREATE TABLE public.session_state_snapshots (
    id                      bigint       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id              text         NOT NULL UNIQUE,
    tenant_id               text         NOT NULL,
    api_key_id              bigint       NOT NULL,
    task_id                 text,
    client_ip               text,
    client_fp               text,
    client_profile          text,
    status                  text         NOT NULL DEFAULT 'active',
    created_at              timestamptz  NOT NULL,
    first_request_at        timestamptz,
    last_request_at         timestamptz,
    stopped_at              timestamptz,
    stop_reason             text,
    recovered_at            timestamptz,
    final_credential_id     bigint,
    final_model             text,
    final_provider          text,
    total_turns             integer      NOT NULL DEFAULT 0,
    total_duration_sec      integer      NOT NULL DEFAULT 0,
    total_prompt_tokens     bigint       NOT NULL DEFAULT 0,
    total_completion_tokens bigint       NOT NULL DEFAULT 0,
    total_cost_usd          numeric(14,8) NOT NULL DEFAULT 0,
    title                   text,
    summary                 text,
    annotation              text,
    tags                    text[]       NOT NULL DEFAULT '{}',
    fp_slot_index           integer,
    raw_snapshot            jsonb,
    created_at_db           timestamptz  NOT NULL DEFAULT now()
);
