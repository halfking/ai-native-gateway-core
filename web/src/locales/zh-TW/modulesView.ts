// modulesView.ts — Modules management page.
export default {
  pageTitle: 'Modules',
  pageSubtitle: 'Centralized management of enterprise-grade feature modules — enable or disable capabilities on demand.',
  modulesEnabled: 'modules enabled',
  loading: 'Loading…',

  category: {
    compression: 'Request compression',
    session: 'Session management',
    security: 'Security',
    rate_limit: 'Rate limiting',
    general: 'General',
    integration: 'Integration',
  },

  status: {
    enabled: 'Enabled',
    disabled: 'Disabled',
    processing: 'Processing…',
    enabledAction: 'Disable this module',
    disabledAction: 'Enable this module',
  },

  dangerLevel: {
    safe: 'Safe',
    warn: 'Caution',
    danger: 'Dangerous',
    breaking: 'Critical',
    unknown: 'Unknown',
  },

  tabs: {
    overview: 'Overview',
    config: 'Configuration',
    integration: 'Integration',
  },

  overview: {
    sectionDescription: 'Description',
    sectionCapabilities: 'Capabilities',
    labelKey: 'Module key',
    labelDanger: 'Danger level',
    labelConfigCount: 'Config items',
    labelStatus: 'Current status',
    viewAllSettings: 'View all system settings',
  },

  config: {
    noSettings: 'This module has no configurable settings.',
    sourceDefault: 'Default',
    switchOn: 'On',
    switchOff: 'Off',
    inputPlaceholder: 'Enter {description}',
  },

  integration: {
    docsLabel: '對接文件：',
    stepsTitle: '配置步驟',
    enabledStatus: '整合已啟用',
    disabledHint: '整合未啟用 — 請先開啟此模組',
    prerequisitesTitle: '前置模組',
    prerequisitesHint: '請先啟用以上前置模組，再開啟此模組',
    feishuSteps: [
      '在飛書開放平台建立自訂機器人',
      '複製 Webhook URL 並貼上到下方設定中',
      '（選填）設定簽章驗證權杖',
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
  },

  empty: {
    selectModule: 'Select a module to view details and configuration',
  },

  error: {
    loadFailed: 'Failed to load module list',
    operationFailed: 'Operation failed',
    saveFailed: 'Failed to save configuration',
  },
}
