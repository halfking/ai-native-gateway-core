// formatAnomaliesView.ts — Format anomalies monitoring page (en-US).
export default {
  pageTitle: 'Format Anomaly Monitor',
  pageSubtitle: 'Quickly view provider response format changes, token extraction failures, and compatibility issues.',

  stats: {
    total: 'Total Anomalies',
    unresolved: 'Unresolved',
    critical: 'Critical',
    window: 'Stats Window',
  },

  filter: {
    provider: 'Provider',
    providerPlaceholder: 'Select provider…',
    model: 'Model',
    modelPlaceholder: 'Select model…',
    anomalyType: 'Anomaly Type',
    anomalyTypePlaceholder: 'Select anomaly type…',
    unresolvedOnly: 'Unresolved Only',
    query: 'Query',
    refresh: 'Refresh',
  },

  anomalyType: {
    all: 'All Anomaly Types',
    missing_usage_block: 'Missing Usage Block',
    zero_completion_tokens: 'Zero Completion Tokens',
    extraction_failed: 'Extraction Failed',
    unexpected_structure: 'Unexpected Structure',
    null_usage_values: 'Null Usage Values',
    token_mismatch: 'Token Mismatch',
    missing_provider_tokens: 'Missing Provider Tokens',
    missing_client_tokens: 'Missing Client Tokens',
    json_parse_error: 'JSON Parse Error',
    missing_finish_reason: 'Missing Finish Reason',
    missing_content: 'Missing Content',
  },

  anomalyTypeDescription: {
    missing_usage_block: 'Upstream response missing usage block',
    zero_completion_tokens: 'Response has content but completion_tokens is 0',
    extraction_failed: 'Unable to extract usable usage info from response',
    unexpected_structure: 'Upstream returned structure inconsistent with expected',
    null_usage_values: 'Usage fields exist but values are null',
  },

  severity: {
    critical: 'Critical',
    high: 'High',
    medium: 'Medium',
    low: 'Low',
  },

  status: {
    resolved: 'Resolved',
    unresolved: 'Unresolved',
  },

  table: {
    detectedAt: 'Detected At',
    severity: 'Severity',
    anomalyType: 'Anomaly Type',
    providerModel: 'Provider / Model',
    requestId: 'Request ID',
    tokenInfo: 'Token Info',
    status: 'Status',
    actions: 'Actions',
    loading: 'Loading...',
    noData: 'No anomaly records found',
    viewDetail: 'Details',
    expectedTokens: 'Expected: {count}',
    actualTokens: 'Actual: {count}',
  },

  token: {
    expected: 'Expected',
    actual: 'Actual',
  },

  pager: {
    prev: 'Previous',
    next: 'Next',
    summary: 'Page {page} / {totalPages}, {total} records',
  },

  detail: {
    title: 'Anomaly Details',
    close: 'Close',
    requestId: 'Request ID',
    detectedAt: 'Detected At',
    provider: 'Provider',
    model: 'Model',
    outboundModel: 'Outbound Model',
    usageSource: 'Usage Source',
    responseStructure: 'Response Structure',
    responseSample: 'Response Sample',
    resolutionNotes: 'Resolution Notes',
    resolutionNotesPlaceholder: 'Record fix notes for future tracking',
    markResolved: 'Mark as Resolved',
    processing: 'Processing...',
    resolutionInfo: 'Resolution Info',
    noNotes: 'No resolution notes',
  },

  error: {
    loadFailed: 'Failed to load',
    summaryLoadFailed: 'Failed to load stats',
    markFailed: 'Failed to mark',
    needSuperAdmin: 'Super admin permission required',
  },
}
