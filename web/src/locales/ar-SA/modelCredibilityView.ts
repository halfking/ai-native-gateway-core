// modelCredibilityView.ts — موثوقية النماذج (صفحة ar-SA).
export default {
  pageTitle: 'موثوقية النماذج',
  pageSubtitle:
    'يكشف ما إذا كان المزوّد يبيع نموذجًا أصغر على أنه أكبر (استبدال أو تخفيض أو تخفيف). ' +
    'يرتكز الحكم على أربع قياسات مستقلة: النموذج الذي يعلنه المزوّد، ومقارنة القدرة مع بقية العُقد التي ' +
    'تخدم النموذج نفسه، وثبات بصمة الطلب، ومُرجَع خارجي. وحين لا يمكن استنتاج شيء تُعرض ' +
    '«أدلة غير كافية» — ولا تُعرض أبدًا على أنها «لا مشكلة».',

  stats: {
    total: 'العُقد المقيَّمة',
    verified: 'موثَّقة',
    suspected: 'مشتبه بها',
    diluted: 'مخفَّفة',
    unknown: 'أدلة غير كافية',
    providers: 'المزوّدون المعنيّون',
  },

  filter: {
    verdict: 'الحكم',
    verdictPlaceholder: 'كل الأحكام',
    model: 'النموذج',
    modelPlaceholder: 'تصفية حسب اسم النموذج',
    query: 'استعلام',
    refresh: 'تحديث',
    scanAll: 'فحص الكل',
    scanning: 'جارٍ الفحص…',
    sort: 'الترتيب',
    sortTime: 'حسب وقت القياس (الأحدث أولًا)',
    sortName: 'حسب اسم النموذج',
    sortScore: 'حسب الموثوقية (تصاعديًا)',
  },

  verdict: {
    verified: 'موثَّق',
    suspected: 'مشتبه به',
    diluted: 'مخفَّف',
    unknown: 'أدلة غير كافية',
  },

  dimension: {
    identity: 'الهوية',
    capability: 'القدرة',
    consistency: 'ثبات البصمة',
    reference: 'المرجع الخارجي',
  },

  table: {
    provider: 'المزوّد',
    credential: 'بيانات الاعتماد',
    model: 'النموذج',
    credibility: 'الموثوقية',
    verdict: 'الحكم',
    dimensions: 'أبعاد الأدلة',
    measuredAt: 'وقت القياس',
    action: 'إجراءات',
  },

  status: {
    success: 'سليم',
    partial: 'جزئي',
    failed: 'فشل الفحص',
    inconclusive: 'غير حاسم',
    unknown: 'غير مقيس',
  },

  anomaly: {
    identity_model_name_mismatch: 'المزوّد يعلن نموذجًا مختلفًا عن المطلوب',
    identity_family_mismatch: 'المزوّد يعلن نموذجًا من جهة مصنّعة أخرى',
    identity_reported_model_absent: 'المزوّد لا يُرجع اسم النموذج، فلا يمكن إثبات الهوية',
    capability_below_peer_best: 'القدرة أقل من أفضل عقدة تخدم النموذج نفسه',
    capability_severely_below_peer_best: 'القدرة أقل بكثير من أفضل عقدة تخدم النموذج نفسه',
    capability_above_peer_best: 'القدرة أعلى من أفضل عقدة تخدم النموذج نفسه',
    consistency_prompt_token_drift: 'بصمة الطلب تغيّرت — قد يكون البديل الخلفي قد استُبدل',
    consistency_system_fingerprint_drift: 'بصمة المزوّد تغيّرت',
    reference_below_expected_accuracy: 'دون المستوى الذي يتوقعه المرجع الخارجي',
    capability_no_measurement: 'لا تتوفر قياسات قدرة قابلة للاستخدام',
    capability_peer_sample_too_small: 'عدد العقد المرجعية لهذا النموذج قليل جدًا',
    identity_probe_failed: 'فشل طلب فحص الهوية',
    identity_probe_empty_response: 'لم يعِد فحص الهوية بأي إجابة',
  },

  detail: {
    title: 'تفاصيل الحكم',
    evidence: 'الأدلة',
    anomalies: 'الملاحظات الشاذة',
    history: 'سجل الأحكام',
    noHistory: 'لا يوجد سجل أحكام بعد',
    requestedModel: 'النموذج المطلوب',
    reportedModel: 'النموذج الذي أعلنه المزوّد',
    promptSignature: 'بصمة الطلب (عدد رموز الطلب)',
    fingerprint: 'بصمة المزوّد',
    measuredDimensions: 'الأبعاد التي توفّرت لها بيانات هذه الجولة',
    close: 'إغلاق',
  },

  empty: {
    noData: 'لا توجد بيانات موثوقية بعد.',
    hint: 'تنتج الأحكام من الفحص الدوري في الخلفية (طلب فحص صغير واحد لكل عقدة في كل جولة)؛ ويمكنك الضغط على «فحص الكل» لتشغيل جولة الآن.',
  },

  error: {
    loadFailed: 'تعذّر تحميل بيانات موثوقية النماذج. يُرجى إعادة المحاولة.',
    scanFailed: 'تعذّر إكمال فحص موثوقية النماذج. يُرجى إعادة المحاولة.',
    detailFailed: 'تعذّر تحميل تفاصيل حكم الموثوقية. يُرجى إعادة المحاولة.',
  },
}
