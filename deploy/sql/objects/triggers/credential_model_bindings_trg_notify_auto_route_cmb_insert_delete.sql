--
-- Name: credential_model_bindings trg_notify_auto_route_cmb_insert_delete; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_cmb_insert_delete AFTER INSERT OR DELETE ON public.credential_model_bindings FOR EACH ROW EXECUTE FUNCTION public.notify_auto_route_refresh();

