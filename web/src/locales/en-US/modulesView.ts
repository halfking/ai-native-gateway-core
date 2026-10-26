// modulesView.ts — Modules management page.
export default {
  pageTitle: 'Modules',
  pageSubtitle: 'Centralized management of enterprise-grade feature modules. Enable or disable capabilities on demand.',
  // 31 §2.3 / §6: three-section hard constraint banner
  modeBannerTitle: 'This page only does three things:',
  modeBannerBody: 'Toggle installed modules, and download modules that are already licensed but not enabled. No pricing, ordering, or plan entry here (purchase flows are announced separately).',
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
    status: 'Runtime',
  },

  overview: {
    sectionDescription: 'Description',
    sectionCapabilities: 'Capabilities',
    sectionDependencies: 'Dependencies',
    dependenciesTitle: 'Dependencies',
    labelKey: 'Module key',
    labelDanger: 'Danger level',
    labelConfigCount: 'Config items',
    labelStatus: 'Current status',
    dependencyDisabled: 'Dependency module is disabled',
    notEnabled: 'Not enabled',
    viewAllSettings: 'View all system settings',
    required: 'Required',
    optional: 'Optional',
    enableAllDeps: 'Enable {count} missing required deps',
    enableAllDepsHint: 'The backend will enable all missing required deps in stable order, then enable this module.',
    cascading: 'Enabling dependencies…',
    cascadeSuccess: 'Auto-enabled {count} deps: {names}',
    cascadeEmpty: 'All dependencies were already enabled; module is now active.',
    cascadeAlreadyEnabled: 'Module is already enabled, no cascade needed.',
    cascadeLogTitle: 'Cascade-enabled in this run:',
    testConnection: 'Test connection',
    testSuccess: 'Connection test succeeded',
    testFailed: 'Connection test failed',
    testInProgress: 'Sending test message…',
  },

  config: {
    noSettings: 'This module has no configurable settings.',
    sourceDefault: 'Default',
    sourceEnv: 'Environment variable',
    sourceDb: 'Database',
    switchOn: 'On',
    switchOff: 'Off',
    inputPlaceholder: 'Enter {description}',
    sections: {
      connection: 'Connection',
      alerts: 'Alert forwarding',
      approvals: 'Approval notifications',
      commands: 'Command panel',
      security: 'Security',
      general: 'General',
    },
  },

  feishu: {
    connectionHint: 'Create a custom bot in the Feishu open platform, then paste the Webhook URL below. See the official Feishu docs for details.',
  },

  handoff: {
    groupMaster: 'Master switch',
    groupMasterHint: 'Master toggle and trigger mode: determine how session handoff is enabled and when it fires',
    groupTrigger: 'Trigger thresholds',
    groupTriggerHint: '4 trigger conditions run in parallel: any one of them fires handoff; the strictest wins',
    groupSummary: 'Summary generation',
    groupSummaryHint: 'Summary engine and prompt template: how to compress the old session into a new-session-readable context',
    groupSafety: 'Safety limits and notifications',
    groupSafetyHint: 'Cooldown, per-session cap, retry on failure, and log/Webhook notifications',
  },

  integration: {
    docsLabel: 'Docs: ',
    stepsTitle: 'Setup steps',
    enabledStatus: 'Integration enabled',
    disabledHint: 'Integration disabled: please enable the module first',
    feishuSteps: [
      'Create a custom bot in the Feishu open platform',
      'Copy the webhook URL and paste it into the config below',
      '(Optional) Configure signature verification token and encrypt key',
      'Click "Test connection" after configuration',
      'Turn on the "Feishu bot integration" switch',
    ],
    feishuBotIntegration: 'Feishu bot integration',
    wechatBotIntegration: 'WeChat Work bot integration',
    dingtalkBotIntegration: 'DingTalk bot integration',
    wechatSteps: [
      'Create a custom app in the WeCom admin console',
      '(Optional) Configure the callback URL and token under "Receive messages"',
      'Copy the CorpID, AgentID, and Secret into the config below',
      '(Optional) Configure the group bot webhook URL for group message push',
      '(Optional) Configure the EncodingAESKey to enable callback encryption',
      'Turn on the "WeChat Work bot integration" switch',
      'Make sure the prerequisite modules (compression, injection detection, session cache, session audit) are enabled',
    ],
    dingtalkSteps: [
      'Add a "custom bot" in the DingTalk group settings, choose the "Signature" security setting and copy the Webhook',
      'Paste the Webhook URL into the config below and fill in the signature Secret',
      '(Optional) Create an internal app in the developer console, enter the AppKey/AppSecret/AgentID to enable work notifications',
      'Set the approval callback address (/api/webhooks/dingtalk/approval-callback) as the bot message receiver',
      'Turn on the "DingTalk bot integration" switch',
      'Make sure the prerequisite modules (compression, injection detection, session cache, session audit) are enabled',
    ],
  },


  empty: {
    selectModule: 'Select a module to view details and configuration',
  },

  error: {
    loadFailed: 'Failed to load module list',
    operationFailed: 'Operation failed',
    saveFailed: 'Failed to save configuration',
    testFailed: 'Test failed',
  },
}