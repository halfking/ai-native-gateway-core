// Arabic locale for ClientConfigDialog
export default {
  title: 'مولد تكوين {tool}',
  close: 'إغلاق',

  step1: {
    title: "① اختر مفتاح API متاحًا",
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
    pathHint: "موقع التكوين:",
  },

  step3: {
    title: "④ اختر نطاق النماذج",
    featured: "النماذج المميزة (توجيه مستأجر المفتاح المحدد)",
    all: 'جميع النماذج المتاحة (من البوابة)',
    featuredPreview: {
      empty: "لم يتم تكوين نماذج مميزة للمستأجر المحدد",
      manageButton: 'انتقل إلى "سياسة التوجيه" للتكوين →',
      manage: 'إدارة →',
    },
    allModels: {
      selectAll: 'تحديد الكل',
      deselectAll: 'مسح',
      selected: 'محدد',
      filterHint: '({count} تطابق البحث)',
      searchPlaceholder: '🔍 بحث عن اسم النموذج / المورد / العائلة…',
      noMatch: 'لا توجد نماذج مطابقة',
    },
  },

  retry: "إعادة المحاولة",
  otherFamily: "أخرى",
  gateway: {
    title: "③ عنوان API للبوابة",
    hint: "يُستخدم عنوان هذه البوابة افتراضيًا. تحتوي الملفات والبرامج النصية الناتجة على مفتاح API المحدد؛ احفظها بأمان.",
  },
  provider: {
    label: "اسم الموفر",
    hint: "يُحدّث الإدخال إذا تطابق الاسم والعنوان. إذا اختلف العنوان، يتوقف البرنامج النصي ويطلب تغيير الاسم.",
  },
  notes: {
    cherryStudio: "أدخل المفتاح المحدد في إعدادات العميل. يسرد هذا الملف المعلمات فقط.",
    rooCode: "كوّن OpenAI Compatible في إعدادات Roo Code.",
  },

  footer: {
    generated: 'تم إنشاء {count} تكوينات نموذج',
    generate: 'إنشاء التكوين',
    generating: 'جارٍ الإنشاء…',
    regenerate: 'إعادة الإنشاء',
  },

  results: {
    tabs: {
      yaml: "مقتطف YAML",
      file: 'ملف التكوين',
      script: 'سكريبت التكوين',
      manual: 'الخطوات اليدوية',
    },
    actions: {
      scriptWarning: "يحتوي البرنامج النصي على مفتاحك. نزّله وشغّله محليًا؛ لا يُعرض محتواه هنا.",
      mergeHint: "ينسخ البرنامج النصي التكوين القديم احتياطيًا ويدمج {provider} ويحافظ على الموفرين الآخرين",
      copyContent: 'نسخ المحتوى',
      downloadFile: 'تحميل الملف',
      downloadScript: 'تحميل السكريبت',
    },
  },

  applyDialog: {
    title: 'طلب مفتاح API جديد',
    applicationCode: 'رمز التطبيق (application_code)',
    applicationCodePlaceholder: 'مثال: default-app',
    description: 'الوصف',
    descriptionPlaceholder: 'اختياري: السبب / الغرض',
    cancel: 'إلغاء',
    submit: 'إرسال الطلب',
    submitting: 'جارٍ الإرسال…',
  },

  error: {
    invalidGatewayURL: "أدخل عنوان API صالحًا يبدأ بـ HTTP(S) ولا يحتوي على بيانات اعتماد أو معاملات استعلام أو جزء عنوان.",
    invalidProviderName: "استخدم من 1 إلى 64 حرفًا لاتينيًا (A–Z) أو رقمًا أو شرطة سفلية أو واصلة لاسم المزوّد، على أن يبدأ بحرف أو رقم.",
    featuredLoadFailed: "تعذّر تحميل النماذج المميزة. أعد المحاولة.",
    catalogLoadFailed: "تعذّر تحميل دليل النماذج. أعد المحاولة.",
    selectModel: "اختر نموذجًا واحدًا على الأقل. إذا لم توجد نماذج مميزة، فكوّنها أولًا في سياسة التوجيه.",
    keyUnavailable: "تعذّرت قراءة المفتاح المحدد. اختره مرة أخرى.",
    generateFailed: "فشل إنشاء التكوين. أعد المحاولة.",
    copyFailed: "فشل النسخ. نزّل الملف.",
    applyFailed: 'فشل الطلب',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'لا يدعم Cursor الكتابة إلى الملفات: يرجى التهيئة يدويًا عبر واجهة Settings',
}
