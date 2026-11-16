// liveStageTree.ts — Request stage tree (live2) copy (2026-11-16)
export default {
  title: 'Request stage tree',
  empty: 'No requests in the window under the current filters',
  stage: {
    auth: 'Auth / rate limit',
    sanitize: 'Redaction',
    approval: 'Approval',
    injection: 'Injection check',
    route: 'Routing',
    compress: 'Compression',
    firstByte: 'First byte',
    restore: 'Redaction restore',
    provider: 'Provider / token pool',
    meter: 'Metering',
    reply: 'Reply',
  },
  state: {
    pending: 'Not reached',
    active: 'In progress',
    done: 'Done',
    skipped: 'Not applicable',
    failed: 'Failed',
    warned: 'Warning',
    unmeasured: 'Not measured',
  },
  value: {
    sanitize: '{n} spot(s)',
    route: '{n} candidate(s)',
    meter: '{n} tok',
    injection: 'score {n} ({d})',
    compress: {
      yes: 'Compressed',
      no: 'Not compressed',
    },
  },
  decision: {
    pass: 'Passed',
    need_approval: 'Awaiting approval',
    block: 'Blocked',
    safe: 'Safe',
    warn: 'Matched (warning only)',
    unknown: 'Not determined',
  },
  return: {
    lane: 'The reply travels back to the client along the return lane',
    label: 'Back to client',
  },
  band: {
    status: {
      success: 'Succeeded',
      in_progress: 'In progress',
      failure: 'Failed',
    },
    toolsRestore: 'tools restored {n}',
    toolsRestored: 'restored',
    finalModel: 'Final model',
    upstreamModel: 'Upstream model',
    finalModelTip: {
      action: 'Source: the routing node-selection event (standard model name after resolution).',
      canonical: 'Source: the standard model name recorded on the request frame.',
      upstreamRaw: 'Source: the model string actually sent upstream.',
      frame: 'Source: the model field on the request frame.',
      none: 'Not measured: no determinable model source.',
    },
    modelEntry: {
      auto: 'AUTO chose {model}',
      direct: 'Requested {model}',
    },
    chain: {
      auto: 'auto task type {task} → matched {model}',
      alias: '{from} resolved to {model}',
      switch: 'Model switch {from} → {to}',
    },
    cache: {
      hit: 'Cache hit {pct}%',
      na: 'Cache hit rate —',
      anomaly: 'Cache basis anomaly',
      tip: {
        na: 'The upstream did not report a cache hit count; this is not 0%.',
        unknown: 'The upstream protocol is undetermined, so the denominator is unknown.',
        anomaly: 'The rate exceeds 100%, which means two different bases were mixed.',
        parallel: 'Basis: cache reads sit alongside the input; denominator = input + cache reads.',
        subset: 'Basis: cache reads are already inside the input; denominator = input.',
      },
    },
  },
  credibility: {
    verified: 'Trusted',
    suspected: 'Possibly diluted',
    diluted: 'Diluted',
    unknown: 'Undetermined',
    unmeasured: 'Credibility not measured',
    fingerprintMissing: 'Not provided',
    measuredAtMissing: 'Not provided',
    tip: {
      unmeasured: 'There is no credibility measurement for this credential and upstream model pair.',
      detail:
        'Verdict {verdict} | identity {identity} | capability {capability} | stability {consistency} | reference {reference} | system fingerprint {fingerprint} | measured at {measuredAt}',
    },
  },
  stats: {
    open: 'Expand statistics (full page)',
    readout: '{total} total | {rate}% success | {inFlight} in flight',
    title: 'Live statistics',
    hint:
      'Same basis as the live topology page: the last 5 minutes and last 1 hour are read straight from the database, while the current day comes from the daily bucket (lagging by up to 1 hour).',
    close: 'Close',
  },
  idle: {
    wait: 'No new request for {ms} ms',
  },
}
