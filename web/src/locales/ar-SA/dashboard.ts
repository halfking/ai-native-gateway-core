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

    emptyWaiting: 'Waiting for live request stream data…',
    groupByVendor: 'By vendor', groupByProvider: 'By provider', groupByModel: 'By model',
    probeAll: 'All', probeOnly: 'Probes only',
    probeAllTitle: 'Show all requests (default)', probeOnlyTitle: 'Show probe requests only',
    cacheWindow: 'Cache / window', connectionDetailTitle: 'Click for connection details',
    dimensionVendor: 'Vendor', dimensionProvider: 'Provider', dimensionModel: 'Model',
    statusOpen: 'Connected', statusConnecting: 'Connecting', statusReconnecting: 'Reconnecting',
    statusUnsupported: 'Unsupported', statusClosed: 'Disconnected',
    sseDetailTitle: 'SSE connection details', sseStatusLabel: 'Status', sseUrlLabel: 'SSE URL',
    editUrl: 'Edit', editUrlPlaceholder: 'Enter SSE URL', save: 'Save', resetDefault: 'Default',
    cancel: 'Cancel', testConnection: 'Test connection', close: 'Close',
    sseTestOk: 'SSE connection OK!\nStatus: connected\nURL: {url}',
    sseTestFail: 'SSE not connected\nStatus: {status}\nURL: {url}',
    redisWarning: 'Redis unavailable: {error}. Live data falls back to DB queries.',
    redisFallbackError: 'Cache service connection failed',

    probeDirect: 'Active probe (direct upstream)',

    probeGateway: 'Active probe (gateway path)',

    probeScheduled: 'Scheduled probe (scheduler)',

    probeGeneric: 'Probe request',

    probeOriginLabel: 'Origin: {origin}',

    probeAttempt: 'Attempt: #{n}',

    originGateway: 'Gateway path',

    originScheduled: 'Scheduled probe',

    originDirect: 'Direct upstream',

    idleHeartbeat: 'Heartbeat placeholder',

    tileIdle: 'Idle',

    idleUnderOneMin: 'Idle < 1 min',

    idleMinutes: 'Idle {n} min',

    idleHours: 'Idle {h} h',

    idleHoursMinutes: 'Idle {h} h {m} min',

    idleReasonNoTraffic: 'No traffic (5 minutes idle)',
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
    executions: 'Executions',
    cacheHitRate: 'Cache Hit Rate',
    rate: 'Rate',
    totalModules: 'Total Modules',
    totalExecutions: 'Total Executions',
    avgCacheHitRate: 'Avg Cache Hit Rate',
    avgDuration: 'Avg Duration',

    title: 'Module Execution Statistics',

    successRate: 'Success Rate',
  },

  errors: {
    trendTitle: 'Error Trend',
    errorCount: 'Error Count',
    totalCount: 'Total Requests',
    errorRate: 'Error Rate',
    count: 'Count',
    rate: 'Rate',
    totalErrors: 'Total Errors',
    topErrors: 'Top Errors',

    title: 'Error Statistics',

    totalRequests: 'Total Requests',

    avgLatency: 'Avg Error Latency',
  },

  performance: {
    throughput: 'Throughput',
    requests: 'Requests',
    p50: 'P50 Latency',
    p95: 'P95 Latency',
    p99: 'P99 Latency',
    latencyDist: 'Latency Distribution',
    slowQueries: 'Slow Queries',

    title: 'Performance Metrics',

    avgLatency: 'Avg Latency',

    latency: 'Latency',
  },

  providerUsage: {
    title: 'Provider usage',
    subtitle: '{period} — all-provider consumption (reconciliation-ready)',
    periodHint: 'Current period: {period}',
    more: 'More',
    search: 'Search name, code, or ID…',
    back: 'Back to list',
    exportAll: 'Export all (Excel)',
    exportDetail: 'Export detail (Excel)',
    colName: 'Provider',
    colCode: 'Code',
    colRequests: 'Requests',
    colTokens: 'Tokens',
    colCost: 'Cost (USD)',
    colSuccess: 'Success rate',
    colModel: 'Model',
    colDate: 'Date',
    periodDay: 'Daily',
    periodWeek: 'Weekly',
    periodMonth: 'Monthly',
    periodLabel: 'Period: {period}',
    periodRange: '{start} to {end}',
    modelBreakdown: 'By model',
    dailyBreakdown: 'Daily model breakdown',
  },
}