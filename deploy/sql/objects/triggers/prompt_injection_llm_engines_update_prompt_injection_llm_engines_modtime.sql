--
-- Name: prompt_injection_llm_engines update_prompt_injection_llm_engines_modtime; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER update_prompt_injection_llm_engines_modtime BEFORE UPDATE ON public.prompt_injection_llm_engines FOR EACH ROW EXECUTE FUNCTION public.update_modified_column();

