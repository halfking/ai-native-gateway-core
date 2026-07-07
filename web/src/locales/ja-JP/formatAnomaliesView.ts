// formatAnomaliesView.ts — フォーマット異常監視ページ（ja-JP）。
export default {
  pageTitle: 'フォーマット異常監視',
  pageSubtitle: 'プロバイダーレスポンスのフォーマット変更、トークン抽出失敗、互換性の問題をすばやく確認できます。',

  stats: {
    total: '異常総数',
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
    unresolvedOnly: '未解決のみ',
    query: '検索',
    refresh: '更新',
  },

  anomalyType: {
    all: 'すべての異常タイプ',
    missing_usage_block: 'Usageブロック欠落',
    zero_completion_tokens: 'Completion Tokens が 0',
    extraction_failed: '抽出失敗',
    unexpected_structure: '予期しない構造',
    null_usage_values: 'Usage値がNull',
    token_mismatch: 'トークン不一致',
    missing_provider_tokens: 'プロバイダートークン欠落',
    missing_client_tokens: 'クライアントトークン欠落',
    json_parse_error: 'JSON解析エラー',
    missing_finish_reason: 'Finish Reason欠落',
    missing_content: 'Content欠落',
  },

  anomalyTypeDescription: {
    missing_usage_block: '上流レスポンスにusageブロックがない',
    zero_completion_tokens: 'レスポンスにコンテンツがあるがcompletion_tokensが0',
    extraction_failed: 'レスポンスから使用可能なusage情報を抽出できない',
    unexpected_structure: '上流が返した構造が期待と一致しない',
    null_usage_values: 'usageフィールドは存在するが値がnull',
  },

  severity: {
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
    tokenInfo: 'トークン情報',
    status: 'ステータス',
    actions: '操作',
    loading: '読み込み中...',
    noData: '異常レコードが見つかりません',
    viewDetail: '詳細',
    expectedTokens: '期待値: {count}',
    actualTokens: '実際: {count}',
  },

  token: {
    expected: '期待値',
    actual: '実際',
  },

  pager: {
    prev: '前へ',
    next: '次へ',
    summary: 'ページ {page} / {totalPages}、全{total}件',
  },

  detail: {
    title: '異常の詳細',
    close: '閉じる',
    requestId: 'Request ID',
    detectedAt: '検出日時',
    provider: 'プロバイダー',
    model: 'モデル',
    outboundModel: '送信モデル',
    usageSource: 'Usage Source',
    responseStructure: 'レスポンス構造',
    responseSample: 'レスポンスサンプル',
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
    markFailed: 'マーク失敗',
    needSuperAdmin: 'スーパー管理者権限が必要です',
  },
}
