--
-- Name: session_bodies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_bodies (
    id bigint NOT NULL,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id character varying(255) NOT NULL,
    request_id text NOT NULL,
    request_delta jsonb,
    response_delta jsonb,
    outbound_body jsonb,
    request_attachments jsonb DEFAULT '[]'::jsonb,
    response_attachments jsonb DEFAULT '[]'::jsonb,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    partition_date date DEFAULT CURRENT_DATE NOT NULL
)
PARTITION BY RANGE (partition_date);


--
-- Name: TABLE session_bodies; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.session_bodies IS 'V2正文存储表：存储增量正文，避免request_logs的全量JSONB膨胀问题。
     ★ 存储引擎真相（2026-10-20 修正，审计 D3）：本表是 **heap + lz4 TOAST**，
     **从未使用列存（columnar）**。压缩由 TOAST 的 lz4 完成
     （sql/migrations/startup/765_bodies_columnar_storage.sql:127-128 的
     SET COMPRESSION lz4）；此前那条把本表说成「列存 + 压缩可省 60-80% 磁盘」的
     COMMENT 与实现相反 —— 本族因 promote_session_bodies 用
     ON CONFLICT DO NOTHING（列存关系不能承载唯一索引）而**必须**留 heap，
     论证见 765_bodies_columnar_storage.sql:13-21，
     829_bodies_columnar_rollback.sql 记录了列存方案被回滚。
     Created: 2026-07-17, Migration 430';

