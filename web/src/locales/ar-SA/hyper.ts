// hyper.ts — نصوص غلاف Hyper (شريط التنقل السفلي / لوحة الحساب / حالة القائمة / التركيز / سطح المكتب).
// 2026-10-04، أُضيفت مع وقت تشغيل Hyper (docs/UI规范/00 §5.4، H2/H3).
export default {
  bottomNav: {
    ariaLabel: 'التنقل الرئيسي',
    more: 'المزيد',
  },
  account: {
    title: 'الحساب',
    language: 'اللغة',
    theme: 'المظهر',
    help: 'مساعدة',
    health: 'حالة الخدمة',
    healthy: 'يمكن الوصول',
    unhealthy: 'تعذّر الوصول',
    unknown: 'الحالة غير معروفة',
    logout: 'تسجيل الخروج',
    adminEntry: 'وحدة التحكم',
    close: 'إغلاق',
  },
  dataView: {
    table: 'جدول',
    cards: 'بطاقات',
  },
  list: {
    refreshing: 'جارٍ التحديث…',
    loadMore: 'تحميل المزيد',
    loadingMore: 'جارٍ التحميل…',
    retry: 'إعادة المحاولة',
    allLoaded: 'تم تحميل {count} كلها',
    loadFailed: 'فشل التحميل. أعد المحاولة.',
  },
}
