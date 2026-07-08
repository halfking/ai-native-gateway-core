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
    docsLabel: 'الوثائق: ',
    stepsTitle: 'خطوات الإعداد',
    enabledStatus: ' التكامل مُفعّل',
    disabledHint: 'التكامل معطّل — يرجى تفعيل الوحدة أولاً',
    prerequisitesTitle: 'المتطلبات الأساسية',
    prerequisitesHint: 'يرجى تفعيل الوحدات الأساسية أعلاه قبل تفعيل هذه الوحدة',
    feishuSteps: [
      'أنشئ روبوتًا مخصصًا في منصة Feishu المفتوحة',
      'انسخ عنوان URL الخاص بالـ webhook والصقه في الإعدادات',
      '(اختياري) قم بتكوين رمز التحقق من التوقيع',
      'قم بتفعيل مفتاح "تكامل روبوت Feishu"',
    ],
    feishuBotIntegration: 'تكامل روبوت Feishu',
    wechatSteps: [
      'أنشئ تطبيقًا في وحدة تحكم إدارة WeCom',
      '(اختياري) قم بتكوين عنوان URL للاستدعاء والرمز في "استقبال الرسائل"',
      'انسخ CorpID و AgentID و Secret إلى الإعدادات',
      '(اختياري) قم بتكوين عنوان URL الخاص بـ webhook لروبوت المجموعة',
      '(اختياري) قم بتكوين EncodingAESKey لتشفير الاستدعاء',
      'قم بتفعيل مفتاح "تكامل روبوت WeChat"',
      'تأكد من تفعيل جميع الوحدات الأساسية',
    ],
    wechatBotIntegration: 'تكامل روبوت WeChat',
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
