// requestRegistry.ts — request registry view
export default {
  title: 'Request Registry',
  subtitle: 'Registre à trois états (en attente / en cours / terminé) : cycle de vie et fenêtre chaude',
  sseConnected: 'Live stream connected',
  sseReconnecting: 'SSE reconnecting…',
  apiDegraded: 'Échec de l\'appel à l\'API de file, affichage du repli SSE',
  scopeAll: 'Super admin global ingress view',
  historyNote: 'La liste est la fenêtre chaude (environ 100). Une recherche par ID retombe sur le détail PostgreSQL',
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
