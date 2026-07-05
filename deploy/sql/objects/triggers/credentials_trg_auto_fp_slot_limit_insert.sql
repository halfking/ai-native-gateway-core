--
-- Name: credentials trg_auto_fp_slot_limit_insert; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_auto_fp_slot_limit_insert BEFORE INSERT ON public.credentials FOR EACH ROW EXECUTE FUNCTION public.auto_set_fp_slot_limit();

ALTER TABLE public.credentials DISABLE TRIGGER trg_auto_fp_slot_limit_insert;

