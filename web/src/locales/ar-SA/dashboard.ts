export default {
  title: "لوحة المعلومات",
  refresh: "تحديث",
tabs: {
    board: 'Board',
    liveStream: 'تدفق الطلبات المباشر',
    sessionStats: 'الجلسات والإحصائيات',
    selfcheck: 'مراقبة النظام',
  },
v2: {
    quickApiKey: 'API Key',
    quickModels: 'Models',
    quickApiKeyTitle: 'View API key ranking',
    quickModelsTitle: 'View model statistics',
    refreshData: 'Refresh data',
    retry: 'Retry',
    reloadAria: 'Reload data',
    degradedViewLabel: 'View',
    degradedHint: 'Data view {view} is not initialized yet. Run the aggregation migration first.',
    emptyTitle: 'No request data yet',
    emptyHint: 'After configuring providers, send a call via /v1/chat/completions to see the live stream here.',
    modelsPage: 'Models',
    totalTokensShort: 'Total Tokens',
    totalCredits: 'Credits charged',
    creditsSub: 'Pricing × tokens',
    modelCount: 'Models',
    sessionCompression: 'Session compression',
  },
  range: {
    today: "اليوم",
    last7d: "آخر 7 أيام",
    last30d: "آخر 30 يومًا",
    last90d: "آخر 90 يومًا"
  },
  tenantLabel: {
    default: "بيانات الموقع بالكامل",
    super: "المستأجر الافتراضي",
    tenant: "المستأجر: {tenantId}"
  },
  loadError: "فشل التحميل",
  board: {
    empty: 'No data',
    bgTasks: 'Background tasks',
    discoveryStatus: 'Model discovery',
    probeChecks: 'Probes (10m)',
    selfcheckTitle: 'Self-check',
    selfcheckLast: 'Last status',
    selfcheckRate: '24h success rate',
    selfcheckHint: 'Click for details →',
    pieClients: 'Client types',
    pieVirtualIp: 'عنوان IP للعميل',
    pieIdentity: 'Identity hash',
    pieModels: 'Model usage',
    pieErrors: 'Error types',
    pieTenants: 'Tenant usage',
    pieProviders: 'Provider usage',
    metricRequests: 'Requests',
    metricTokens: 'Tokens',
    metricToggle: 'المقياس',
    errorDrill: 'Error drill-down: {kind}',
    drillModel: 'By model',
    drillProvider: 'By provider',
    drillClient: 'By client',
    trendTitle: 'Usage trends',
    trendRequests: 'Requests',
    trendTokens: 'Tokens',
    trendCredits: 'Credits',
    trendCost: 'Cost (USD)',
    filterProvider: 'Provider ID',
    allProviders: 'All',

    rangeCustomTab: 'Custom',

    rangeFrom: 'From',

    rangeTo: 'To',

    rangePicker: 'Statistics period',

    rangeCustom: '{start} to {end}',
  },
  proxyWarning: {
    title: "⚠ وكيل الخروج غير قابل للوصول",
    detail: "فشل اختبار الوكيل المُكوّن {proxy}، تم الرجوع تلقائيًا إلى وضع الاتصال المباشر. قد تفشل النماذج الأجنبية (Anthropic / OpenAI / OpenRouter / GitHub Copilot، إلخ).",
    detailHealthy: "تعافى الوكيل {proxy}.",
    hint: "بعد تعافي الوكيل، سيقوم النظام بإعادة تمكينه تلقائيًا"
  },
  backgroundTasks: {
    title: "المهام الخلفية قيد التنفيذ",
    running: "اكتشاف النماذج ({trigger})",
    startedAt: "بدأ {time}",
    heartbeat: "نبضة {time}",
    slow: "قد تتباطأ صفحة الإدارة",
    detailsLink: "عرض التفاصيل"
  },
  discovery: {
    latest: "آخر اكتشاف للنماذج:",
    failuresTitle: "اكتشاف النماذج · اختبارات فاشلة في آخر 6 ساعات",
    failuresTally: "{n} فشل · {m} نماذج",
    summary: "عرض قائمة الفشل",
    meta: "{n} مرة · يشمل {m} بيانات اعتماد · آخر {date} · خطأ {code}"
  },
  stat: {
    totalRequests: "إجمالي الطلبات",
    inLastDays: "آخر {days} يوم",
    totalTokens: "إجمالي استخدام الرموز",
    prompt: "إدخال {n}",
    completion: "إكمال {n}",
    totalCost: "التكلفة الإجمالية",
    costUnit: "USD",
    successRate: "معدل النجاح",
    avgLatency: "متوسط الكمون {n} ms",
    apiKeys: "مفاتيح API المدمجة",
    enabledActive: "مفعّل {enabled} · نشط {active}",
    models: "عدد النماذج",
    activeInDays: "آخر {days} يوم نشط {n}",
    providers: "المزودون / بيانات الاعتماد",
    enabledCredentials: "مفعّل {enabled} · بيانات اعتماد {total}",
    offline: "الموارد غير المتصلة",
    modelsCredentials: "نماذج {models} · بيانات اعتماد {creds}"
  },
  table: {
    hotKeysTitle: "ترتيب مفاتيح API الأكثر استخدامًا",
    byModelTitle: "الإحصاءات حسب النموذج",
    colKey: "المفتاح",
    colApplication: "التطبيق",
    colOwner: "المستخدم المسؤول",
    colRequests: "عدد الطلبات",
    colTokens: "استخدام الرموز",
    colCost: "التكلفة (USD)",
    colLastUsed: "آخر استخدام",
    colModel: "النموذج",
    colProvider: "المزود"
  },
  compression: {
    title: "🤖 ضغط الجلسات",
    delta: "تدريجي",
    sliding: "منزلق",
    outboundTokens: "≈ {n} رمز صادر"
  },
  empty: {
    firstUse: "🚀 لا توجد بيانات طلبات حتى الآن. بعد تكوين المزود، ابدأ الاتصال عبر /v1/chat/completions."
  },
  loading: "جاري التحميل…",
  noData: "لا توجد بيانات في هذه الفترة",
  costSuffix: "USD",
  viewFailedRequests: "عرض الطلبات الفاشلة",
  liveStream: {
    title: "تدفق الطلبات المباشر",
    connected: "متصل",
    disconnected: "إعادة الاتصال...",
    pause: "إيقاف مؤقت",
    resume: "استئناف",
    filterAll: "جميع الحالات",
    filterSuccess: "الناجحة فقط",
    filterFailure: "الفاشلة فقط",
    filterInProgress: "قيد التنفيذ",
    filterGroupFailures: "تصنيف الفشل",
    filterFailure5xx: "الخادم / المنبع (5xx)",
    filterFailure4xx: "العميل / المصادقة (4xx)",
    filterFailureTimeout: "انتهت المهلة / الشبكة",
    filterFailureNotFound: "التوجيه / نموذج غير موجود",
    filterFailureOther: "أعطال أخرى",
    idleLabel: "خامل {duration}",
    countTooltip: "{buffer} في المخزن / {visible} مرئية على الشاشة",
    countAria: "{buffer} طلب في المخزن، {visible} مرئية",
    legendButton: 'مفتاح الرموز',
    legend: {
      title: "مفتاح الرموز",
      model: "عائلة النموذج",
      status: "الحالة",
      openai: "OpenAI",
      anthropic: "Anthropic",
      domestic: "محلي",
      oss: "مفتوح المصدر",
      other: "أخرى",
      success: "نجح",
      inProgress: "قيد التنفيذ",
      failure: "فشل"
,
      cancelled: 'Cancelled',
    },
    tooltip: {
      model: "النموذج",
      provider: "المزود",
      status: "الحالة",
      latency: "الكمون",
      tokens: "الرموز",
      cost: "التكلفة",
      error: "خطأ",
      time: "الوقت"
,
      vendor: 'Vendor',

      requestId: 'ID',

      errorKind: 'Error type',

      errorRawCode: 'Raw code',

      statusValue: {
        success: 'Success',
        inProgress: 'In progress',
        cancelled: 'Cancelled by user',
        idle: 'Idle (heartbeat placeholder)',
        failure: 'Request failed',
      },

      tokenFormat: '{p} + {c}',
    },
    connecting: "...جارٍ الاتصال",
    reconnecting: "...جارٍ إعادة الاتصال",
    unsupported: "البث المباشر غير مدعوم في هذا المتصفح",
    empty: "لا توجد طلبات مباشرة",

    emptyWaiting: 'في انتظار بيانات البث المباشر للطلبات…',
    groupByVendor: 'حسب المورّد', groupByProvider: 'حسب المزوّد', groupByModel: 'حسب النموذج',
    modeSmall: 'صغير',
    modeLarge: 'كبير',
    modeSmallTitle: 'وضع صغير: أعمدة عمودية، تستوعب المزيد من الطلبات (افتراضي)',
    modeLargeTitle: 'وضع كبير: بطاقات بتفاصيل طلب أكثر',
    probeAll: 'الكل', probeOnly: 'عمليات الفحص فقط',
    probeAllTitle: 'عرض جميع الطلبات (افتراضي)', probeOnlyTitle: 'عرض طلبات الفحص فقط',
    cacheWindow: 'ذاكرة التخزين المؤقت / النافذة', connectionDetailTitle: 'انقر لعرض تفاصيل الاتصال',
    dimensionVendor: 'المورّد', dimensionProvider: 'المزوّد', dimensionModel: 'النموذج',
    statusOpen: 'متصل', statusConnecting: 'جارٍ الاتصال', statusReconnecting: 'جارٍ إعادة الاتصال',
    statusUnsupported: 'غير مدعوم', statusClosed: 'غير متصل',
    sseDetailTitle: 'تفاصيل اتصال SSE', sseStatusLabel: 'حالة الاتصال', sseUrlLabel: 'عنوان SSE',
    editUrl: 'تحرير', editUrlPlaceholder: 'أدخل عنوان SSE', save: 'حفظ', resetDefault: 'افتراضي',
    cancel: 'إلغاء', testConnection: 'اختبار الاتصال', close: 'إغلاق',
    sseTestOk: 'اتصال SSE ناجح!\nالحالة: متصل\nالعنوان: {url}',
    sseTestFail: 'SSE غير متصل\nالحالة: {status}\nالعنوان: {url}',
    redisWarning: 'Redis غير متوفر: {error}. البيانات المباشرة تعود إلى استعلامات قاعدة البيانات.',
    redisFallbackError: 'فشل الاتصال بخدمة التخزين المؤقت',

    probeDirect: 'فحص نشط (مباشر مع المنبع)',

    probeGateway: 'فحص نشط (عبر البوابة)',

    probeScheduled: 'فحص مجدول (المجدول)',

    probeGeneric: 'طلب فحص',

    probeOriginLabel: 'المصدر: {origin}',

    probeAttempt: 'المحاولة: رقم {n}',

    originGateway: 'مسار البوابة',

    originScheduled: 'فحص مجدول',

    originDirect: 'مباشر مع المنبع',

    idleHeartbeat: 'عنصر نائب لنبضات القلب',

    tileIdle: 'خامل',

    idleUnderOneMin: 'خامل < 1 دقيقة',

    idleMinutes: 'خامل {n} دقيقة',

    idleHours: 'خامل {h} ساعة',

    idleHoursMinutes: 'خامل {h} ساعة {m} دقيقة',

    idleReasonNoTraffic: 'لا توجد حركة مرور (5 دقائق خاملة)',
    // 2026-07-24: Multi-dimension filters
    filterStatus: 'الحالة',
    filterAllOptions: 'الكل',
    filterEmpty: 'لا توجد خيارات متاحة',
    filterModel: 'النموذج',
    filterProvider: 'المزود',
    filterVendor: 'المورّد',
    clearFilters: 'مسح الفلاتر',
    status: {
      in_progress: 'قيد التنفيذ',
      success: 'نجح',
      failure: 'فشل',
      rate_limited: 'محدود',
    },
    vendor: {
      openai: 'OpenAI',
      anthropic: 'Anthropic',
      domestic: 'محلي',
      oss: 'مفتوح المصدر',
      other: 'أخرى',
    },
  },
  charts: {
    gradeA: "الدرجة أ",
    gradeB: "الدرجة ب",
    gradeC: "الدرجة ج",
    gradeD: "الدرجة د",
    gradeF: "الدرجة هـ",
    avgScore: "المتوسط",
    healthDistribution: "توزيع الصحة",
    totalSessions: "إجمالي {n} جلسة",
    newSessions: "جلسات جديدة",
    activeSessions: "جلسات نشطة",
    closedSessions: "جلسات مغلقة",
    costUSD: "التكلفة (دولار أمريكي)",
    sessionCount: "عدد الجلسات",
    sessionTrend: "اتجاه الجلسات"
  },

  moduleStats: {
    executions: 'عمليات التنفيذ',
    cacheHitRate: 'معدل إصابة ذاكرة التخزين المؤقت',
    rate: 'المعدل',
    totalModules: 'إجمالي الوحدات',
    totalExecutions: 'إجمالي عمليات التنفيذ',
    avgCacheHitRate: 'متوسط معدل إصابة ذاكرة التخزين المؤقت',
    avgDuration: 'متوسط المدة',

    title: 'إحصائيات تنفيذ الوحدات',

    successRate: 'معدل النجاح',
  },

  errors: {
    trendTitle: 'اتجاه الأخطاء',
    errorCount: 'عدد الأخطاء',
    totalCount: 'إجمالي الطلبات',
    errorRate: 'معدل الخطأ',
    count: 'العدد',
    rate: 'المعدل',
    totalErrors: 'إجمالي الأخطاء',
    topErrors: 'أهم الأخطاء',

    title: 'إحصائيات الأخطاء',

    totalRequests: 'إجمالي الطلبات',

    avgLatency: 'متوسط تأخير الخطأ',
  },

  performance: {
    throughput: 'معدل النقل',
    requests: 'الطلبات',
    p50: 'تأخير P50',
    p95: 'تأخير P95',
    p99: 'تأخير P99',
    latencyDist: 'توزيع التأخير',
    slowQueries: 'الاستعلامات البطيئة',

    title: 'مقاييس الأداء',

    avgLatency: 'متوسط التأخير',

    latency: 'التأخير',
  },

  providerUsage: {
    title: 'استخدام المورد',
    subtitle: '{period} — استهلاك جميع الموردين (جاهز للتسوية)',
    periodHint: 'الفترة الحالية: {period}',
    more: 'المزيد',
    search: 'البحث عن الاسم أو الرمز أو المعرّف…',
    back: 'العودة إلى القائمة',
    exportAll: 'تصدير الكل (Excel)',
    exportDetail: 'تصدير التفاصيل (Excel)',
    colName: 'المورد',
    colCode: 'الرمز',
    colRequests: 'الطلبات',
    colTokens: 'الرموز',
    colCost: 'التكلفة (دولار أمريكي)',
    colSuccess: 'معدل النجاح',
    colModel: 'النموذج',
    colDate: 'التاريخ',
    periodDay: 'يومي',
    periodWeek: 'أسبوعي',
    periodMonth: 'شهري',
    periodLabel: 'الفترة: {period}',
    periodRange: '{start} إلى {end}',
    modelBreakdown: 'حسب النموذج',
    dailyBreakdown: 'تفصيل النموذج اليومي',
  },
}