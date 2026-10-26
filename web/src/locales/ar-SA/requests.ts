// Auto-translated draft (ar-SA) · 2026-07-02 · please review
// requests.ts — نصوص صفحة سجل الطلبات (RequestLogsView) + تفاصيل الدرج (RequestLogDrawer).
// فضاء الأسماء: common مشترك / list صفحة القائمة / detail الدرج / status كود الحالة / errors نوع الخطأ / finish سبب انتهاء المنبع / compression استراتيجية الضغط / session ملخص الجلسة.
// التسمية تستند إلى سلسلة kind في الخلفية كمفتاح، مفصولة عن نص واجهة المستخدم — يستدعيها العارض حسب الحاجة.
export default {
  common: {
    loading: 'جاري التحميل…',
  },
  list: {
    filter: {
      keyAll: 'كل المفاتيح',
      providerAll: 'كل المزوّدين',
      credentialAll: 'كل بيانات الاعتماد',
      loadFailed: 'فشل تحميل خيارات المزوّد/بيانات الاعتماد: القائمة الفارغة لا تعني عدم وجودها',
      partialLoadFailed: 'فشل تحميل بيانات الاعتماد لـ {count} من المزوّدين ({names})؛ التصفية بها تعطي نتائج ناقصة',
      keysLoadFailed: 'فشل تحميل قائمة مفاتيح API: التصفية بمفتاح ستعيد صفر نتائج',
      timeOptions: {
        h1: 'ساعة واحدة',
        h6: '6 ساعات',
        h24: '24 ساعة',
        d3: '3 أيام',
        d7: '7 أيام',
        // 2026-08-09: إعدادات التقويم
        today: 'اليوم',
        thisWeek: 'هذا الأسبوع',
        thisMonth: 'هذا الشهر',
        thisYear: 'هذا العام',
        custom: 'مخصص',
      },
      // 2026-08-09: تسميات stats-overview + stats-grid
      inputTokenLabel: 'tokens الإدخال',
      outputTokenLabel: 'tokens الإخراج',
      cacheReadLabel: 'tokens قراءة ذاكرة التخزين المؤقت',
      cacheWriteLabel: 'tokens كتابة ذاكرة التخزين المؤقت',
      costLabel: 'التكلفة USD',
      resultAll: 'الكل',
      errorAll: 'كل الأخطاء',
      tokenSourceTitle: 'estimated = تقدير محلي (المنبع لم يرجع usage)',
      tokenSourceAll: 'مصدر Token',
      tokenSourceLlm: 'يرجعه LLM',
      tokenSourceEstimated: 'تقدير محلي',

      resultRateLimited: 'Rate limited',
    },
    trace: {
      sessionSummaryHint: 'متاح فقط في تصفية معرّف الجلسة',
      // 2026-08-06: تسمية زر "ملخص الجلسة" في درج التفاصيل ونص التلميح.
      drawerSummaryButton: 'ملخص',
      generating: 'جاري التلخيص…',
      generate: 'إنشاء ملخص',
      exportMd: 'تصدير Markdown',
      exportTxt: 'تصدير TXT',
      writingMemora: 'جاري الكتابة…',
      writeMemora: 'كتابة في Memora',
      memoraWritten: 'تمت الكتابة في Memora: {n} سجل ({status})',
      summaryRange: 'النطاق: {from} ~ {to}: {n} سجل',
    },
    table: {
      attachmentsTitle: 'المرفقات',
      noAttachments: 'لا توجد مرفقات',
    },
    summary: {
      // عناوين أقسام ملخص الجلسة markdown (تُلحق بالمحتوى المصدر)
      title: '# ملخص الجلسة',
      summaryHeading: '## ملخص',
      keyPointsHeading: '## نقاط رئيسية',
    },
    // 2026-08-09: بطاقات stats-overview الثلاثية البارزة في الأعلى
    stats: {
      totalRequests: 'إجمالي الطلبات',
      totalTokens: 'إجمالي الـ tokens',
      totalCredits: 'إجمالي الأرصدة',
      totalCreditsTitle: 'مجموع الأرصدة المخصومة عبر جميع الصفوف المطابقة للمرشح الحالي',
      scopeAll: 'كل الصفوف المطابقة: مستقل عن التقسيم إلى صفحات',
    },
  },
  // ── تعيينات التعداد ──────────────────────────────────────────────────────────────
  // حالة الطلب العامة
  // سبب finish_reason للمنبع
  // نوع الخطأ في الخلفية (ثوابت KIND_*)
  errorKind: {
    model_not_found: 'النموذج غير موجود',
    provider_error: 'خطأ المزود',
    auth_error: 'فشل المصادقة',
    missing_key: 'لا يوجد مفتاح',
    invalid_key: 'مفتاح غير صالح',
    auth_unavailable: 'المصادقة غير متاحة',
    body_read_error: 'فشل قراءة Body',
    body_too_large: 'Body كبير جدًا',
    json_parse_error: 'JSON غير صالح',
    rate_limit: 'تحديد معدل المزود',
    rate_limit_exceeded: 'تحديد RPM للبوابة',
    key_throttled: 'تحديد المفتاح',
    budget_exhausted: 'الميزانية مستنفدة',
    insufficient_credits: 'رصيد غير كافٍ',
    timeout: 'انتهت المهلة',
    canceled: 'تم الإلغاء',
    upstream_error: 'خطأ المنبع',
    stream_error: 'انقطاع التدفق',
    no_candidate: 'لا يوجد توجيه متاح',
    session_forbidden: 'الجلسة غير مصرح لها',
    executor_unavailable: 'المنفذ غير متاح',
    unknown_failure: 'فشل',
  },
  // أخطاء مستوى البوابة (مع بادئة gw_)
  gwErrorKind: {
    gw_rpm_exceeded: 'تحديد RPM للبوابة',
    gw_concurrent_exceeded: 'تحديد التزامن للبوابة',
    gw_tpm_exceeded: 'تحديد TPM للبوابة',
    gw_key_throttled: 'تحديد المفتاح',
    gw_budget_exhausted: 'الميزانية مستنفدة',
    gw_no_candidate: 'لا يوجد توجيه متاح',
    gw_session_forbidden: 'الجلسة غير مصرح لها',
    eof_without_done: 'EOF المنبع بدون [DONE]',
    stream_timeout: 'مهلة التدفق',
    client_cancel: 'إلغاء العميل',
    client_disconnected: 'انقطاع العميل',
    no_deltas: 'لا توجد كتل محتوى',
    invalid_first_chunk: 'الكتلة الأولى غير صالحة',
    invalid_json: 'JSON غير صالح',
    upstream_5xx: 'المنبع 5xx',
    upstream_4xx: 'المنبع 4xx',
    unexpected_status: 'حالة غير متوقعة',
    connection_reset: 'إعادة تعيين الاتصال',
    write_failed: 'فشل الكتابة',
    hangup: 'الطرف البعيد أغلق الخط',
    body_too_large: 'Body كبير جدًا',
    eof_mid_tool_call: 'انقطاع استدعاء الأداة',
    first_byte_timeout: 'مهلة البايت الأول',
  },
  // استراتيجية الضغط (لتسميات مثل request.outbound_token_est_uses_strategy)
  // منطقة التفاصيل الإضافية في درج التفاصيل
  detail_extra: {
    sessionIdTitleWithId: 'معرّف الجلسة (تصفية حسب المسار)\n{id}',
    taskIdTitleWithId: 'معرّف المهمة (تصفية بهذه المهمة فقط)\n{id}',
  },
  // تلميح علامة ملخص الجلسة (أعلى درج التفاصيل)
  defaultTenantOptions: {
    whole: 'بيانات الموقع بالكامل',
    defaultTenant: 'المستأجر الافتراضي',
    tenantPrefix: 'المستأجر: {id}',
  },

  // 同步的扁平键
  collapse: 'طي ▲',
  delta_append: 'إلحاق تدريجي',
  estimatedNote: 'قيمة مقدرة (المنبع لم يرجع usage، يتم التقدير محليًا حسب الأحرف/الكلمات)',
  expand: 'توسيع ▼',
  length: 'تم بلوغ الحد الأقصى للطول',
  llmReported: 'القيمة المرجعة من LLM',
  llm_summary: 'ملخص LLM',
  mechanical_trim: 'تشذيب آلي',
  memora_l1_inject: 'حقن Memora',
  mode_1_auto_threshold: 'عتبة تلقائية',
  mode_2_on_4xx: 'إعادة محاولة 4xx',
  noKey: 'بدون مفتاح',
  noKeyDetail: 'المفتاح: لا يوجد',
  none: '—',
  noop: 'غير مضغوط',
  pendingPricingNoCurrency: 'في انتظار التسعير',
  resultFailure: 'فشل',
  resultInProgress: 'قيد الطلب',
  resultSuccess: 'نجاح',
  same_session_delta: 'تدريجي نفس الجلسة',
  sliding_window_count: 'نافذة منزلقة·عدد الرسائل',
  sliding_window_idle: 'نافذة منزلقة·خامل',
  sliding_window_mechanical_trim: 'نافذة منزلقة·آلي',
  sliding_window_token: 'نافذة منزلقة·token',
  table: {
    sessionTitleFallback: 'لا يوجد عنوان الجلسة بعد. افتح التفاصيل لعرض هذا الطلب.',
  },
}