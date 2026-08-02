// modelIntegrityView.ts — モデル整合性監視ページ（ja-JP）。
export default {
  pageTitle: 'モデル整合性監視',
  pageSubtitle: 'モデルの置き換え、レスポンスの切り詰め、空のレスポンス、繰り返しコンテンツ、フィンガープリントのドリフトを追跡します。',

  tabs: {
    events: '整合性イベント',
    drift: 'フィンガープリントドリフト',
  },

  stats: {
    total: '総イベント数',
    unresolved: '未解決',
    critical: '重大',
    window: '統計ウィンドウ',
  },

  filter: {
    provider: 'プロバイダー',
    providerPlaceholder: 'プロバイダーを選択…',
    model: 'モデル',
    modelPlaceholder: 'モデルを選択…',
    anomalyType: '異常タイプ',
    anomalyTypePlaceholder: '異常タイプを選択…',
    severity: '重要度',
    unresolvedOnly: '未解決のみ',
    query: '検索',
    refresh: '更新',
  },

  anomalyType: {
    all: 'すべての異常タイプ',
    model_mismatch: 'モデル不一致',
    finish_refusal: '拒否 / コンテンツフィルター',
    finish_truncation: '終了時切り詰め',
    token_arith_fail: 'トークン計算失敗',
    empty_response: '空のレスポンス',
    repeated_content: '繰り返しコンテンツ',
    fingerprint_drift: 'フィンガープリントドリフト',
  },

  anomalyTypeDescription: {
    model_mismatch: '上流がリクエストと異なるモデルを返しました（サイレント置換の可能性）',
    finish_refusal: '上流が拒否または content_filter を返しました',
    finish_truncation: '長さ / max_tokens により切り詰められました',
    token_arith_fail: 'prompt + completion が total と一致しません',
    empty_response: 'ストリームがコンテンツもトークンも生成しませんでした',
    repeated_content: 'レスポンステキストに大きな繰り返しブロックが含まれています（モデルのループの可能性）',
    fingerprint_drift: 'system_fingerprint がベースラインからドリフトしました',
  },

  severity: {
    all: 'すべての重要度',
    critical: '重大',
    high: '高',
    medium: '中',
    low: '低',
  },

  status: {
    resolved: '解決済み',
    unresolved: '未解決',
  },

  table: {
    detectedAt: '検出日時',
    severity: '重要度',
    anomalyType: '異常タイプ',
    providerModel: 'プロバイダー / モデル',
    requestId: 'Request ID',
    actual: '実際',
    status: 'ステータス',
    actions: '操作',
    loading: '読み込み中...',
    noData: '整合性イベントが見つかりません',
    viewDetail: '詳細',
  },

  drift: {
    days: '日',
    query: '検索',
    noData: '選択したウィンドウ内でフィンガープリントドリフトは検出されませんでした',
  },

  pager: {
    prev: '前へ',
    next: '次へ',
    summary: 'ページ {page} / {totalPages}、全{total}件',
  },

  detail: {
    title: '整合性イベントの詳細',
    close: '閉じる',
    requestId: 'Request ID',
    detectedAt: '検出日時',
    provider: 'プロバイダー',
    model: 'モデル',
    outboundModel: '送信モデル',
    credential: 'クレデンシャル ID',
    expected: '期待値',
    actual: '実際',
    context: 'コンテキスト',
    sample: 'サンプル',
    resolutionNotes: '解決メモ',
    resolutionNotesPlaceholder: '修正内容を記録して後から追跡可能に',
    markResolved: '解決済みにする',
    processing: '処理中...',
    resolutionInfo: '解決情報',
    noNotes: '解決メモなし',
  },

  error: {
    loadFailed: '読み込み失敗',
    summaryLoadFailed: '統計の読み込み失敗',
    driftLoadFailed: 'フィンガープリントドリフトの読み込み失敗',
    markFailed: 'マーク失敗',
    needSuperAdmin: 'スーパー管理者権限が必要です',
  },
}
