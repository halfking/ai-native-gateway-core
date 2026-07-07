// formatAnomaliesView.ts — صفحة مراقبة شذوذات التنسيق (ar-SA).
export default {
  pageTitle: 'مراقبة شذوذات التنسيق',
  pageSubtitle: 'عرض سريع لتغييرات تنسيق استجابة المزود، وفشل استخراج الرموز، ومشاكل التوافق.',

  stats: {
    total: 'إجمالي الشذوذات',
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
    unresolvedOnly: 'غير المحلولة فقط',
    query: 'بحث',
    refresh: 'تحديث',
  },

  anomalyType: {
    all: 'جميع أنواع الشذوذات',
    missing_usage_block: 'كتلة usage مفقودة',
    zero_completion_tokens: 'Completion Tokens يساوي 0',
    extraction_failed: 'فشل الاستخراج',
    unexpected_structure: 'هيكل غير متوقع',
    null_usage_values: 'قيم usage فارغة',
    token_mismatch: 'عدم تطابق الرموز',
    missing_provider_tokens: 'رموز المزود مفقودة',
    missing_client_tokens: 'رموز العميل مفقودة',
    json_parse_error: 'خطأ في تحليل JSON',
    missing_finish_reason: 'Finish Reason مفقود',
    missing_content: 'Content مفقود',
  },

  anomalyTypeDescription: {
    missing_usage_block: 'استجابة المنبع تفتقد كتلة usage',
    zero_completion_tokens: 'الاستجابة تحتوي محتوى لكن completion_tokens يساوي 0',
    extraction_failed: 'تعذر استخراج معلومات usage من الاستجابة',
    unexpected_structure: 'الهيكل المُرجّع من المنبع غير متسق مع المتوقع',
    null_usage_values: 'حقول usage موجودة لكن القيم فارغة',
  },

  severity: {
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
    tokenInfo: 'معلومات الرمز',
    status: 'الحالة',
    actions: 'الإجراءات',
    loading: 'جارٍ التحميل...',
    noData: 'لم يتم العثور على سجلات شذوذات',
    viewDetail: 'التفاصيل',
    expectedTokens: 'المتوقع: {count}',
    actualTokens: 'الفعلي: {count}',
  },

  token: {
    expected: 'المتوقع',
    actual: 'الفعلي',
  },

  pager: {
    prev: 'السابق',
    next: 'التالي',
    summary: 'صفحة {page} / {totalPages}، {total} سجل',
  },

  detail: {
    title: 'تفاصيل الشذوذ',
    close: 'إغلاق',
    requestId: 'Request ID',
    detectedAt: 'تاريخ الاكتشاف',
    provider: 'المزود',
    model: 'النموذج',
    outboundModel: 'النموذج الصادر',
    usageSource: 'Usage Source',
    responseStructure: 'هيكل الاستجابة',
    responseSample: 'نموذج الاستجابة',
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
    markFailed: 'فشل التحديد',
    needSuperAdmin: 'يتطلب صلاحية مدير عام',
  },
}
