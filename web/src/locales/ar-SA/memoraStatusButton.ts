// Arabic locale for MemoraStatusButton
export default {
  state: {
    disabled: 'معطل',
    paused: 'غير متصل',
    ok: 'متصل',
    error: 'فشل الاتصال',
    loading: 'جارٍ الفحص',
  },
  
  panel: {
    title: 'اتصال Memora',
    closeLabel: 'إغلاق',
    
    fields: {
      serviceUrl: 'عنوان URL للخدمة',
      recentLatency: 'التأخير الأخير',
      error: 'خطأ',
      check: 'فحص',
      writeQueue: 'قائمة انتظار الكتابة',
      processedErrored: 'تمت المعالجة / فشلت',
      consecutiveErrors: 'الأخطاء المتتالية',
      recentWriteError: 'خطأ الكتابة الأخير',
    },
    
    actions: {
      processing: 'جارٍ المعالجة…',
      reconnect: 'إعادة الاتصال',
      checkConnectivity: 'فحص الاتصال',
      disconnect: 'قطع الكتابة',
      refresh: 'تحديث',
      sessionContext: 'سياق الجلسة',
    },
  },
  
  error: {
    connectionFailed: 'فشل الاتصال',
    checkFailed: 'فشل الفحص',
  },
}
