// sessionTimeline.ts — SessionTurnsTimeline.vue 用語 (OBS-FE5 ターンタイムライン)
// カバー：遅延 null フォールバック / 更新 / エラー / 空 / もっと読み込む / 全件読み込み済み / ネットワークエラー
export default {
  latencyUnknown: '不明',
  refresh: '更新',
  refreshing: '更新中…',
  retry: '再試行',
  empty: 'このセッションには記録がありません',
  loading: '読み込み中…',
  loadMore: '輪次をもっと読み込む',
  allLoaded: '{n} 輪、すべて読み込み済み',
  errors: {
    network: 'ネットワークエラー、接続を確認して再試行してください',
  },
}
