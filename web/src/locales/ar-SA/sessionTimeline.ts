// sessionTimeline.ts — SessionTurnsTimeline.vue نصوص (OBS-FE5 الجدول الزمني للجولات)
// يغطي: قيمة خالية للكمون / تحديث / خطأ / فارغ / تحميل المزيد / تم تحميل الكل / خطأ في الشبكة
export default {
  latencyUnknown: 'غير معروف',
  refresh: 'تحديث',
  refreshing: 'جارٍ التحديث…',
  retry: 'إعادة المحاولة',
  empty: 'لا توجد جولات في هذه الجلسة',
  loading: 'جارٍ التحميل…',
  loadMore: 'تحميل المزيد من الجولات',
  allLoaded: 'تم تحميل {n} جولات، الكل مكتمل',
  errors: {
    network: 'خطأ في الشبكة، يرجى التحقق من الاتصال وإعادة المحاولة',
  },
}
