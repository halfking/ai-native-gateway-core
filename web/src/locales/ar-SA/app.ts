// Auto-translated draft (ar-SA) · 2026-07-02 · please review
// app.ts — نصوص غلاف App.vue (دور الشريط العلوي، طي الشريط الجانبي، تسجيل الخروج، تبديل اللغة).
export default {
  brand: 'AI-Native Org Gateway',
  role: {
    super_admin: 'المسؤول الأعلى',
    tenant_admin: 'مسؤول المستأجر',
  },
  sidebar: {
    expand: 'توسيع الشريط الجانبي',
    collapse: 'طي الشريط الجانبي',
    collapseMenu: 'طي القائمة',
  },
  logout: 'تسجيل الخروج',
  lang: {
    switch: 'تبديل اللغة',
    label: 'اللغة',
  },
  // 2026-07-22: تذييل الـ shell العام (مقترن بـ LifecycleShell).
  footer: {
    left: '© 2026 AI-Native Gateway',
    right: 'هل تحتاج إلى مساعدة؟',
    feedbackLink: 'إرسال ملاحظات',
  },
  // 2026-07-22: قائمة المستخدم (UserMenuDropdown) + حوار المعلومات (UserInfoDialog).
  userMenu: {
    profile: 'الملف الشخصي',
    changePassword: 'تغيير كلمة المرور',
    logout: 'تسجيل الخروج',
  },
  userInfo: {
    displayName: 'اسم العرض',
    username: 'اسم المستخدم',
    email: 'البريد الإلكتروني',
    role: 'الدور',
    tenant: 'المستأجر',
    close: 'إغلاق',
  },
  // 2026-07-22: skip-link لإمكانية الوصول.
  nav: {
    mainAria: 'التنقل الرئيسي',
    skip: 'الانتقال إلى المحتوى الرئيسي',
  },

  theme: {
    switchToLight: 'Switch to light',
    switchToDark: 'Switch to dark',
    lightTitle: 'Light mode',
    darkTitle: 'Dark mode',
  },
  v1DataFrozen: {
    title: 'توقفت بيانات حركة المرور عن التحديث: الأرقام في هذه الصفحة تعكس ما قبل فترة إيقاف الكتابة فقط',
    titleUnconfirmed: 'مفتاح إيقاف الكتابة غير مُهيّأ صراحةً: يتعذر معرفة ما إذا كانت هذه الأرقام قديمة',
    titleUnavailable: 'يتعذر تأكيد حالة بيانات حركة المرور: قد تكون الأرقام في هذه الصفحة قديمة',
    affects: 'فئات نقاط القراءة المتأثرة',
    gateKey: 'الإعداد المتحكم فيه',
    retry: 'إعادة الفحص',
    failedHint: 'تعذّرت قراءة حالة البيانات. عدم تأكيد الحالة ليس دليلًا على أن الحالة سليمة — عامل أرقام هذه الصفحة على أنها قد تكون قديمة.',
  },
  deploySeq: {
    bannerTitle: 'New version available',
    bannerHint: 'A newer build is deployed. Reload the page to switch.',
    updateNow: 'Reload now',
    later: 'Later',
    currentSeq: 'Page deploy seq',
    state: 'Update state',
    stateLatest: 'Up to date',
    stateAvailable: 'Update available',
    stateIndeterminate: 'Indeterminate',
    stateChecking: 'Checking…',
    reasonNoLocal: 'This page carries no deploy seq (not injected at build time)',
    reasonNoRemote: 'Server returned no deploy seq (old build or non-repo hosting)',
    reasonNetwork: 'Check request failed; cannot determine',
    checks: 'Checks run',
    checkNow: 'Check for updates',
    sourceUnknown: 'Not injected',
  },
}
