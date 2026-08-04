--
-- Name: prompt_injection_policies fk_prompt_injection_tenant; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prompt_injection_policies
    ADD CONSTRAINT fk_prompt_injection_tenant FOREIGN KEY (tenant_id) REFERENCES public.tenants(code) ON DELETE CASCADE;

