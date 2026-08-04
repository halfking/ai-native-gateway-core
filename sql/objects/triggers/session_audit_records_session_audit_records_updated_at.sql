--
-- Name: session_audit_records session_audit_records_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER session_audit_records_updated_at BEFORE UPDATE ON public.session_audit_records FOR EACH ROW EXECUTE FUNCTION public.trg_session_audit_records_updated_at();

