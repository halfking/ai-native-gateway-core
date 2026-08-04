--
-- Name: releases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.releases (
    id bigint NOT NULL,
    version text NOT NULL,
    build_seq integer NOT NULL,
    channel text DEFAULT 'stable'::text NOT NULL,
    title text NOT NULL,
    description text,
    changelog text,
    image_tag text NOT NULL,
    image_digest text,
    min_version text,
    mandatory boolean DEFAULT false NOT NULL,
    created_by text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    published_at timestamp with time zone,
    CONSTRAINT releases_channel_check CHECK ((channel = ANY (ARRAY['stable'::text, 'beta'::text, 'canary'::text])))
);

