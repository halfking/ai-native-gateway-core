export default {
  // عام
  login: 'تسجيل الدخول',
  logout: 'تسجيل الخروج',
  changePassword: 'تغيير كلمة المرور',
  cancel: 'إلغاء',
  confirm: 'تأكيد',
  save: 'حفظ',
  delete: 'حذف',
  edit: 'تعديل',
  search: 'بحث',
  reset: 'إعادة تعيين',
  submit: 'إرسال',
  back: 'رجوع',
  next: 'التالي',
  previous: 'السابق',
  close: 'إغلاق',
  
  // أدوار المستخدم
  role: {
    super_admin: 'مدير النظام',
    tenant_admin: 'مدير المستأجر',
  },
  
  // التنقل
  nav: {
    collapseSidebar: 'طي القائمة',
    expandSidebar: 'توسيع الشريط الجانبي',
  },
  
  // كلمة المرور
  password: {
    changeSuccess: 'تم تغيير كلمة المرور بنجاح',
    changeFailed: 'فشل تغيير كلمة المرور',
  },
  
  // معلومات الإصدار
  version: 'الإصدار',
  build: 'البناء',

  // 2026-07-02 (عرض مرفقات request-logs): نصوص المرفقات، وفقًا لوثيقة
  // المرجع §6.
  requests: {
    list: {
      table: {
        attachmentsTitle: 'مرفقات',
        noAttachments: 'لا توجد مرفقات',
      },
    },
    detail_extra: {
      attachmentsTab: 'مرفقات',
      attachmentsLoading: 'جارٍ تحميل المرفقات…',
      noAttachments: 'لا توجد مرفقات',
      clickToPreviewTitle: 'انقر للمعاينة',
      download: 'تنزيل',
      downloadOriginal: 'تنزيل الأصلي',
      closePreview: 'إغلاق',
    },
  },

  // 2026-07-03 (تدفق الطلبات في الوقت الفعلي): swim lane للوحة المعلومات.
  dashboard: {
    liveStream: {
      title: 'تدفق الطلبات في الوقت الفعلي',
      connected: 'متصل',
      connecting: 'جارٍ الاتصال…',
      reconnecting: 'إعادة الاتصال…',
      disconnected: 'غير متصل',
      unsupported: 'غير مدعوم',
      pause: 'إيقاف مؤقت',
      resume: 'استئناف',
      filterAll: 'جميع الحالات',
      filterSuccess: 'نجاح فقط',
      filterInProgress: 'قيد التنفيذ',
      filterGroupFailures: 'تفصيل الإخفاقات',
      filterFailure5xx: 'الخادم / المنبع (5xx)',
      filterFailure4xx: 'العميل / المصادقة (4xx)',
      filterFailureTimeout: 'انتهاء المهلة / الشبكة',
      filterFailureNotFound: 'التوجيه / النموذج غير موجود',
      filterFailureOther: 'إخفاقات أخرى',
      empty: 'في انتظار الطلبات…',
      countTooltip: '{buffer} في المخزن / {visible} مرئي',
      countAria: '{buffer} طلبات في المخزن، {visible} مرئي',
      legend: {
        model: 'عائلة النموذج',
        status: 'الحالة',
        openai: 'OpenAI',
        anthropic: 'Anthropic',
        domestic: 'محلي',
        oss: 'مفتوح المصدر',
        other: 'أخرى',
        success: 'نجاح',
        inProgress: 'قيد التنفيذ',
        failure: 'فشل',
      },
    },
  },
}