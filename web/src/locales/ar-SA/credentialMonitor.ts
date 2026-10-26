// Auto-translated draft (ar-SA) · 2026-07-02 · please review
// credentialMonitor.ts — نصوص صفحة مراقبة بيانات الاعتماد (CredentialMonitorView).
// فضاء الأسماء: page / filter / summary / table / drawer / dialog / status / action / error / chart.
// سلاسل التعداد في الخلفية (ready/degraded/healthy/warning/broken، إلخ) تبقى كما هي — الترجمة تكسر استدعاءات API.
// يعيد استخدام مفردات الأزرار/الحالة من common/keys؛ المصطلحات الخاصة بهذه الصفحة مركزة في هذا الملف.
export default {
  page: {
    title: 'مراقبة بيانات الاعتماد',
  },
  table: {
    cell: {
      modelsNotMeasured: 'لم تُرجع هذه بيانات الاعتماد أي أعداد للنماذج في هذه المرة (لم تُقاس). وليست «0 متاحة»',
    },
  },
  drawer: {
    tab: {
      overview: 'المعلومات الأساسية',
      models: 'النماذج',
      requests: 'بيانات الطلبات',
    },
    operationFailedWithReason: 'فشلت العملية: {reason}. أعد المحاولة.',
    offlineFailedWithReason: 'فشل عدم الاتصال: {reason}. أعد المحاولة.',
    onlineFailedWithReason: 'فشل الاتصال: {reason}. أعد المحاولة.',
    degradeFailedWithReason: 'فشل التخفيض: {reason}. أعد المحاولة.',
    upgradeFailedWithReason: 'فشل الترقية: {reason}. أعد المحاولة.',
    settingFailedWithReason: 'فشل الإعداد: {reason}. أعد المحاولة.',
    manualDisabledTooltip: 'تم التعطيل يدويًا، لن يلمسه الفحص التلقائي. يمكن تشغيله يدويًا.',
    availableTooltip: 'متاح حاليًا، يمكن تعطيله يدويًا.',
    autoControlledTooltip: 'يتحكم فيه الفحص التلقائي (broken_confirmed وغيرها). يمكن تعطيله يدويًا (إجبار عدم الاتصال).',
    rowSelectsMonitor: 'تحديد الصف يُظهر المراقبة المرتبطة على اليمين.',
    selectModelRow: 'تحديد صف النموذج على اليسار لعرضه.',
  },
  chart: {
    allHealthy: 'الكل صحي',
    errorsWhenHealthy: 'توزيع أنواع الأخطاء: الكل صحي',
    errorsTitle: 'توزيع أنواع الأخطاء',
    /**
     * حالة فارغة (الدفعة 14 · «حالات فارغة»).
     * ★ kind = **Filtered**: هذه الصفحة فيها تصفية بالنموذج + استثناء الفحص الذاتي
     *   + «غير السليم فقط» + نطاق زمني ⇒ الفراغ يعني «تمت التصفية»، لا «لا توجد بيانات».
     *   كان النص السابق مفتاحًا عامًا واحدًا + تلميحًا ثابتًا مكتوبًا داخل المكوّن
     *   ⇒ الآن جملة واحدة قابلة للترجمة.
     */
    emptyFiltered: 'لا توجد سجلات بيانات الاعتماد في النطاق الزمني الحالي وشروط التصفية المطبَّقة. يمكن توسيع الشروط ثم إعادة المحاولة.',
  },
  error: {
    selectFirst: 'يرجى اختيار بيانات الاعتماد أولاً',
    clearFailed: 'فشل المسح: ', // يليه e.message
    batchFailed: 'فشلت العملية المجمعة: ',
    superAdminOnlyHint: 'يمكن للمشرفين الكبار فقط إجراء تصحيحات الحالة؛ حاليًا للقراءة فقط.',
    superAdminOnlyError: 'يمكن للمشرفين الكبار فقط إجراء تصحيحات الحالة.',
  },
}