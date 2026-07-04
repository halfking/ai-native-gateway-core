// Chinese (Traditional) locale for MemoraStatusButton
export default {
  state: {
    disabled: '未啟用',
    paused: '已中斷',
    ok: '已連線',
    error: '連線失敗',
    loading: '檢測中',
  },
  
  panel: {
    title: 'Memora 連線',
    closeLabel: '關閉',
    
    fields: {
      serviceUrl: '服務位址',
      recentLatency: '最近延遲',
      error: '錯誤',
      check: '檢測',
      writeQueue: '寫入佇列',
      processedErrored: '已處理 / 失敗',
      consecutiveErrors: '連續失敗',
      recentWriteError: '最近寫入錯誤',
    },
    
    actions: {
      processing: '處理中…',
      reconnect: '重新連線',
      checkConnectivity: '檢測連通性',
      disconnect: '中斷寫入',
      refresh: '重新整理',
      sessionContext: '會話上下文',
    },
  },
  
  error: {
    connectionFailed: '連線失敗',
    checkFailed: '檢測失敗',
  },
}
