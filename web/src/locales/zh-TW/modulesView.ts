// modulesView.ts — 模組管理頁(ModulesView)文案。
export default {
  pageTitle: '模組管理',
  pageSubtitle: '企業級功能模組統一管理，按需開啟/關閉各項能力',
  modulesEnabled: '個模組已啟用',
  loading: '載入中…',

  category: {
    compression: '請求壓縮',
    session: '會話管理',
    security: '安全防護',
    rate_limit: '流量控制',
    general: '通用能力',
    integration: '整合對接',
  },

  status: {
    enabled: '已啟用',
    disabled: '已停用',
    processing: '處理中…',
    enabledAction: '停用此模組',
    disabledAction: '啟用此模組',
  },

  dangerLevel: {
    safe: '安全',
    warn: '注意',
    danger: '危險',
    breaking: '高危',
    unknown: '未知',
  },

  tabs: {
    overview: '概覽',
    config: '設定',
    integration: '整合',
    status: '執行狀態',
    routing: '路由規則',
  },

  overview: {
    sectionDescription: '模組描述',
    sectionCapabilities: '能力清單',
    sectionRequirements: '依賴模組',
    labelKey: '模組識別',
    labelDanger: '危險等級',
    labelConfigCount: '設定項數',
    labelStatus: '目前狀態',
    viewAllSettings: '檢視所有系統設定',
    requirementsMet: '所有依賴模組已啟用',
    requirementsMissing: '以下依賴模組未啟用，相關功能可能受限：',
    jumpToModule: '前往設定',
    testConnection: '測試連線',
    testSuccess: '連線測試成功',
    testFailed: '連線測試失敗',
    testInProgress: '正在傳送測試訊息…',
  },

  config: {
    noSettings: '此模組沒有可設定的項目。',
    sourceDefault: '預設',
    sourceEnv: '環境變數',
    sourceDb: '資料庫',
    switchOn: '啟用',
    switchOff: '停用',
    inputPlaceholder: '輸入{description}',
    sections: {
      connection: '連線',
      alerts: '告警轉發',
      approvals: '審核通知',
      commands: '指令面板',
      security: '安全',
      general: '通用',
    },
  },

  feishu: {
    connectionHint: '在飛書開放平台建立自訂機器人後，將 Webhook 網址貼到下方。詳細步驟請參考飛書官方文件。',
    callbackUrlLabel: '回呼網址（需在飛書機器人後台設定）',
    callbackUrlHelp: '將此網址填入飛書自訂機器人 → 回呼設定，用於接收飛書端的審核操作。',
    whitelistHelp: '允許透過飛書執行指令的使用者 OpenID，多個以英文逗號分隔。留空時依 commands.admin_only 決定是否全部允許。',
    quietHoursHelp: '免打擾時段內僅 critical 等級告警會推送，避免夜間打擾。支援跨夜時段（22:00 → 08:00）。',
    commandsHelp: '啟用後管理員可在飛書對話中透過指令與系統互動（/status /help /stats /audit /test）。',
    signatureHelp: '生產環境務必啟用。啟用後飛書回呼必須攜帶有效簽章（HMAC-SHA256）且時間戳記在視窗內。',
  },

  integration: {
    docsLabel: '對接文件：',
    stepsTitle: '設定步驟',
    enabledStatus: '整合已啟用',
    disabledHint: '整合未啟用 — 請先開啟此模組',
    feishuSteps: [
      '在飛書開放平台建立自訂機器人',
      '複製 Webhook 網址並貼到下方設定',
      '（選用）設定簽章驗證權杖和加密金鑰',
      '設定完成後點擊「測試連線」驗證',
      '開啟「飛書機器人整合」開關',
    ],
    feishuBotIntegration: '飛書機器人整合',
  },

    importCsv: '批次匯入 CSV',
    close: '關閉',
    csvImportResult: '批次匯入結果',
    csvImportSuccess: '已匯入 {imported} 條，跳過 {skipped} 條',
    csvErrorRow: '第 {row} 行：{error}',
  empty: {
    selectModule: '選擇一個模組以檢視詳細資料與設定',
  },

  routing: {
    title: '飛書路由規則',
    addNew: '新增規則',
    cancel: '取消',
    save: '儲存',
    formTitle: '新增飛書 OpenID 規則',
    openId: '飛書 OpenID',
    openIdPlaceholder: 'ou_xxxxxxxx',
    displayName: '顯示名',
    userRole: '使用者角色',
    priority: '優先順序',
    note: '備註',
    enabled: '啟用',
    riskLevels: '風險等級',
    actions: '操作',
    enable: '啟用',
    disable: '停用',
    delete: '刪除',
    loading: '載入中…',
    empty: '尚無規則。點擊「新增規則」建立第一條。',
  },

  error: {
    loadFailed: '載入模組清單失敗',
    operationFailed: '操作失敗',
    saveFailed: '儲存設定失敗',
    testFailed: '測試失敗',
  },
}