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
    docsLabel: 'Docs: ',
    stepsTitle: 'Setup steps',
    enabledStatus: 'Integration enabled',
    disabledHint: 'Integration disabled — please enable the module first',
    prerequisitesTitle: 'Prerequisites',
    prerequisitesHint: 'Please enable the prerequisite modules above before enabling this module',
    feishuSteps: [
      'Create a custom bot in the Feishu open platform',
      'Copy the webhook URL and paste it into the config below',
      '(Optional) Configure the signature verification token',
      'Turn on the "Feishu bot integration" switch',
    ],
    feishuBotIntegration: 'Feishu bot integration',
    wechatSteps: [
      'Create a self-built app in the WeCom admin console',
      '(Optional) Configure callback URL and Token under "Receive Messages"',
      'Copy the CorpID, AgentID, and Secret into the config below',
      '(Optional) Configure the group robot Webhook URL for group message push',
      '(Optional) Configure EncodingAESKey to enable callback encryption',
      'Turn on the "WeChat bot integration" switch',
      'Ensure prerequisite modules (compression, injection detection, session cache, session audit) are all enabled',
    ],
    wechatBotIntegration: 'WeChat bot integration',
    dingtalkSteps: [
      'Add a "Custom Robot" in a DingTalk group, choose "Sign" security and copy the Webhook',
      'Paste the Webhook URL below and fill in the Sign Secret',
      '(Optional) Create an internal app in the dev console and fill AppKey/AppSecret/AgentID to enable work notification',
      'Configure the approval callback URL (/api/webhooks/dingtalk/approval-callback) as the bot message receiver',
      'Enable the "DingTalk bot integration" switch',
      'Ensure prerequisite modules (compression, injection detection, session cache, session audit) are enabled',
    ],
    dingtalkBotIntegration: 'DingTalk bot integration',
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