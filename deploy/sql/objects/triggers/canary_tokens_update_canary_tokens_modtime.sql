--
-- Name: canary_tokens update_canary_tokens_modtime; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_canary_tokens_modtime BEFORE UPDATE ON public.canary_tokens FOR EACH ROW EXECUTE FUNCTION public.update_modified_column();

