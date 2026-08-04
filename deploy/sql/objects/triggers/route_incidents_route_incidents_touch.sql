--
-- Name: route_incidents route_incidents_touch; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER route_incidents_touch BEFORE UPDATE ON public.route_incidents FOR EACH ROW EXECUTE FUNCTION public.touch_route_incidents_updated_at();

