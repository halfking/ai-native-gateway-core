// requestRegistry.ts — request registry view
export default {
  title: 'Request Registry',
  subtitle: 'Register mit drei Zuständen (ausstehend / in Arbeit / abgeschlossen): Lebenszyklus und heißes Fenster',
  sseConnected: 'Live stream connected',
  sseReconnecting: 'SSE reconnecting…',
  apiDegraded: 'Abruf der Queue-API fehlgeschlagen, SSE-Ersatzdaten werden angezeigt',
  scopeAll: 'Super admin global ingress view',
  historyNote: 'Die Liste ist das heiße Fenster (ca. 100). Eine Suche per ID greift auf die PostgreSQL-Details zu',
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
