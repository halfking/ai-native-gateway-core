--
-- Name: diagnostic_runs diagnostic_runs_touch; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER diagnostic_runs_touch BEFORE UPDATE ON public.diagnostic_runs FOR EACH ROW EXECUTE FUNCTION public.touch_route_incidents_updated_at();

