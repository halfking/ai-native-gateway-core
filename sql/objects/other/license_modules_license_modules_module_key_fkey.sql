--
-- Name: license_modules license_modules_module_key_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.license_modules
    ADD CONSTRAINT license_modules_module_key_fkey FOREIGN KEY (module_key) REFERENCES public.product_modules(key);

