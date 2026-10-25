// Chinese (Traditional) locale for ClientConfigDialog
export default {
  title: '{tool} 配置產生器',
  close: '關閉',

  step1: {
    title: "① 選擇可用 API Key",
    refresh: '重新整理',
    refreshing: '重新整理中…',
    loading: '載入中…',
    empty: {
      title: '暫無 API Key',
      description: '目前租戶下還沒有可用的 API Key。管理員可在下方按鈕申請一個新 Key。',
      applyButton: '申請新金鑰',
    },
    selected: '已選：',
  },

  step2: {
    title: '② 作業系統',
    pathHint: "設定入口：",
  },

  step3: {
    title: "④ 選擇模型範圍",
    featured: "特色模型（所選金鑰租戶的路由配置）",
    all: '全部可用模型（從閘道器拉取）',
    featuredPreview: {
      empty: "所選租戶尚未配置特色模型",
      manageButton: '前往「路由策略」設定 →',
      manage: '管理 →',
    },
    allModels: {
      selectAll: '全選目前',
      deselectAll: '清空',
      selected: '已選',
      filterHint: '（{count} 符合搜尋）',
      searchPlaceholder: '🔍 搜尋模型名稱 / 廠商 / family…',
      noMatch: '沒有符合的模型',
    },
  },

  retry: "重試",
  otherFamily: "其他",
  gateway: {
    title: "③ 閘道器 API 位址",
    hint: "預設使用目前閘道器網站位址。配置中會包含所選 API Key，請妥善保管產生的檔案與指令碼。",
  },
  provider: {
    label: "供應商名稱",
    hint: "同名且同位址時更新原項。若位址不同，指令碼會停止並提示重新命名。",
  },
  notes: {
    cherryStudio: "請在用戶端設定面板填入所選金鑰；此檔案僅為參數清單。",
    rooCode: "在 Roo Code 設定面板配置 OpenAI Compatible。",
  },

  footer: {
    generated: '已產生 {count} 個模型配置',
    generate: '產生配置',
    generating: '產生中…',
    regenerate: '重新產生',
  },

  results: {
    tabs: {
      yaml: "YAML 片段",
      file: '配置檔案',
      script: '配置指令碼',
      manual: '手動步驟',
    },
    actions: {
      scriptWarning: "指令碼包含金鑰，請下載後在本機執行。產生的指令碼不會在頁面展開。",
      mergeHint: "指令碼先備份舊配置，再合併 {provider}，保留其他供應商",
      copyContent: '複製內容',
      downloadFile: '下載檔案',
      downloadScript: '下載指令碼',
    },
  },

  applyDialog: {
    title: '申請新 API Key',
    applicationCode: '應用標識 (application_code)',
    applicationCodePlaceholder: '例如: default-app',
    description: '備註 (description)',
    descriptionPlaceholder: '可選：申請原因 / 用途',
    cancel: '取消',
    submit: '提交申請',
    submitting: '送出中…',
  },

  error: {
    invalidGatewayURL: "API 位址須為有效的 HTTP(S) URL，且不能包含帳號、查詢參數或片段。",
    invalidProviderName: "供應商名稱須為 1–64 個英文字母、數字、底線或連字號，且以字母或數字開頭。",
    featuredLoadFailed: "特色模型載入失敗，請重試。",
    catalogLoadFailed: "模型目錄載入失敗，請重試。",
    selectModel: "請至少選擇一個模型；特色模型為空時請先到「路由策略」配置。",
    keyUnavailable: "無法讀取所選金鑰，請重新選擇。",
    generateFailed: "配置產生失敗，請重試。",
    copyFailed: "複製失敗，請下載檔案。",
    applyFailed: '申請失敗',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor 不支援檔案寫入，請於 Settings UI 手動設定',
}
