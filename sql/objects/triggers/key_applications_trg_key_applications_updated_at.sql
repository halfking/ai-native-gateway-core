--
-- Name: key_applications trg_key_applications_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_key_applications_updated_at BEFORE UPDATE ON public.key_applications FOR EACH ROW EXECUTE FUNCTION public.key_applications_set_updated_at();

ALTER TABLE public.key_applications DISABLE TRIGGER trg_key_applications_updated_at;

