// formatAnomaliesView.ts — Format anomalies monitoring page.
export default {
  pageTitle: 'Format anomalies',
  pageSubtitle: 'Quickly inspect provider response format changes, token extraction failures, and compatibility issues.',

  stats: {
    total: 'Total anomalies',
    unresolved: 'Unresolved',
    critical: 'Critical',
    window: 'Window',
  },

  filter: {
    provider: 'Provider',
    providerPlaceholder: 'Select provider…',
    model: 'Model',
    modelPlaceholder: 'Select model…',
    anomalyType: 'Anomaly type',
    anomalyTypePlaceholder: 'Select anomaly type…',
    unresolvedOnly: 'Unresolved only',
    query: 'Query',
    refresh: 'Refresh',
  },

  anomalyType: {
    all: 'All anomaly types',
    missing_usage_block: 'Missing usage block',
    zero_completion_tokens: 'Completion tokens = 0',
    extraction_failed: 'Extraction failed',
    unexpected_structure: 'Unexpected structure',
    null_usage_values: 'Null usage values',
  },

  anomalyTypeDescription: {
    missing_usage_block: 'Upstream response is missing the usage block',
    zero_completion_tokens: 'Response has content but completion_tokens is 0',
    extraction_failed: 'Cannot extract usable usage information from response',
    unexpected_structure: 'Upstream structure does not match expectations',
    null_usage_values: 'Usage fields exist but values are null',
  },

  severity: {
    low: 'Low',
    medium: 'Medium',
    high: 'High',
    critical: 'Critical',
  },

  table: {
    detectedAt: 'Detected at',
    severity: 'Severity',
    anomalyType: 'Anomaly type',
    providerModel: 'Provider / Model',
    requestId: 'Request ID',
    tokenInfo: 'Token info',
    status: 'Status',
    detail: 'Detail',
    loading: 'Loading...',
    empty: 'No anomaly records found',
  },

  token: {
    expected: 'Expected',
    actual: 'Actual',
  },

  status: {
    resolved: 'Resolved',
    unresolved: 'Unresolved',
  },

  pager: {
    prev: 'Previous',
    next: 'Next',
    summary: 'Page {page} / {totalPages}, total {total}',
  },

  detail: {
    title: 'Anomaly detail',
    close: 'Close',
    requestId: 'Request ID',
    detectedAt: 'Detected at',
    provider: 'Provider',
    model: 'Model',
    outboundModel: 'Outbound model',
    usageSource: 'Usage source',
    responseStructure: 'Response structure',
    responseSample: 'Response sample',
    resolutionNotes: 'Resolution notes',
    resolutionNotesPlaceholder: 'Document the fix for future tracking',
    markResolved: 'Mark as resolved',
    processing: 'Processing...',
    resolutionInfo: 'Resolution info',
    noNotes: 'No resolution notes',
  },

  error: {
    loadFailed: 'Failed to load',
    summaryLoadFailed: 'Failed to load summary',
    markFailed: 'Failed to mark',
    needSuperAdmin: 'Super admin permission required',
  },
}