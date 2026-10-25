// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// credentialMonitor.ts — 認証情報監視ページ (CredentialMonitorView) 文案。
// 名前空間：page / filter / summary / table / drawer / dialog / status / action / error / chart。
// バックエンドの列挙文字列（ready/degraded/healthy/warning/broken など）はそのまま保持します — 翻訳すると API 呼び出しを壊すためです。
// common/keys の既存のボタン/状態語彙を再利用し、このページ特有の用語はこのファイルに集約します。
export default {
  page: {
    title: '認証情報モニター',
  },
  table: {
    cell: {
      modelsNotMeasured: 'この資格情報は今回モデル件数を返しませんでした（未計測）。「0 件利用可能」という意味ではありません',
    },
  },
  drawer: {
    tab: {
      overview: '概要',
      models: 'モデル',
      requests: 'リクエスト',
    },
    operationFailedWithReason: '操作に失敗しました：{reason}。再試行してください。',
    offlineFailedWithReason: 'オフライン化に失敗しました：{reason}。再試行してください。',
    onlineFailedWithReason: 'オンライン化に失敗しました：{reason}。再試行してください。',
    degradeFailedWithReason: '降格に失敗しました：{reason}。再試行してください。',
    upgradeFailedWithReason: 'アップグレードに失敗しました：{reason}。再試行してください。',
    settingFailedWithReason: '設定に失敗しました：{reason}。再試行してください。',
    manualDisabledTooltip: '手動で無効化済み。自動プローブは触れません。手動で起動できます',
    availableTooltip: '現在利用可能。手動で無効化できます',
    autoControlledTooltip: '自動プローブで制御されています（broken_confirmed など）。手動で無効化（強制オフライン化）できます',
    rowSelectsMonitor: '行を選択すると右側の監視表示に連動します',
    selectModelRow: '左側のモデル行を選択すると表示できます',
  },
  chart: {
    allHealthy: 'すべて健全',
    errorsWhenHealthy: 'エラー分布：すべて健全',
    errorsTitle: 'エラー分布',
  },
  error: {
    selectFirst: 'まず認証情報を選択してください',
    clearFailed: 'クリア失敗: ',
    batchFailed: '一括操作失敗: ',
    superAdminOnlyHint: 'ステータス修正はスーパー管理者のみ実行できます。現在は読み取り専用です。',
    superAdminOnlyError: 'ステータス修正はスーパー管理者のみ実行できます。',
  },
}
