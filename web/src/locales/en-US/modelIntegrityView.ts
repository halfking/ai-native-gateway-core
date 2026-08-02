// modelIntegrityView.ts — Model integrity monitor page (en-US).
export default {
  pageTitle: 'Model Integrity Monitor',
  pageSubtitle: 'Track model substitution, response truncation, empty responses, repeated content and fingerprint drift.',

  tabs: {
    events: 'Integrity Events',
    drift: 'Fingerprint Drift',
  },

  stats: {
    total: 'Total Events',
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
    severity: 'Severity',
    unresolvedOnly: 'Unresolved Only',
    query: 'Query',
    refresh: 'Refresh',
  },

  anomalyType: {
    all: 'All Anomaly Types',
    model_mismatch: 'Model Mismatch',
    finish_refusal: 'Refusal / Content Filter',
    finish_truncation: 'Finish Truncation',
    token_arith_fail: 'Token Arithmetic Fail',
    empty_response: 'Empty Response',
    repeated_content: 'Repeated Content',
    fingerprint_drift: 'Fingerprint Drift',
  },

  anomalyTypeDescription: {
    model_mismatch: 'Upstream returned a model that does not match the requested one (possible silent substitution)',
    finish_refusal: 'Upstream returned refusal or content_filter',
    finish_truncation: 'Truncated by length / max_tokens',
    token_arith_fail: 'prompt + completion does not equal total',
    empty_response: 'Stream produced no content and no tokens',
    repeated_content: 'Response text contains large repeated blocks (possible model loop)',
    fingerprint_drift: 'system_fingerprint drifted from baseline',
  },

  severity: {
    all: 'All Severities',
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
    actual: 'Actual',
    status: 'Status',
    actions: 'Actions',
    loading: 'Loading...',
    noData: 'No integrity events found',
    viewDetail: 'Details',
  },

  drift: {
    days: 'Days',
    query: 'Query',
    noData: 'No fingerprint drift detected in the selected window',
  },

  pager: {
    prev: 'Previous',
    next: 'Next',
    summary: 'Page {page} / {totalPages}, {total} records',
  },

  detail: {
    title: 'Integrity Event Details',
    close: 'Close',
    requestId: 'Request ID',
    detectedAt: 'Detected At',
    provider: 'Provider',
    model: 'Model',
    outboundModel: 'Outbound Model',
    credential: 'Credential ID',
    expected: 'Expected',
    actual: 'Actual',
    context: 'Context',
    sample: 'Sample',
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
    driftLoadFailed: 'Failed to load fingerprint drift',
    markFailed: 'Failed to mark',
    needSuperAdmin: 'Super admin permission required',
  },
}
