// modulesView.ts — صفحة إدارة الوحدات.
export default {
  pageTitle: 'الوحدات',
  pageSubtitle: 'إدارة مركزية لوحدات ميزات المؤسسة: قم بتمكين أو تعطيل الإمكانيات حسب الحاجة.',
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
    labelKey: 'مفتاح الوحدة',
    labelDanger: 'مستوى الخطر',
    labelConfigCount: 'عناصر الإعداد',
    labelStatus: 'الحالة الحالية',
    viewAllSettings: 'عرض جميع إعدادات النظام',
    testConnection: 'اختبار الاتصال',
    testSuccess: 'نجح اختبار الاتصال',
    testFailed: 'فشل اختبار الاتصال',
    testInProgress: 'جارٍ إرسال رسالة الاختبار…',
    dependenciesTitle: 'التبعيات',
    required: 'إلزامي',
    optional: 'اختياري',
    enableAllDeps: 'تفعيل {count} من التبعيات الإلزامية الناقصة بنقرة واحدة',
    enableAllDepsHint: 'تفعّل الواجهة الخلفية كل التبعيات الإلزامية الناقصة بترتيب ثابت ثم تفعّل هذه الوحدة.',
    cascading: 'جاري تفعيل التبعيات…',
    cascadeSuccess: 'تم تفعيل {count} من التبعيات تلقائيًا: {names}',
    cascadeEmpty: 'كل التبعيات كانت مفعّلة بالفعل، والوحدة الآن نشطة.',
    cascadeAlreadyEnabled: 'الوحدة مفعّلة بالفعل، لا حاجة إلى تفعيل التبعيات تلقائيًا.',
    cascadeLogTitle: 'التبعيات المفعّلة في هذه العملية:',

    sectionDependencies: 'Dependencies',

    dependencyDisabled: 'Dependency module is disabled',

    notEnabled: 'Not enabled',
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
  },

  handoff: {
    groupMaster: 'Master switch',
    groupMasterHint: 'المفتاح العام ووضع الإطلاق: يحددان ما إذا كان تسليم الجلسة مُفعّلًا ومتى يحدث',
    groupTrigger: 'Trigger thresholds',
    groupTriggerHint: 'تُقيَّم 4 شروط للإطلاق على التوازي: يكفي واحد منها لبدء التسليم، والأشدّ هو الفائز.',
    groupSummary: 'Summary generation',
    groupSummaryHint: 'محرك التلخيص وقالب الموجّه: كيف تُختصر الجلسة القديمة في سياق تستطيع الجلسة الجديدة قراءته',
    groupSafety: 'Safety limits and notifications',
    groupSafetyHint: 'مهلة تهدئة، وحد لكل جلسة، وإعادة محاولة عند الفشل، وإشعارات عبر السجل أو Webhook',
  },

  integration: {
    docsLabel: 'الوثائق: ',
    stepsTitle: 'خطوات الإعداد',
    enabledStatus: 'التكامل مفعّل',
    disabledHint: 'التكامل معطّل: يرجى تفعيل الوحدة أولاً',
    feishuSteps: [
      'أنشئ بوتًا مخصصًا في منصة Feishu المفتوحة',
      'انسخ عنوان URL للـ webhook والصقه في الإعداد أدناه',
      '(اختياري) اضبط رمز التحقق من التوقيع ومفتاح التشفير',
      'انقر فوق "اختبار الاتصال" بعد الإعداد',
      'فعّل مفتاح "تكامل بوت Feishu"',
    ],
    feishuBotIntegration: 'تكامل بوت Feishu',
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
    dingtalkSteps: [
      'أضف "روبوت مخصص" في مجموعة DingTalk، اختر أمان "التوقيع" وانسخ الـ Webhook',
      'الصق رابط Webhook أدناه وأدخل سر التوقيع',
      '(اختياري) أنشئ تطبيقًا داخليًا في الكونسول وأدخل AppKey/AppSecret/AgentID لتمكين إشعارات العمل',
      'اضبط رابط استدعاء الموافقة (/api/webhooks/dingtalk/approval-callback) كمستقبل رسائل الروبوت',
      'فعّل مفتاح "تكامل روبوت DingTalk"',
      'تأكد من تفعيل الوحدات المطلوبة (الضغط، كشف الحقن، تخزين الجلسة، تدقيق الجلسة)',
    ],
    dingtalkBotIntegration: 'تكامل روبوت DingTalk',  },

  empty: {
    selectModule: 'اختر وحدة لعرض التفاصيل والإعدادات',
  },


  error: {
    loadFailed: 'تعذّر تحميل قائمة الوحدات. أعد المحاولة.',
    operationFailed: 'تعذّر إتمام عملية الوحدة. أعد المحاولة.',
    saveFailed: 'تعذّر حفظ إعدادات الوحدة. أعد المحاولة.',
    testFailed: 'تعذّر اجتياز اختبار الوحدة. أعد المحاولة.',
  },

  // 同步的扁平键

  modeBannerTitle: 'This page only does three things:',

  modeBannerBody: 'فعّل أو عطّل الوحدات المثبتة، ونزّل الوحدات المرخّصة لكنها غير مفعّلة. لا أسعار ولا طلبات ولا خطط هنا.',
}