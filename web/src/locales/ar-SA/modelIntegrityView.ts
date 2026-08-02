// modelIntegrityView.ts — صفحة مراقبة سلامة النماذج (ar-SA).
export default {
  pageTitle: 'مراقبة سلامة النماذج',
  pageSubtitle: 'تتبع استبدال النماذج، اقتطاع الاستجابة، الاستجابات الفارغة، المحتوى المكرر، وانحراف البصمة.',

  tabs: {
    events: 'أحداث السلامة',
    drift: 'انحراف البصمة',
  },

  stats: {
    total: 'إجمالي الأحداث',
    unresolved: 'غير محلولة',
    critical: 'حرجة',
    window: 'نافذة الإحصائيات',
  },

  filter: {
    provider: 'المزود',
    providerPlaceholder: 'اختر مزودًا…',
    model: 'النموذج',
    modelPlaceholder: 'اختر نموذجًا…',
    anomalyType: 'نوع الشذوذ',
    anomalyTypePlaceholder: 'اختر نوع الشذوذ…',
    severity: 'الخطورة',
    unresolvedOnly: 'غير المحلولة فقط',
    query: 'بحث',
    refresh: 'تحديث',
  },

  anomalyType: {
    all: 'جميع أنواع الشذوذات',
    model_mismatch: 'عدم تطابق النموذج',
    finish_refusal: 'رفض / فلتر المحتوى',
    finish_truncation: 'اقتطاع عند الانتهاء',
    token_arith_fail: 'فشل حساب الرموز',
    empty_response: 'استجابة فارغة',
    repeated_content: 'محتوى مكرر',
    fingerprint_drift: 'انحراف البصمة',
  },

  anomalyTypeDescription: {
    model_mismatch: 'المنبع أعاد نموذجًا لا يطابق النموذج المطلوب (استبدال صامت محتمل)',
    finish_refusal: 'المنبع أعاد رفضًا أو content_filter',
    finish_truncation: 'تم الاقتطاع بسبب الطول / max_tokens',
    token_arith_fail: 'prompt + completion لا يساوي total',
    empty_response: 'الدفق لم ينتج محتوى ولا رموزًا',
    repeated_content: 'نص الاستجابة يحتوي على كتل مكررة كبيرة (حلقة نموذج محتملة)',
    fingerprint_drift: 'انحرف system_fingerprint عن خط الأساس',
  },

  severity: {
    all: 'جميع مستويات الخطورة',
    critical: 'حرجة',
    high: 'عالية',
    medium: 'متوسطة',
    low: 'منخفضة',
  },

  status: {
    resolved: 'محلولة',
    unresolved: 'غير محلولة',
  },

  table: {
    detectedAt: 'تاريخ الاكتشاف',
    severity: 'الخطورة',
    anomalyType: 'نوع الشذوذ',
    providerModel: 'المزود / النموذج',
    requestId: 'Request ID',
    actual: 'الفعلي',
    status: 'الحالة',
    actions: 'الإجراءات',
    loading: 'جارٍ التحميل...',
    noData: 'لم يتم العثور على أحداث سلامة',
    viewDetail: 'التفاصيل',
  },

  drift: {
    days: 'الأيام',
    query: 'بحث',
    noData: 'لم يتم اكتشاف انحراف بصمة في النافذة المحددة',
  },

  pager: {
    prev: 'السابق',
    next: 'التالي',
    summary: 'صفحة {page} / {totalPages}، {total} سجل',
  },

  detail: {
    title: 'تفاصيل حدث السلامة',
    close: 'إغلاق',
    requestId: 'Request ID',
    detectedAt: 'تاريخ الاكتشاف',
    provider: 'المزود',
    model: 'النموذج',
    outboundModel: 'النموذج الصادر',
    credential: 'معرّف بيانات الاعتماد',
    expected: 'المتوقع',
    actual: 'الفعلي',
    context: 'السياق',
    sample: 'نموذج',
    resolutionNotes: 'ملاحظات الحل',
    resolutionNotesPlaceholder: 'سجّل ملاحظات الإصلاح للتتبع المستقبلي',
    markResolved: 'تحديد كمحلول',
    processing: 'جارٍ المعالجة...',
    resolutionInfo: 'معلومات الحل',
    noNotes: 'لا توجد ملاحظات حل',
  },

  error: {
    loadFailed: 'فشل التحميل',
    summaryLoadFailed: 'فشل تحميل الإحصائيات',
    driftLoadFailed: 'فشل تحميل انحراف البصمة',
    markFailed: 'فشل التحديد',
    needSuperAdmin: 'يتطلب صلاحية مدير عام',
  },
}
