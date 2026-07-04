// Auto-translated draft (ar-SA) · 2026-07-02 · please review
// users.ts — نصوص صفحة إدارة المستخدمين. يعيد استخدام المصطلحات العامة من الوحدة common (مفعّل/معطّل، إلغاء، تأكيد، إنشاء، حذف، إلخ).
export default {
  title: 'إدارة المستخدمين',
  create: 'إنشاء مستخدم',
  readOnlyNotice: '📖 أنت مسؤول مستأجر، الوضع الحالي للقراءة فقط. إدارة المستخدمين للعرض فقط، لا يمكن إنشاء أو تحرير أو حذف المستخدمين.',
  filter: {
    byTenant: 'تصفية حسب المستأجر:',
    allTenants: 'كل المستأجرين',
  },
  table: {
    username: 'اسم المستخدم',
    displayName: 'اسم العرض',
    email: 'البريد الإلكتروني',
    tenant: 'المستأجر',
    role: 'الدور',
    lastLogin: 'آخر تسجيل دخول',
  },
  action: {
    resetPassword: 'إعادة تعيين كلمة المرور',
  },
  role: {
    super_admin: 'المسؤول الأعلى',
    tenant_admin: 'مسؤول المستأجر',
  },
  modal: {
    create: {
      title: 'إنشاء مستخدم',
      username: 'اسم المستخدم *',
      password: 'كلمة المرور *',
      passwordPlaceholder: '8 أحرف على الأقل',
      displayName: 'اسم العرض',
      email: 'البريد الإلكتروني',
      tenant: 'المستأجر *',
      role: 'الدور',
    },
    reset: {
      title: 'إعادة تعيين كلمة المرور — {name}',
      newPassword: 'كلمة المرور الجديدة',
      passwordPlaceholder: '8 أحرف على الأقل',
    },
  },
  error: {
    usernamePasswordRequired: 'لا يمكن أن يكون اسم المستخدم وكلمة المرور فارغين',
    createFailed: 'فشل الإنشاء',
    resetFailed: 'فشلت إعادة التعيين',
    passwordMinLength: 'يجب أن تتكون كلمة المرور من 8 أحرف على الأقل',
  },
  confirmDelete: 'تأكيد حذف المستخدم {name}؟',
}