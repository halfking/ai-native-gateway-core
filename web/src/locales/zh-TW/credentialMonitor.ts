// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// credentialMonitor.ts — 憑證監控頁(CredentialMonitorView)文案。
// 命名空間：page / filter / summary / table / drawer / dialog / status / action / error / chart。
// 後端列舉字串（ready/degraded/healthy/warning/broken 等）保持原樣 — 翻譯會破壞 API 呼叫。
// 複用 common/keys 已有的按鈕/狀態詞彙；本頁特有術語集中在本檔案。
export default {
  page: {
    title: '憑證監控',
  },
  table: {
    cell: {
      modelsNotMeasured: '該憑據本次未回傳模型計數（未測量），不是「0 個可用」',
    },
  },
  drawer: {
    tab: {
      overview: '基本資訊',
      models: '模型',
      requests: '請求資料',
    },
    operationFailedWithReason: '操作失敗：{reason}。請重試',
    offlineFailedWithReason: '下線失敗：{reason}。請重試',
    onlineFailedWithReason: '上線失敗：{reason}。請重試',
    degradeFailedWithReason: '降級失敗：{reason}。請重試',
    upgradeFailedWithReason: '升級失敗：{reason}。請重試',
    settingFailedWithReason: '設定失敗：{reason}。請重試',
    manualDisabledTooltip: '已透過手動方式停用，自動偵測不會觸碰。可手動啟動',
    availableTooltip: '目前可用，可手動停用',
    autoControlledTooltip: '由自動偵測控制（broken_confirmed 等）。可手動停用（強制下線）',
    rowSelectsMonitor: '選擇資料列可聯動查看右側監控',
    selectModelRow: '選擇左側模型列可查看',
  },
  chart: {
    allHealthy: '全部健康',
    errorsWhenHealthy: '錯誤類型分佈：全部健康',
    errorsTitle: '錯誤類型分佈',
    /**
     * 空狀態（2026-10-27 第 14 增量 · 空狀態 5 分）。
     * ★ 原先這裡用的是通用鍵 `common.feedback.noData`（「暫時沒有內容可顯示」），
     *   頁面另有一條硬編碼中文提示「請調整時間範圍或篩選條件」。
     *   ⇒ 本輪併成整句（時間範圍 + 篩選條件 + 下一步：放寬條件）。
     *   kind = Filtered：查得到，但被目前的時間範圍與篩選條件篩掉了。
     */
    emptyFiltered: '目前時間範圍與篩選條件下沒有憑證記錄，可放寬條件後再檢視。',
  },
  error: {
    selectFirst: '請先選擇憑證',
    clearFailed: '清除失敗: ',
    batchFailed: '批次操作失敗: ',
    superAdminOnlyHint: '僅超級管理員可執行狀態修正；目前為唯讀。',
    superAdminOnlyError: '僅超級管理員可執行狀態修正操作。',
  },
}
