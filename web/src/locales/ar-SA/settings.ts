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
    routing: 'التوجيه',
    session: 'الجلسة',
    security: 'الأمان',
    outputCompliance: 'التوافق مع المخرجات',
    circuitBreaker: 'قاطع الدائرة',
    general: 'أخرى',
  },
  dangerLevel: {
    note: '🟡 ملاحظة',
    warn: '🟠 تحذير',
    danger: '🔴 خطر',
  },
  list: {
    total: 'إجمالي {n} إعداد',
    loading: 'جاري التحميل…',
    empty: 'لا توجد إعدادات في هذه الفئة',
    table: {
      setting: 'الإعداد',
      currentValue: 'القيمة الحالية',
      source: 'المصدر',
      danger: 'الخطر',
    },
  },
  detail: {
    selectPrompt: '← اختر إعدادًا من اليسار لعرض التفاصيل',
    type: 'النوع',
    currentValue: 'القيمة الحالية',
    defaultValue: 'القيمة الافتراضية',
    options: 'الخيارات',
    dangerLevel: 'مستوى الخطر',
    hotReload: 'إعادة التحميل الساخنة',
    hotReloadYes: 'نعم',
    hotReloadNo: 'لا (يتطلب إعادة التشغيل)',
    observability: 'نقطة المراقبة',
    tenantWarningTitle: 'تكوين على مستوى المستأجر',
    tenantWarningBody: 'هذا الإعداد يعمل على مستأجر واحد، ولا يمكن تعيينه على مستوى النظام. يرجى الانتقال إلى صفحة <strong>إدارة المستأجرين</strong> لتكوين هذا العنصر لمستأجرين محددين.',
    errors: {
      loadListFailed: 'فشل التحميل',
      loadDetailFailed: 'فشل تحميل التفاصيل',
      saveFailed: 'فشل الحفظ',
      rollbackFailed: 'فشل التراجع',
      invalidNumber: 'الرجاء إدخال رقم صالح',
      confirmRollback: 'تأكيد التراجع عن {key} إلى القيمة السابقة؟',
    },
  },
  editor: {
    newValueLabel: 'القيمة الجديدة',
    enabledText: 'مفعّل',
    disabledText: 'معطّل',
    stringPlaceholder: 'إدخال قيمة نصية',
    jsonPlaceholder: 'إدخال قيمة بتنسيق JSON',
    jsonHint: 'الأنواع المعقدة، يرجى استخدام تنسيق JSON',
    saving: 'جاري الحفظ…',
    save: 'حفظ',
    rollback: 'تراجع',
  },
  compression: {
    selectLabels: {
      off: '0 - إيقاف (off)',
      auto: '1 - عتبة تلقائية (auto_threshold)',
      on4xx: '2 - ضغط عند 4xx (on_4xx) 【موصى به】',
      deltaOnly: 'فروقات فقط (delta_only)',
      smart: 'ضغط ذكي (smart) 【افتراضي】',
      aggressive: 'ضغط هجومي (aggressive)',
      headroom: 'هامش (headroom)',
      headroomAggressive: 'هامش هجومي (headroom_aggressive)',
    },
    enumLabels: {
      off: '0 - إيقاف (off)',
      auto: '1 - عتبة تلقائية (auto_threshold)',
      on4xx: '2 - ضغط عند 4xx (on_4xx)',
      deltaOnly: 'فروقات فقط (delta_only)',
      smart: 'ضغط ذكي (smart)',
      aggressive: 'ضغط هجومي (aggressive)',
      headroom: 'هامش (headroom)',
      headroomAggressive: 'هامش هجومي (headroom_aggressive)',
    },
    enumDescriptions: {
      off: 'إيقاف وظيفة ضغط الرسائل تمامًا',
      auto: 'ضغط تلقائي عندما يتجاوز طول الرسالة عتبة نافذة السياق',
      on4xx: 'تشغيل الضغط وإعادة المحاولة عند استلام خطأ 4xx (مثل context_length_exceeded)',
      deltaOnly: 'الاحتفاظ فقط بفروقات الرسائل، دون ضغط الجلسة',
      smart: 'الضغط الذكي v4: تقليص الأدوات + نافذة منزلقة',
      aggressive: 'الضغط الهجومي v4: تحليل المهمة + ضغط استباقي',
      headroom: 'ضغط الهامش v7: ضغط ذكي لمصفوفات JSON',
      headroomAggressive: 'الهامش الهجومي v7: ضغط أكثر هجومية للمصفوفات + تخزين CCR',
    },
    hint: {
      off: 'إيقاف وظيفة ضغط الرسائل تمامًا',
      auto: 'ضغط تلقائي عندما يتجاوز طول الرسالة عتبة نافذة السياق',
      on4xx: 'تشغيل الضغط وإعادة المحاولة عند استلام خطأ 4xx (مثل context_length_exceeded)',
      deltaOnly: 'الاحتفاظ فقط بفروقات الرسائل، دون ضغط الجلسة (مناسب للسيناريوهات عديمة الحالة)',
      smart: 'الضغط الذكي v4: يتضمن تقليص تعريفات الأدوات وتحسين النافذة المنزلقة (موصى به افتراضيًا)',
      aggressive: 'الضغط الهجومي v4: يتضمن تحليل المهمة واستراتيجيات الضغط الاستباقية (نسبة ضغط عالية)',
      headroom: 'ضغط الهامش v7: يضغط مصفوفات JSON بذكاء (مثل البيانات المنظمة الكبيرة) مع الحفاظ على السلامة الدلالية',
      headroomAggressive: 'وضع الهامش الهجومي v7: خوارزمية ضغط مصفوفات أكثر هجومية + تخزين ذاكرة التخزين المؤقت ثلاثي المستويات CCR (أعلى نسبة ضغط)',
    },
    strategyEnum: {
      naive: 'naive - ضغط بسيط',
      smart: 'smart - ضغط ذكي',
      adaptive: 'adaptive - ضغط تكيفي',
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
  outputCompliance: '输出合规',
}