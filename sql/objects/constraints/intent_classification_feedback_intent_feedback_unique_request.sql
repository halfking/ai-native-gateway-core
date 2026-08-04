--
-- Name: intent_classification_feedback intent_feedback_unique_request; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.intent_classification_feedback
    ADD CONSTRAINT intent_feedback_unique_request UNIQUE (request_id);

