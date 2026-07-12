export default {
  title: "لوحة المعلومات",
  refresh: "تحديث",
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
    },
    connecting: "...جارٍ الاتصال",
    reconnecting: "...جارٍ إعادة الاتصال",
    unsupported: "البث المباشر غير مدعوم في هذا المتصفح",
    empty: "لا توجد طلبات مباشرة"
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
  tabs: {
    liveStream: "实时请求流",
    sessionStats: "会话与统计"
  },
  moduleStats: {
    title: "模块执行统计",
    executions: "执行次数",
    successRate: "成功率",
    cacheHitRate: "缓存命中率",
    rate: "比率",
    totalModules: "总模块数",
    totalExecutions: "总执行次数",
    avgCacheHitRate: "平均缓存命中率",
    avgDuration: "平均耗时"
  },
  errors: {
    title: "错误统计",
    trendTitle: "错误趋势",
    errorCount: "错误数",
    totalCount: "总请求数",
    errorRate: "错误率",
    count: "数量",
    rate: "比率",
    totalErrors: "总错误数",
    totalRequests: "总请求数",
    avgLatency: "平均错误延迟",
    topErrors: "Top错误"
  },
  performance: {
    title: "性能指标",
    throughput: "吞吐量",
    avgLatency: "平均延迟",
    requests: "请求数",
    latency: "延迟",
    p50: "P50 延迟",
    p95: "P95 延迟",
    p99: "P99 延迟",
    latencyDist: "延迟分布",
    slowQueries: "慢查询"
  }
}
