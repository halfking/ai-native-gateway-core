// modulesView.ts — صفحة إدارة الوحدات.
export default {
  pageTitle: 'الوحدات',
  pageSubtitle: 'إدارة مركزية لوحدات ميزات المؤسسة — قم بتمكين أو تعطيل الإمكانيات حسب الحاجة.',
  modulesEnabled: 'الوحدات المفعّلة',
  loading: 'جارٍ التحميل…',

  category: {
    compression: 'ضغط الطلبات',
    session: 'إدارة الجلسات',
    security: 'الأمان',
    rate_limit: 'تحديد المعدل',
    general: 'عام',
    integration: 'التكامل',
  },

  status: {
    enabled: 'مفعّل',
    disabled: 'معطّل',
    processing: 'جارٍ المعالجة…',
    enabledAction: 'تعطيل هذه الوحدة',
    disabledAction: 'تمكين هذه الوحدة',
  },

  dangerLevel: {
    safe: 'آمن',
    warn: 'تحذير',
    danger: 'خطير',
    breaking: 'حرج',
    unknown: 'غير معروف',
  },

  tabs: {
    overview: 'نظرة عامة',
    config: 'الإعدادات',
    integration: 'التكامل',
    status: 'وقت التشغيل',
  },

  overview: {
    sectionDescription: 'الوصف',
    sectionCapabilities: 'الإمكانيات',
    sectionRequirements: 'التبعيات',
    labelKey: 'مفتاح الوحدة',
    labelDanger: 'مستوى الخطر',
    labelConfigCount: 'عناصر الإعداد',
    labelStatus: 'الحالة الحالية',
    viewAllSettings: 'عرض جميع إعدادات النظام',
    requirementsMet: 'جميع التبعيات مفعّلة',
    requirementsMissing: 'التبعيات التالية معطّلة — قد تكون الميزات المرتبطة محدودة:',
    jumpToModule: 'إعداد',
    testConnection: 'اختبار الاتصال',
    testSuccess: 'نجح اختبار الاتصال',
    testFailed: 'فشل اختبار الاتصال',
    testInProgress: 'جارٍ إرسال رسالة الاختبار…',
  },

  config: {
    noSettings: 'لا تحتوي هذه الوحدة على إعدادات قابلة للتكوين.',
    sourceDefault: 'افتراضي',
    sourceEnv: 'متغير بيئة',
    sourceDb: 'قاعدة بيانات',
    switchOn: 'تشغيل',
    switchOff: 'إيقاف',
    inputPlaceholder: 'أدخل {description}',
    sections: {
      connection: 'الاتصال',
      alerts: 'إعادة توجيه التنبيهات',
      approvals: 'إشعارات الموافقة',
      commands: 'لوحة الأوامر',
      security: 'الأمان',
      general: 'عام',
    },
  },

  feishu: {
    connectionHint: 'أنشئ بوتًا مخصصًا في منصة Feishu المفتوحة، ثم الصق عنوان URL للـ Webhook أدناه.',
    callbackUrlLabel: 'عنوان URL للاستدعاء (يُكوَّن في واجهة بوت Feishu الخلفية)',
    callbackUrlHelp: 'الصق عنوان URL هذا في إعداد استدعاء بوت Feishu المخصص لتلقي إجراءات الموافقة.',
    whitelistHelp: 'معرّفات OpenID المسموح لها بتنفيذ أوامر البوت، مفصولة بفواصل. فارغ = commands.admin_only يقرر.',
    quietHoursHelp: 'خلال ساعات الهدوء، تُرسَل التنبيهات الحرجة فقط (لتجنب الإزعاج الليلي). تدعم الفترات العابرة لمنتصف الليل (22:00 → 08:00).',
    commandsHelp: 'عند التفعيل، يمكن للمسؤولين التفاعل مع النظام عبر أوامر Feishu (/status /help /stats /audit /test).',
    signatureHelp: 'فعّله دائمًا في الإنتاج. عند التفعيل، يجب أن تحمل استدعاءات Feishu توقيع HMAC-SHA256 صالحًا و طابعًا زمنيًا ضمن النافذة.',
  },

  integration: {
    docsLabel: 'الوثائق: ',
    stepsTitle: 'خطوات الإعداد',
    enabledStatus: 'التكامل مفعّل',
    disabledHint: 'التكامل معطّل — يرجى تفعيل الوحدة أولاً',
    feishuSteps: [
      'أنشئ بوتًا مخصصًا في منصة Feishu المفتوحة',
      'انسخ عنوان URL للـ webhook والصقه في الإعداد أدناه',
      '(اختياري) اضبط رمز التحقق من التوقيع ومفتاح التشفير',
      'انقر فوق "اختبار الاتصال" بعد الإعداد',
      'فعّل مفتاح "تكامل بوت Feishu"',
    ],
    feishuBotIntegration: 'تكامل بوت Feishu',
  },

  empty: {
    selectModule: 'اختر وحدة لعرض التفاصيل والإعدادات',
  },

  error: {
    loadFailed: 'فشل تحميل قائمة الوحدات',
    operationFailed: 'فشلت العملية',
    saveFailed: 'فشل حفظ الإعدادات',
    testFailed: 'فشل الاختبار',
  },
}