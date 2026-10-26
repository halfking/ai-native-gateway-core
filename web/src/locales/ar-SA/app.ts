// Auto-translated draft (ar-SA) · 2026-07-02 · please review
// app.ts — نصوص غلاف App.vue (دور الشريط العلوي، طي الشريط الجانبي، تسجيل الخروج، تبديل اللغة).
export default {
  brand: {
    siteTitle: 'AI Native البوابة الأساسية',
    siteTitleLineTwo: 'البوابة الأساسية',
    alt: 'شعار Kaixuan',
  },
  role: {
    super_admin: 'المسؤول الأعلى',
    tenant_admin: 'مسؤول المستأجر',
  },
  // 2026-07-22: تذييل الـ shell العام (مقترن بـ LifecycleShell).
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

  theme: {
    switchToLight: 'Switch to light',
    switchToDark: 'Switch to dark',
    lightTitle: 'Light mode',
    darkTitle: 'Dark mode',
  },
  dataHorizon: {
    title: 'بيانات حركة المرور تتأخر عن التسجيل: البوابة تستقبل طلبات لكن session_turns لا تلحق بها',
    titleUnavailable: 'يتعذر تأكيد حالة بيانات حركة المرور: قد تكون الأرقام في هذه الصفحة قديمة',
    hintLabel: 'استقصاء',
    retry: 'إعادة الفحص',
    failedHint: 'تعذّرت قراءة حالة البيانات. عدم تأكيد الحالة ليس دليلًا على أن الحالة سليمة. عامل أرقام هذه الصفحة على أنها قد تكون قديمة.',
  },
  deploySeq: {
    bannerTitle: 'New version available',
    bannerHint: 'تم نشر نسخة أحدث. أعد تحميل الصفحة للانتقال إليها.',
    updateNow: 'Reload now',
    later: 'Later',
    currentSeq: 'Page deploy seq',
    state: 'Update state',
    stateLatest: 'Up to date',
    stateAvailable: 'Update available',
    stateIndeterminate: 'Indeterminate',
    stateChecking: 'Checking…',
    reasonNoLocal: 'لا يحمل هذا الرقم التسلسلي للنشر (لم يُحقن وقت البناء)',
    reasonNoRemote: 'لم يُرجع الخادم رقم التسلسل للنشر (نسخة قديمة أو استضافة خارج المستودع)',
    reasonNetwork: 'Check request failed; cannot determine',
    checks: 'Checks run',
    checkNow: 'Check for updates',
    sourceUnknown: 'Not injected',
  },
}
