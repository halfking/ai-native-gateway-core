// requestRegistry.ts — request registry view
export default {
  title: 'Request Registry',
  subtitle: 'Registro de tres estados (pendiente / en curso / completado): ciclo de vida y ventana caliente',
  sseConnected: 'Live stream connected',
  sseReconnecting: 'SSE reconnecting…',
  apiDegraded: 'Falló la consulta a la API de cola; se muestran los datos de respaldo por SSE',
  scopeAll: 'Super admin global ingress view',
  historyNote: 'La lista es la ventana caliente (unos 100); la búsqueda por ID recurre al detalle en PostgreSQL',
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
