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
    docsLabel: 'ドキュメント：',
    stepsTitle: '設定手順',
    enabledStatus: '統合が有効です',
    disabledHint: '統合が無効です — モジュールを有効にしてください',
    prerequisitesTitle: '前提モジュール',
    prerequisitesHint: 'このモジュールを有効にする前に、前提モジュールを有効にしてください',
    feishuSteps: [
      'Feishuオープンプラットフォームでカスタムボットを作成',
      'Webhook URLをコピーして下の設定に貼り付け',
      '（任意）署名検証トークンを設定',
      '「Feishuボット統合」スイッチをオンにする',
    ],
    feishuBotIntegration: 'Feishuボット統合',
    wechatSteps: [
      'WeCom管理コンソールで自社アプリを作成',
      '（任意）「メッセージ受信」でコールバックURLとTokenを設定',
      'CorpID、AgentID、Secretを下の設定にコピー',
      '（任意）グループボットのWebhook URLを設定',
      '（任意）コールバック暗号化のEncodingAESKeyを設定',
      '「WeChatボット統合」スイッチをオンにする',
      '前提モジュール（圧縮管理、注入検出、セッションキャッシュ、セッション監査）がすべて有効であることを確認',
    ],
    wechatBotIntegration: 'WeChatボット統合',
    dingtalkSteps: [
      'DingTalkグループに「カスタムボット」を追加し、セキュリティ「署名」を選択してWebhookをコピー',
      '下部の設定にWebhook URLを貼り付け、署名Secretを入力',
      '（任意）開発者コンソールで社内アプリを作成し、AppKey/AppSecret/AgentIDを入力してワーク通知を有効化',
      '承認コールバックURL（/api/webhooks/dingtalk/approval-callback）をボットのメッセージ受信先に設定',
      '「DingTalkボット統合」スイッチを有効化',
      '前提モジュール（圧縮、注入検知、セッションキャッシュ、セッション監査）が有効であることを確認',
    ],
    dingtalkBotIntegration: 'DingTalkボット統合',
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
