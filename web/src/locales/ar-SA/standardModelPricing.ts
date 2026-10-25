// Auto-translated draft (ar-SA) · 2026-07-02 · please review
// standardModelPricing.ts — نصوص صفحة إدارة تسعير النماذج القياسية (StandardModelPricingView).
// فضاء الأسماء: page / desc / settings / table / editModal / status / feedback.
// المصطلحات التقنية تبقى كما هي: canonical_name، credits_per_1m_in/out/cache_in/cache_out، cents_per_credit، global_discount، CNY/USD.
export default {
  page: {
    title: 'إدارة التسعير',
    refresh: 'تحديث',
    refreshLoading: 'جاري التحميل…',
  },
  desc: 'تسعير جانب المستأجر (رصيد/1M رمز). يتم تطبيق المعيار العام × معامل الخصم على الأبعاد <strong>غير المُسعّرة يدويًا</strong>؛ الحقول التي تم تحديد «يدوي» عليها لا تتأثر بالتغييرات العامة.',
  settings: {
    title: 'المعيار العام (رصيد / 1M رمز)',
    inputToken: 'إدخال الرموز',
    outputToken: 'إخراج الرموز',
    cacheReadToken: 'قراءة ذاكرة التخزين المؤقت للرموز',
    cacheWriteToken: 'كتابة ذاكرة التخزين المؤقت للرموز',
    discount: 'خصم شامل',
    discountHint: 'تطبيق خصم موحد لجميع الأبعاد غير اليدوية',
    centsPerCredit: 'سعر الرصيد (سنت/رصيد)',
    currencyDisplay: 'عملة العرض',
    save: 'حفظ الإعدادات العامة',
    saving: 'جاري الحفظ…',
    effective: 'فعّال ≈ {value} (يشمل الخصم)',
  },
  filter: {
    summary: 'يدوي {custom}: عام {defaults} / {total}',
  },
  table: {
    colModel: 'النموذج القياسي',
    colStatus: 'الحالة',
    edit: 'تسعير',
    reset: 'استعادة',
    empty: 'لا توجد نماذج',
    statusDefault: 'عام',
    statusFullCustom: 'يدوي بالكامل',
    statusPartialCustom: '{n}/4 يدوي',
    manualTagTitle: 'تسعير يدوي',
    manualTag: 'ي',

    colModality: 'Modality',

    modalityText: 'Text',

    modalityVision: 'Vision',

    modalityAudio: 'Audio',

    modalityVideo: 'Video',

    modalityMultimodal: 'Multimodal',

    modalityEmbedding: 'Embedding',

    modalityOther: 'Other',

    hasMultimodal: 'Has manual multimodal pricing',
  },
  editModal: {
    title: 'تسعير يدوي: {name}',
    hint: 'حدد «يدوي» واملأ الرصيد؛ تستمر الأبعاد غير المحددة في اتباع المعيار العام × الخصم.',
    fieldSuffix: ': تسعير يدوي',
    resetBase: 'استعادة المعيار',
    globalApprox: 'العام ≈ {value}',
    save: 'حفظ',
    saving: 'جاري الحفظ…',
    fillGlobal: 'ملء بالقيمة العامة الحالية',
    cancel: 'إلغاء',
    resetConfirm: 'استعادة جميع أبعاد {name} إلى المعيار العام؟',

    sectionText: 'Text-token dimensions',

    sectionMultimodal: 'أبعاد الرموز متعددة الوسائط (رؤية / صوت / فيديو)',



  },
  field: {

    image: 'Vision',

    audio: 'Audio',

    video: 'Video',
  },

  // 扁平键（供 Vue 组件直接使用）
  defaultOnly: 'المعيار العام فقط',
  customOnly: 'يشمل التسعير اليدوي',
  partialOnly: 'يدوي جزئي',
  otherVendor: 'أخرى',
  loadFailed: 'فشل التحميل',
  saved: 'تم حفظ المعيار العام (يؤثر فقط على الأبعاد غير المُسعّرة يدويًا)',
  saveFailed: 'فشل الحفظ',
  needOneManual: 'يرجى تحديد بُعد واحد على الأقل لـ «تسعير يدوي»',
  resetFailed: 'فشل الاستعادة',
  input: 'إدخال',
  output: 'إخراج',
  cacheRead: 'قراءة ذاكرة التخزين المؤقت',
  cacheWrite: 'كتابة ذاكرة التخزين المؤقت',

  batch: {
    selected: 'تم تحديد {n} عنصر',
    paste: 'لصق في المحدد',
    price: 'تسعير جماعي',
    resetAll: 'استعادة الكل إلى المعيار العام',
    resetAllConfirm: 'هل تريد استعادة التسعير اليدوي بالكامل في {n} نموذجًا إلى المعيار العام؟',
    fillGlobalConfirm: 'هل تريد كتابة المعيار العام الحالي مضروبًا في الخصم على {n} نموذجًا مع الإبقاء على علامات التسعير اليدوي؟',
    msgPasted: 'تم تحديث {n} نموذجًا',
    msgReset: 'تمت استعادة {n} نموذجًا',
    msgFilled: 'تمت الكتابة في {n} نموذجًا',
    msgFailed: 'فشلت العملية الجماعية',

    fillGlobal: 'Fill current global',
  },
}