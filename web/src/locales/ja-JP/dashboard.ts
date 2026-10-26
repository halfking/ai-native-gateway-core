export default {
  title: "ダッシュボード",
  refresh: "更新",
  modelsSelectedCount: '{n} 個のモデルを選択中',  // R50 ModelPicker compact
tabs: {
    board: 'ボード',
    liveStream: 'リアルタイムリクエスト',
    sessionStats: 'セッションと統計',
    selfcheck: 'システム自己診断',
  },
  v2: {
    quickApiKey: 'API Key', quickModels: 'モデル', quickApiKeyTitle: 'API Key ランキング',
    quickModelsTitle: 'モデル統計', refreshData: 'データ更新', retry: '再試行',
    reloadAria: 'データ再読み込み',
 totalTokensShort: '総トークン', totalCredits: '総クレジット',
    creditsSub: '価格 × トークン', modelCount: 'モデル数',
    creditsDegraded: 'ポイント集計ビュー {view} が未初期化です。この数値は信頼できません',
    summaryDegraded: "集計ビュー {view} が未初期化です — 下のリクエスト数/トークン/費用は「計算できなかった」のであって 0 ではありません。結論として使わないでください。",
  },
  range: {
    today: "本日",
    last7d: "直近7日間",
    last30d: "直近30日間",
    last90d: "直近90日間"
  },
  tenantLabel: {
    default: "サイト全体",
    super: "デフォルトテナント",
    tenant: "テナント: {tenantId}"
  },
  loadError: 'ダッシュボードデータの読み込みに失敗しました。再試行してください。',
  board: {
    heroErrors: 'エラー',
    heroInput: '入力',
    heroOutput: '出力',
    heroCreditsEq: '≈ {n} クレジット',
    drillDown: 'ドリルダウン',
    rpmLabel: 'RPM',
    rpmTitle: '毎分のリクエスト数（期間平均）',
    tpmLabel: 'TPM',
    tpmTitle: '毎分のトークン数（期間平均）',
    providerCount: '{n} 社',
    opsRunning: '実行中',
    opsChecks: '直近10分のプローブ {n}',
    opsRate24h: "24h 成功率：{n}",
    opsChecksUnknown: '直近10分のチェック数：不明',
    opsRateUnknown: '直近24時間の成功率：不明',
    providerCostTitle: 'プロバイダーコスト調達',
    providerDetail: '詳細',
    providerWindowCost: '期間コスト',
    providerCredits: 'クレジット',
    providerBalance: '残高',
    providerScore: 'スコア {n}',
    providerPlan: 'プラン',
    providerStatsTitle: 'プロバイダー使用統計',
    providerStatsHint: '時間範囲に連動：任意の行を選んで詳細を表示',
    providerExport: 'Excel エクスポート',
    filterRange: '期間',
    filterGran: '粒度',
    granAuto: '自動',
    granAutoTitle: '粒度は範囲に自動整合（今日=5分 / 7日=15分 / 30日=1時間）。手動指定は第2期',
    granHour: '1時間ごと',
    granMinutes: '{n} 分',
    sourceLabel: 'データソース',
    sourcePg: 'PG ベースライン',
    sourceRedis: 'Redis 差分',
    sourceSse: 'SSE リアルタイム',
    sourceTitle: 'ボードのデータソース（PostgreSQL ベースライン / Redis 差分 / SSE リアルタイム統合）',
    modelDistTitle: 'モデル分布',
    modelTop: '期間内 Top {n}',
    colShare: '割合',
    distTitle: "分布の分析",
    distTop: 'TOP {n}',
    distOthers: '他 {n} 件',
    distDrillHint: 'エラータイプをクリック → モデル/プロバイダー/クライアント別にドリル',
    /**
     * 空状態（2026-10-27 第 14 インクリメント · 空状態 5 分）。
     * ★ 以前は `empty`（このボードにはまだデータがありません）が 6 つの**別の**カードで
     *   共有されていた（プロバイダー / 分布ランキング / モデル分布 / トレンド図 /
     *   使用量表 / 運用ボードの 2 つのタスクカード）⇒
     *   1 文で 6 種類の意味を覆っていた。
     *   ⇒ いまは**ページの実態**で分割し、kind をキー名に書く。
     *   · emptyNoResult* = 該当なし（実際に調べて、確かに無し）
     *   · emptyFailed*    = 失敗/不明（サーバーが返していない ⇒「無し」とは言えない）
     */
    emptyNoResultProviders: 'この期間のプロバイダーデータはありません。',
    emptyNoResultDist: 'この期間にランキングを作成できるデータはありません。',
    emptyNoResultDrill: 'このエラー種別では失敗したリクエストはありません。',
    emptyNoResultModelDist: 'この期間のモデルデータはありません。',
    emptyNoResultModelTrend: 'この期間のモデルトレンドデータはありません。',
    emptyNoResultTrend: 'この期間の使用量トレンドデータはありません。',
    emptyNoResultDiscovery: 'モデル検出タスクの実行記録はまだありません。',
    emptyNoResultProbe: 'プローブ点検の実行記録はまだありません。',
    bgTasks: 'バックグラウンドタスク',
    discoveryStatus: 'モデル検出',
    selfcheckTitle: 'システム自己診断',
    pieClients: 'クライアント種別',
    pieDegraded: "利用できません — {reason}。ここが空のランキングなのは、データが無かったのではなく算出できなかったためです。",
    pieDegradedGeneric: 'ランキングの取得を完了できませんでした。再試行してください。',
    drillFailed: "この内訳を読み込めませんでした — {reason}。ここが空なのは、この種類のエラーがなかったことを意味しません。",
    pieClientIp: 'クライアント IP',
    pieIdentity: 'ID フィンガープリント',
    pieErrors: 'エラー種別',
    pieTenants: 'テナント使用量',
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
    trendMore: 'もっと見る',
    trendMoreTitle: '全画面の使用量トレンド分析を開く',
    trendOthers: 'その他',





    rangeCustom: "{start} 〜 {end}",
  },
  stat: {
    totalRequests: "総リクエスト数",
    totalCost: "総費用",
    successRate: "成功率",
    avgLatency: "平均レイテンシ {n} ms",
    models: "モデル数",
    activeInDays: "直近 {days} 日間でアクティブ {n}",
    avgRequestSize: "平均リクエストサイズ",
    avgResponseSize: "平均レスポンスサイズ",
    maxLabel: "ピーク",
  },
  table: {
    colModel: "モデル",
  },
  loading: "読み込み中…",
  liveStream: {
    title: "リアルタイムリクエスト",
    groupByQueue: "処理キュー別",
    controlsAria: "リアルタイムリクエストストリームのコントロール",
    pause: "一時停止",
    resume: "再開",
    legendButton: '凡例',
    legend: {
      success: "成功",
      inProgress: "処理中",
      cancelled: "キャンセル済み",
    },
    tooltip: {




      statusValue: {
        success: '成功',
        inProgress: '处理中',
        cancelled: '用户取消',
        failure: '请求失败',
      },

      tokenFormat: '{p} + {c}',
      cacheFormat: '読み{r}/書き{w}（ヒット率{pct}）',
    },

    emptyWaiting: 'リアルタイムリクエストストリームデータを待機中…',
    emptyConnecting: 'ライブストリームに接続中です。接続後に自動的に読み込まれます…',
    groupByCredential: '認証情報別', groupByProvider: 'プロバイダー別', groupByModel: 'モデル別',
    modeSmall: "小モード",
    modeLarge: "大モード",
    modeSmallTitle: '小モード：縦棒表示、より多くのリクエストを収容（デフォルト）',
    modeLargeTitle: '大モード：カード表示、より詳細なリクエスト情報',
    // 2026-08-06: 業務 / プローブフィルタ + タイトル（zh-CN との整合）
    business: '業務',
    probe: 'プローブ',
    businessTitle: '実業務のリクエストのみ表示',
    probeTitle: 'プローブリクエストのみ表示',
    cacheWindow: 'キャッシュ / ウィンドウ', connectionDetailTitle: '接続詳細を表示',
    dimensionCredential: '認証情報', dimensionVendor: 'ベンダー', dimensionProvider: 'プロバイダー', dimensionModel: 'モデル',
    statusOpen: '接続済み', statusConnecting: '接続中', statusReconnecting: '再接続中',
    statusUnsupported: '未対応', statusClosed: '未接続',
    queueStreamConnecting: 'ライブストリームに接続中です。接続後、このエリアのデータは自動的に更新されます…',
    queueStreamClosed: 'ライブストリームが切断されました。自動的に再接続しています…',
    queueStreamUnsupported: 'このブラウザはライブストリーム（EventSource）に対応していません。このエリアでリアルタイムデータを受信できません。',
    queueStreamAwaiting: 'ライブストリームに接続済み、最初のデータを受信しています…',
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

    filterStatus: 'ステータス',
    filterModel: 'モデル',
    filterGroup: 'フィルター',
    filterProvider: 'プロバイダー',
    filterVendor: 'ベンダー',
    filterAgent: 'クライアント',  // 2026-08-06 zh-CN との整合
    clearFilters: 'フィルターをクリア',
    status: {
      in_progress: '処理中',
      success: '成功',
      failure: '失敗',
      rate_limited: '制限あり',
    },
  },
  charts: {
    gradeA: "Grade A",
    gradeB: "Grade B",
    gradeC: "Grade C",
    gradeD: "Grade D",
    gradeF: "Grade F",
    avgScore: "平均スコア",
    healthDistribution: "ヘルス分布",
    totalSessions: "合計 {n} セッション",
    newSessions: "新規セッション",
    activeSessions: "アクティブセッション",
    closedSessions: "終了セッション",
    costUSD: "コスト (USD)",
    sessionCount: "セッション数",
    sessionTrend: "セッション傾向",
    /** 空状態（2026-10-27 第 14 インクリメント）：以前は 2 箇所とも `dashboard.noData` を使っていた。 */
    emptyNoResultHealthGrade: 'この期間のヘルス分布データはありません。',
    emptyNoResultSessionTrend: 'この期間のセッション傾向データはありません。',
  },



  performance: {


    avgLatency: '平均レイテンシ',

  },

  providerUsage: {
    title: 'プロバイダー使用状況',
    subtitle: '{period} 全プロバイダーの消費量（照合可能）',
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
    /** 空状態：使用量記録が無い（降格状態の文言は `dataLifecycle.usageCost.degraded.title` 側で別）。 */
    emptyNoResult: 'この期間の使用量記録はありません。',
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
    childRequestsTitle: '子リクエスト {count} 件（展開して表示）',
    childRequestsEmpty: '子リクエスト（まだプッシュはありません。展開して表示）',
    actionTrailTitle: 'アクション履歴 {count} 件（展開して表示）',
    actionTrailEmpty: 'アクション履歴（まだプッシュはありません。展開して表示）',
  },
}
