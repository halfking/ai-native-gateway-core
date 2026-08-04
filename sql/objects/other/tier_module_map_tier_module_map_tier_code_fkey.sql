--
-- Name: tier_module_map tier_module_map_tier_code_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tier_module_map
    ADD CONSTRAINT tier_module_map_tier_code_fkey FOREIGN KEY (tier_code) REFERENCES public.subscription_tiers(code) ON DELETE CASCADE;

