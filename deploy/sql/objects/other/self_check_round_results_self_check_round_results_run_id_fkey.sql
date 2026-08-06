--
-- Name: self_check_round_results self_check_round_results_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.self_check_round_results
    ADD CONSTRAINT self_check_round_results_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.self_check_runs(id) ON DELETE CASCADE;

