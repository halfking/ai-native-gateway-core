--
-- Name: v_model_pricing_comparison; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_model_pricing_comparison AS
 SELECT mp.model_canonical,
    mp.display_name,
    mp.provider,
    mp.tier,
    mp.input_credits_per_1m,
    mp.output_credits_per_1m,
    round((((mp.input_credits_per_1m)::numeric * ms.cents_per_credit) / 100.0), 2) AS input_price_cny,
    round((((mp.output_credits_per_1m)::numeric * ms.cents_per_credit) / 100.0), 2) AS output_price_cny,
        CASE
            WHEN mp.supports_caching THEN round((((mp.cache_read_credits_per_1m)::numeric * ms.cents_per_credit) / 100.0), 2)
            ELSE NULL::numeric
        END AS cache_read_price_cny,
        CASE
            WHEN (mp.output_credits_per_1m > 0) THEN round((1000000.0 / (((mp.output_credits_per_1m)::numeric * ms.cents_per_credit) / 100.0)), 0)
            ELSE NULL::numeric
        END AS output_tokens_per_cny,
    mp.context_window,
    mp.supports_tools,
    mp.supports_vision,
    mp.supports_caching,
    mp.active
   FROM (public.model_pricing mp
     CROSS JOIN public.maas_settings ms)
  WHERE (mp.active = true)
  ORDER BY mp.provider, mp.tier DESC, mp.output_credits_per_1m;

