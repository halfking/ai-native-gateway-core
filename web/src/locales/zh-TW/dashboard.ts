export default {
  title: "儀表板",
  refresh: "重新整理",
tabs: {
    board: '看板',
    liveStream: '即時請求流',
    sessionStats: '會話與統計',
    selfcheck: '系統自檢',
    systemmonitor: '系統監測',
  },
  v2: {
    quickApiKey: 'API Key', quickModels: '模型', quickApiKeyTitle: '查看 API Key 排行',
    quickModelsTitle: '查看模型統計', refreshData: '重新整理資料', retry: '重試',
    reloadAria: '重新載入資料', degradedViewLabel: '視圖',
    degradedHint: '資料視圖 {view} 尚未初始化，請先執行資料聚合遷移',
    emptyTitle: '暫無請求資料',
    emptyHint: '設定供應商後，透過 /v1/chat/completions 發起呼叫即可查看即時流。',
    modelsPage: '模型頁', totalTokensShort: '總 Token', totalCredits: '總積分消耗',
    creditsSub: '按定價 × token 計算', modelCount: '模型數', sessionCompression: '會話壓縮',
  },
  range: {
    today: "今日",
    last7d: "近 7 天",
    last30d: "近 30 天",
    last90d: "近 90 天"
  },
  tenantLabel: {
    default: "全站資料",
    super: "預設租戶",
    tenant: "租戶: {tenantId}"
  },
  loadError: "載入失敗",
  board: {
    empty: '暫無資料',
    bgTasks: '後台任務',
    discoveryStatus: '模型發現',
    probeChecks: '近10分鐘探測',
    selfcheckTitle: '系統自檢',
    selfcheckLast: '最近狀態',
    selfcheckRate: '24h 成功率',
    selfcheckHint: '點擊查看詳情 →',
    pieClients: '客戶端類型',
    pieVirtualIp: '客戶端 IP',
    pieIdentity: '身份指紋',
    pieModels: '模型用量',
    pieErrors: '錯誤類型',
    pieTenants: '租戶用量',
    pieProviders: '供應商用量',
    metricRequests: '請求次數',
    metricTokens: 'Token 數',
    metricToggle: '指標切換',
    errorDrill: '錯誤下鑽：{kind}',
    drillModel: '按模型',
    drillProvider: '按供應商',
    drillClient: '按客戶端',
    trendTitle: '用量趨勢',
    trendRequests: '請求數',
    trendTokens: 'Token',
    trendCredits: '積分',
    trendCost: '成本 (USD)',
    filterProvider: '供應商 ID',
    allProviders: '全部',

    rangeCustomTab: '自定义',

    rangeFrom: '起始',

    rangeTo: '截止',

    rangePicker: '统计时间范围',

    rangeCustom: '{start} 至 {end}',
  },
  proxyWarning: {
    title: "⚠ 出口代理無法連線",
    detail: "已設定代理 {proxy} 探測失敗，已自動降級為直連模式。國外模型（Anthropic / OpenAI / OpenRouter / GitHub Copilot 等）可能失敗。",
    detailHealthy: "代理 {proxy} 已恢復。",
    hint: "代理恢復後系統將自動重新啟用"
  },
  backgroundTasks: {
    title: "背景任務進行中",
    running: "模型發現（{trigger}）",
    startedAt: "開始 {time}",
    heartbeat: "心跳 {time}",
    slow: "管理頁可能變慢",
    detailsLink: "查看詳情"
  },
  discovery: {
    latest: "最近模型發現：",
    failuresTitle: "模型發現 · 最近 6h 測試失敗",
    failuresTally: "{n} 次失敗 · {m} 個模型",
    summary: "查看失敗清單",
    meta: "{n} 次 · 涉及 {m} 個憑證 · 最近 {date} · 錯誤 {code}"
  },
  stat: {
    totalRequests: "總請求數",
    inLastDays: "近 {days} 天",
    totalTokens: "總 Token 用量",
    prompt: "提示 {n}",
    completion: "補完 {n}",
    totalCost: "總費用",
    costUnit: "USD",
    successRate: "成功率",
    avgLatency: "平均延遲 {n} ms",
    apiKeys: "接入 API Key",
    enabledActive: "啟用 {enabled} · 活躍 {active}",
    models: "模型數量",
    activeInDays: "近 {days} 天活躍 {n}",
    providers: "供應商 / 憑證",
    enabledCredentials: "啟用 {enabled} · 憑證 {total}",
offline: "下線資源",
     modelsCredentials: "模型 {models} · 憑證 {creds}",
     // 2026-08-06: 平均請求體 / 平均回應體 / 峰值（與 zh-CN 對齊）
     avgRequestSize: "平均請求體",
     avgResponseSize: "平均回應體",
     maxLabel: "峰值",
   },
  table: {
    hotKeysTitle: "高用量 API Key 排行",
    byModelTitle: "依模型統計",
    colKey: "Key",
    colApplication: "應用",
    colOwner: "歸屬使用者",
    colRequests: "請求數",
    colTokens: "Token 用量",
    colCost: "費用 (USD)",
    colLastUsed: "最後使用",
    colModel: "模型",
    colProvider: "提供商"
  },
  compression: {
    title: "🤖 會話壓縮",
    delta: "增量",
    sliding: "滑動",
    outboundTokens: "≈ {n} 出站 token"
  },
  empty: {
    firstUse: "🚀 暫無請求資料。設定好提供商後，透過 /v1/chat/completions 發起呼叫吧。"
  },
  loading: "載入中…",
  noData: "該時段暫無資料",
  costSuffix: "USD",
  viewFailedRequests: "查看失敗請求",
  liveStream: {
    title: "即時請求流",
    groupByQueue: "按處理佇列",
    controlsAria: "即時請求流控制列",
    connected: "已連線",
    disconnected: "重新連線中…",
    pause: "暫停",
    resume: "繼續",
    filterAll: "全部狀態",
    filterSuccess: "僅成功",
    filterFailure: "僅失敗",
    filterInProgress: "進行中",
    filterGroupFailures: "失敗分類",
    filterFailure5xx: "伺服器 / 上游 (5xx)",
    filterFailure4xx: "用戶端 / 驗證 (4xx)",
    filterFailureTimeout: "逾時 / 網路",
    filterFailureNotFound: "路由 / 模型不存在",
    filterFailureOther: "其它失敗",
    idleLabel: "閒置 {duration}",
    countTooltip: "緩衝區 {buffer} / 螢幕可見 {visible}",
    countAria: "緩衝區 {buffer} 個請求，螢幕可見 {visible}",
    legendButton: '圖例',
    legend: {
      title: "圖例",
      model: "模型族",
      status: "狀態",
      openai: "OpenAI",
      anthropic: "Anthropic",
      domestic: "本地",
      oss: "開源",
      other: "其他",
      success: "成功",
      inProgress: "進行中",
      failure: "失敗"
,
      cancelled: '已取消',
    },
    tooltip: {
      model: "模型",
      provider: "供應商",
      status: "狀態",
      latency: "延遲",
      tokens: "Token",
      cost: "成本",
      error: "錯誤",
      time: "時間"
,
      vendor: '原厂',

      requestId: 'ID',

      errorKind: '错误类型',

      errorRawCode: '原始代码',

      statusValue: {
        success: '成功',
        inProgress: '处理中',
        cancelled: '用户取消',
        idle: '空闲（心跳占位）',
        failure: '请求失败',
      },

      tokenFormat: '{p} + {c}',
    },
    connecting: "連線中…",
    reconnecting: "重新連線中…",
    unsupported: "此瀏覽器不支援即時串流",
    empty: "暫無即時請求",

    emptyWaiting: '等待即時請求流資料…',
    groupByCredential: '按憑據', groupByVendor: '按原廠', groupByProvider: '按供應商', groupByModel: '按模型',
    modeSmall: '小',
    modeLarge: '大',
    modeSmallTitle: '小模式：直條顯示，可容納更多請求（預設）',
    modeLargeTitle: '大模式：卡片顯示，包含更多請求詳情',
    probeAll: '全部', probeOnly: '僅探測',
    probeAllTitle: '顯示所有請求（預設）', probeOnlyTitle: '僅顯示探測請求',
    // 2026-08-06: 業務 / 探測過濾 + 標題（與 zh-CN 對齊）
    business: '業務',
    probe: '探測',
    businessTitle: '僅顯示真實業務請求',
    probeTitle: '僅顯示探測請求',
    cacheWindow: '快取 / 視窗', connectionDetailTitle: '點擊查看連線詳情',
    dimensionCredential: '憑據', dimensionVendor: '原廠', dimensionProvider: '供應商', dimensionModel: '模型',
    statusOpen: '已連線', statusConnecting: '連線中', statusReconnecting: '重新連線中',
    statusUnsupported: '不支援', statusClosed: '未連線',
    sseDetailTitle: 'SSE 連線詳情', sseStatusLabel: '連線狀態', sseUrlLabel: 'SSE 位址',
    editUrl: '編輯', editUrlPlaceholder: '輸入 SSE 位址', save: '儲存', resetDefault: '預設',
    cancel: '取消', testConnection: '測試連線', close: '關閉',
    sseTestOk: 'SSE 連線正常！\n狀態: 已連線\n位址: {url}',
    sseTestFail: 'SSE 未連線\n狀態: {status}\n位址: {url}',
    redisWarning: 'Redis 不可用：{error}。即時資料降級為資料庫查詢。',
    redisFallbackError: '快取服務連線失敗',

    probeDirect: '主動探測（直連上游）',

    probeGateway: '主動探測（閘道路徑）',

    probeScheduled: '週期探測（排程器）',

    probeGeneric: '探測請求',

    probeOriginLabel: '來源: {origin}',

    probeAttempt: '輪次: 第 {n} 輪',

    originGateway: '閘道路徑',

    originScheduled: '排程探測',

    originDirect: '直連上游',

    idleHeartbeat: '心跳佔位',

    tileIdle: '閒置',

    idleUnderOneMin: '閒置 < 1 分鐘',

    idleMinutes: '閒置 {n} 分鐘',

    idleHours: '閒置 {h} 小時',

    idleHoursMinutes: '閒置 {h} 小時 {m} 分鐘',

    idleReasonNoTraffic: '無流量（5 分鐘無請求）',
    // 2026-07-24: 多維過濾器
    filterStatus: '狀態',
    filterAllOptions: '全部',
    filterEmpty: '暫無可選項',
    filterModel: '模型',
    filterProvider: '供應商',
    filterVendor: '原廠',
    filterAgent: '客戶端',  // 2026-08-06 與 zh-CN 對齊
    clearFilters: '清除過濾',
    status: {
      in_progress: '進行中',
      success: '成功',
      failure: '失敗',
      rate_limited: '限流',
    },
    vendor: {
      openai: 'OpenAI',
      anthropic: 'Anthropic',
      domestic: '本地',
      oss: '開源',
      other: '其他',
    },
  },
  charts: {
    gradeA: "優秀",
    gradeB: "良好",
    gradeC: "一般",
    gradeD: "較差",
    gradeF: "異常",
    avgScore: "平均分",
    healthDistribution: "健康度分布",
    totalSessions: "總會話數 {n}",
    newSessions: "新會話",
    activeSessions: "活躍會話",
    closedSessions: "已關閉",
    costUSD: "費用 (USD)",
    sessionCount: "會話數",
    sessionTrend: "會話趨勢"
  },

  moduleStats: {
    executions: '執行次數',
    cacheHitRate: '快取命中率',
    rate: '比率',
    totalModules: '模組數',
    totalExecutions: '總執行次數',
    avgCacheHitRate: '平均快取命中率',
    avgDuration: '平均耗時',

    title: '模組執行統計',

    successRate: '成功率',
  },

  errors: {
    trendTitle: '錯誤趨勢',
    errorCount: '錯誤數',
    totalCount: '總請求數',
    errorRate: '錯誤率',
    count: '數量',
    rate: '比率',
    totalErrors: '總錯誤數',
    topErrors: 'Top 錯誤',

    title: '錯誤統計',

    totalRequests: '總請求數',

    avgLatency: '平均錯誤延遲',
  },

  performance: {
    throughput: '吞吐量',
    requests: '請求數',
    p50: 'P50 延遲',
    p95: 'P95 延遲',
    p99: 'P99 延遲',
    latencyDist: '延遲分布',
    slowQueries: '慢查詢',

    title: '效能指標',

    avgLatency: '平均延遲',

    latency: '延遲',
  },

  providerUsage: {
    title: '供應商用量',
    subtitle: '{period} 全站供應商消耗彙總（可用於對帳）',
    periodHint: '當前週期：{period}',
    more: '更多',
    search: '搜尋供應商名稱、代碼或 ID…',
    back: '返回列表',
    exportAll: '匯出全部 Excel',
    exportDetail: '匯出明細 Excel',
    colName: '供應商',
    colCode: '代碼',
    colRequests: '請求數',
    colTokens: 'Token',
    colCost: '成本 (USD)',
    colSuccess: '成功率',
    colModel: '模型',
    colDate: '日期',
    periodDay: '按天',
    periodWeek: '按週',
    periodMonth: '按月',
    periodLabel: '統計週期：{period}',
    periodRange: '{start} 至 {end}',
    modelBreakdown: '模型彙總',
    dailyBreakdown: '每日模型明細',
  },
  statsRow: {
    totalSessions: "Total Sessions",
    activeSessions: "Active Sessions",
    activeHint: "Active in 24h",
    totalCost: "Total Cost",
    complianceRate: "Compliance Rate",
    avgHealthScore: "Avg Health Score",
    healthHint: "From session health score",
    avgLatency: "Avg Latency",
    totalRequests: "Total Requests",
    totalTokens: "Total Tokens",
  },
}
