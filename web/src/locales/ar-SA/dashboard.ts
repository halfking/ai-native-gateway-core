export default {
  title: "لوحة المعلومات",
  refresh: "تحديث",
  modelsSelectedCount: 'تم اختيار {n} من النماذج',  // R50 ModelPicker compact
tabs: {
    board: 'Board',
    liveStream: 'تدفق الطلبات المباشر',
    sessionStats: 'الجلسات والإحصائيات',
    selfcheck: 'الفحص الذاتي',
  },
v2: {
    quickApiKey: 'API Key',
    quickModels: 'Models',
    quickApiKeyTitle: 'View API key ranking',
    quickModelsTitle: 'View model statistics',
    refreshData: 'Refresh data',
    retry: 'Retry',
    reloadAria: 'Reload data',
    totalTokensShort: 'Total Tokens',
    totalCredits: 'Credits charged',
    creditsSub: 'Pricing × tokens',
    creditsDegraded: 'عرض نقاط الائتمان {view} غير مهيّأ: هذا الرقم غير موثوق',
    summaryDegraded: "عرض التجميع {view} غير مهيّأ: أرقام الطلبات/التوكنات/التكلفة أدناه غير متاحة وليست صفرًا. لا تستخدمها كاستنتاج.",
    modelCount: 'Models',
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
    heroErrors: 'الأخطاء',
    heroInput: 'الإدخال',
    heroOutput: 'الإخراج',
    heroCreditsEq: '≈ {n} رصيدًا',
    drillDown: 'تعميق',
    rpmLabel: 'RPM',
    rpmTitle: 'الطلبات في الدقيقة (متوسط الفترة)',
    tpmLabel: 'TPM',
    tpmTitle: 'الرموز في الدقيقة (متوسط الفترة)',
    providerCount: '{n} مزوّدًا',
    opsRunning: 'قيد التشغيل',
    opsChecks: 'فحوص آخر 10 دقائق: {n}',
    opsRate24h: 'نجاح 24 ساعة {n}',
    opsChecksUnknown: 'فحوصات آخر 10 دقائق: غير معروفة',
    opsRateUnknown: 'معدل نجاح 24 ساعة: غير معروف',
    providerCostTitle: 'تكلفة المزوّدين والمشتريات',
    providerDetail: 'التفاصيل',
    providerWindowCost: 'تكلفة الفترة',
    providerCredits: 'الأرصدة',
    providerBalance: 'الرصيد',
    providerScore: 'تقييم {n}',
    providerPlan: 'باقة',
    providerStatsTitle: 'إحصاءات استخدام المزوّدين',
    providerStatsHint: 'مرتبط بالنطاق الزمني: اختر أي صف لعرض التفاصيل',
    providerExport: 'تصدير Excel',
    filterRange: 'النطاق الزمني',
    filterGran: 'الدقة',
    granAuto: 'تلقائي',
    granAutoTitle: 'تتطابق الدقة تلقائيًا مع النطاق (اليوم=5د / 7أيام=15د / 30 يومًا=1س)، والتحديد اليدوي في المرحلة الثانية',
    granHour: 'كل ساعة',
    granMinutes: '{n} دقيقة',
    sourceLabel: 'المصدر',
    sourcePg: 'أساس PG',
    sourceRedis: 'فارق Redis',
    sourceSse: 'SSE مباشر',
    sourceTitle: 'مصدر بيانات اللوحة (أساس PostgreSQL / فارق Redis / دمج SSE المباشر)',
    modelDistTitle: 'توزيع النماذج',
    modelTop: 'أفضل {n} في الفترة',
    colShare: 'الحصة',
    distTitle: 'تحليل التوزيع',
    distTop: 'الأفضل {n}',
    distOthers: '{n} أخرى',
    distDrillHint: 'اختر نوع خطأ → تعميق حسب النموذج/المزوّد/العميل',
    /**
     * الحالات الفارغة (الدفعة 14 · «حالات فارغة»).
     * ★ كان هنا مفتاح واحد `empty` («No data») تشاركه ستّ بطاقات مختلفة
     *   (المزوّدون / تحليل التوزيع / توزيع النماذج / المخططات / جدول الاستخدام / بطاقتا مهام اللوحة)
     *   ⇒ جملة واحدة تغطّي ستّ دلالات، فلم يستطع المستخدم معرفة أهو «لا يوجد فعلًا»
     *   أم «تمّت التصفية» أم «الحساب فشل». ⇒ الآن كل بطاقة لها مفتاحها.
     *   · emptyNoResult* = لا نتائج (تم البحث فعلًا، ولم نجد شيئًا)
     *   · emptyFailed*    = فشل/مجهول (الخادم لم يُرجع ⇒ لا يجوز القول «لا يوجد»)
     */
    emptyNoResultProviders: 'لا توجد بيانات المزوّدين في الفترة المحدّدة.',
    emptyNoResultDist: 'لا توجد بيانات يمكن ترتيبها في الفترة المحدّدة.',
    emptyNoResultDrill: 'لا توجد طلبات فاشلة ضمن هذا النوع من الأخطاء.',
    emptyNoResultModelDist: 'لا توجد بيانات النماذج في الفترة المحدّدة.',
    emptyNoResultModelTrend: 'لا توجد بيانات اتجاهات النماذج في الفترة المحدّدة.',
    emptyNoResultTrend: 'لا توجد بيانات اتجاهات الاستخدام في الفترة المحدّدة.',
    emptyNoResultDiscovery: 'لا توجد سجلات تشغيل لمهمة اكتشاف النماذج بعد.',
    emptyNoResultProbe: 'لا توجد سجلات تشغيل لدورات الفحص بعد.',
    bgTasks: 'Background tasks',
    discoveryStatus: 'Model discovery',
    selfcheckTitle: 'Self-check',
    pieClients: 'أنواع العملاء',
    pieDegraded: "غير متاح: {reason}. الترتيب الفارغ هنا يعني «لم يتم الحساب»، وليس «لا توجد بيانات».",
    pieDegradedGeneric: "فشل استعلام الترتيب",
    drillFailed: "تعذّر تحميل هذا التفصيل: {reason}. القائمة الفارغة هنا لا تعني عدم وجود أخطاء.",
    pieClientIp: 'عنوان IP للعميل',
    pieIdentity: 'بصمة الهوية',
    pieErrors: 'أنواع الأخطاء',
    pieTenants: 'استخدام المستأجرين',
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
    trendMore: 'المزيد',
    trendMoreTitle: 'فتح تحليل اتجاهات الاستخدام في صفحة كاملة',
    trendOthers: 'أخرى',





    rangeCustom: '{start} to {end}',
  },
  stat: {
    totalRequests: "إجمالي الطلبات",
    totalCost: "التكلفة الإجمالية",
    successRate: "معدل النجاح",
    avgLatency: "متوسط الكمون {n} ms",
    models: "عدد النماذج",
    activeInDays: "آخر {days} يوم نشط {n}",
    avgRequestSize: "متوسط حجم الطلب",
    avgResponseSize: "متوسط حجم الاستجابة",
    maxLabel: "ذروة",
  },
  table: {
    colModel: "النموذج",
  },
  loading: "جاري التحميل…",
  liveStream: {
    title: "تدفق الطلبات المباشر",
    groupByQueue: "حسب قائمة المعالجة",
    controlsAria: "عناصر التحكم في تدفق الطلبات المباشر",
    pause: "إيقاف مؤقت",
    resume: "استئناف",
    legendButton: 'مفتاح الرموز',
    legend: {
      success: "نجح",
      inProgress: "قيد التنفيذ",
      cancelled: 'Cancelled',
    },
    tooltip: {




      statusValue: {
        success: 'Success',
        inProgress: 'In progress',
        cancelled: 'Cancelled by user',
        failure: 'Request failed',
      },

      tokenFormat: '{p} + {c}',
      cacheFormat: 'قراءة {r} / كتابة {w} ({pct})',
    },

    emptyWaiting: 'في انتظار بيانات البث المباشر للطلبات…',
    emptyConnecting: 'جارٍ الاتصال بالبث المباشر، سيتم التحميل تلقائيًا بعد الاتصال…',
    groupByCredential: 'حسب بيانات الاعتماد', groupByProvider: 'حسب المزوّد', groupByModel: 'حسب النموذج',
    modeSmall: 'صغير',
    modeLarge: 'كبير',
    modeSmallTitle: 'وضع صغير: أعمدة عمودية، تستوعب المزيد من الطلبات (افتراضي)',
    modeLargeTitle: 'وضع كبير: بطاقات بتفاصيل طلب أكثر',
    // 2026-08-06: فلاتر الأعمال / الفحص + العناوين (محاذاة مع zh-CN)
    business: 'الأعمال',
    probe: 'فحص',
    businessTitle: 'عرض طلبات الأعمال الحقيقية فقط',
    probeTitle: 'عرض طلبات الفحص فقط',
    cacheWindow: 'ذاكرة التخزين المؤقت / النافذة', connectionDetailTitle: 'عرض تفاصيل الاتصال',
    dimensionCredential: 'بيانات الاعتماد', dimensionVendor: 'المورّد', dimensionProvider: 'المزوّد', dimensionModel: 'النموذج',
    statusOpen: 'متصل', statusConnecting: 'جارٍ الاتصال', statusReconnecting: 'جارٍ إعادة الاتصال',
    statusUnsupported: 'غير مدعوم', statusClosed: 'غير متصل',
    queueStreamConnecting: 'جارٍ الاتصال بالبث المباشر: ستتحدث بيانات هذه المنطقة تلقائيًا بعد الاتصال…',
    queueStreamClosed: 'انقطع اتصال البث المباشر، تجري إعادة الاتصال تلقائيًا…',
    queueStreamUnsupported: 'هذا المتصفح لا يدعم البث المباشر (EventSource)؛ لا يمكن لهذه المنطقة استقبال البيانات في الوقت الفعلي.',
    queueStreamAwaiting: 'تم الاتصال بالبث المباشر، جارٍ استقبال أول البيانات…',
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

    filterStatus: 'الحالة',
    filterModel: 'النموذج',
    filterGroup: 'تصفية',
    filterProvider: 'المزود',
    filterVendor: 'المورّد',
    filterAgent: 'العميل',  // 2026-08-06 محاذاة مع zh-CN
    clearFilters: 'مسح الفلاتر',
    status: {
      in_progress: 'قيد التنفيذ',
      success: 'نجح',
      failure: 'فشل',
      rate_limited: 'محدود',
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
    sessionTrend: "اتجاه الجلسات",
    /** حالة فارغة: مخطط توزيع الصحة فارغ (كان يستخدم المفتاح العام `dashboard.noData`). */
    emptyNoResultHealthGrade: 'لا توجد بيانات توزيع الصحة في الفترة المحدّدة.',
    /** حالة فارغة: مخطط اتجاه الجلسات فارغ (كان يستخدم المفتاح العام `dashboard.noData`). */
    emptyNoResultSessionTrend: 'لا توجد بيانات اتجاهات الجلسات في الفترة المحدّدة.',
  },



  performance: {


    avgLatency: 'متوسط التأخير',

  },

  providerUsage: {
    title: 'استخدام المورد',
    subtitle: '{period}: استهلاك جميع الموردين (جاهز للتسوية)',
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
    /** حالة فارغة: لا توجد سجلات استهلاك (لها وحدها صياغة خاصة في `dataLifecycle.usageCost.degraded.title`). */
    emptyNoResult: 'لا توجد سجلات استهلاك في الفترة المحدّدة.',
  },
  statsRow: {
    totalSessions: "Total Sessions",
    activeSessions: "Active Sessions",
    activeHint: "Active in 24h",
    totalCost: "Total Cost",
    complianceRate: "Compliance Rate",
    avgHealthScore: "Avg Health Score",
    healthHint: "From session health score",
    avgLatency: "Avg Latency",
    totalRequests: "Total Requests",
    totalTokens: "Total Tokens",
  },
  requestTile: {
    childRequestsTitle: 'الطلبات الفرعية: {count} (توسيع للعرض)',
    childRequestsEmpty: 'الطلبات الفرعية (لا توجد بيانات مرسلة، توسيع للعرض)',
    actionTrailTitle: 'سجل الإجراءات: {count} (توسيع للعرض)',
    actionTrailEmpty: 'سجل الإجراءات (لا توجد بيانات مرسلة، توسيع للعرض)',
  },
}
