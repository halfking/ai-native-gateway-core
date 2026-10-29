// modelCredibilityView.ts — Model credibility (anti-downgrade) page (en-US).
export default {
  pageTitle: 'Model credibility',
  pageSubtitle:
    'Detects whether an upstream is passing off a smaller model as a bigger one ' +
    '(downgrade / swap / dilution). The verdict rests on four independent measurements: the model ' +
    'the upstream self-reports, capability compared against other nodes serving the same model, ' +
    'whether the request signature stays stable, and an external baseline. When nothing can be ' +
    'concluded the result reads "insufficient evidence", never "no problem".',

  stats: {
    total: 'Judged nodes',
    verified: 'Verified',
    suspected: 'Suspected',
    diluted: 'Diluted',
    unknown: 'Insufficient evidence',
    providers: 'Providers involved',
  },

  filter: {
    verdict: 'Verdict',
    verdictPlaceholder: 'All verdicts',
    model: 'Model',
    modelPlaceholder: 'Filter by model name',
    query: 'Query',
    refresh: 'Refresh',
    scanAll: 'Scan all',
    scanning: 'Scanning…',
    sort: 'Sort',
    sortTime: 'By measured time (newest first)',
    sortName: 'By model name',
    sortScore: 'By credibility (lowest first)',
  },

  verdict: {
    verified: 'Verified',
    suspected: 'Suspected',
    diluted: 'Diluted',
    unknown: 'Insufficient evidence',
  },

  dimension: {
    identity: 'Identity',
    capability: 'Capability',
    consistency: 'Fingerprint consistency',
    reference: 'External baseline',
  },

  table: {
    provider: 'Provider',
    credential: 'Credential',
    model: 'Model',
    credibility: 'Credibility',
    verdict: 'Verdict',
    dimensions: 'Evidence dimensions',
    measuredAt: 'Measured at',
    action: 'Actions',
  },

  status: {
    success: 'OK',
    partial: 'Partial',
    failed: 'Probe failed',
    inconclusive: 'Inconclusive',
    unknown: 'Not measured',
  },

  anomaly: {
    identity_model_name_mismatch: 'Upstream reports a different model than requested',
    identity_family_mismatch: 'Upstream reports a model from a different vendor',
    identity_reported_model_absent: 'Upstream returns no model name, so identity cannot be self-attested',
    capability_below_peer_best: 'Capability is below the best node serving the same model',
    capability_severely_below_peer_best: 'Capability is far below the best node serving the same model',
    capability_above_peer_best: 'Capability is above the best node serving the same model',
    consistency_prompt_token_drift: 'Request signature drifted; the backend may have been swapped',
    consistency_system_fingerprint_drift: 'The upstream fingerprint changed',
    reference_below_expected_accuracy: 'Below the level the external baseline expects',
    capability_no_measurement: 'No usable capability measurement available',
    capability_peer_sample_too_small: 'Too few comparison nodes for this model',
    identity_probe_failed: 'The identity probe request failed',
    identity_probe_empty_response: 'The identity probe got no answer',
  },

  detail: {
    title: 'Verdict detail',
    evidence: 'Evidence',
    anomalies: 'Anomalies',
    history: 'Verdict history',
    noHistory: 'No verdict history yet',
    requestedModel: 'Requested model',
    reportedModel: 'Model reported by upstream',
    promptSignature: 'Request signature (prompt token count)',
    fingerprint: 'Upstream fingerprint',
    measuredDimensions: 'Dimensions with data this round',
    close: 'Close',
  },

  empty: {
    noData: 'No credibility verdicts yet.',
    hint: 'Verdicts come from the periodic background scan (one tiny probe request per node per round); you can also press "Scan all" to run a round now.',
  },

  error: {
    loadFailed: 'The model credibility data could not be loaded. Please retry.',
    scanFailed: 'The model credibility scan could not be completed. Please retry.',
    detailFailed: 'The model credibility verdict detail could not be loaded. Please retry.',
  },
}
