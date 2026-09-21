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
    actions: '操作',
    loading: '加载中...',
    noData: '没有找到异常记录',
    viewDetail: '详情',
    expectedTokens: '预期: {count}',
    actualTokens: '实际: {count}',
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

  // 2026-09-21: 请求错误 tab（reqprobe）
  tabs: {
      format: '响应格式异常',
      request: '请求错误',
  },
  requestTab: {
      stats: {
          unresolved: '未解决（本页）',
          autoRecovered: '自动恢复成功',
          total: '总数',
      },
      filter: {
          day: '日期',
          trigger: '类型',
          triggerPlaceholder: '选择类型…',
      },
      trigger: {
          all: '全部类型',
          param_rejected: '参数被拒',
          mode_mismatch: '请求形态不匹配',
          upstream_error: '上游错误',
      },
      triggerDesc: {
          param_rejected: '上游拒绝了请求参数（如 reasoning_effort），网关已自动剔除并重试',
          mode_mismatch: '请求的 API 形态（responses / chat）与供应商不符',
          upstream_error: '无法归类的请求侧 4xx，待人工分类',
      },
      batch: {
          selected: '已选 {n} 项',
          resolveSelected: '解决已选',
          resolveFiltered: '一键解决全部未解决',
      },
      table: {
          day: '日期',
          trigger: '类型',
          param: '参数 / 建议',
          status: '状态码',
          occurrences: '次数',
          recoveredCount: '自愈 {n} 次',
      },
      detail: {
          title: '请求错误详情',
          clientModel: '客户端模型',
          protocol: '出站协议',
          suggestMode: '建议形态',
          firstSeen: '首次出现',
          lastSeen: '最近出现',
          errorSample: '上游错误样例',
          occurrences: '出现统计',
          recoveredHint: '其中 {n} 次通过剔除参数 / 切换形态自动恢复',
      },
  },
}
