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
    legendButton: '凡例',
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

    emptyWaiting: 'リアルタイムリクエストストリームデータを待機中…',
    groupByVendor: 'ベンダー別', groupByProvider: 'プロバイダー別', groupByModel: 'モデル別',
    modeSmall: '小',
    modeLarge: '大',
    modeSmallTitle: '小モード：縦棒表示、より多くのリクエストを収容（デフォルト）',
    modeLargeTitle: '大モード：カード表示、より詳細なリクエスト情報',
    probeAll: 'すべて', probeOnly: 'プローブのみ',
    probeAllTitle: 'すべてのリクエストを表示（デフォルト）', probeOnlyTitle: 'プローブリクエストのみ表示',
    cacheWindow: 'キャッシュ / ウィンドウ', connectionDetailTitle: 'クリックで接続詳細を表示',
    dimensionVendor: 'ベンダー', dimensionProvider: 'プロバイダー', dimensionModel: 'モデル',
    statusOpen: '接続済み', statusConnecting: '接続中', statusReconnecting: '再接続中',
    statusUnsupported: '未対応', statusClosed: '未接続',
    sseDetailTitle: 'SSE 接続詳細', sseStatusLabel: '接続状態', sseUrlLabel: 'SSE URL',
    editUrl: '編集', editUrlPlaceholder: 'SSE URL を入力', save: '保存', resetDefault: 'デフォルト',
    cancel: 'キャンセル', testConnection: '接続テスト', close: '閉じる',
    sseTestOk: 'SSE 接続 OK！\n状態: 接続済み\nURL: {url}',
    sseTestFail: 'SSE 未接続\n状態: {status}\nURL: {url}',
    redisWarning: 'Redis 利用不可: {error}。ライブデータは DB クエリにフォールバックします。',
    redisFallbackError: 'キャッシュサービス接続失敗',

    probeDirect: 'アクティブプローブ（アップストリーム直接）',

    probeGateway: 'アクティブプローブ（ゲートウェイ経由）',

    probeScheduled: 'スケジュールプローブ（スケジューラ）',

    probeGeneric: 'プローブリクエスト',

    probeOriginLabel: 'ソース: {origin}',

    probeAttempt: '試行: 第{n}回',

    originGateway: 'ゲートウェイパス',

    originScheduled: 'スケジュールプローブ',

    originDirect: 'アップストリーム直接',

    idleHeartbeat: 'ハートビートプレースホルダ',

    tileIdle: 'アイドル',

    idleUnderOneMin: 'アイドル < 1分',

    idleMinutes: 'アイドル {n}分',

    idleHours: 'アイドル {h}時間',

    idleHoursMinutes: 'アイドル {h}時間{m}分',

    idleReasonNoTraffic: 'トラフィックなし（5分間アイドル）',
    // 2026-07-24: マルチディメンションフィルター
    filterStatus: 'ステータス',
    filterAllOptions: 'すべて',
    filterEmpty: '選択肢がありません',
    filterModel: 'モデル',
    filterProvider: 'プロバイダー',
    filterVendor: 'ベンダー',
    clearFilters: 'フィルターをクリア',
    status: {
      in_progress: '処理中',
      success: '成功',
      failure: '失敗',
      rate_limited: '制限あり',
    },
    vendor: {
      openai: 'OpenAI',
      anthropic: 'Anthropic',
      domestic: '国産',
      oss: 'オープンソース',
      other: 'その他',
    },
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

    title: 'モジュール実行統計',

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

    title: 'エラー統計',

    totalRequests: '総リクエスト数',

    avgLatency: '平均エラーレイテンシ',
  },

  performance: {
    throughput: 'スループット',
    requests: 'リクエスト数',
    p50: 'P50 レイテンシ',
    p95: 'P95 レイテンシ',
    p99: 'P99 レイテンシ',
    latencyDist: 'レイテンシ分布',
    slowQueries: 'スロークエリ',

    title: 'パフォーマンス指標',

    avgLatency: '平均レイテンシ',

    latency: 'レイテンシ',
  },

  providerUsage: {
    title: 'プロバイダー使用状況',
    subtitle: '{period} 全プロバイダーの消費量（照合可能）',
    periodHint: '現在の期間：{period}',
    more: '詳細',
    search: '名前・コード・ID で検索…',
    back: '一覧に戻る',
    exportAll: 'すべてエクスポート (Excel)',
    exportDetail: '詳細をエクスポート (Excel)',
    colName: 'プロバイダー',
    colCode: 'コード',
    colRequests: 'リクエスト数',
    colTokens: 'トークン',
    colCost: 'コスト (USD)',
    colSuccess: '成功率',
    colModel: 'モデル',
    colDate: '日付',
    periodDay: '日次',
    periodWeek: '週次',
    periodMonth: '月次',
    periodLabel: '期間：{period}',
    periodRange: '{start} から {end}',
    modelBreakdown: 'モデル別',
    dailyBreakdown: '日次モデル内訳',
  },
}