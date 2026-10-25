// Japanese locale for MemoraStatusButton
export default {
  state: {
    disabled: '無効',
    paused: '切断',
    ok: '接続済み',
    error: '接続できません。ネットワークを確認して再試行してください。',
    loading: '確認中',
    detectFailed: '結果を検出できませんでした。再試行してください。',
  },

  panel: {
    title: 'Memora 接続',
    closeLabel: '閉じる',

    fields: {
      serviceUrl: 'サービス URL',
      recentLatency: '最近のレイテンシ',
      error: 'エラー',
      check: '確認',
      writeQueue: '書き込みキュー',
      processedErrored: '処理済み / エラー',
      consecutiveErrors: '連続エラー',
      recentWriteError: '最近の書き込みエラー',
    },

    actions: {
      processing: '処理中…',
      reconnect: '再接続',
      checkConnectivity: '接続性を確認',
      disconnect: '書き込みを切断',
      refresh: '更新',
      sessionContext: 'セッションコンテキスト',
    },
  },

  error: {
    connectionFailed: '接続に失敗しました',
    checkFailed: '確認に失敗しました',
  },
}
