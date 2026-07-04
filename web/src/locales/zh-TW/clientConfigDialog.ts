// Chinese (Traditional) locale for ClientConfigDialog
export default {
  title: '{tool} 配置產生器',
  close: '關閉',
  
  step1: {
    title: '① 選擇 API Key（目前租戶下所有金鑰）',
    refresh: '重新整理',
    refreshing: '重新整理中…',
    loading: '載入中…',
    empty: {
      title: '暫無 API Key',
      description: '目前租戶下還沒有可用的 API Key。管理員可點擊下方按鈕申請一個新 Key。',
      applyButton: '申請新金鑰',
    },
    selected: '已選：',
  },
  
  step2: {
    title: '② 作業系統',
    pathHint: '配置檔案路徑：',
  },
  
  step3: {
    title: '③ 選擇模型範圍',
    featured: '熱門模型（路由 featured 配置）',
    all: '全部可用模型（從閘道器拉取）',
    featuredPreview: {
      loading: '載入中…',
      empty: '路由中尚未配置熱門模型',
      manageButton: '前往「路由策略」配置 →',
      manage: '管理 →',
    },
    allModels: {
      selectAll: '全選目前',
      deselectAll: '清空',
      selected: '已選',
      filterHint: '（{count} 符合搜尋）',
      searchPlaceholder: '🔍 搜尋模型名稱 / 廠商 / family…',
      loading: '載入中…',
      noMatch: '沒有符合的模型',
    },
  },
  
  footer: {
    generated: '已產生 {count} 個模型配置',
    generate: '產生配置',
    generating: '產生中…',
    regenerate: '重新產生',
  },
  
  results: {
    tabs: {
      file: '配置檔案',
      script: '配置指令碼',
      manual: '手動步驟',
    },
    actions: {
      copyContent: '複製內容',
      downloadFile: '下載檔案',
      downloadScript: '下載指令碼',
      scriptHint: '指令碼自動備份舊配置檔案',
    },
  },
  
  applyDialog: {
    title: '申請新 API Key',
    close: '關閉',
    applicationCode: '應用標識 (application_code)',
    applicationCodePlaceholder: '例如: default-app',
    description: '備註 (description)',
    descriptionPlaceholder: '可選：申請原因 / 用途',
    cancel: '取消',
    submit: '提交申請',
    submitting: '提交中…',
  },
  
  error: {
    applyFailed: '申請失敗',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor 不支援檔案寫入，請於 Settings UI 手動設定',
}
