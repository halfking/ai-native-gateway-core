--
-- Name: providers trg_notify_auto_route_providers; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_providers AFTER UPDATE OF enabled, manual_disabled ON public.providers FOR EACH ROW WHEN ((old.* IS DISTINCT FROM new.*)) EXECUTE FUNCTION public.notify_auto_route_refresh();

