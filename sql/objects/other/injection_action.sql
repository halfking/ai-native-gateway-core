--
-- Name: injection_action; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.injection_action AS ENUM (
    'pass',
    'log',
    'warn',
    'replace',
    'redact',
    'remove',
    'reject',
    'terminate',
    'approve',
    'quarantine',
    'block'
);


--
-- Name: TYPE injection_action; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TYPE public.injection_action IS '处理动作类型 - 11种响应动作';

