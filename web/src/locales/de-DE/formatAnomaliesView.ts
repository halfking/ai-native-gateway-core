// formatAnomaliesView.ts — Format anomalies monitoring page.
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
    token_mismatch: 'Token Mismatch',
    missing_provider_tokens: 'Missing Provider Tokens',
    missing_client_tokens: 'Missing Client Tokens',
    json_parse_error: 'JSON Parse Error',
    missing_finish_reason: 'Missing Finish Reason',
    missing_content: 'Missing Content',
    unexpected_structure: 'Unexpected Structure',
  },

  severity: {
    critical: 'Critical',
    high: 'High',
    medium: 'Medium',
    low: 'Low',
  },

  resolved: 'Resolved',
  unresolved: 'Unresolved',
  processing: 'Processing...',
  markResolved: 'Mark as Resolved',
  noNotes: 'No resolution notes',

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

  pagination: {
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
