// taskProfile.ts — 任务档案页文案（v2 闭环 P0③，2026-09-24）。
export default {
    refresh: '更新',
    refreshing: '更新中…',
    loadFailed: '読み込みに失敗しました',
    noData: 'プロファイルデータなし',
  registry: {
    version: 'レジストリ版',
    types: 'タスクタイプ数',
    correctedTotal: '期間内の修正合計',
    pendingSuggestions: '裁決待ち階層変更',
  },
  table: {
    taskType: 'タスクタイプ',
    description: '説明',
    tier: 'ティア',
    fallbacks: 'フォールバック chain',
    minConf: '最低信頼度',
    total: '修正サンプル',
    rate: '修正率',
    suggestion: '提案ティア',
  },
  action: {
    apply: '提案を適用',
    applyConfirm: '修正駆動のティア提案を tier 設定（task_type_tier_config）に書き込みますか？',
    applyDone: '適用しました：{types}',
    applyNone: '現在しきい値を満たす提案はありません',
    reload: 'overlay を再読み込み',
    reloadConfirm: 'TASKPROFILE_OVERLAY プロファイルを再読み込みしますか？未設定の場合は組み込み既定に戻ります。',
    reloadDone: 'レジストリを再読み込みしました：{version}',
    exportCsv: '修正 CSV をエクスポート',
    days: '集計期間（日）',
  },
  status: {
    applying: '適用中…',
    reloading: '再読み込み中…',
    exporting: 'エクスポート中…',
  },
}
