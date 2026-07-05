--
-- Name: credentials trg_check_credential_dates; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_check_credential_dates BEFORE INSERT OR UPDATE ON public.credentials FOR EACH ROW EXECUTE FUNCTION public.check_credential_dates();

ALTER TABLE public.credentials DISABLE TRIGGER trg_check_credential_dates;

