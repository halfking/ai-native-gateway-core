// requestRegistry.ts — request registry view
export default {
  title: 'Request Registry',
  subtitle: 'سجل بثلاث حالات (قيد الانتظار / قيد التنفيذ / مكتمل): دورة حياة حيّة ونافذة ساخنة',
  sseConnected: 'Live stream connected',
  sseReconnecting: 'SSE reconnecting…',
  apiDegraded: 'فشل جلب واجهة الطابور، ويُعرض البديل عبر SSE',
  scopeAll: 'Super admin global ingress view',
  historyNote: 'القائمة هي النافذة الساخنة (نحو 100 عنصر)، والبحث بالمعرّف يعود إلى التفاصيل في PostgreSQL',
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
