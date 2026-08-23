--
-- Name: credentials trg_notify_governor_revision_insert; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_credentials_governor_revision_insert AFTER INSERT ON public.credentials FOR EACH ROW EXECUTE FUNCTION public.notify_credentials_governor_revision();
