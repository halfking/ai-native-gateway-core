// Arabic locale for ClientConfigDialog
export default {
  title: 'مولد تكوين {tool}',
  close: 'إغلاق',
  
  step1: {
    title: '① اختر مفتاح API (جميع المفاتيح تحت المستأجر الحالي)',
    refresh: 'تحديث',
    refreshing: 'جارٍ التحديث…',
    loading: 'جارٍ التحميل…',
    empty: {
      title: 'لا توجد مفاتيح API',
      description: 'لا توجد مفاتيح API متاحة تحت المستأجر الحالي. يمكن للمسؤولين النقر على الزر أدناه لطلب مفتاح جديد.',
      applyButton: 'طلب مفتاح جديد',
    },
    selected: 'المحدد:',
  },
  
  step2: {
    title: '② نظام التشغيل',
    pathHint: 'مسار ملف التكوين:',
  },
  
  step3: {
    title: '③ اختر نطاق النموذج',
    featured: 'النماذج المميزة (تكوين التوجيه featured)',
    all: 'جميع النماذج المتاحة (من البوابة)',
    featuredPreview: {
      loading: 'جارٍ التحميل…',
      empty: 'لا توجد نماذج مميزة مكونة في التوجيه',
      manageButton: 'انتقل إلى "سياسة التوجيه" للتكوين →',
      manage: 'إدارة →',
    },
    allModels: {
      selectAll: 'تحديد الكل',
      deselectAll: 'مسح',
      selected: 'محدد',
      filterHint: '({count} تطابق البحث)',
      searchPlaceholder: '🔍 بحث عن اسم النموذج / المورد / العائلة…',
      loading: 'جارٍ التحميل…',
      noMatch: 'لا توجد نماذج مطابقة',
    },
  },
  
  footer: {
    generated: 'تم إنشاء {count} تكوينات نموذج',
    generate: 'إنشاء التكوين',
    generating: 'جارٍ الإنشاء…',
    regenerate: 'إعادة الإنشاء',
  },
  
  results: {
    tabs: {
      file: 'ملف التكوين',
      script: 'سكريبت التكوين',
      manual: 'الخطوات اليدوية',
    },
    actions: {
      copyContent: 'نسخ المحتوى',
      downloadFile: 'تحميل الملف',
      downloadScript: 'تحميل السكريبت',
      scriptHint: 'يقوم السكريبت تلقائيًا بنسخ احتياطية لملفات التكوين القديمة',
    },
  },
  
  applyDialog: {
    title: 'طلب مفتاح API جديد',
    close: 'إغلاق',
    applicationCode: 'رمز التطبيق (application_code)',
    applicationCodePlaceholder: 'مثال: default-app',
    description: 'الوصف',
    descriptionPlaceholder: 'اختياري: السبب / الغرض',
    cancel: 'إلغاء',
    submit: 'إرسال الطلب',
    submitting: 'جارٍ الإرسال…',
  },
  
  error: {
    applyFailed: 'فشل الطلب',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'لا يدعم Cursor الكتابة إلى الملفات — يرجى التهيئة يدويًا عبر واجهة Settings',
}
