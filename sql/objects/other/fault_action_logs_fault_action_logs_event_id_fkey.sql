--
-- Name: fault_action_logs fault_action_logs_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fault_action_logs
    ADD CONSTRAINT fault_action_logs_event_id_fkey FOREIGN KEY (event_id) REFERENCES public.fault_events(id) ON DELETE CASCADE;

