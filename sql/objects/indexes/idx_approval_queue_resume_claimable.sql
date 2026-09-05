CREATE INDEX idx_approval_queue_resume_claimable ON public.approval_queue USING btree (resume_lease_until, created_at)
    WHERE ((status = 'approved'::text) AND (resume_state = ANY (ARRAY['idle'::text, 'running'::text, 'failed'::text])));
