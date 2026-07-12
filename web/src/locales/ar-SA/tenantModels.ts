// tenantModels.ts — نصوص صفحة /tenant/models (عرض المستأجر: النماذج القياسية + الأسعار).
export default {
  page: {
    title: 'النماذج القياسية',
    desc: 'النماذج القياسية المفتوحة للمستأجر الخاص بك. تشمل التسعيرة الإدخال / الإخراج / قراءة ذاكرة التخزين المؤقت / كتابة ذاكرة التخزين المؤقت، بوحدات ائتمان لكل مليون رمز.',
    filterTitle: 'النماذج القياسية · تصفية',
    filterPlaceholder: 'اختر نموذجًا قياسيًا…',
    modelCount: '{n} نموذج',
    refresh: 'تحديث',
    loading: 'جارٍ التحميل…',
    empty: 'لا توجد نماذج متاحة',
    loadFailed: 'فشل التحميل',
    vendorSectionTitle: 'النماذج المتاحة',
  },
  columns: {
    model: 'النموذج القياسي',
    canonicalName: 'معرّف النموذج',
    family: 'العائلة',
    contextWindow: 'السياق',
    multimodal: 'متعدد الوسائط',
    modalityTag: 'النوع',
    billingMode: 'طريقة الفوترة',
    inPrice: 'إدخال / 1M',
    outPrice: 'إخراج / 1M',
    cacheIn: 'قراءة ذاكرة التخزين المؤقت / 1M',
    cacheOut: 'كتابة ذاكرة التخزين المؤقت / 1M',
  },
  modalities: {
    text: 'نص',
    vision: 'رؤية',
    audio: 'صوت',
    multimodal: 'متعدد الوسائط',
    embedding: 'تضمين',
  },
  billing: {
    token: 'ائتمانات لكل رمز',
  },
  multimodal: {
    yes: 'نعم',
    no: 'نص فقط',
  },
  context: {
    notSet: '—',
  },
  filterBar: {
    selectedVendor: 'المورّد المحدد: {vendor}',
    count: 'عرض {n} / {m}',
  },
  cta: {
    buyCredits: 'شراء ائتمانات',
  },
}