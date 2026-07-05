--
-- Name: api_keys trg_notify_auto_route_apikeys; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_apikeys AFTER UPDATE OF rate_limit_rpm, budget_usd, enabled, status ON public.api_keys FOR EACH ROW WHEN ((old.* IS DISTINCT FROM new.*)) EXECUTE FUNCTION public.notify_auto_route_refresh();

ALTER TABLE public.api_keys DISABLE TRIGGER trg_notify_auto_route_apikeys;

