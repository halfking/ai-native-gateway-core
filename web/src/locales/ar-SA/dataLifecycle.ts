export default {
  refresh: "تحديث",
  loading: "جاري التحميل…",
  storage: {
    migrateFailedWithReason: 'تعذّر إتمام الترحيل: {reason}. أعد المحاولة.',
  },
  /**
   * نصوص إدارة الحقول الكبيرة (BlobManager).
   * ★ الحالة الفارغة (الدفعة 14 · «حالات فارغة»): كان用的是 مفتاحًا عامًا
   *   (`common.feedback.noData` — «لا يوجد محتوى لعرضه حاليًا»).
   *   هذه الصفحة **بلا مُرشِّح زمني** ⇒ لا نكتب نطاقًا زمنيًا (لا نخترع معلومة لا تعرفها الصفحة).
   */
  blob: {
    emptyNoResult: 'لا توجد سجلات للحقول الكبيرة بعد.',
  },
  storageOverview: {
    summary: {
      databaseTitle: "قاعدة بيانات PostgreSQL",
      databaseSubtitle: "الحجم الكلي (الجداول + الفهارس + TOAST)",
      databaseMetric: "قاعدة البيانات",
      tablesMetric: "الجداول",
      indexesMetric: "الفهارس",
      toastMetric: "TOAST",
      diskTitle: "القرص المحلي",
      diskUsedOf: "المستخدم / {total}",
      path: "المسار",
      used: "المستخدم",
      free: "المتاح",
      columnarTitle: "التخزين العمودي (citus_columnar)",
      enabled: "مفعّل",
      notInstalled: "غير مثبت",
      columnarTableCount: "عدد الجداول (بما فيها التقسيمات)",
      columnarTableCountMeta: "الجداول التي تستخدم أسلوب الوصول columnar",
      columnarTotalColumns: "إجمالي الأعمدة",
      columnarTotalColumnsMeta: "مجموع الأعمدة في جميع الجداول العمودية",
      columnarSize: "حجم التخزين العمودي",
      columnarSizeMeta: "الحجم بعد الضغط (بما فيه بيانات chunk الوصفية)",
      columnarDbShare: "حصة DB",
      columnarDbShareMeta: "العمودي / الحجم الكلي لقاعدة البيانات",
      columnarNotInstalledFallback: "امتداد citus_columnar غير مثبت",
      localLogsTitle: "دليل السجلات المحلي",
      notFound: "غير موجود",
      fileCount: "الملفات",
      size: "الحجم",
      oldest: "الأقدم",
      newest: "الأحدث",
      localLogsMissingHint: "لم يتم العثور على دليل السجلات؛ النسخ/التدوير gzip لن يكتب هنا.",
      warningsTitle: "تحذيرات"
    },
    tablesTitle: "أفضل {n} جداول في قاعدة البيانات",
    refresh: "تحديث",
    refreshLoading: "جاري التحميل…",
    columns: {
      tableName: "الجدول",
      rows: "الصفوف",
      totalSize: "الحجم الكلي",
      indexes: "الفهارس",
      toast: "TOAST",
      percent: "النسبة",
      partition: "التقسيم",
      actions: "الإجراءات"
    },
    partitionBadge: "تقسيم",
    partitionNone: "—",
    buttons: {
      vacuum: "🧹 VACUUM",
      vacuumFull: "🗜 FULL",
      reindex: "🔧 REINDEX",
      vacuumTitle: "VACUUM (ANALYZE) {table}: بدون قفل، يستعيد الصفوف الميتة",
      vacuumFullTitle: "VACUUM FULL {table}: بقفل، يعيد المساحة إلى نظام التشغيل",
      reindexTitle: "REINDEX TABLE {table}: بقفل، يعيد بناء الفهارس"
    },
    modal: {
      vacuumTitle: "🧹 تأكيد تنفيذ VACUUM",
      vacuumFullTitle: "🗜 تأكيد تنفيذ VACUUM FULL",
      reindexTitle: "🔧 تأكيد تنفيذ REINDEX",
      intro: "سيتم تنفيذ {op} على {table}.",
      currentSize: "الحجم الحالي: ",
      rowCount: "عدد الصفوف: ",
      partition: "مقسّمة: ",
      partitionYes: "نعم (⚠️ القفل يؤثر على جميع التقسيمات الفرعية)",
      partitionNo: "لا",
      flow: "سير العملية",
      impact: "الأثر على الأداء",
      disk: "الأثر على مساحة القرص",
      time: "الوقت المقدر",
      cancel: "إلغاء",
      confirm: "تنفيذ {op}",
      success: "اكتمل {op} {table}: تم تحرير {size} ({pct}%)",
      failed: "فشل {op}: {msg}"
    },
    rowResult: {
      running: "{op} قيد التشغيل…",
      success: "{op}: تحرير {size} ({pct}%, {ms} مللي ثانية)",
      failed: "فشل {op}: {msg}"
    },
    op: {
      vacuum: {
        flow: "يفحص الجدول بحثًا عن الصفوف الميتة ويعلّم مساحتها كقابلة لإعادة الاستخدام (دون إعادتها فورًا إلى نظام التشغيل). يحدّث أيضًا الإحصاءات لمساعدة المُخطِّط على اختيار خطط أفضل.",
        impact: "لا يقفل الجدول؛ تستمر SELECT / INSERT / UPDATE / DELETE بشكل طبيعي. يستهلك القليل من CPU وIO إضافيين أثناء التشغيل.",
        disk: "لا يعيد المساحة فورًا إلى نظام التشغيل، بل يعيد استخدامها داخل الجدول. يقلل النمو المستقبلي لكنه لا يُقلل pg_database_size فورًا.",
        time: "عادة من ثوانٍ إلى دقائق. التكلفة ≈ حجم الجدول / سرعة قراءة IO التسلسلية (≈200 ميجابايت/ث على SSD نموذجي)."
      },
      vacuumFull: {
        flow: "يكتسب قفل ACCESS EXCLUSIVE ويعيد كتابة الجدول بالكامل (مع TOAST) في ملف جديد، ثم يستبدل الملف الجديد بالقديم ويحرر القديم.",
        impact: "⚠️ أثناء الاحتفاظ بقفل ACCESS EXCLUSIVE، تُحظر جميع عمليات القراءة والكتابة على هذا الجدول. يُفضّل تنفيذه في أوقات انخفاض الحركة.",
        disk: "يتضاعف استخدام القرص مؤقتًا (القديم + الجديد). بعد الانتهاء يُحرر الملف القديم. أقصى قابل للاستعادة ≈ حجم الجدول + TOAST + الفهارس.",
        time: "التكلفة ≈ (الجدول + الفهارس) / سرعة كتابة IO التسلسلية (≈200 ميجابايت/ث على SSD نموذجي). مثال: جدول 10 جيجابايت ≈ 1–3 دقائق، 100 جيجابايت ≈ 15–30 دقيقة."
      },
      reindex: {
        flow: "يكتسب قفل الجدول ويعيد بناء كل فهرس مدعوم (مثل B-tree) واحدًا تلو الآخر. في النهاية، تحل الفهارس الجديدة محل القديمة.",
        impact: "⚠️ القفل يحظر عمليات DML (INSERT / UPDATE / DELETE) طوال مدة إعادة البناء. يُنصح بالتنفيذ في أوقات انخفاض الحركة.",
        disk: "يتضاعف حجم الفهارس مؤقتًا (القديم + الجديد). في النهاية تُحرر الفهارس القديمة. يزيل bloat الفهارس ويمكنه تسريع القراءات اللاحقة.",
        time: "التكلفة ≈ إجمالي حجم الفهارس / سرعة كتابة IO التسلسلية (≈200 ميجابايت/ث على SSD نموذجي). فهارس هذا الجدول ≈ {size}؛ توقع ثوانٍ إلى دقائق."
      }
    }
  },
  hotPartition: {
    hotTableTitle: "ترحيل بيانات جداول Hot",
    hotTableDesc: "يرحّل الصفوف من جداول Hot إلى التقسيمات الشهرية. يلزم تنفيذ VACUUM FULL لاحقًا لإعادة المساحة فعلًا إلى نظام التشغيل.",
    retention: {
      all: "ترحيل الكل الآن",
      "1day": "الاحتفاظ بآخر يوم واحد",
      "3day": "الاحتفاظ بآخر 3 أيام",
      "7day": "الاحتفاظ بآخر 7 أيام",
      "30day": "الاحتفاظ بآخر 30 يومًا"
    },
    startMigrate: "بدء الترحيل",
    migrating: "جاري الترحيل…",
    migrationProgress: "تم ترحيل {migrated} صفًا ({batches} دفعات، {duration} ثانية)",
    promoteConfirm: "ترحيل البيانات الأقدم من {hours} من {label}؟",
    promoteAll: "الكل",
    days: "{n} أيام",
    partitionTitle: "إدارة التقسيمات الشهرية",
    partitionDesc: "احذف التقسيمات الشهرية القديمة لتحرير مساحة القرص.",
    partitionWarning: "هذا الإجراء لا يمكن التراجع عنه: يرجى توخي الحذر.",
    tables: {
      title: "الجداول المقسّمة",
      refresh: "تحديث",
      columns: {
        name: "الجدول",
        description: "الوصف",
        totalSize: "الحجم الكلي",
        rows: "الصفوف",
        partitions: "عدد التقسيمات",
        archivable: "قابل للأرشفة",
        actions: "الإجراءات"
      },
      viewPartitions: "عرض / إدارة التقسيمات",
      noTables: "لا توجد جداول مقسّمة"
    },
    partitionList: {
      title: "تقسيمات {table}",
      columns: {
        name: "القسم",
        storageType: "التخزين",
        size: "الحجم",
        rows: "الصفوف",
        month: "الشهر",
        action: "الإجراءات"
      },
      storage: {
        columnar: "عمودي",
        heap: "هيب",
        archive: "مؤرشف"
      },
      delete: "حذف",
      deleting: "جاري الحذف…",
      empty: "لا توجد تقسيمات لهذا الجدول"
    },
    deleteModal: {
      title: "تأكيد حذف القسم",
      warning: "هذا الإجراء لا يمكن التراجع عنه!",
      fields: {
        name: "القسم",
        size: "الحجم",
        rows: "الصفوف",
        month: "الشهر"
      },
      inputLabel: "اكتب اسم القسم للتأكيد:",
      cancel: "إلغاء",
      confirm: "تنفيذ الحذف",
      deleting: "جاري الحذف…",
    },
    hotTableNames: {
      requestLogs: "سجلات الطلبات",
      credentialModelIndex: "فهرس بيانات اعتماد النماذج",
      usageLedger: "دفتر الاستخدام",
      routingDecisionLog: "سجل قرارات التوجيه"
    },
    labels: {
      rows: "الصفوف",
      toast: "TOAST"
    },















    statsLoadFailedWithReason: 'تعذّر تحميل إحصائيات جداول hot: {reason}. أعد المحاولة.',
    startMigrationFailedWithReason: 'تعذّر بدء مهمة الترحيل: {reason}. أعد المحاولة.',
    submitDeleteFailedWithReason: 'تعذّر إرسال مهمة الحذف: {reason}. أعد المحاولة.',
  },
  preview: "معاينة التنظيف",
  hotData: "بيانات ساخنة (0-7 أيام)",
  warmData: "بيانات دافئة (7-30 يومًا)",
  coldData: "بيانات باردة (30-90 يومًا)",
  expiredData: "بيانات منتهية الصلاحية (>90 يومًا)",
  missingDate: "يرجى اختيار تاريخ البدء والانتهاء",
  previewFailed: 'تعذّر إتمام المعاينة. أعد المحاولة.',
  needPreview: "يرجى معاينة التأثير أولاً",
  executeNotImpl: "سيتم تنفيذ وظيفة التنظيف في المرحلة التالية. يرجى استخدام برنامج نصي سطر الأوامر لتنفيذ عمليات التنظيف."
,
  pages: {
    filesystemMaintenance: 'صيانة نظام الملفات',
    storageConfig: 'إعداد التخزين',
    logManagement: 'إدارة السجلات',
    usageCost: 'تكلفة الاستخدام',
    usageEnhanced: 'تحليلات الاستخدام المتقدمة',
  },

  usageEnhanced: {
    refresh: 'تحديث',
    loading: 'جارٍ التحميل…',
    empty: 'لا توجد سجلات استهلاك في هذه الفترة',
    cards: {
      totalCost: 'التكلفة الإجمالية',
      totalRequests: 'إجمالي الطلبات',
      periodDelta: 'فارق التكلفة بين الفترتين',
      cacheHitRate: 'معدل إصابة الذاكرة المؤقتة',
    },
    trend: {
      title: 'اتجاه التكلفة (آخر {days} يومًا)',
    },
    hotKeys: {
      title: 'أبرز مفاتيح API (حسب استهلاك الرموز)',
      colId: 'معرّف مفتاح API',
      colPrefix: 'بادئة المفتاح',
      colApplication: 'التطبيق',
      colRequests: 'الطلبات',
      colTokens: 'الرموز',
      colCost: 'التكلفة الإجمالية',
    },
    degraded: {
      title: 'بعض المقاييس غير متاحة: ',
      hint: 'مخطط قاعدة البيانات أقدم من هذا الإصدار؛ الأرقام التالية قيم بديلة تعني «تعذّر الحساب» وليست قياسات حقيقية.',
    },
    errors: {
      load: 'تعذّر تحميل بيانات الاستخدام المتقدمة. أعد المحاولة.',
    },
    sources: {
      summary: 'ملخص الاستخدام',
      costTrend: 'اتجاه التكلفة',
      periodCompare: 'مقارنة الفترات',
      cacheEconomics: 'اقتصاديات الذاكرة المؤقتة',
      hotKeys: 'مفاتيح API النشطة',
    },
    footer: 'آخر تحديث: {time}',
  },

  usageCost: {
    refresh: 'تحديث',
    loading: 'جاري التحميل…',
    compare: {
      title: 'مقارنة التكلفة',
      currentPeriod: 'الفترة الحالية:',
      previousPeriod: 'المقارنة:',
      currentLabel: 'الحالية ({period})',
      previousLabel: 'فترة المقارنة ({period})',
      change: 'التغيير',
      significant: 'ملحوظ',
      meta: '{requests} طلب: {models} نماذج',
    },
    attribution: {
      title: 'إسناد التكلفة',
      groupBy: 'التجميع حسب:',
      model: 'حسب النموذج',
      provider: 'حسب المزود',
      intent: 'حسب النية',
      shareChart: 'حصة التكلفة',
      detailChart: 'تفصيل التكلفة (إدخال مقابل إخراج)',
      otherNote: 'عناصر أخرى {count}: {cost}',
      otherLabel: 'أخرى',
      colModel: 'النموذج',
      colProvider: 'المزود',
      colIntent: 'النية',
      colRequests: 'الطلبات',
      colTotalCost: 'التكلفة الإجمالية',
      colInputCost: 'تكلفة الإدخال',
      colOutputCost: 'تكلفة الإخراج',
      colShare: 'الحصة',
      colLatency: 'متوسط التأخير',
    },
    charts: {
      inputCost: 'تكلفة الإدخال',
      outputCost: 'تكلفة الإخراج',
    },
    cache: {
      title: 'اقتصاديات التخزين المؤقت',
      hitRate: 'معدل إصابة التخزين المؤقت',
      cacheSaved: 'توفير التخزين المؤقت',
      cacheSavedHint: 'مقارنة بلا تخزين مؤقت',
      compressionSaved: 'توفير الضغط',
      compressionCount: '{n} عمليات ضغط',
      savingsRate: 'معدل التوفير الإجمالي',
      totalSaved: 'إجمالي التوفير {cost}',
      actualSpend: 'الإنفاق الفعلي',
      effectiveRatio: '{pct} من التكلفة غير المحسّنة',
      totalRequests: 'إجمالي الطلبات',
    },
    errors: {
      costTrend: 'تعذّر تحميل اتجاه التكلفة. أعد المحاولة.',
      periodCompare: 'تعذّر تحميل مقارنة الفترات. أعد المحاولة.',
      cacheEconomics: 'تعذّر تحميل اقتصاديات الذاكرة المؤقتة. أعد المحاولة.',
    },
    degraded: {
      title: 'بعض المقاييس غير متاحة:',
      hint: 'مخطط قاعدة البيانات أقدم من هذه النسخة، لذا الأرقام أدناه قيم نائبة تعني «تعذّر الحساب» وليست قياسات حقيقية (وهذا لا يعني عدم وجود بيانات لهذه الفترة). اطلب من المسؤول تنفيذ ترحيل تجميع البيانات.',
    },
  },
  confirmDeleteCleanup: 'حذف {rows} صفًا؟\nالمساحة المتوقَّع تحريرها: {size}\n\nهذا الإجراء لا رجعة فيه!',
  confirmArchiveCleanup: 'أرشفة {rows} صفًا؟\nالمساحة المتوقَّع تحريرها: {size}\n\nهذا الإجراء لا رجعة فيه!',
  confirmAttachmentNullify: 'تنفيذ؟\nسيُضبط {n} من العناصر على NULL\nالإجمالي: {size}\n(لا تُحذف الملفات الفعلية)',
  confirmBlobCleanup: 'تنظيف؟\nيؤثر على {rows} صفًا\nيحرر نحو {size}\n(يضبط فقط request_body / outbound_body على NULL مع إبقاء البيانات الوصفية)',
  confirmReplay: 'إعادة تشغيل {filename}؟',
  confirmReplayArchive: 'إعادة تشغيل {filename} وأرشفته؟',
  confirmFileCleanup: 'حذف الملفات الأقدم من {days} يومًا؟\nالسبب: {reason}',
  confirmLogCleanup: 'حذف ملفات السجلات الأقدم من {days} يومًا؟ لا يمكن التراجع عن هذا الإجراء.',
  confirmDirMigrate: 'سيتم تغيير دليل تخزين المرفقات. ستُنسخ الملفات الحالية إلى الدليل الجديد ويُحذف القديم بعد التحقق.\n\n· لا تتأثر عمليات التنزيل/الكتابة أثناء النسخ (يظل الدليل القديم مستخدمًا)\n· ينتقل تلقائيًا إلى الدليل الجديد عند الاكتمال\n· قد يستغرق وقتًا مع كثرة الملفات؛ يظهر التقدم بالأسفل\n\nتغيير الدليل والترحيل؟',
  dirMigrateCancelled: 'أُلغي (لم يتغير الدليل)',
}