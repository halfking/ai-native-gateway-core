// Auto-translated draft (ar-SA) · 2026-07-02 · please review
// credentialMonitor.ts — نصوص صفحة مراقبة بيانات الاعتماد (CredentialMonitorView).
// فضاء الأسماء: page / filter / summary / table / drawer / dialog / status / action / error / chart.
// سلاسل التعداد في الخلفية (ready/degraded/healthy/warning/broken، إلخ) تبقى كما هي — الترجمة تكسر استدعاءات API.
// يعيد استخدام مفردات الأزرار/الحالة من common/keys؛ المصطلحات الخاصة بهذه الصفحة مركزة في هذا الملف.
export default {
  page: {
    title: 'مراقبة بيانات الاعتماد',
    autoRefresh: 'تحديث تلقائي',
    refreshInterval: { s10: '10 ثوانٍ', s30: '30 ثانية', s60: '60 ثانية', s5: '5 ثوانٍ' },
    manualRefresh: 'تحديث يدوي',
  },
  filter: {
    availability: 'التوفر',
    health: 'الصحة',
    all: 'الكل', // إعادة تصدير المفتاح؛ تأكيد مكرر مع common.table.all
    quickNone: 'الكل',
    quickBroken: 'عرض broken فقط',
    quickLowRate: 'معدل النجاح <50%',
    batchRestore: 'استعادة مجمعة ({n})',
    batchDemote: 'تخفيض مجمع ({n})',
  },
  summary: {
    total: 'إجمالي بيانات الاعتماد',
    ready: 'متاح (ready)',
    abnormal: 'غير طبيعي',
    unreachable: 'unreachable/cooling/rate_limited', // تعداد خالص
    brokenModels: 'نماذج broken',
    brokenModelsHint: 'تأكيد الفحص للكسر',
  },
  table: {
    header: {
      credential: 'بيانات الاعتماد',
      provider: 'المزود',
      availability: 'التوفر',
      health: 'الصحة',
      models: 'النماذج (متاح/إجمالي)',
      recentSuccessRate: 'معدل النجاح الأخير',
      brokenModels: 'نماذج broken',
      concurrency: 'التزامن',
    },
    cell: {
      idPrefix: 'ID: ',
      manualPrefix: 'يدوي: ',
      effectivePrefix: 'فعّال: ',
    },
    loading: 'جاري التحميل...', // نفس معنى common.feedback.loading
    empty: 'لا توجد بيانات اعتماد',
  },
  drawer: {
    close: 'إغلاق',
    refreshTooltip: 'تحديث التفاصيل',
    credentialIdPrefix: 'معرّف بيانات الاعتماد: ',
    action: {
      clearManualDisabledTitle: 'إلغاء تعطيل بيانات الاعتماد بالكامل',
      clearManualDisabled: '🔓 إلغاء التعطيل',
      setManualDisabledTitle: 'تعطيل بيانات الاعتماد هذه يدويًا (لن يتم اختيارها في التوجيه)',
      setManualDisabled: '⛔ تعطيل يدوي',
    },
    tab: {
      overview: 'المعلومات الأساسية',
      models: 'النماذج',
      requests: 'بيانات الطلبات',
    },
    overview: {
      sectionTitle: 'المعلومات الأساسية · إحصائيات بيانات الاعتماد',
      fields: {
        availability: 'التوفر',
        health: 'الصحة',
        quota: 'الحصة',
        consecutiveFailures: 'الفشل المتتالي',
        manualDisabled: 'manual_disabled', // تعداد خالص
        yes: 'YES',
        no: 'NO',
        totalRequests: 'إجمالي الطلبات',
        totalModels: 'إجمالي النماذج',
        availableModels: 'النماذج المتاحة',
        aggregateSuccessRate: 'معدل النجاح الإجمالي',
        brokenModelCount: 'عدد نماذج broken',
      },
    },
    concurrency: {
      sectionTitle: 'تحديد التزامن',
      manual: 'يدوي',
      auto: 'تلقائي',
      effective: 'فعّال',
      notSet: 'غير معين',
      adjustAuto: 'تعديل القيمة التلقائية',
      tempDemote: 'تخفيض مؤقت',
      restore: 'استعادة الاتصال',
    },
    modelsTab: {
      listTitle: 'قائمة النماذج ({n})',
      clickHint: 'انقر لعرض التفاصيل',
      empty: 'لا توجد نماذج',
      sampleUnit: 'عينة',
      neverCalled: 'لم يتم استدعاؤها',
      unavail: 'unavail',
      // أيقونات الحالة اليدوية + تلميح الأدوات
      manualDisabledTitle: 'حالة التعطيل اليدوي الحالية، انقر للإلغاء',
      manualDisabledNot: 'تعطيل هذا النموذج يدويًا',
      manualDisabled: 'تعطيل يدوي',
      manualOnlineTitle: 'حالة التشغيل اليدوي (offline) الحالية، انقر للتبديل إلى التلقائي',
      manualOnlineNot: 'تعيين هذا النموذج يدويًا كـ offline (سيتم تخطي الفحص التلقائي)',
      manualOnline: 'تشغيل يدوي',
      autoTitle: 'يتحكم فيه الفحص التلقائي (لا يمكن التبديل يدويًا إلى هذه الحالة)',
      auto: 'تلقائي',
      // بطاقة إحصائيات النموذج
      stats: {
        p95: 'P95 للكمون',
        msUnit: 'ms',
        recentSuccessRate: 'معدل النجاح الأخير',
        sampleCount: 'عدد العينات',
        last24hCalls: 'عدد الاستدعاءات في 24 ساعة',
      },
      // النافذة المنزلقة
      slidingTitle: 'النافذة المنزلقة (آخر ساعة)',
      redisSource: 'Redis',
      requestLogsSource: 'request_logs',
      loading: 'جاري التحميل...',
      noData: 'لا توجد بيانات',
      refresh: '↻ تحديث',
      // مخطط توزيع الأخطاء
      errorsTitle: 'توزيع الأخطاء',
      // بطاقة الفتحة
      slotInfoTitle: 'معلومات الفتحة المزدوجة',
      emptyHint: 'يرجى اختيار نموذج من اليسار لعرض التفاصيل',
    },
    requestsTab: {
      historySectionTitle: 'بيانات الطلبات · تاريخ تغيرات حالة النموذج',
      historyEmpty: 'انقر فوق النموذج في تبويب «النماذج» للعرض',
      historyRefresh: '↻ تحديث',
      historyLoading: 'جاري التحميل...',
      historyNoEvents: 'لا توجد سجلات تغير الحالة',
      historyColTime: 'الوقت',
      historyColSource: 'المصدر',
      historyColEvent: 'الحدث',
      historyColDetail: 'التفاصيل',
      autoPrefix: 'تلقائي · ', // يليه scheduler/admin، إلخ
      manualPrefix: 'يدوي · ',
      routingSectionTitle: 'بيانات الطلبات · قرارات التوجيه الأخيرة ({n})',
      routingRefresh: '↻ تحديث',
      routingLoading: 'جاري التحميل...',
      routingNoEvents: 'لا توجد سجلات قرارات توجيه',
      routingColTime: 'الوقت',
      routingColRequestId: 'معرّف الطلب',
      routingColModel: 'النموذج',
      routingColTier: 'Tier',
      routingColResult: 'النتيجة',
      routingColLatency: 'الكمون',
      routingColError: 'الخطأ',
      msUnit: 'ms',
    },
  },
  dialog: {
    cancel: 'إلغاء', // نفس معنى common.button.cancel
    // الاستعادة/التخفيض المجمع
    batchTitle: {
      promote: 'استعادة مجمعة',
      demote: 'تخفيض مجمع',
    },
    batchTitleSuffix: 'بيانات اعتماد)', // يأتي بعد عنوان الدفعة
    batchReasonLabel: 'السبب',
    batchReasonPlaceholder: 'يرجى إدخال السبب',
    batchHoursLabel: 'وقت الاستعادة التلقائي (ساعات)',
    batchSubmit: {
      promote: 'تأكيد الاستعادة',
      demote: 'تأكيد التخفيض',
    },
    // تخفيض واحد
    demoteTitle: 'تخفيض مؤقت',
    demoteReasonLabel: 'سبب التخفيض',
    demoteReasonPlaceholder: 'يرجى إدخال السبب',
    demoteHoursLabel: 'وقت الاستعادة التلقائي (ساعات)',
    demoteSubmit: 'تأكيد التخفيض',
    // استعادة واحدة
    promoteTitle: 'استعادة الاتصال',
    promoteReasonLabel: 'سبب الاستعادة',
    promoteReasonPlaceholder: 'يرجى إدخال السبب',
    promoteSubmit: 'تأكيد الاستعادة',
    // التزامن
    concurrencyTitle: 'ضبط قيمة التزامن التلقائي يدويًا',
    concurrencyLimitLabel: 'الحد الأعلى للتزامن',
    concurrencyReasonLabel: 'سبب التعديل',
    concurrencyReasonPlaceholder: 'يرجى إدخال السبب',
    concurrencySubmit: 'تأكيد',
    // تبديل واحد offline/online
    toggleTitle: {
      offline: 'تأكيد عدم الاتصال',
      online: 'تأكيد الاتصال',
    },
    toggleCredentialPrefix: 'بيانات اعتماد #',
    toggleCredentialSep: ' - ',
    toggleNoLabel: 'بدون علامة',
    toggleOfflineBody: 'بعد عدم الاتصال، لن يلمس الفحص التلقائي هذا النموذج (السبب = {code})، تحتاج إلى استعادته يدويًا.',
    toggleOnlineBody: 'بعد الاستعادة، ستعيد جولة الفحص التلقائي التالية (~10 دقائق) التقييم.',
    toggleReasonLabel: 'السبب (مطلوب)',
    toggleReasonPlaceholder: 'مثل: حكم خاطئ broken / حظر طارئ / تحقق تدريجي',
    toggleSubmit: {
      offline: 'تأكيد عدم الاتصال',
      online: 'تأكيد الاتصال',
    },
    // مسح manual_disabled
    clearTitle: 'مسح manual_disabled',
    clearWarning: '⚠️ ستستعيد هذا الإجراء بيانات الاعتماد فورًا إلى تجمع التوجيه العادي، وستُمسح علامة manual_disabled. يرجى التأكد من أن بيانات الاعتماد هذه تعمل بشكل طبيعي بالفعل.',
    clearReasonLabel: 'سبب العملية (مطلوب)',
    clearReasonPlaceholder: 'مثل: تعافي المزود / تصحيح عملية خاطئة / اكتمال التحقق التدريجي',
    clearSubmit: 'تأكيد المسح',
    // تعيين manual_disabled
    setTitle: {
      disable: 'تعطيل بيانات الاعتماد',
      enable: 'تفعيل بيانات الاعتماد',
    },
    setDisableBody: '⚠️ سيعين هذا الإجراء manual_disabled = true، ستُزال بيانات الاعتماد من تجمع التوجيه، ولن تعالج أي حركة مرور حتى الاستعادة اليدوية.',
    setEnableBody: '✓ سيعين هذا الإجراء manual_disabled = false، ستستعيد بيانات الاعتماد تجمع التوجيه العادي.',
    setReasonLabel: 'سبب العملية (مطلوب)',
    setReasonPlaceholderDisable: 'مثل: صيانة المزود / استنفاد الحصة / عدم اتصال مؤقت',
    setReasonPlaceholderEnable: 'مثل: تعافي المزود / اكتمال الصيانة / اجتياز الاختبار',
    setSubmit: {
      disable: 'تأكيد التعطيل',
      enable: 'تأكيد التفعيل',
    },
  },
  chart: {
    allHealthy: 'الكل صحي',
    errorsWhenHealthy: 'توزيع أنواع الأخطاء · الكل صحي',
    errorsTitle: 'توزيع أنواع الأخطاء',
    slidingStatsTotal: 'الإجمالي: ',
    slidingStatsSuccess: 'نجاح: ',
    slidingStatsFailed: 'فشل: ',
    slidingStatsFailureRate: 'معدل الفشل: ',
  },
  error: {
    selectFirst: 'يرجى اختيار بيانات الاعتماد أولاً',
    clearFailed: 'فشل المسح: ', // يليه e.message
    setManualDisabledFailed: 'فشلت العملية: ',
    batchFailed: 'فشلت العملية المجمعة: ',
    demoteFailed: 'فشل التخفيض: ',
    promoteFailed: 'فشل الترقية: ',
    concurrencyFailed: 'فشل الإعداد: ',
    offlineFailed: 'فشل عدم الاتصال: ',
    onlineFailed: 'فشل الاتصال: ',
  },
}