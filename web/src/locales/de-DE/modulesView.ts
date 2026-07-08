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
    docsLabel: 'Dokumentation: ',
    stepsTitle: 'Einrichtungsschritte',
    enabledStatus: 'Integration aktiviert',
    disabledHint: 'Integration deaktiviert — bitte aktivieren Sie das Modul',
    prerequisitesTitle: 'Voraussetzungen',
    prerequisitesHint: 'Bitte aktivieren Sie die oben genannten Voraussetzungs-Module, bevor Sie dieses Modul aktivieren',
    feishuSteps: [
      'Erstellen Sie einen benutzerdefinierten Bot in der Feishu-Plattform',
      'Kopieren Sie die Webhook-URL und fügen Sie sie in die Konfiguration ein',
      '(Optional) Konfigurieren Sie das Signatur-Verifizierungs-Token',
      'Aktivieren Sie den "Feishu-Bot-Integration"-Schalter',
    ],
    feishuBotIntegration: 'Feishu-Bot-Integration',
    wechatSteps: [
      'Erstellen Sie eine Eigenentwicklung im WeCom-Administrationsbereich',
      '(Optional) Konfigurieren Sie Callback-URL und Token unter "Nachrichten empfangen"',
      'Kopieren Sie CorpID, AgentID und Secret in die Konfiguration',
      '(Optional) Konfigurieren Sie die Webhook-URL des Gruppen-Bots',
      '(Optional) Konfigurieren Sie EncodingAESKey für Callback-Verschlüsselung',
      'Aktivieren Sie den "WeChat-Bot-Integration"-Schalter',
      'Stellen Sie sicher, dass alle Voraussetzungs-Module aktiviert sind',
    ],
    wechatBotIntegration: 'WeChat-Bot-Integration',
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
