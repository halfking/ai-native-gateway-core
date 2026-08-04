--
-- Name: asset_relationships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.asset_relationships (
    src_kind text NOT NULL,
    src_ref_id bigint NOT NULL,
    dst_kind text NOT NULL,
    dst_ref_id bigint NOT NULL,
    rel text NOT NULL,
    weight double precision DEFAULT 1.0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_asset_rel_type CHECK ((rel = ANY (ARRAY['depends_on'::text, 'calls'::text, 'similar_to'::text])))
);

