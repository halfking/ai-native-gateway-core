export default {

  // License Management

  // Fault Management

  // Auto Update

  // Center Operations

  // VibeCoding
  vibecoding: {
    title: 'VibeCoding',
    loadFailed: 'تعذّر تحميل البيانات. أعد المحاولة.',
    fillRequired: 'يرجى ملء الحقول المطلوبة',
    projects: 'المشاريع',
    sessions: 'الجلسات',
    codeReviews: 'مراجعات الشيفرة',
    createProject: 'إنشاء مشروع',
    createProjectTitle: 'إنشاء مشروع جديد',
    createProjectSuccess: 'تم إنشاء المشروع بنجاح',
    createProjectFailed: 'تعذّر إنشاء المشروع. أعد المحاولة.',
    newSession: 'جلسة جديدة',
    createSessionTitle: 'إنشاء جلسة جديدة',
    createSessionSuccess: 'تم إنشاء الجلسة بنجاح',
    createSessionFailed: 'تعذّر إنشاء الجلسة. أعد المحاولة.',
    viewSessions: 'عرض الجلسات',
    viewReviews: 'عرض المراجعات',
    showAll: 'عرض الكل',
    reviewDetail: 'تفاصيل المراجعة',
    /**
     * الحالات الفارغة (الدفعة 14 · «حالات فارغة»).
     * ★ الجداول الثلاث كانت تشارك مفتاحًا عامًا واحدًا («القائمة بلا محتوى»)،
     *   لكنها حالتان مختلفتان:
     *   · projects — يستخدم المصفوفة الخام ⇒ الفراغ = لم تُنشئ أي مشروع بعد (FirstUse)
     *   · sessions / reviews — يستخدم المصفوفة بعد التصفية ⇒ الفراغ = تمّت التصفية (Filtered)
     *   ⇒ جملة واحدة تغطّي الحالتين، فتعني «لم تبدأ بعد» و«تصفية أضيق من اللازم» الشيء نفسه.
     */
    emptyFirstUseProjects: 'لم يتم إنشاء أي مشروع بعد.',
    emptyFilteredSessions: 'لا توجد سجلات جلسات ضمن شروط التصفية الحالية.',
    emptyFilteredReviews: 'لا توجد سجلات مراجعات الشيفرة ضمن شروط التصفية الحالية.',
    projectName: 'اسم المشروع',
    projectNamePlaceholder: 'أدخل اسم المشروع',
    language: 'اللغة',
    languagePlaceholder: 'مثال: TypeScript',
    framework: 'إطار العمل',
    frameworkPlaceholder: 'مثال: Vue 3',
    projectId: 'معرّف المشروع',
    sessionName: 'اسم الجلسة',
    taskType: 'نوع المهمة',
    taskTypePlaceholder: 'مثال: مراجعة شيفرة، إعادة هيكلة، شرح',
    startedAt: 'بدأ في',
    endedAt: 'انتهى في',
    filePath: 'مسار الملف',
    score: 'الدرجة',
    summary: 'الملخص',
    issues: 'المشكلات',
    suggestions: 'الاقتراحات',
    category: 'الفئة',
    reviewedAt: 'تمت المراجعة في',
    line: 'السطر',
    severity: 'الخطورة',
    message: 'الرسالة',
  },


  overview: {




























    nodeDetailSuperAdminOnlyHint: 'يمكن للمشرفين الكبار فقط صيانة العُقد؛ العرض حاليًا للقراءة فقط.',
    nodeDetailDefaultTenantOnlyHint: 'لا يمكن صيانة العُقد إلا في المستأجر الافتراضي؛ العرض حاليًا للقراءة فقط.',

















    pingFailedWithReason: 'فشل Ping الجلسة: {reason}. أعد المحاولة.',
    saveSettingFailedWithReason: 'تعذّر حفظ الإعدادات: {reason}. أعد المحاولة.',
    maintainFailed: 'تعذّر إتمام الصيانة. أعد المحاولة.',
    modelMaintainFailed: 'تعذّر إتمام صيانة النماذج. أعد المحاولة.',
    fpSlotLoadFailed: 'تعذّر تحميل خانات البصمة. أعد المحاولة.',
    fpSlotProviderMissing: 'provider_id مفقود، تعذّر تحميل خانات البصمة. اختر المزوّد أولًا ثم أعد المحاولة.',
    accessErrorsHint: 'الاستدعاءات الفاشلة + أخطاء التوجيه، افتح تفاصيل الطلب الأصلي',
    recentRequestsHint: 'آخر ساعة{source}، لعرض تفاصيل الطلبات',
  }
,

}
