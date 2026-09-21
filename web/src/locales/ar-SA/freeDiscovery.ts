// freeDiscovery.ts — free discovery page copy (ar-SA).
export default {
  page: {
      title: "اكتشاف الموارد المجانية",
      desc: "تكوين قوالب المزودين → مسح قوائم النماذج المصدرية → المراجعة → الاستيراد الجماعي إلى مجمع الموارد المجانية (متصل بتتبع حصة OmniFree)."
    },
  common: {
      refresh: "تحديث",
      loading: "جارٍ المعالجة…",
      empty: "لا بيانات",
      actions: "إجراءات",
      enabled: "ممكّن",
      disabled: "معطّل",
      hideForm: "طي"
    },
  tabs: {
      templates: "إدارة القوالب",
      tasks: "المهام والمراجعة",
      history: "سجل الاستيراد"
    },
  presets: {
      title: "الإعدادات المسبقة للمزودين",
      create: "إنشاء",
      exists: "منشأ",
      createDone: "تم إنشاء القالب من الإعداد المسبق: {name}",
      scannerPending: "ماسح هذا البروتوكول قيد التكييف؛ سيفشل المسح حالياً",
      keyless: "keyless (بدون مفتاح)"
    },
  form: {
      show: "+ قالب مخصص",
      providerCode: "Provider Code *",
      displayName: "اسم العرض",
      baseUrl: "Base URL *",
      apiType: "بروتوكول API",
      apiKeyEnv: "متغير بيئة مفتاح API",
      apiKeyEnvHint: "لا يتم تخزين المفتاح نفسه: استخدم مرجع بيئة $VAR (مثل $GROQ_API_KEY)؛ اتركه فارغاً لـ keyless.",
      tosVerdict: "حكم ToS",
      submit: "إنشاء القالب",
      created: "تم إنشاء القالب"
    },
  orbi: {
      show: "استيراد JSON قالب Orbi",
      hint: "الصق محتوى ملف قالب Orbi pi-providers (JSON يحتوي على مفتاح providers في المستوى الأعلى). يمكن أن يحتوي الملف على عدة مزودين.",
      import: "استيراد",
      invalidJson: "فشل تحليل JSON؛ تحقق من التنسيق",
      done: "انتهى الاستيراد: نجح {created}، فشل {failed}"
    },
  tpl: {
      listTitle: "القوالب ({n})",
      name: "القالب",
      baseUrl: "Base URL",
      apiType: "بروتوكول API",
      keyEnv: "متغير بيئة المفتاح",
      tos: "ToS",
      enabled: "ممكّن",
      createdAt: "أُنشئ في",
      scan: "مسح",
      delete: "حذف",
      deleteConfirm: "حذف القالب \"{name}\"؟ المهام ونتائج الاكتشاف الموجودة لن تتأثر.",
      deleted: "تم حذف القالب: {name}",
      disabledScanHint: "القالب معطل — يرجى تمكينه قبل الفحص"
    },
  scan: {
      title: "بدء الاكتشاف",
      pickTemplate: "اختر قالباً ممكّناً…",
      start: "بدء المسح",
      running: "جارٍ المسح…",
      done: "انتهى المسح: عُثر على {n} نموذجاً؛ راجع النتائج",
      failed: "فشل المسح"
    },
  task: {
      listTitle: "مهام الاكتشاف ({n})",
      provider: "المزود",
      status: "الحالة",
      trigger: "المشغّل",
      found: "المعثور عليها",
      imported: "المستوردة",
      by: "بواسطة",
      time: "أُنشئت في",
      error: "خطأ",
      review: "مراجعة النتائج"
    },
  res: {
      title: "النتائج · مهمة {id} · {provider}",
      pending: "بانتظار المراجعة",
      all: "الكل",
      model: "معرف النموذج",
      displayName: "اسم العرض",
      freeType: "النوع المجاني",
      monthly: "الحصة الشهرية (tokens)",
      daily: "الحصة اليومية (tokens)",
      tos: "ToS",
      importStatus: "حالة الاستيراد",
      none: "لا نتائج لهذا الفلتر",
      policy: "سياسة التعارض",
      policySkip: "skip: الاحتفاظ بالمدخلات الموجودة",
      policyOverwrite: "overwrite: الكتابة فوقها وإعادة التمكين",
      policyMerge: "merge: ملء الحقول الفارغة فقط",
      importSelected: "استيراد المحدد ({n})",
      importAllPending: "استيراد كل المعلقة",
      importedToast: "انتهى الاستيراد: نجح {imported}، تخطى {skipped}، تعارض {conflicted}، فشل {failed}"
    },
  status: {
      taskPending: "بانتظار",
      running: "قيد التشغيل",
      success: "نجاح",
      failed: "فشل",
      review: "معلقة",
      imported: "مستورد",
      skipped: "متخطى",
      conflict: "تعارض"
    },
  trigger: {
      manual: "يدوي",
      scheduled: "مجدول",
      webhook: "Webhook"
    },
  hist: {
      title: "سجل الاستيراد ({n})",
      desc: "المهام التي استوردت نماذج فعلياً (عدد المستورد > 0). انقر التفاصيل لمعرفة المصير النهائي لكل نتيجة.",
      completed: "اكتملت في",
      detail: "التفاصيل",
      none: "لا سجلات استيراد بعد. أكمل استيراداً جماعياً في تبويب المهام والمراجعة وسيظهر هنا.",
      detailTitle: "تفاصيل الاستيراد · مهمة {id} · {provider}",
      importedAt: "استُورد في"
    },
}
