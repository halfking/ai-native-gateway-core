--
-- Name: session_summaries fk_session_tenant; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_summaries
    ADD CONSTRAINT fk_session_tenant FOREIGN KEY (tenant_id) REFERENCES public.tenants(code) ON DELETE CASCADE;

