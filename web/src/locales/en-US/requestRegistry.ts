// requestRegistry.ts — T9 request registry view
export default {
  title: 'Request Registry',
  subtitle: 'Three-state registry (pending / in_flight / completed): real-time request lifecycle + historical snapshots',
  sseConnected: 'Live stream connected',
  sseReconnecting: 'SSE reconnecting…',
  apiDegraded: 'Queue API failed — falling back to SSE real-time data',
  scopeAll: 'Super-admin global ingress view',
  sections: {
    pending: 'Pending',
    inFlight: 'In flight',
    completed: 'Completed',
  },
  status: {
    pending: 'Pending',
    inFlight: 'In flight',
    completed: 'Completed',
  },
  outcome: {
    success: 'Success',
    failure: 'Failed',
    canceled: 'Canceled',
  },
  retryAt: 'Retry at {time} ({delta})',
  detail: {
    requestId: 'Request ID',
    outcome: 'Outcome',
    error: 'Error',
    http: 'HTTP',
    attempt: 'Attempt',
    finishedAt: 'Finished at',
  },
  empty: {
    pending: 'No pending requests',
    inFlight: 'No in-flight requests',
    completed: 'No completed requests',
  },
}