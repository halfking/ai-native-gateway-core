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
    docsLabel: 'Documentation : ',
    stepsTitle: 'Étapes de configuration',
    enabledStatus: 'Intégration activée',
    disabledHint: 'Intégration désactivée — veuillez activer le module',
    prerequisitesTitle: 'Prérequis',
    prerequisitesHint: "Veuillez activer les modules prérequis avant d'activer ce module",
    feishuSteps: [
      'Créez un bot personnalisé sur la plateforme Feishu',
      "Copiez l'URL du webhook et collez-la dans la configuration",
      '(Facultatif) Configurez le jeton de vérification de signature',
      'Activez l\'interrupteur "Intégration du bot Feishu"',
    ],
    feishuBotIntegration: 'Intégration du bot Feishu',
    wechatSteps: [
      "Créez une application dans la console d'administration WeCom",
      "(Facultatif) Configurez l'URL de callback et le Token dans \"Recevoir des messages\"",
      'Copiez CorpID, AgentID et Secret dans la configuration',
      "(Facultatif) Configurez l'URL du webhook du bot de groupe",
      '(Facultatif) Configurez EncodingAESKey pour le chiffrement des callbacks',
      'Activez l\'interrupteur "Intégration du bot WeChat"',
      'Assurez-vous que tous les modules prérequis sont activés',
    ],
    wechatBotIntegration: 'Intégration du bot WeChat',
    dingtalkSteps: [
      'Ajoutez un « Robot personnalisé » dans un groupe DingTalk, choisissez la sécurité « Signature » et copiez le Webhook',
      'Collez l’URL Webhook ci-dessous et renseignez le Secret de signature',
      '(Optionnel) Créez une application interne dans la console et renseignez AppKey/AppSecret/AgentID pour activer la notification de travail',
      'Configurez l’URL de rappel d’approbation (/api/webhooks/dingtalk/approval-callback) comme récepteur du robot',
      'Activez le commutateur « Intégration du bot DingTalk »',
      'Assurez-vous que les modules requis (compression, détection d’injection, cache de session, audit de session) sont activés',
    ],
    dingtalkBotIntegration: 'Intégration du bot DingTalk',
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
