// usageTrend.ts — 全页用量趋势分析（2026-10-02 看板轮；8 语言同步）。
export default {
  pageTitle: '用量趨勢分析',
  pageSub: '按模型拆分的用量變化；支援供應商 / 租戶 / API Key / 模型 / 指標過濾',
  loadFailed: '載入失敗',
  detailTimeoutHint: '按 API Key 過濾時會掃描請求明細，大時間範圍可能逾時；請縮短時間範圍（如 7 天內）後重試',
  filterProvider: '供應商',
  filterTenant: '租戶',
  filterApiKey: 'API Key',
  filterModel: '模型',
  filterMetric: '指標',
  filterAll: '全部',
  shareByMetric: '當前指標佔比',
  sourceDetail: '請求明細',
  // 2026-10-02 多選/清除全部/自動重新整理
  clearAll: '清除全部',
  autoRefresh: '自動重新整理',
  autoRefreshOff: '關',
  autoRefresh30s: '30 秒',
  autoRefresh1m: '1 分鐘',
  autoRefresh5m: '5 分鐘',
}