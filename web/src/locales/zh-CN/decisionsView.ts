// decisionsView.ts — 路由决策日志页面文案
export default {
  title: '路由决策日志',
  autoRefresh: '每 5 秒自动刷新',
  
  filter: {
    status: '状态',
    statusAll: '全部',
    statusSuccess: '成功',
    statusFailed: '失败',
    timeRange: '时间范围',
    time10m: '10分钟',
    time30m: '30分钟',
    time1h: '1小时',
    time6h: '6小时',
    time24h: '24小时',
    limit: '条数',
    limit20: '20条',
    limit50: '50条',
    limit100: '100条',
    limit200: '200条',
    refresh: '刷新',
    totalCount: '共 {n} 条',
    modelLabel: '模型（可选）',
    modelPlaceholder: '选择模型…',
    modelTitle: '筛选路由决策模型',
  },

  pagination: {
    summary: '共 <strong>{total}</strong> 条，当前 {start} - {end}',
    prev: '← 上一页',
    next: '下一页 →',
  },

  table: {
    time: '时间',
    status: '状态',
    model: '模型',
    interpretation: '解析',
    usage: 'Usage',
    latency: '延迟',
    provider: '供应商',
    outboundModel: '出站模型',
    cost: '费用',
    candidateChain: '候选链',
    blockReason: '拦截原因',
    error: '错误',
    loading: '加载中…',
    noData: '暂无决策记录',
  },

  status: {
    success: '成功',
    failed: '失败',
  },

  loading: '加载中…',
  error: {
    loadFailed: '加载失败',
  },
}
