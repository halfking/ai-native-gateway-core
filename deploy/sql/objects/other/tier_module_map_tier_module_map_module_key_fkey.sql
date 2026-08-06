--
-- Name: tier_module_map tier_module_map_module_key_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tier_module_map
    ADD CONSTRAINT tier_module_map_module_key_fkey FOREIGN KEY (module_key) REFERENCES public.product_modules(key) ON DELETE CASCADE;

