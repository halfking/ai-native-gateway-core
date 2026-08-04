--
-- Name: prompt_injection_detections prompt_injection_detections_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prompt_injection_detections
    ADD CONSTRAINT prompt_injection_detections_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES public.prompt_injection_rules(id) ON DELETE SET NULL;

