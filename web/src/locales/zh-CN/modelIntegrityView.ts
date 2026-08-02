// modelIntegrityView.ts — 模型完整性监控页 (zh-CN)。
export default {
  pageTitle: '模型完整性监控',
  pageSubtitle: '监控模型替换、响应截断、空响应、重复内容与指纹漂移等完整性异常。',

  tabs: {
    events: '完整性事件',
    drift: '指纹漂移',
  },

  stats: {
    total: '事件总数',
    unresolved: '未解决',
    critical: '严重事件',
    window: '统计窗口',
  },

  filter: {
    provider: 'Provider',
    providerPlaceholder: '选择供应商…',
    model: '模型',
    modelPlaceholder: '选择模型…',
    anomalyType: '异常类型',
    anomalyTypePlaceholder: '选择异常类型…',
    severity: '严重度',
    unresolvedOnly: '仅未解决',
    query: '查询',
    refresh: '刷新',
  },

  anomalyType: {
    all: '全部异常类型',
    model_mismatch: '模型不一致',
    finish_refusal: '拒绝/内容过滤',
    finish_truncation: '响应截断',
    token_arith_fail: 'Token 校验失败',
    empty_response: '空响应',
    repeated_content: '重复内容',
    fingerprint_drift: '指纹漂移',
  },

  anomalyTypeDescription: {
    model_mismatch: '上游返回的模型与请求模型不一致（疑似静默替换）',
    finish_refusal: '上游返回 refusal 或 content_filter',
    finish_truncation: '命中 length / max_tokens 而截断',
    token_arith_fail: 'prompt + completion 与 total 不相等',
    empty_response: '流式响应为空（无内容、无 token）',
    repeated_content: '响应文本出现大段重复（疑似模型循环）',
    fingerprint_drift: 'system_fingerprint 相对基线发生漂移',
  },

  severity: {
    all: '全部严重度',
    critical: '严重',
    high: '高',
    medium: '中',
    low: '低',
  },

  status: {
    resolved: '已解决',
    unresolved: '未解决',
  },

  table: {
    detectedAt: '检测时间',
    severity: '级别',
    anomalyType: '异常类型',
    providerModel: 'Provider / 模型',
    requestId: 'Request ID',
    actual: '实际值',
    status: '状态',
    actions: '操作',
    loading: '加载中...',
    noData: '没有找到完整性事件',
    viewDetail: '详情',
  },

  drift: {
    days: '天数',
    query: '查询',
    noData: '所选窗口内未检测到指纹漂移',
  },

  pager: {
    prev: '上一页',
    next: '下一页',
    summary: '第 {page} / {totalPages} 页，共 {total} 条',
  },

  detail: {
    title: '完整性事件详情',
    close: '关闭',
    requestId: 'Request ID',
    detectedAt: '检测时间',
    provider: 'Provider',
    model: '模型',
    outboundModel: '出站模型',
    credential: '凭证 ID',
    expected: '期望值',
    actual: '实际值',
    context: '上下文',
    sample: '样本',
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
    driftLoadFailed: '指纹漂移加载失败',
    markFailed: '标记失败',
    needSuperAdmin: '需要超级管理员权限',
  },
}
