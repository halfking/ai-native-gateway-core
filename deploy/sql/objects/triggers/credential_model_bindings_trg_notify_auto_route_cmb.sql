--
-- Name: credential_model_bindings trg_notify_auto_route_cmb; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_cmb AFTER INSERT OR DELETE OR UPDATE ON public.credential_model_bindings FOR EACH ROW EXECUTE FUNCTION public.notify_auto_route_refresh();

ALTER TABLE public.credential_model_bindings DISABLE TRIGGER trg_notify_auto_route_cmb;

