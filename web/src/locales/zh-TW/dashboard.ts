export default {
  title: "儀表板",
  refresh: "重新整理",
  modelsSelectedCount: '已選 {n} 個模型',  // R50 ModelPicker compact 觸發器
tabs: {
    board: '看板',
    liveStream: '即時請求流',
    sessionStats: '會話與統計',
    selfcheck: '系統自檢',
  },
  v2: {
    quickApiKey: 'API Key', quickModels: '模型', quickApiKeyTitle: '檢視 API Key 排行',
    quickModelsTitle: '查看模型統計', refreshData: '重新整理資料', retry: '重試',
    reloadAria: '重新載入資料',
 totalTokensShort: '總 Token', totalCredits: '總積分消耗',
    creditsSub: '按定價 × token 計算', modelCount: '模型數',
    creditsDegraded: '積分彙總檢視 {view} 尚未初始化，此數不可信',
    summaryDegraded: "彙總彙總檢視 {view} 尚未初始化 —— 下方請求/Token/費用數字是「沒算出來」，而不是 0，不可作為結論。",
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
  loadError: '看板資料沒能載入出來，請重試',
  board: {
    heroErrors: '錯誤',
    heroInput: '輸入',
    heroOutput: '輸出',
    heroCreditsEq: '折合積分 {n}',
    drillDown: '下鑽',
    rpmLabel: 'RPM',
    rpmTitle: '每分鐘請求數（視窗均值）',
    tpmLabel: 'TPM',
    tpmTitle: '每分鐘 Token（視窗均值）',
    providerCount: '{n} 家',
    opsRunning: '執行中',
    opsChecks: '近10分鐘探測 {n}',
    opsRate24h: '24h 成功率 {n}',
    opsChecksUnknown: '近 10 分鐘檢查次數未知',
    opsRateUnknown: '近 24 小時成功率未知',
    providerCostTitle: '供應商成本採購',
    providerDetail: '詳情',
    providerWindowCost: '視窗成本',
    providerCredits: '積分',
    providerBalance: '餘額',
    providerScore: '評分 {n}',
    providerPlan: '套餐',
    providerStatsTitle: '供應商用量統計',
    providerStatsHint: '隨時間範圍聯動：選擇任意一行查看詳情',
    providerExport: '匯出 Excel',
    filterRange: '時間範圍',
    filterGran: '粒度',
    granAuto: '自動',
    granAutoTitle: '粒度隨範圍自動對齊（今日=5m / 7d=15m / 30d=1h），手動檔位二期開放',
    granHour: '按小時',
    granMinutes: '{n} 分鐘',
    sourceLabel: '資料源',
    sourcePg: 'PG 基線',
    sourceRedis: 'Redis 增量',
    sourceSse: 'SSE 即時',
    sourceTitle: '看板資料來源（PostgreSQL 基線 / Redis 增量 / SSE 即時合併）',
    modelDistTitle: '模型分佈',
    modelTop: '視窗內 Top {n}',
    colShare: '佔比',
    distTitle: '分佈分析',
    distTop: 'TOP {n}',
    distOthers: '其他 {n} 項',
    distDrillHint: '選擇錯誤類型 → 按模型/供應商/客戶端下鑽',
    /**
     * 空狀態（2026-10-27 第 14 增量 · 空狀態 5 分）。
     * ★ 原本這裡只有一個 `empty: '看板暫時沒有資料'`，被 6 個**不同**的卡片共用
     *   （供應商 / 分佈排行 / 模型分佈 / 趨勢圖 / 用量表 / 運維看板的兩張任務卡）——
     *   一句話覆蓋 6 種語意 ⇒ 使用者看到「沒有資料」時無法判斷是「真的沒有」「被篩掉了」
     *   還是「算錯了」。⇒ 現在按**頁面事實**拆開，kind 寫進鍵名。
     *   · emptyNoResult* = 無結果（查過了，真的沒有）
     *   · emptyFailed*    = 失敗/未知（伺服器沒給 ⇒ 不能說成「沒有」）
     *   ★ 時間範圍沿用本檔既有譯法（`dashboard.noData` 舊值「該時段暫無資料」）⇒ 該時段內。
     */
    emptyNoResultProviders: '該時段內沒有供應商資料。',
    emptyNoResultDist: '該時段內沒有可排名的資料。',
    emptyNoResultDrill: '該錯誤類型下沒有失敗的請求。',
    emptyNoResultModelDist: '該時段內沒有模型資料。',
    emptyNoResultModelTrend: '該時段內沒有模型趨勢資料。',
    emptyNoResultTrend: '該時段內沒有用量趨勢資料。',
    emptyNoResultDiscovery: '模型發現任務還沒有執行記錄。',
    emptyNoResultProbe: '探測巡檢還沒有執行記錄。',
    bgTasks: '後台任務',
    discoveryStatus: '模型發現',
    selfcheckTitle: '系統自檢',
    pieClients: '客戶端類型',
    pieDegraded: "不可用 —— {reason}。此處的空排名表示「沒算出來」，而不是「沒有資料」。",
    pieDegradedGeneric: '該排行的查詢沒能完成，請重試',
    drillFailed: "無法載入該錯誤構成 —— {reason}。此處顯示為空不代表真的沒有這類錯誤。",
    pieClientIp: '客戶端 IP',
    pieIdentity: '身份指紋',
    pieErrors: '錯誤類型',
    pieTenants: '租戶用量',
    metricRequests: '請求次數',
    metricTokens: 'Token 數',
    metricToggle: '指標切換',
    errorDrill: '錯誤下鑽：{kind}',
    drillModel: '依模型',
    drillProvider: '按供應商',
    drillClient: '按客戶端',
    trendTitle: '用量趨勢',
    trendRequests: '請求數',
    trendTokens: 'Token',
    trendCredits: '積分',
    trendCost: '成本 (USD)',
    trendMore: '更多',
    trendMoreTitle: '開啟全頁用量趨勢分析',
    trendOthers: '其他',





    rangeCustom: '{start} 至 {end}',
  },
  stat: {
    totalRequests: "總請求數",
    totalCost: "總費用",
    successRate: "成功率",
    avgLatency: "平均延遲 {n} ms",
    models: "模型數量",
    activeInDays: "近 {days} 天活躍 {n}",
     avgRequestSize: "平均請求體",
     avgResponseSize: "平均回應體",
     maxLabel: "峰值",
   },
  table: {
    colModel: "模型",
  },
  loading: "載入中…",
  liveStream: {
    title: "即時請求流",
    groupByQueue: "按處理佇列",
    controlsAria: "即時請求流控制列",
    pause: "暫停",
    resume: "繼續",
    legendButton: '圖例',
    legend: {
      success: "成功",
      inProgress: "進行中",
      cancelled: '已取消',
    },
    tooltip: {




      statusValue: {
        success: '成功',
        inProgress: '处理中',
        cancelled: '用户取消',
        failure: '请求失败',
      },

      tokenFormat: '{p} + {c}',
      cacheFormat: '讀{r}/寫{w}（命中率{pct}）',
    },

    emptyWaiting: '等待即時請求流資料…',
    emptyConnecting: '正在連線即時串流，連線後自動載入…',
    groupByCredential: '按憑據', groupByProvider: '按供應商', groupByModel: '依模型',
    modeSmall: '小',
    modeLarge: '大',
    modeSmallTitle: '小模式：直條顯示，可容納更多請求（預設）',
    modeLargeTitle: '大模式：卡片顯示，包含更多請求詳情',
    // 2026-08-06: 業務 / 探測過濾 + 標題（與 zh-CN 對齊）
    business: '業務',
    probe: '探測',
    businessTitle: '僅顯示真實業務請求',
    probeTitle: '僅顯示探測請求',
    cacheWindow: '快取 / 視窗', connectionDetailTitle: '查看連線詳情',
    dimensionCredential: '憑據', dimensionVendor: '原廠', dimensionProvider: '供應商', dimensionModel: '模型',
    statusOpen: '已連線', statusConnecting: '連線中', statusReconnecting: '重新連線中',
    statusUnsupported: '不支援', statusClosed: '未連線',
    queueStreamConnecting: '正在連線即時串流，連線後本區資料自動刷新…',
    queueStreamClosed: '即時串流連線已中斷，正在自動重連…',
    queueStreamUnsupported: '目前瀏覽器不支援即時串流（EventSource），本區無法接收即時資料。',
    queueStreamAwaiting: '已連線即時串流，正在接收首批資料…',
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

    filterStatus: '狀態',
    filterModel: '模型',
    filterGroup: '篩選',
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
  },
  charts: {
    gradeA: "優秀",
    gradeB: "良好",
    gradeC: "一般",
    gradeD: "較差",
    gradeF: "異常",
    avgScore: "平均分",
    healthDistribution: "健康度分佈",
    totalSessions: "總會話數 {n}",
    newSessions: "新會話",
    activeSessions: "活躍會話",
    closedSessions: "已關閉",
    costUSD: "費用 (USD)",
    sessionCount: "會話數",
    sessionTrend: "會話趨勢",
    /**
     * 空狀態（2026-10-27 第 14 增量 · 空狀態 5 分）。
     * ★ 原先這兩處都用通用鍵 `dashboard.noData`（「該時段暫無資料」）。
     *   kind = NoResult：查過了，該時段內真的沒有這種資料。
     */
    emptyNoResultHealthGrade: '該時段內沒有健康度分佈資料。',
    emptyNoResultSessionTrend: '該時段內沒有會話趨勢資料。',
  },



  performance: {


    avgLatency: '平均延遲',

  },

  providerUsage: {
    title: '供應商用量',
    subtitle: '{period} 全站供應商消耗彙總（可用於對帳）',
    more: '更多',
    search: '搜尋供應商名稱、代碼或 ID…',
    back: '返回清單',
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
    periodDay: '依天',
    periodWeek: '按週',
    periodMonth: '依月',
    periodLabel: '統計週期：{period}',
    periodRange: '{start} 至 {end}',
    modelBreakdown: '模型彙總',
    dailyBreakdown: '每日模型明細',
    /**
     * 空狀態（2026-10-27 第 14 增量 · 空狀態 5 分）。
     * ★ 原先用通用鍵 `dashboard.noData`（「該時段暫無資料」）。
     *   kind = NoResult：降級態已有獨立措辭（dataLifecycle.usageCost.degraded.title），維持不變。
     */
    emptyNoResult: '該時段內沒有用量記錄。',
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
  requestTile: {
    childRequestsTitle: '子請求 {count} 個（展開查看）',
    childRequestsEmpty: '子請求（暫無推送，展開查看）',
    actionTrailTitle: '動作軌跡 {count} 條（展開查看）',
    actionTrailEmpty: '動作軌跡（暫無推送，展開查看）',
  },
}
