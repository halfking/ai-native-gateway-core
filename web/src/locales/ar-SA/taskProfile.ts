// taskProfile.ts — 任务档案页文案（v2 闭环 P0③，2026-09-24）。
export default {
    refresh: 'تحديث',
    refreshing: 'جارٍ التحديث…',
    loadFailed: 'فشل التحميل',
    noData: 'لا توجد بيانات ملفات',
  registry: {
    version: 'إصدار السجل',
    types: 'أنواع المهام',
    correctedTotal: 'إجمالي التصحيحات في النافذة',
    pendingSuggestions: 'تغييرات مستوى بانتظار القرار',
  },
  table: {
    taskType: 'نوع المهمة',
    description: 'الوصف',
    tier: 'الطبقة',
    fallbacks: 'سلسلة الاحتياط',
    minConf: 'الثقة الدنيا',
    total: 'التصحيحات',
    rate: 'معدل التصحيح',
    suggestion: 'الطبقة المقترحة',
  },
  action: {
    apply: 'تطبيق الاقتراحات',
    applyConfirm: 'كتابة اقتراحات الطبقة المدفوعة بالتصحيحات في إعداد الطبقة (task_type_tier_config)؟',
    applyDone: 'تم التطبيق: {types}',
    applyNone: 'لا يوجد حاليًا اقتراح يحقق الحد الأدنى',
    reload: 'إعادة تحميل التراكب',
    reloadConfirm: 'إعادة تحميل ملف ملفات TASKPROFILE_OVERLAY؟ يعود إلى الإعدادات الافتراضية المدمجة عند عدم تهيئته.',
    reloadDone: 'تمت إعادة تحميل السجل: {version}',
    exportCsv: 'تصدير التصحيحات CSV',
    days: 'النافذة (أيام)',
  },
  status: {
    applying: 'جارٍ التطبيق…',
    reloading: 'جارٍ إعادة التحميل…',
    exporting: 'جارٍ التصدير…',
  },
}
