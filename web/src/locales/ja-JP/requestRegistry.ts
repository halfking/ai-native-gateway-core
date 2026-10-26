// requestRegistry.ts — request registry view
export default {
  title: 'Request Registry',
  subtitle: '3 状態の台帳（待機中 / 実行中 / 完了）：ライフサイクルとホットウィンドウ',
  sseConnected: 'Live stream connected',
  sseReconnecting: 'SSE reconnecting…',
  apiDegraded: 'キュー API の取得に失敗しました。SSE のフォールバックを表示します',
  scopeAll: 'Super admin global ingress view',
  historyNote: 'リストはホットウィンドウ（約 100 件）。ID 検索は PostgreSQL の詳細にフォールバックします',
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
