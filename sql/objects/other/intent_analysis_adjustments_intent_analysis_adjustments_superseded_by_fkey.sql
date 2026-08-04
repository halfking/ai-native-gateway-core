--
-- Name: intent_analysis_adjustments intent_analysis_adjustments_superseded_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.intent_analysis_adjustments
    ADD CONSTRAINT intent_analysis_adjustments_superseded_by_fkey FOREIGN KEY (superseded_by) REFERENCES public.intent_analysis_adjustments(id);

