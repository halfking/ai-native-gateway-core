--
-- Name: credit_ledger_with_current_month; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.credit_ledger_with_current_month AS
 SELECT credit_ledger_hot.id,
    credit_ledger_hot.tenant_id,
    credit_ledger_hot.entry_type,
    credit_ledger_hot.amount,
    credit_ledger_hot.balance_after,
    credit_ledger_hot.ref_type,
    credit_ledger_hot.ref_id,
    credit_ledger_hot.note,
    credit_ledger_hot.created_at,
    credit_ledger_hot.pool
   FROM public.credit_ledger_hot
UNION ALL
 SELECT credit_ledger.id,
    credit_ledger.tenant_id,
    credit_ledger.entry_type,
    credit_ledger.amount,
    credit_ledger.balance_after,
    credit_ledger.ref_type,
    credit_ledger.ref_id,
    credit_ledger.note,
    credit_ledger.created_at,
    credit_ledger.pool
   FROM public.credit_ledger;

