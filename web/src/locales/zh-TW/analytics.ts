// analytics.ts — 營運看板詳情分析系統文案（頂層 analytics 命名空間）。
// 2026-11-21 審計輪：元件引用 t('analytics.*')，此前 zh-CN 誤掛 dashboard 命名空間、
// 其餘語種整組缺席。zh-TW 措辭沿用 sessionAnalytics 既有詞彙（AI 模型/成功比例/費用），
// 避免與 zh-CN 繁簡同形撞棘輪。
export default {
  dimension: {
    model: 'AI 模型',
    provider: '供應商',
    client: '用戶端',
    tenant: '租戶',
  },
  list: {
    title: '完整清單',
    models: '模型完整清單',
    providers: '供應商完整清單',
    clients: '用戶端完整清單',
    tenants: '租戶完整清單',
    subtitle: '最近 {days} 天資料',
    empty: '此時間範圍內沒有資料',
    total: '共 {count} 項',
    column: {
      name: '名稱',
      requests: '請求數',
      tokens: 'Token 數',
      cost: '費用',
      credits: '點數',
    },
  },
  detail: {
    title: '詳情分析',
    subtitle: '最近 {days} 天資料',
    summary: '核心指標',
    trends: '趨勢分析',
    crossAnalysis: '交叉維度分析',
    crossAnalysisHint: '點擊表格列可檢視該物件的詳細分析',
    degraded: '資料降級',
    degradedTrends: '趨勢資料降級',
  },
  summary: {
    totalRequests: '總請求數',
    successRate: '成功比例',
    totalTokens: '總 Token',
    totalCost: '總費用',
    totalCredits: '總點數',
    avgLatency: '平均延遲',
    success: '筆成功',
    errors: '筆失敗',
    prompt: '輸入',
    completion: '輸出',
    avgPerRequest: '平均 {value}/請求',
  },
  trend: {
    empty: '此時間範圍內沒有趨勢資料',
  },
  cross: {
    byModel: '依模型分布',
    byProvider: '依供應商分布',
    byClient: '依用戶端分布',
    byTenant: '依租戶分布',
    byError: '依錯誤類型分布',
    byStatus: '依狀態分布',
    empty: '沒有可用的交叉分析資料',
    noData: '該維度暫無資料',
    degraded: '資料降級',
    showingTop: '顯示前 {count} 項，共 {total} 項',
    column: {
      name: '名稱',
      requests: '請求數',
      tokens: 'Token 數',
      cost: '費用',
      credits: '點數',
    },
  },
}
