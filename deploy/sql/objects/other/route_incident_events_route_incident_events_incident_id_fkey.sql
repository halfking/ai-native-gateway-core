--
-- Name: route_incident_events route_incident_events_incident_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.route_incident_events
    ADD CONSTRAINT route_incident_events_incident_id_fkey FOREIGN KEY (incident_id) REFERENCES public.route_incidents(id) ON DELETE CASCADE;

