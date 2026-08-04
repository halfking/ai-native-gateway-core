--
-- Name: injection_category; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.injection_category AS ENUM (
    'role_hijack',
    'instruction_override',
    'instruction_leak',
    'jailbreak',
    'encoding_bypass',
    'injection_marker',
    'multi_turn_attack',
    'resource_exhaustion',
    'data_exfiltration',
    'social_engineering',
    'prompt_leaking',
    'payload_smuggling',
    'unicode_obfuscation',
    'context_manipulation',
    'tool_abuse'
);


--
-- Name: TYPE injection_category; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TYPE public.injection_category IS '提示词注入风险类别 - 15种攻击类型';

