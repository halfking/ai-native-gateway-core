// Auto-translated draft (ar-SA) · 2026-07-02 · please review
// auditLog.ts — نصوص AuditLogView. فضاء الأسماء: page / filter / table / pagination / detail / actions / errors.
// المصطلحات التقنية تبقى كما هي: target_type / before_json / after_json.
export default {
  page: {
    title: 'سجلات التدقيق',
    desc: 'يسجل العمليات المتعلقة بإدارة المستخدمين والمصادقة، متاح فقط للمسؤول الأعلى.',
    totalChip: 'إجمالي {n} سجل',
    refresh: 'تحديث',
    refreshing: 'جاري التحديث…',
    emptyTitle: 'لا توجد سجلات تدقيق',
    emptyHint: 'اضبط شروط التصفية أو وسّع النطاق الزمني ثم أعد المحاولة',
    loading: 'جاري التحميل…',
  },
  filter: {
    actorLabel: 'المنفذ',
    actorPlaceholder: 'مطابقة جزئية لاسم المستخدم',
    actionLabel: 'الإجراء',
    actionPlaceholder: 'مثال: user.* أو auth.*',
    fromLabel: 'وقت البدء',
  },
  table: {
    headers: {
      time: 'الوقت',
      actor: 'المنفذ',
      action: 'الإجراء',
      target: 'الهدف',
      details: 'التفاصيل',
    },
  },
  detail: {
    titleWithId: 'تفاصيل التدقيق #{id}',
    close: 'إغلاق',
    metaTime: 'الوقت',
    metaActor: 'المنفذ',
    metaAction: 'الإجراء',
    metaTarget: 'الهدف',
    beforeTitle: 'قبل التغيير',
    afterTitle: 'بعد التغيير',
    noExtra: 'لا توجد تفاصيل إضافية',
  },
  actions: {
    'user.create': 'إنشاء مستخدم',
    'user.update': 'تحديث مستخدم',
    'user.delete': 'حذف مستخدم',
    'user.reset_password': 'إعادة تعيين كلمة المرور',
    'user.change_password': 'تغيير كلمة المرور',
    'auth.login': 'تسجيل الدخول',
    'auth.login_failed': 'فشل تسجيل الدخول',
    'auth.logout': 'تسجيل الخروج',
    'auth.rate_limited': 'تحديد المعدل',
    'authentication.login': 'تسجيل الدخول',
    'authentication.login_failed': 'فشل تسجيل الدخول',
    'authentication.logout': 'تسجيل الخروج',
  },

  // 扁平键（供 Vue 组件直接使用）
  loadFailed: 'تعذّر تحميل قائمة سجلات التدقيق. أعد المحاولة.',
  dash: '—',
}