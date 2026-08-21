// requestRegistry.ts — T9 请求注册表视图文案
export default {
  title: '请求注册表',
  subtitle: '三态注册表（pending / in_flight / completed）：实时请求 lifecycle + 历史快照',
  sseConnected: '实时连接正常',
  sseReconnecting: 'SSE 重连中…',
  apiDegraded: '队列 API 拉取失败，正在以 SSE 实时数据降级展示',
  scopeAll: '超级管理员全量入站视图',
  sections: {
    pending: '待请求',
    inFlight: '正在请求',
    completed: '已完成',
  },
  status: {
    pending: '待请求',
    inFlight: '正在请求',
    completed: '已完成',
  },
  outcome: {
    success: '成功',
    failure: '失败',
    canceled: '已取消',
  },
  retryAt: '下次重试 {time}（{delta}）',
  detail: {
    requestId: '请求 ID',
    outcome: '结果',
    error: '错误',
    http: 'HTTP',
    attempt: '尝试',
    finishedAt: '完成时间',
  },
  empty: {
    pending: '暂无待请求',
    inFlight: '暂无正在请求',
    completed: '暂无已完成请求',
  },
}