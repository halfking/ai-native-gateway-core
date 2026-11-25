import type { DailySection } from '@/api/dailyReconciliation'

interface EvidenceLayout {
  count: string
  key: string
  summary: readonly string[]
  rows: readonly string[]
}

export const dailyEvidenceLayouts: Record<DailySection, EvidenceLayout> = {
  coverage: {
    count: '', key: '', rows: [],
    summary: ['received_requests', 'orchestration_child_receipts', 'orchestration_dispatches_unlinked', 'terminal_usage_without_retained_receipt',
      'legacy_usage_request_keys', 'legacy_usage_without_retained_receipt', 'legacy_usage_without_retained_terminal_usage',
      'recorded_sends', 'recorded_sends_without_contract_estimate', 'recorded_sends_without_reported_amount', 'recorded_media_sends_without_quantity'],
  },
  requests: {
    count: 'requests', key: 'request_id',
    summary: ['requests', 'business_requests', 'platform_requests', 'requests_with_terminal_projection', 'success', 'failure', 'rate_limited', 'cancelled', 'pending', 'unknown_status', 'total_tokens', 'measured_total_requests'],
    rows: ['tenant_id', 'received_at', 'session_id', 'application_id', 'client_id', 'traffic_class', 'status', 'projected_status',
      'admission_rejection', 'input_tokens', 'output_tokens', 'total_tokens', 'usage_source', 'recorded_supplier_sends'],
  },
  attempts: {
    count: 'recorded_attempts', key: 'attempt_id',
    summary: ['sent', 'business_sent', 'platform_sent', 'unassigned_sent', 'unknown_delivery', 'not_sent'],
    rows: ['request_id', 'parent_request_id', 'tenant_id', 'prepared_at', 'sent_at', 'traffic_class', 'model', 'provider_id', 'credential_id',
      'delivery', 'outcome', 'provider_request_id', 'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_write_tokens',
      'estimated_amount', 'estimated_currency', 'estimate_basis', 'reported_amount', 'reported_currency', 'reported_semantics',
      'generated_images', 'audio_input_duration_us', 'audio_billed_duration_us', 'audio_output_duration_us', 'video_output_duration_us'],
  },
  sessions: {
    count: 'sessions', key: 'session_id',
    summary: ['active', 'new', 'closed'],
    rows: ['tenant_id', 'window_requests', 'active', 'new', 'closed', 'first_request_at', 'closed_at'],
  },
  postings: {
    count: 'postings', key: 'posting_id',
    summary: ['postings', 'linked_client_requests', 'net_credits'],
    rows: ['tenant_id', 'request_id', 'parent_request_id', 'posted_at', 'entry_type', 'pool', 'net_credits', 'internal_amount', 'internal_currency'],
  },
  internal: {
    count: 'tenants_with_evidence', key: 'tenant_id',
    summary: ['recorded_gross_debit_credits', 'recorded_net_credits',
      'tenants_with_bucket_debit_difference', 'tenants_with_hourly_bucket_gaps'],
    rows: ['recorded_debited_requests', 'recorded_gross_debit_credits', 'recorded_net_credits', 'bucket_credits',
      'boundary_buckets', 'unaligned_hour_buckets', 'ledger_hours_without_bucket', 'bucket_hours_without_debits',
      'hours_with_bucket_debit_difference', 'bucket_minus_recorded_debit_credits'],
  },
}

export const invoiceEvidenceLayout: EvidenceLayout = {
  count: 'invoice_lines', key: 'line_ref',
  summary: ['invoice_total', 'matched_lines', 'bucket_matched_lines', 'review_lines', 'adjustment_total',
    'estimated_minus_invoiced_amount', 'reported_minus_invoiced_amount'],
  rows: ['kind', 'model', 'provider_request_id', 'match_status', 'invoice_amount', 'invoice_currency', 'comparable_estimate', 'comparable_reported',
    'estimated_minus_invoiced_amount', 'reported_minus_invoiced_amount'],
}
