--
-- Name: credentials trg_notify_governor_revision_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_credentials_governor_revision_update AFTER UPDATE OF concurrency_limit, concurrency_mode, rpm_limit, tpm_limit, fp_slot_limit, max_queue_depth, max_queue_wait_ms ON public.credentials FOR EACH ROW WHEN ((old.revision IS DISTINCT FROM new.revision)) EXECUTE FUNCTION public.notify_credentials_governor_revision();
