--
-- Name: product_module_features product_module_features_module_key_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_module_features
    ADD CONSTRAINT product_module_features_module_key_fkey FOREIGN KEY (module_key) REFERENCES public.product_modules(key) ON DELETE CASCADE;

