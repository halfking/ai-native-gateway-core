--
-- Name: credentials trg_bump_governor_revision; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_bump_credentials_governor_revision BEFORE INSERT OR UPDATE OF concurrency_limit, concurrency_mode, rpm_limit, tpm_limit, fp_slot_limit, max_queue_depth, max_queue_wait_ms ON public.credentials FOR EACH ROW EXECUTE FUNCTION public.bump_credentials_governor_revision();
