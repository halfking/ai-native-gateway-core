--
-- Name: session_last_requests trigger_session_last_requests_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trigger_session_last_requests_updated_at BEFORE UPDATE ON public.session_last_requests FOR EACH ROW EXECUTE FUNCTION public.update_session_last_requests_updated_at();

