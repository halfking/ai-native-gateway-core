// Chinese (Simplified) locale for MemoraStatusButton
export default {
  state: {
    disabled: '未启用',
    paused: '已断开',
    ok: '已连接',
    error: '连接失败',
    loading: '检测中',
  },
  
  panel: {
    title: 'Memora 连接',
    closeLabel: '关闭',
    
    fields: {
      serviceUrl: '服务地址',
      recentLatency: '最近延迟',
      error: '错误',
      check: '检测',
      writeQueue: '写入队列',
      processedErrored: '已处理 / 失败',
      consecutiveErrors: '连续失败',
      recentWriteError: '最近写入错误',
    },
    
    actions: {
      processing: '处理中…',
      reconnect: '重连',
      checkConnectivity: '检测连通性',
      disconnect: '断开写入',
      refresh: '刷新',
      sessionContext: '会话上下文',
    },
  },
  
  error: {
    connectionFailed: '连接失败',
    checkFailed: '检测失败',
  },
}
