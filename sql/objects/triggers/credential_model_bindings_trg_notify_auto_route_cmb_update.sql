--
-- Name: credential_model_bindings trg_notify_auto_route_cmb_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_cmb_update AFTER UPDATE ON public.credential_model_bindings FOR EACH ROW WHEN (((old.available IS DISTINCT FROM new.available) OR (old.unavailable_reason IS DISTINCT FROM new.unavailable_reason) OR (old.unavailable_at IS DISTINCT FROM new.unavailable_at) OR (old.routing_tier IS DISTINCT FROM new.routing_tier) OR (old.weight IS DISTINCT FROM new.weight) OR (old.manual_priority IS DISTINCT FROM new.manual_priority) OR (old.active_sessions IS DISTINCT FROM new.active_sessions) OR (old.consecutive_failures IS DISTINCT FROM new.consecutive_failures) OR (old.context_window_override IS DISTINCT FROM new.context_window_override))) EXECUTE FUNCTION public.notify_auto_route_refresh();

