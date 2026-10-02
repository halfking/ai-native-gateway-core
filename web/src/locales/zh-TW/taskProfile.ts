// taskProfile.ts — 任务档案页文案（v2 闭环 P0③，2026-09-24）。
export default {
    refresh: '重新整理',
    refreshing: '重新整理中…',
    loadFailed: '載入失敗',
    noData: '暫無檔案資料',
  registry: {
    version: '檔案版本',
    types: '任務類型數',
    correctedTotal: '視窗內修正總數',
    pendingSuggestions: '待裁決升檔',
  },
  table: {
    taskType: '任務類型',
    description: '描述',
    tier: '目前檔位',
    fallbacks: '降級鏈',
    minConf: '最低置信度',
    total: '修正樣本',
    rate: '修正率',
    suggestion: '建議檔位',
  },
  action: {
    apply: '套用升檔建議',
    applyConfirm: '把修正驅動的分層建議寫入 tier 設定（task_type_tier_config）？',
    applyDone: '已套用：{types}',
    applyNone: '目前沒有滿足門檻的升檔建議',
    reload: '重新載入 overlay 檔案',
    reloadConfirm: '重新載入 TASKPROFILE_OVERLAY 檔案？未設定時復位為內建預設。',
    reloadDone: '檔案已重新載入：{version}',
    exportCsv: '匯出修正 CSV',
    days: '統計視窗(天)',
  },
  status: {
    applying: '套用中…',
    reloading: '重新載入中…',
    exporting: '匯出中…',
  },
}
