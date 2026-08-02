// modelIntegrityView.ts — 模型完整性監控頁（zh-TW）。
export default {
  pageTitle: '模型完整性監控',
  pageSubtitle: '追蹤模型替換、回應截斷、空回應、重複內容和指紋漂移。',

  tabs: {
    events: '完整性事件',
    drift: '指紋漂移',
  },

  stats: {
    total: '總事件數',
    unresolved: '未解決',
    critical: '嚴重',
    window: '統計視窗',
  },

  filter: {
    provider: 'Provider',
    providerPlaceholder: '選擇供應商…',
    model: '模型',
    modelPlaceholder: '選擇模型…',
    anomalyType: '異常類型',
    anomalyTypePlaceholder: '選擇異常類型…',
    severity: '級別',
    unresolvedOnly: '僅未解決',
    query: '查詢',
    refresh: '重新整理',
  },

  anomalyType: {
    all: '全部異常類型',
    model_mismatch: '模型不匹配',
    finish_refusal: '拒絕 / 內容篩選',
    finish_truncation: '結束截斷',
    token_arith_fail: 'Token 運算失敗',
    empty_response: '空回應',
    repeated_content: '重複內容',
    fingerprint_drift: '指紋漂移',
  },

  anomalyTypeDescription: {
    model_mismatch: '上游回傳的模型與請求的模型不符（可能為靜默替換）',
    finish_refusal: '上游回傳拒絕或 content_filter',
    finish_truncation: '因長度 / max_tokens 而截斷',
    token_arith_fail: 'prompt + completion 不等於 total',
    empty_response: '串流未產生任何內容與權杖',
    repeated_content: '回應文字包含大量重複區塊（可能為模型迴圈）',
    fingerprint_drift: 'system_fingerprint 與基準相比發生漂移',
  },

  severity: {
    all: '全部級別',
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
    actual: '實際',
    status: '狀態',
    actions: '操作',
    loading: '載入中...',
    noData: '沒有找到完整性事件',
    viewDetail: '詳情',
  },

  drift: {
    days: '天',
    query: '查詢',
    noData: '所選視窗內未偵測到指紋漂移',
  },

  pager: {
    prev: '上一頁',
    next: '下一頁',
    summary: '第 {page} / {totalPages} 頁，共 {total} 條',
  },

  detail: {
    title: '完整性事件詳情',
    close: '關閉',
    requestId: 'Request ID',
    detectedAt: '偵測時間',
    provider: 'Provider',
    model: '模型',
    outboundModel: '出站模型',
    credential: '憑證 ID',
    expected: '預期',
    actual: '實際',
    context: '上下文',
    sample: '樣本',
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
    driftLoadFailed: '指紋漂移載入失敗',
    markFailed: '標記失敗',
    needSuperAdmin: '需要超級管理員權限',
  },
}
