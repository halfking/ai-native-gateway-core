export default {
  title: "ダッシュボード",
  refresh: "更新",
tabs: {
    board: 'ボード',
    liveStream: 'リアルタイムリクエスト',
    sessionStats: 'セッションと統計',
    selfcheck: 'システム監視',
  },
  v2: {
    quickApiKey: 'API Key', quickModels: 'モデル', quickApiKeyTitle: 'API Key ランキング',
    quickModelsTitle: 'モデル統計', refreshData: 'データ更新', retry: '再試行',
    reloadAria: 'データ再読み込み', degradedViewLabel: 'ビュー',
    degradedHint: 'データビュー {view} は未初期化です。集計マイグレーションを実行してください。',
    emptyTitle: 'リクエストデータなし',
    emptyHint: 'プロバイダー設定後、/v1/chat/completions で呼び出すと表示されます。',
    modelsPage: 'モデル', totalTokensShort: '総トークン', totalCredits: '総クレジット',
    creditsSub: '価格 × トークン', modelCount: 'モデル数', sessionCompression: 'セッション圧縮',
  },
  range: {
    today: "今日",
    last7d: "直近7日間",
    last30d: "直近30日間",
    last90d: "直近90日間"
  },
  tenantLabel: {
    default: "サイト全体",
    super: "デフォルトテナント",
    tenant: "テナント: {tenantId}"
  },
  loadError: "読み込み失敗",
  board: {
    empty: 'データなし',
    bgTasks: 'バックグラウンドタスク',
    discoveryStatus: 'モデル検出',
    probeChecks: '直近10分のプローブ',
    selfcheckTitle: 'システム自己診断',
    selfcheckLast: '最新ステータス',
    selfcheckRate: '24時間成功率',
    selfcheckHint: 'クリックして詳細 →',
    pieClients: 'クライアント種別',
    pieVirtualIp: 'クライアント IP',
    pieIdentity: 'ID フィンガープリント',
    pieModels: 'モデル使用量',
    pieErrors: 'エラー種別',
    pieTenants: 'テナント使用量',
    pieProviders: 'プロバイダー使用量',
    metricRequests: 'リクエスト数',
    metricTokens: 'トークン数',
    metricToggle: '指標',
    errorDrill: 'エラー詳細: {kind}',
    drillModel: 'モデル別',
    drillProvider: 'プロバイダー別',
    drillClient: 'クライアント別',
    trendTitle: '使用量トレンド',
    trendRequests: 'リクエスト',
    trendTokens: 'トークン',
    trendCredits: 'クレジット',
    trendCost: 'コスト (USD)',
    filterProvider: 'プロバイダー ID',
    allProviders: 'すべて',

    rangeCustomTab: '自定义',

    rangeFrom: '起始',

    rangeTo: '截止',

    rangePicker: '统计时间范围',

    rangeCustom: '{start} 至 {end}',
  },
  proxyWarning: {
    title: "⚠ 出口プロキシに到達できません",
    detail: "設定済みプロキシ {proxy} の検出に失敗したため、直接接続モードに自動降格しました。海外モデル（Anthropic / OpenAI / OpenRouter / GitHub Copilot など）は失敗する可能性があります。",
    detailHealthy: "プロキシ {proxy} が復旧しました。",
    hint: "プロキシ復旧後、システムは自動的に再有効化します"
  },
  backgroundTasks: {
    title: "バックグラウンドタスク実行中",
    running: "モデル検出（{trigger}）",
    startedAt: "開始 {time}",
    heartbeat: "ハートビート {time}",
    slow: "管理ページの動作が遅くなる可能性があります",
    detailsLink: "詳細を表示"
  },
  discovery: {
    latest: "直近のモデル検出:",
    failuresTitle: "モデル検出 · 直近6時間のテスト失敗",
    failuresTally: "{n} 回失敗 · {m} モデル",
    summary: "失敗リストを表示",
    meta: "{n} 回 · {m} 件の認証情報 · 直近 {date} · エラー {code}"
  },
  stat: {
    totalRequests: "総リクエスト数",
    inLastDays: "直近 {days} 日間",
    totalTokens: "総トークン使用量",
    prompt: "プロンプト {n}",
    completion: "補完 {n}",
    totalCost: "総費用",
    costUnit: "USD",
    successRate: "成功率",
    avgLatency: "平均レイテンシ {n} ms",
    apiKeys: "接続中の API キー",
    enabledActive: "有効 {enabled} · アクティブ {active}",
    models: "モデル数",
    activeInDays: "直近 {days} 日間でアクティブ {n}",
    providers: "プロバイダー / 認証情報",
    enabledCredentials: "有効 {enabled} · 認証情報 {total}",
    offline: "オフラインリソース",
    modelsCredentials: "モデル {models} · 認証情報 {creds}"
  },
  table: {
    hotKeysTitle: "使用量が多い API キーのランキング",
    byModelTitle: "モデル別統計",
    colKey: "キー",
    colApplication: "アプリケーション",
    colOwner: "所有者",
    colRequests: "リクエスト数",
    colTokens: "トークン使用量",
    colCost: "費用 (USD)",
    colLastUsed: "最終使用",
    colModel: "モデル",
    colProvider: "プロバイダー"
  },
  compression: {
    title: "🤖 セッション圧縮",
    delta: "増分",
    sliding: "スライディング",
    outboundTokens: "≈ {n} 出力トークン"
  },
  empty: {
    firstUse: "🚀 まだリクエストデータがありません。プロバイダーを設定したら、/v1/chat/completions で呼び出してみてください。"
  },
  loading: "読み込み中…",
  noData: "この期間のデータはありません",
  costSuffix: "USD",
  viewFailedRequests: "失敗リクエストを表示",
  liveStream: {
    title: "リアルタイムリクエスト",
    connected: "接続中",
    disconnected: "再接続中...",
    pause: "一時停止",
    resume: "再開",
    filterAll: "すべてのステータス",
    filterSuccess: "成功のみ",
    filterFailure: "失敗のみ",
    filterInProgress: "処理中",
    filterGroupFailures: "失敗の内訳",
    filterFailure5xx: "サーバー / アップストリーム (5xx)",
    filterFailure4xx: "クライアント / 認証 (4xx)",
    filterFailureTimeout: "タイムアウト / ネットワーク",
    filterFailureNotFound: "ルーティング / モデル未検出",
    filterFailureOther: "その他の失敗",
    idleLabel: "アイドル {duration}",
    countTooltip: "バッファ内 {buffer} / 表示中 {visible}",
    countAria: "バッファ内に {buffer} 件、表示中 {visible} 件",
    legend: {
      title: "凡例",
      model: "モデルファミリー",
      status: "ステータス",
      openai: "OpenAI",
      anthropic: "Anthropic",
      domestic: "国産",
      oss: "オープンソース",
      other: "その他",
      success: "成功",
      inProgress: "処理中",
      failure: "失敗"
,
      cancelled: '已取消',
    },
    tooltip: {
      model: "モデル",
      provider: "プロバイダー",
      status: "ステータス",
      latency: "レイテンシ",
      tokens: "トークン",
      cost: "コスト",
      error: "エラー",
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
    connecting: "接続中…",
    reconnecting: "再接続中…",
    unsupported: "このブラウザはリアルタイムストリームに対応していません",
    empty: "リアルタイムリクエストがありません",

    emptyWaiting: '待機中 live request stream data…',
    groupByVendor: 'ベンダー別', groupByProvider: 'By provider', groupByModel: 'By model',
    modeSmall: '小',
    modeLarge: '大',
    modeSmallTitle: '小モード：縦棒表示、より多くのリクエストを収容（デフォルト）',
    modeLargeTitle: '大モード：カード表示、より詳細なリクエスト情報',
    probeAll: 'All', probeOnly: 'Probes only',
    probeAllTitle: 'Show all requests (default)', probeOnlyTitle: 'Show probe requests only',
    cacheWindow: 'Cache / window', connectionDetailTitle: 'Click for connection details',
    dimensionVendor: 'Vendor', dimensionProvider: 'Provider', dimensionModel: 'Model',
    statusOpen: 'Connected', statusConnecting: 'Connecting', statusReconnecting: 'Reconnecting',
    statusUnsupported: 'Unsupported', statusClosed: 'Disconnected',
    sseDetailTitle: 'SSE connection details', sseStatusLabel: 'Status', sseUrlLabel: 'SSE URL',
    editUrl: 'Edit', editUrlPlaceholder: 'Enter SSE URL', save: 'Save', resetDefault: 'Default',
    cancel: 'Cancel', testConnection: 'Test connection', close: 'Close',
    sseTestOk: 'SSE connection OK!\nStatus: connected\nURL: {url}',
    sseTestFail: 'SSE not connected\nStatus: {status}\nURL: {url}',
    redisWarning: 'Redis unavailable: {error}. Live data falls back to DB queries.',
    redisFallbackError: 'Cache service connection failed',

    probeDirect: '主动探测 (直连上游)',

    probeGateway: '主动探测 (网关路径)',

    probeScheduled: '周期探测 (scheduler)',

    probeGeneric: '探测请求',

    probeOriginLabel: '来源: {origin}',

    probeAttempt: '轮次: 第 {n} 轮',

    originGateway: '网关路径',

    originScheduled: '定时探测',

    originDirect: '直连上游',

    idleHeartbeat: '心跳占位',

    tileIdle: '空闲',

    idleUnderOneMin: '空闲 < 1 分钟',

    idleMinutes: '空闲 {n} 分钟',

    idleHours: '空闲 {h} 小时',

    idleHoursMinutes: '空闲 {h} 小时 {m} 分钟',

    idleReasonNoTraffic: '无流量（5 分钟无请求）',
  },
  charts: {
    gradeA: "Grade A",
    gradeB: "Grade B",
    gradeC: "Grade C",
    gradeD: "Grade D",
    gradeF: "Grade F",
    avgScore: "平均スコア",
    healthDistribution: "健康度分布",
    totalSessions: "合計 {n} セッション",
    newSessions: "新規セッション",
    activeSessions: "アクティブセッション",
    closedSessions: "終了セッション",
    costUSD: "コスト (USD)",
    sessionCount: "セッション数",
    sessionTrend: "セッション傾向"
  },

  moduleStats: {
    executions: '実行回数',
    cacheHitRate: 'キャッシュヒット率',
    rate: '比率',
    totalModules: 'モジュール数',
    totalExecutions: '総実行回数',
    avgCacheHitRate: '平均キャッシュヒット率',
    avgDuration: '平均所要時間',

    title: '模块执行统计',

    successRate: '成功率',
  },

  errors: {
    trendTitle: 'エラー傾向',
    errorCount: 'エラー数',
    totalCount: '総リクエスト数',
    errorRate: 'エラー率',
    count: '件数',
    rate: '比率',
    totalErrors: '総エラー数',
    topErrors: '上位エラー',

    title: '错误统计',

    totalRequests: '总请求数',

    avgLatency: '平均错误延迟',
  },

  performance: {
    throughput: 'スループット',
    requests: 'リクエスト数',
    p50: 'P50 レイテンシ',
    p95: 'P95 レイテンシ',
    p99: 'P99 レイテンシ',
    latencyDist: 'レイテンシ分布',
    slowQueries: 'スロークエリ',

    title: '性能指标',

    avgLatency: '平均延迟',

    latency: '延迟',
  },

  providerUsage: {
    title: '供应商用量',
    subtitle: '{period} 全站供应商消耗汇总（可用于对账）',
    periodHint: '当前周期：{period}',
    more: '更多',
    search: '搜索供应商名称、代码或 ID…',
    back: '返回列表',
    exportAll: '导出全部 Excel',
    exportDetail: '导出明细 Excel',
    colName: '供应商',
    colCode: '代码',
    colRequests: '请求数',
    colTokens: 'Token',
    colCost: '成本 (USD)',
    colSuccess: '成功率',
    colModel: '模型',
    colDate: '日期',
    periodDay: '按天',
    periodWeek: '按周',
    periodMonth: '按月',
    periodLabel: '统计周期：{period}',
    periodRange: '{start} 至 {end}',
    modelBreakdown: '模型聚合',
    dailyBreakdown: '每日模型明细',
  },
}