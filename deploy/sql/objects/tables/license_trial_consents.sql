--
-- Name: license_trial_consents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.license_trial_consents (
    id bigint NOT NULL,
    license_id bigint NOT NULL,
    agreement_version text NOT NULL,
    accepted_at timestamp with time zone NOT NULL,
    source text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

