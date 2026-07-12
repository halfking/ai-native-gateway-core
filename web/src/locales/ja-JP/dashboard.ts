export default {
  title: "ダッシュボード",
  refresh: "更新",
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
    },
    connecting: "接続中…",
    reconnecting: "再接続中…",
    unsupported: "このブラウザはリアルタイムストリームに対応していません",
    empty: "リアルタイムリクエストがありません"
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
  tabs: {
    liveStream: "实时请求流",
    sessionStats: "会话与统计"
  },
  moduleStats: {
    title: "模块执行统计",
    executions: "执行次数",
    successRate: "成功率",
    cacheHitRate: "缓存命中率",
    rate: "比率",
    totalModules: "总模块数",
    totalExecutions: "总执行次数",
    avgCacheHitRate: "平均缓存命中率",
    avgDuration: "平均耗时"
  },
  errors: {
    title: "错误统计",
    trendTitle: "错误趋势",
    errorCount: "错误数",
    totalCount: "总请求数",
    errorRate: "错误率",
    count: "数量",
    rate: "比率",
    totalErrors: "总错误数",
    totalRequests: "总请求数",
    avgLatency: "平均错误延迟",
    topErrors: "Top错误"
  },
  performance: {
    title: "性能指标",
    throughput: "吞吐量",
    avgLatency: "平均延迟",
    requests: "请求数",
    latency: "延迟",
    p50: "P50 延迟",
    p95: "P95 延迟",
    p99: "P99 延迟",
    latencyDist: "延迟分布",
    slowQueries: "慢查询"
  }
}
