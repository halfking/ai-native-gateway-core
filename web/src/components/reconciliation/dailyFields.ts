import type { DailySection } from '../../api/requestReconciliation'

interface DailyFields {
  summary: readonly string[]
  columns: readonly string[]
  count: string
}

export const dailyFields: Record<DailySection, DailyFields> = {
  requests: {
    count: 'requests',
    summary: ['requests', 'requests_with_terminal_projection', 'success', 'failure', 'rate_limited', 'cancelled', 'pending', 'unknown_status', 'admission_rejected_requests', 'unassigned_session', 'total_tokens', 'measured_total_requests'],
    columns: ['local_day', 'request_id', 'tenant_id', 'session_id', 'traffic_class', 'status', 'projected_status', 'admission_rejection', 'input_tokens', 'output_tokens', 'total_tokens', 'usage_source', 'recorded_supplier_sends'],
  },
  attempts: {
    count: 'recorded_attempts',
    summary: ['recorded_attempts', 'sent', 'business_sent', 'platform_sent', 'unassigned_sent', 'unknown_delivery', 'not_sent', 'attempts_with_contract_estimate', 'attempts_with_reported_amount'],
    columns: ['local_day', 'attempt_id', 'request_id', 'parent_request_id', 'provider_id', 'credential_id', 'model', 'traffic_class', 'delivery', 'outcome', 'provider_request_id', 'estimated_amount', 'estimated_currency', 'estimate_basis', 'supplier_contract_reference', 'reported_amount', 'reported_currency', 'reported_semantics', 'audio_input_duration_us', 'audio_billed_duration_us', 'audio_output_duration_us', 'generated_images', 'video_output_duration_us', 'media_usage_source'],
  },
  sessions: {
    count: 'sessions',
    summary: ['sessions', 'active', 'new', 'closed'],
    columns: ['tenant_id', 'session_id', 'window_requests', 'active', 'first_request_at', 'closed_at'],
  },
  postings: {
    count: 'postings',
    summary: ['postings', 'linked_requests', 'linked_client_requests', 'net_credits', 'internal_amount', 'internal_currency'],
    columns: ['local_day', 'tenant_id', 'posting_id', 'request_id', 'parent_request_id', 'entry_type', 'pool', 'net_credits', 'internal_amount', 'internal_currency'],
  },
  coverage: {
    count: '',
    summary: ['received_requests', 'orchestration_child_receipts', 'orchestration_dispatches_unlinked', 'terminal_usage_without_retained_receipt',
      'legacy_usage_request_keys', 'legacy_usage_without_retained_receipt', 'legacy_usage_without_retained_terminal_usage',
      'recorded_sends', 'recorded_sends_without_contract_estimate', 'recorded_sends_without_reported_amount', 'recorded_sends_reported_amount_without_currency'],
    columns: ['local_day', 'received_requests', 'orchestration_child_receipts', 'orchestration_dispatches_registered', 'orchestration_dispatches_unlinked',
      'receipts_without_retained_terminal_usage', 'terminal_usage_requests', 'terminal_usage_without_retained_receipt', 'terminal_usage_receipt_outside_window',
      'legacy_usage_request_keys', 'legacy_usage_rows', 'legacy_usage_without_retained_receipt', 'legacy_usage_receipt_other_day', 'legacy_usage_without_retained_terminal_usage',
      'posted_entries', 'postings_without_retained_receipt', 'recorded_sends', 'recorded_sends_without_contract_estimate', 'recorded_sends_without_reported_amount',
      'recorded_sends_reported_amount_without_currency', 'recorded_media_sends', 'recorded_media_sends_without_quantity'],
  },
  internal: {
    count: 'tenants_with_evidence',
    summary: ['recorded_gross_debit_credits', 'recorded_net_credits',
      'tenants_with_bucket_debit_difference', 'tenants_with_hourly_bucket_gaps'],
    columns: ['tenant_id', 'recorded_gross_debit_credits', 'recorded_net_credits', 'bucket_credits',
      'boundary_buckets', 'unaligned_hour_buckets', 'ledger_hours_without_bucket', 'bucket_hours_without_debits',
      'hours_with_bucket_debit_difference', 'bucket_minus_recorded_debit_credits'],
  },
}
