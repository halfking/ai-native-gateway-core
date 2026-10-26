export default {

  // License Management

  // Fault Management

  // Auto Update

  // Center Operations

  // VibeCoding
  vibecoding: {
    title: 'VibeCoding',
    loadFailed: '資料沒能載入出來，請重試',
    fillRequired: '請填寫必填項',
    projects: '項目',
    sessions: '會話',
    codeReviews: '代碼審查',
    createProject: '創建項目',
    createProjectTitle: '創建新項目',
    createProjectSuccess: '項目創建成功',
    createProjectFailed: '項目沒能建立起來，請重試',
    newSession: '新會話',
    createSessionTitle: '創建新會話',
    createSessionSuccess: '會話創建成功',
    createSessionFailed: '會話沒能建立起來，請重試',
    viewSessions: '查看會話',
    viewReviews: '查看審查',
    showAll: '顯示全部',
    reviewDetail: '審查詳情',
    /**
     * 空狀態（2026-10-27 第 14 增量 · 空狀態 5 分）。
     * ★ 三張表原先共用一句 `hyper.list.empty`（「清單暫時沒有內容」），但它們是**兩種**狀態：
     *   · projects —— 用的是原始陣列 ⇒ 空 = 還沒創建過任何項目（FirstUse）
     *   · sessions / reviews —— 用的是**篩選後**的陣列 ⇒ 空 = 被篩掉了（Filtered）
     *   ⇒ 同一句話覆蓋兩種狀態，等於把「你還沒開始」和「你的篩選條件太窄」講成同一件事。
     */
    emptyFirstUseProjects: '還沒有創建任何項目。',
    emptyFilteredSessions: '目前篩選條件下沒有會話記錄。',
    emptyFilteredReviews: '目前篩選條件下沒有代碼審查記錄。',
    projectName: '項目名稱',
    projectNamePlaceholder: '請輸入項目名稱',
    language: '程式語言',
    languagePlaceholder: '例如: TypeScript',
    framework: '框架',
    frameworkPlaceholder: '例如: Vue 3',
    projectId: '項目ID',
    sessionName: '會話名稱',
    taskType: '任務類型',
    taskTypePlaceholder: '例如: code review, refactor, explain',
    startedAt: '開始時間',
    endedAt: '結束時間',
    filePath: '檔案路徑',
    score: '評分',
    summary: '總結',
    issues: '問題',
    suggestions: '建議',
    category: '分類',
    reviewedAt: '審查時間',
    line: '行號',
    severity: '嚴重度',
    message: '訊息',
  },


  overview: {




























    nodeDetailSuperAdminOnlyHint: '僅超級管理員可以維護節點；目前以唯讀方式展示。',
    nodeDetailDefaultTenantOnlyHint: '僅 default 租戶可以維護節點；目前以唯讀方式展示。',

















    pingFailedWithReason: '會話 Ping 失敗：{reason}。請重試',
    saveSettingFailedWithReason: '設定沒能儲存：{reason}。請重試',
    maintainFailed: '維護沒能完成，請重試',
    modelMaintainFailed: '模型維護沒能完成，請重試',
    fpSlotLoadFailed: '指紋槽位沒能載入出來，請重試',
    fpSlotProviderMissing: '缺少 provider_id，指紋槽位沒能載入。請先選擇供應商再試',
    accessErrorsHint: '失敗呼叫 + 路由錯誤 · 開啟原始請求詳情',
    recentRequestsHint: '最近 1 小時{source} · 查看請求詳情',
  }
,

}