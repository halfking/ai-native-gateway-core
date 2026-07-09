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
    docsLabel: 'Documentación: ',
    stepsTitle: 'Pasos de configuración',
    enabledStatus: 'Integración habilitada',
    disabledHint: 'Integración deshabilitada — habilite el módulo primero',
    prerequisitesTitle: 'Requisitos previos',
    prerequisitesHint: 'Habilite los módulos de requisitos previos antes de habilitar este módulo',
    feishuSteps: [
      'Cree un bot personalizado en la plataforma Feishu',
      'Copie la URL del webhook y péguela en la configuración',
      '(Opcional) Configure el token de verificación de firma',
      'Active el interruptor "Integración del bot Feishu"',
    ],
    feishuBotIntegration: 'Integración del bot Feishu',
    wechatSteps: [
      'Cree una aplicación en el panel de administración de WeCom',
      '(Opcional) Configure la URL de callback y el Token en "Recibir mensajes"',
      'Copie CorpID, AgentID y Secret a la configuración',
      '(Opcional) Configure la URL del webhook del bot de grupo',
      '(Opcional) Configure EncodingAESKey para el cifrado de callback',
      'Active el interruptor "Integración del bot WeChat"',
      'Asegúrese de que todos los módulos de requisitos previos estén habilitados',
    ],
    wechatBotIntegration: 'Integración del bot WeChat',
    dingtalkSteps: [
      'Añada un «Bot personalizado» en un grupo DingTalk, elija seguridad «Firma» y copie el Webhook',
      'Pegue la URL del Webhook abajo e introduzca el Secret de firma',
      '(Opcional) Cree una app interna en la consola e introduzca AppKey/AppSecret/AgentID para notificaciones de trabajo',
      'Configure la URL de callback de aprobación (/api/webhooks/dingtalk/approval-callback) como receptor del bot',
      'Active el interruptor «Integración del bot DingTalk»',
      'Asegúrese de que los módulos requeridos (compresión, detección de inyección, caché de sesión, auditoría de sesión) estén activados',
    ],
    dingtalkBotIntegration: 'Integración del bot DingTalk',
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
