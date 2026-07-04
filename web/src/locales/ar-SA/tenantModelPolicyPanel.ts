// Arabic locale for TenantModelPolicyPanel
export default {
  title: 'التحكم في الوصول إلى النماذج',
  hint: 'النماذج المكونة هنا سيتم رفضها لجميع مفاتيح API تحت هذا المستأجر (403 model_forbidden). جدول فارغ = لا قيود لهذا المستأجر (افتراضي). مسار model="auto" غير خاضع للتحكم في الوصول.',
  showDeleted: 'إظهار المحذوفات',
  addButton: '+ إضافة نموذج مرفوض',
  loading: 'جارٍ التحميل…',
  
  table: {
    canonicalName: 'canonical_name',
    reason: 'reason',
    createdBy: 'created_by',
    createdAt: 'created_at',
    deletedAt: 'deleted_at',
    actions: 'الإجراءات',
  },
  
  actions: {
    softDelete: 'حذف منطقي',
    restore: 'استعادة',
  },
  
  empty: 'لا توجد سياسات (جميع النماذج مسموح بها افتراضيًا)',
  
  audit: {
    title: 'سجل المراجعة',
    recent: 'آخر {count} إدخال',
    headers: {
      ts: 'ts',
      action: 'action',
      canonicalName: 'canonical_name',
      actor: 'actor',
      reason: 'reason',
    },
    actionLabels: {
      insert: 'إنشاء',
      update: 'تحديث',
      delete: 'حذف منطقي',
      undelete: 'استعادة',
    },
  },
  
  dialog: {
    title: 'إضافة نموذج مرفوض',
    hint: 'أدخل canonical_name أدناه (يجب أن يتطابق مع جدول models_canonical).',
    canonicalName: 'canonical_name',
    canonicalNamePlaceholder: 'مثال: minimax-m3',
    checkButton: 'التحقق',
    checkSuccess: '✓ تم العثور عليه في models_canonical (family={family}, modality={modality})',
    checkWarning: '⚠ هذا canonical_name غير موجود في models_canonical (الكتابة لا تزال مسموحة، تحكم دفاعي)',
    reason: 'reason',
    reasonPlaceholder: 'اختياري، مثال: التحكم في التكاليف',
    cancel: 'إلغاء',
    submit: 'إرسال',
    submitting: 'جارٍ الإرسال…',
  },
  
  confirm: {
    softDelete: 'تأكيد الحذف المنطقي للسياسة {name}؟ (قابل للاستعادة)',
  },
  
  error: {
    loadFailed: 'فشل التحميل',
    canonicalNameRequired: 'canonical_name مطلوب',
    createFailed: 'فشل الإنشاء',
    deleteFailed: 'فشل الحذف',
    restoreFailed: 'فشلت الاستعادة',
  },
}
