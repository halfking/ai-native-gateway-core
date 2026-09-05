// requestRegistry.ts — request registry view
export default {
  title: 'Request Registry',
  subtitle: 'Three-state registry (pending / in_flight / completed): live lifecycle + hot window',
  sseConnected: 'Live stream connected',
  sseReconnecting: 'SSE reconnecting…',
  apiDegraded: 'Queue API fetch failed; showing SSE fallback',
  scopeAll: 'Super admin global ingress view',
  historyNote: 'List is hot window (~100); lookup by ID falls back to PostgreSQL detail',
  search: 'Lookup journey',
  searchPlaceholder: 'Enter request_id for full journey…',
  filterPlaceholder: 'Filter current list…',
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
    failure: 'Failure',
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
