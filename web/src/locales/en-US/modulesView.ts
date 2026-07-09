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
    status: 'Runtime',
  },

  overview: {
    sectionDescription: 'Description',
    sectionCapabilities: 'Capabilities',
    sectionRequirements: 'Dependencies',
    labelKey: 'Module key',
    labelDanger: 'Danger level',
    labelConfigCount: 'Config items',
    labelStatus: 'Current status',
    viewAllSettings: 'View all system settings',
    requirementsMet: 'All dependencies enabled',
    requirementsMissing: 'The following dependencies are disabled — related features may be limited:',
    jumpToModule: 'Configure',
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
    callbackUrlLabel: 'Callback URL (configure in Feishu bot backend)',
    callbackUrlHelp: 'Paste this URL into the Feishu custom bot → callback configuration to receive approval actions from the bot.',
    whitelistHelp: 'OpenIDs allowed to execute bot commands, comma-separated. When empty, commands.admin_only decides whether all are allowed.',
    quietHoursHelp: 'During quiet hours only critical alerts are pushed (avoids night-time noise). Cross-midnight windows are supported (22:00 → 08:00).',
    commandsHelp: 'When enabled, admins can interact with the system via Feishu commands (/status /help /stats /audit /test).',
    signatureHelp: 'Always enable in production. When on, Feishu callbacks must carry a valid HMAC-SHA256 signature and timestamp within window.',
  },

  handoff: {
    groupMaster: 'Master switch',
    groupMasterHint: 'Master toggle and trigger mode: determine how session handoff is enabled and when it fires',
    groupTrigger: 'Trigger thresholds',
    groupTriggerHint: '4 trigger conditions run in parallel — any one of them fires handoff; the strictest wins',
    groupSummary: 'Summary generation',
    groupSummaryHint: 'Summary engine and prompt template: how to compress the old session into a new-session-readable context',
    groupSafety: 'Safety limits and notifications',
    groupSafetyHint: 'Cooldown, per-session cap, retry on failure, and log/Webhook notifications',
  },

  integration: {
    docsLabel: 'Docs: ',
    stepsTitle: 'Setup steps',
    enabledStatus: 'Integration enabled',
    disabledHint: 'Integration disabled — please enable the module first',
    feishuSteps: [
      'Create a custom bot in the Feishu open platform',
      'Copy the webhook URL and paste it into the config below',
      '(Optional) Configure signature verification token and encrypt key',
      'Click "Test connection" to verify after configuration',
      'Turn on the "Feishu bot integration" switch',
    ],
    feishuBotIntegration: 'Feishu bot integration',
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