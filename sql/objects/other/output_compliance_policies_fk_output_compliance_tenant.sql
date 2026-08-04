--
-- Name: output_compliance_policies fk_output_compliance_tenant; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.output_compliance_policies
    ADD CONSTRAINT fk_output_compliance_tenant FOREIGN KEY (tenant_id) REFERENCES public.tenants(code) ON DELETE CASCADE;

