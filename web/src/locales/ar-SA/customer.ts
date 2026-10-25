// customer.ts — Customer UI (ar-SA, from en-US) (2026-07-13)
export default {

  info: {
    title: 'License information',
    refresh: 'Refresh',
    gotoActivate: 'Go to activation wizard',
    notActivated: 'This instance has no active license',
    states: {
      active: 'Activated',
      grace: 'Grace period',
      expired: 'Expired',
      revoked: 'Revoked',
      none: 'Not activated',
    },
    expiredBanner: 'انتهى الترخيص، وبعض الميزات مقيّدة. جدّد الترخيص ثم أعد التفعيل.',
    revokedBanner: 'أُلغي الترخيص. اطلب ترخيصًا جديدًا أو تواصل مع جهة الترخيص.',
    reactivate: 'Re-activate',
    loadFailed: 'Failed to load license info: {msg}',
  },
  updateActivate: {
    summary: 'معلومات الموقع، والتفعيل بنقرة واحدة، وترقيات الإصدار، والوحدات التي فعّلتها.',
  },
  versionsUpgradeConfirmBody: 'ستتم ترقية الخدمة إلى {version} ({label}). ستكون غير متاحة لفترة قصيرة. تأكد من عدم وجود مهام مهمة قيد التنفيذ.',
  versionsUpgradeConfirmTitle: 'تأكيد الترقية',
  versionsUpgradeConfirmStart: 'بدء الترقية',
}
