--
-- Name: credentials trg_notify_auto_route_creds; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_creds AFTER UPDATE OF status, availability_state, quota_state, circuit_state, concurrency_limit, concurrency_mode, rpm_limit, tpm_limit, fp_slot_limit, max_queue_depth, max_queue_wait_ms, lifecycle_status, manual_disabled ON public.credentials FOR EACH ROW WHEN ((old.* IS DISTINCT FROM new.*)) EXECUTE FUNCTION public.notify_auto_route_refresh();
