// Auto-translated draft (ar-SA) · 2026-07-02 · please review
// Updated to include settings.compression.* for Headroom modes (2026-07-02).
// settings.ts — نصوص SettingsView. فضاء الأسماء: category / list / detail / editor / docs / errors.
// أسماء الحقول التقنية (JSON / RPM / TPM / WebSocket، إلخ) تبقى كما هي.
export default {
  category: {
    all: 'الكل',
    compression: 'الضغط',
    rateLimit: 'تحديد المعدل',
    timeout: 'المهلة',
    retry: 'إعادة المحاولة',
    routing: 'التوجيه',
    session: 'الجلسة',
    security: 'الأمان',
    outputCompliance: 'التوافق مع المخرجات',
    circuitBreaker: 'قاطع الدائرة',
    loopGuard: 'حماية الحلقة',
    general: 'أخرى',
  },
 list: {
 count: '{n} إعدادات',
 empty: 'لا توجد إعدادات في هذه الفئة',
 colSetting: 'الإعداد',
 colCurrentValue: 'القيمة الحالية',
 colSource: 'المصدر',
 colDanger: 'الخطورة',
 },
  dangerLevel: {
    note: '🟡 ملاحظة',
    warn: '🟠 تحذير',
    danger: '🔴 خطر',
  },
  detail: {
 tenantScopedTitle: 'إعداد على مستوى المستأجر',
 tenantScopedBody: 'ينطبق هذا الإعداد على مستأجر واحد ولا يمكن تعيينه على مستوى النظام. انتقل إلى صفحة إدارة المستأجرين لتكوينه لمستأجر معين.',
 type: 'النوع',
 currentValue: 'القيمة الحالية',
 defaultValue: 'القيمة الافتراضية',
 options: 'الخيارات',
 dangerLevel: 'مستوى الخطورة',
 hotReload: 'إعادة التحميل الفوري',
 noRequiresRestart: 'لا (يتطلب إعادة تشغيل)',
 observability: 'نقطة الرصد',
 detectField: 'حقل التعرف {key}',
 emptyHint: '← اختر إعدادًا من اليسار لعرض التفاصيل',
    errors: {
      loadListFailed: 'تعذّر تحميل قائمة الإعدادات. أعد المحاولة.',
      loadDetailFailed: 'تعذّر تحميل تفاصيل الإعدادات. أعد المحاولة.',
      saveFailed: 'تعذّر حفظ الإعدادات. أعد المحاولة.',
      rollbackFailed: 'تعذّر إتمام التراجع. أعد المحاولة.',
      invalidNumber: 'الرجاء إدخال رقم صالح',
    },
  },
  editor: {
 newValue: 'قيمة جديدة',
 aliasPlaceholder: 'اكتب اسمًا بديلًا واضغط Enter',
 aliasHint: 'يُحفظ كسلسلة مفصولة بفواصل، ويحدّث استخراج الأسماء البديلة للجلسات فورًا.',
 aliasPreviewTitle: 'معاينة مثال الطلب',
 stringPlaceholder: 'أدخل قيمة نصية',
 jsonPlaceholder: 'أدخل قيمة بصيغة JSON',
 jsonHint: 'استخدم صيغة JSON للأنواع المركبة',
 rollback: 'تراجع',
    rollbackConfirm: 'إرجاع {key} إلى قيمته السابقة؟',

    enabledText: 'مفعّل',
    disabledText: 'معطّل',
    saving: 'جاري الحفظ…',
    save: 'حفظ',
  },
  compression: {
    enumLabels: {
      off: '0 - إيقاف (off)',
      auto: '1 - عتبة تلقائية (auto_threshold)',
      on4xx: '2 - ضغط عند 4xx (on_4xx)',
      smart: 'ضغط ذكي (smart)',
      aggressive: 'ضغط هجومي (aggressive)',
    },
    enumDescriptions: {
      off: 'إيقاف وظيفة ضغط الرسائل تمامًا',
      auto: 'ضغط تلقائي عندما يتجاوز طول الرسالة عتبة نافذة السياق',
      on4xx: 'تشغيل الضغط وإعادة المحاولة عند استلام خطأ 4xx (مثل context_length_exceeded)',
    },
    strategyEnum: {
      naive: 'naive - ضغط بسيط',
      smart: 'smart - ضغط ذكي',
      adaptive: 'adaptive - ضغط تكيفي',
    },
  },
 gateway: {
 modeEnum: {
 simple: 'simple - الوضع البسيط',
 expert: 'expert - وضع الخبراء',
 },
 recommended: '(موصى به)',
 modeDescriptions: {
 simple: 'الوضع البسيط: الصفحة الرئيسية قائمة وصول مختصرة، وأدوات الخبراء مطوية ضمن «متقدم».',
 expert: 'وضع الخبراء: تنقل كامل مع جميع الصفحات (الافتراضي الحالي).',
 },
 },
 timezone: {
    hint: 'تحدد المنطقة الزمنية كيفية تجميع وعرض جميع النطاقات الزمنية (حدود اليوم والتقارير والاستعلامات عن السجلات). الافتراضي UTC+8 ويُفعَّل فور الحفظ.',
    currentNow: 'الإزاحة الزمنية الحالية: {offset}',
  },
  search: {
    placeholder: 'البحث في الإعدادات: المفتاح / الوصف / الفئة…',
  },
  basicConfig: {
    toggle: 'الإعدادات الأساسية',
    hotReload: 'تفعيل فوري',
    group: {
      identity: 'الهوية والمظهر',
      storage: 'التخزين',
      schedules: 'المهام الدورية',
    },
    entry: {
      displayName: 'اسم عرض البوابة',
      skin: 'المظهر الافتراضي',
      timezone: 'المنطقة الزمنية للنظام',
      gatewayMode: 'وضع التشغيل',
      attachmentUrl: 'عنوان URL العام للمرفقات',
      attachmentDir: 'مجلد تخزين المرفقات',
      rollupHour: 'ساعة التجميع (UTC)',
    },
    skin: {
      auto: 'اتباع النظام',
      light: 'فاتح',
      dark: 'داكن',
    },
    infraTitle: 'البنية التحتية (للقراءة فقط)',
    infraHint: 'حالة التشغيل من إعدادات البيئة عند الإقلاع. تتطلب التغييرات إعادة تشغيل الخدمة؛ بيانات الاعتماد لا تُعرض في هذه الصفحة إطلاقًا.',
    infraNotConfigured: 'غير مُهيأ',
    infraNotSet: 'غير محدد',
    infra: {
      instance: 'معرّف النسخة',
      redis: '‏Redis (ذاكرة مؤقتة)‎',
      database: 'قاعدة البيانات',
      cacheDir: 'مجلد الذاكرة المؤقتة (env)',
      attachmentDir: 'مجلد المرفقات (env)',
    },
  },
  docs: {
    compressionModeTitle: '📖 شرح مفصل لوضع الضغط',
    compressionModeContent: `<p><strong>وضع الضغط</strong> يتحكم في كيفية معالجة السياق الذي يتجاوز طول المحادثة:</p>
<ul>
  <li><code>0 (off)</code> - إيقاف الضغط، عند تجاوز السياق يتم إرجاع خطأ مباشرة</li>
  <li><code>1 (auto_threshold)</code> - وضع التنبؤ، يضغط بشكل استباقي عندما يقترب طول الرسالة من نافذة السياق للنموذج</li>
  <li><code>2 (on_4xx)</code> - وضع الاستجابة، يضغط ويعيد المحاولة بعد استلام خطأ 4xx【موصى به】</li>
</ul>
<p class="docs-note">💡 <strong>نوصي باستخدام الوضع 2</strong>: يضغط فقط عند الضرورة، لتجنب الأعباء غير الضرورية على الأداء</p>`,
    cacheEnabledTitle: '📖 شرح مفصل لتخزين الجلسة المؤقت',
    cacheEnabledContent: `<p><strong>تخزين الجلسة المؤقت</strong> يتحكم في ما إذا كان سيتم تفعيل التخزين المؤقت L1/L2/L3:</p>
<ul>
  <li><strong>L1</strong> - ذاكرة التخزين المؤقت (الأسرع)</li>
  <li><strong>L2</strong> - ذاكرة التخزين المؤقت Redis (متوسطة)</li>
  <li><strong>L3</strong> - ذاكرة التخزين المؤقت لقاعدة البيانات (الأبطأ)</li>
</ul>
<p class="docs-note">⚠️ بعد الإيقاف، لن يتم حفظ أي حالة جلسة، مما يؤثر على استمرارية السياق</p>`,
    formatConversionTitle: '📖 شرح مفصل لتحويل التنسيق',
    formatConversionContent: `<p><strong>تحويل التنسيق</strong> يسمح بالتحويل التلقائي لتنسيقات الطلب بين البروتوكولات المختلفة:</p>
<ul>
  <li><strong>مسار Q2</strong>: تنسيق Anthropic → نموذج OpenAI</li>
  <li><strong>مسار Q3</strong>: تنسيق OpenAI → نموذج Anthropic</li>
</ul>
<p class="docs-note">💡 يدعم التجاوز على مستوى المزود، يمكن تعطيل التحويل لمزودين محددين</p>`,
    rateLimitRpmTitle: '📖 شرح مفصل لتحديد RPM',
    rateLimitRpmContent: `<p><strong>RPM (Requests Per Minute)</strong> يحدد عدد الطلبات لكل مستأجر في الدقيقة:</p>
<ul>
  <li>مناسب للتحكم في التدفق الخشن</li>
  <li>يستند إلى خوارزمية النافذة المنزلقة بدقة على مستوى الثانية</li>
  <li>يرجع رمز الحالة 429 عند التجاوز</li>
</ul>
<p class="docs-note">⚠️ <strong>تكوين على مستوى المستأجر</strong>: يتطلب هذا الإعداد تحديد tenant_id، يتم تعيينه في صفحة إدارة المستأجرين</p>`,
    rateLimitConcurrentTitle: '📖 شرح مفصل لتحديد التزامن',
    rateLimitConcurrentContent: `<p><strong>تحديد التزامن</strong> يحدد عدد الطلبات التي يعالجها كل مستأجر في وقت واحد:</p>
<ul>
  <li>مناسب لحماية موارد النظام، ومنع مستأجر واحد من شغل عدد كبير جدًا من الاتصالات</li>
  <li>يستند إلى العداد، بسرعة استجابة عالية</li>
  <li>قائمة الانتظار أو إرجاع 429 عند التجاوز</li>
</ul>
<p class="docs-note">⚠️ <strong>تكوين على مستوى المستأجر</strong>: يتطلب هذا الإعداد تحديد tenant_id، يتم تعيينه في صفحة إدارة المستأجرين</p>`,
  },

  // 同步的扁平键
}