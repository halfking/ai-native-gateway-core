// formatAnomaliesView.ts — 格式异常监控页(FormatAnomaliesView)文案。
export default {
  pageTitle: '格式异常监控',
  pageSubtitle: '快速查看供应商响应格式变化、Token 提取失败和兼容性问题。',

  stats: {
    total: '总异常数',
    unresolved: '未解决',
    critical: '严重异常',
    window: '统计窗口',
  },

  filter: {
    provider: 'Provider',
    providerPlaceholder: '选择供应商…',
    model: '模型',
    modelPlaceholder: '选择模型…',
    anomalyType: '异常类型',
    anomalyTypePlaceholder: '选择异常类型…',
    unresolvedOnly: '仅未解决',
    query: '查询',
    refresh: '刷新',
  },

  anomalyType: {
    all: '全部异常类型',
    missing_usage_block: '缺失 Usage 块',
    zero_completion_tokens: 'Completion Tokens 为 0',
    extraction_failed: '提取失败',
    unexpected_structure: '非预期结构',
    null_usage_values: 'Usage 值为 Null',
  },

  anomalyTypeDescription: {
    missing_usage_block: '上游响应缺失 usage 块',
    zero_completion_tokens: '响应有内容但 completion_tokens 为 0',
    extraction_failed: '无法从响应中提取可用 usage 信息',
    unexpected_structure: '上游返回结构与预期不一致',
    null_usage_values: 'usage 中字段存在但值为空',
  },

  severity: {
    low: '低',
    medium: '中',
    high: '高',
    critical: '严重',
  },

  table: {
    detectedAt: '检测时间',
    severity: '级别',
    anomalyType: '异常类型',
    providerModel: 'Provider / 模型',
    requestId: 'Request ID',
    tokenInfo: 'Token 信息',
    status: '状态',
    detail: '详情',
    loading: '加载中...',
    empty: '没有找到异常记录',
  },

  token: {
    expected: '预期',
    actual: '实际',
  },

  status: {
    resolved: '已解决',
    unresolved: '未解决',
  },

  pager: {
    prev: '上一页',
    next: '下一页',
    summary: '第 {page} / {totalPages} 页，共 {total} 条',
  },

  detail: {
    title: '异常详情',
    close: '关闭',
    requestId: 'Request ID',
    detectedAt: '检测时间',
    provider: 'Provider',
    model: '模型',
    outboundModel: '出站模型',
    usageSource: 'Usage Source',
    responseStructure: '响应结构',
    responseSample: '响应样本',
    resolutionNotes: '解决说明',
    resolutionNotesPlaceholder: '记录修复说明，方便后续追踪',
    markResolved: '标记为已解决',
    processing: '处理中...',
    resolutionInfo: '解决信息',
    noNotes: '无解决说明',
  },

  error: {
    loadFailed: '加载失败',
    summaryLoadFailed: '统计加载失败',
    markFailed: '标记失败',
    needSuperAdmin: '需要超级管理员权限',
  },
}