// requestRegistry.ts — request registry view
export default {
  title: 'Request Registry',
  subtitle: '三態登錄表（待處理 / 執行中 / 已完成）：即時生命週期 + 熱窗',
  sseConnected: 'Live stream connected',
  sseReconnecting: 'SSE reconnecting…',
  apiDegraded: '佇列 API 取用失敗，改顯示 SSE 備援資料',
  scopeAll: 'Super admin global ingress view',
  historyNote: '清單是熱窗（約 100 筆）；以 ID 查詢會回退到 PostgreSQL 明細',
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
