// formatAnomaliesView.ts — 格式異常監控頁（zh-TW）。
export default {
  pageTitle: '格式異常監控',
  pageSubtitle: '快速檢視供應商回應格式變化、Token 擷取失敗和相容性問題。',

  stats: {
    total: '總異常數',
    unresolved: '未解決',
    critical: '嚴重異常',
    window: '統計視窗',
  },

  filter: {
    provider: 'Provider',
    providerPlaceholder: '選擇供應商…',
    model: '模型',
    modelPlaceholder: '選擇模型…',
    anomalyType: '異常類型',
    anomalyTypePlaceholder: '選擇異常類型…',
    unresolvedOnly: '僅未解決',
    query: '查詢',
    refresh: '重新整理',
  },

  anomalyType: {
    all: '全部異常類型',
    missing_usage_block: '遺失 Usage 區塊',
    zero_completion_tokens: 'Completion Tokens 為 0',
    extraction_failed: '擷取失敗',
    unexpected_structure: '非預期結構',
    null_usage_values: 'Usage 值為 Null',
    token_mismatch: 'Token 不匹配',
    missing_provider_tokens: '遺失 Provider Tokens',
    missing_client_tokens: '遺失 Client Tokens',
    json_parse_error: 'JSON 解析錯誤',
    missing_finish_reason: '遺失 Finish Reason',
    missing_content: '遺失 Content',
  },

  anomalyTypeDescription: {
    missing_usage_block: '上游回應遺失 usage 區塊',
    zero_completion_tokens: '回應有內容但 completion_tokens 為 0',
    extraction_failed: '無法從回應中擷取可用 usage 資訊',
    unexpected_structure: '上游回傳結構與預期不一致',
    null_usage_values: 'usage 中欄位存在但值為空',
  },

  severity: {
    critical: '嚴重',
    high: '高',
    medium: '中',
    low: '低',
  },

  status: {
    resolved: '已解決',
    unresolved: '未解決',
  },

  table: {
    detectedAt: '偵測時間',
    severity: '級別',
    anomalyType: '異常類型',
    providerModel: 'Provider / 模型',
    requestId: 'Request ID',
    tokenInfo: 'Token 資訊',
    status: '狀態',
    actions: '操作',
    loading: '載入中...',
    noData: '沒有找到異常記錄',
    viewDetail: '詳情',
    expectedTokens: '預期: {count}',
    actualTokens: '實際: {count}',
  },

  token: {
    expected: '預期',
    actual: '實際',
  },

  pager: {
    prev: '上一頁',
    next: '下一頁',
    summary: '第 {page} / {totalPages} 頁，共 {total} 條',
  },

  detail: {
    title: '異常詳情',
    close: '關閉',
    requestId: 'Request ID',
    detectedAt: '偵測時間',
    provider: 'Provider',
    model: '模型',
    outboundModel: '出站模型',
    usageSource: 'Usage Source',
    responseStructure: '回應結構',
    responseSample: '回應樣本',
    resolutionNotes: '解決說明',
    resolutionNotesPlaceholder: '記錄修復說明，方便後續追蹤',
    markResolved: '標記為已解決',
    processing: '處理中...',
    resolutionInfo: '解決資訊',
    noNotes: '無解決說明',
  },

  error: {
    loadFailed: '載入失敗',
    summaryLoadFailed: '統計載入失敗',
    markFailed: '標記失敗',
    needSuperAdmin: '需要超級管理員權限',
  },
}
