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
  },

  overview: {
    sectionDescription: '模組描述',
    sectionCapabilities: '能力清單',
    labelKey: '模組識別',
    labelDanger: '危險等級',
    labelConfigCount: '設定項數',
    labelStatus: '目前狀態',
    viewAllSettings: '檢視所有系統設定',
    testConnection: '測試連線',
    testSuccess: '連線測試成功',
    testFailed: '連線測試失敗',
    testInProgress: '正在傳送測試訊息…',
    dependenciesTitle: '依賴模組',
    required: '必要',
    optional: '選填',
    enableAllDeps: '一鍵啟用 {count} 個必需依賴',
    enableAllDepsHint: '後端會按穩定順序開啟所有未啟用的必需依賴後再啟用本模組',
    cascading: '正在開啟依賴…',
    cascadeSuccess: '已自動開啟 {count} 個依賴：{names}',
    cascadeEmpty: '所有依賴已啟用，模組已正常開啟',
    cascadeAlreadyEnabled: '模組已啟用，無需級聯',
    cascadeLogTitle: '本次級聯開啟的依賴：',

    sectionDependencies: '依賴模組',

    dependencyDisabled: '依賴模組未啟用',

    notEnabled: '未啟用',
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
  },

  handoff: {
    groupMaster: '主開關',
    groupMasterHint: '總開關與觸發模式：決定會話交接的啟用方式與觸發語義',
    groupTrigger: '觸發閾值',
    groupTriggerHint: '4 種觸發條件並行生效，達到任一即觸發交接；多閾值同時觸發時取最嚴格的',
    groupSummary: '摘要產生',
    groupSummaryHint: '摘要引擎與提示詞範本：決定如何把舊會話壓縮成新會話可讀上下文',
    groupSafety: '安全限制與通知',
    groupSafetyHint: '冷卻、單會話上限、失敗重試與日誌/Webhook 通知',
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
    wechatSteps: [
      '在企業微信管理後台建立自建應用',
      '（選填）在「接收訊息」中設定回呼 URL 和 Token',
      '複製企業 CorpID、AgentID、Secret 填入下方設定',
      '（選填）設定群機器人 Webhook URL 以使用群組訊息推送',
      '（選填）設定 EncodingAESKey 以啟用回呼加密',
      '開啟「微信機器人整合」開關',
      '確保前置模組（壓縮管理、注入檢測、會話快取、會話稽核）均已啟用',
    ],
    wechatBotIntegration: '微信機器人整合',
    dingtalkSteps: [
      '在釘釘群設置中加入「自定義機器人」，安全設置選「加簽」並複製 Webhook',
      '將 Webhook URL 貼到下方設定，並填入加簽 Secret',
      '（選用）在開發者後台建立企業內部應用，填入 AppKey/AppSecret/AgentID 啟用工作通知',
      '將審批回呼地址（/api/webhooks/dingtalk/approval-callback）設定為機器人接收訊息',
      '開啟「釘釘機器人整合」開關',
      '確保前置模組（壓縮管理、注入檢測、會話快取、會話審計）均已啟用',
    ],
    dingtalkBotIntegration: '釘釘機器人整合',
  },

  empty: {
    selectModule: '選擇一個模組以檢視詳細資料與設定',
  },


  error: {
    loadFailed: '模組列表沒能載入出來，請重試',
    operationFailed: '模組操作沒能完成，請重試',
    saveFailed: '模組設定沒能儲存，請重試',
    testFailed: '模組測試沒能通過，請重試',
  },

  // 同步的扁平键

  modeBannerTitle: '本頁僅做三件事：',

  modeBannerBody: '已裝模組開關、已授權未開通模組下載；本頁不提供價格、訂單或套餐入口（採購通道另列公告）。',
}