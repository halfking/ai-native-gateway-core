// selfUpdateView.ts — التحديث الذاتي والترقية (SelfUpdateView).
//
// قاعدة الصياغة (نفس المصدر الذي بُني عليه كود الصفحة): فشل الفحص أو غياب
// مصدر الإصدارات البعيدة أو عدم إجراء أي فحص تعني "لا يمكن استنتاج النتيجة".
// تعرض الصفحة حالة غير معروفة مع السبب، ولا تدّعي أبدًا أن الإصدار الحالي هو
// الأحدث.
export default {
  pageTitle: 'التحديث الذاتي والترقية',
  pageSubtitle:
    'يبحث العقدة عن الإصدارات الجديدة تلقائيًا، وتصدر تنبيهًا، ثم تثبّت الترقية بعد تأكيد أحد المشرفين',

  statusCardTitle: 'حالة الإصدار الحالي',
  statusCardHint:
    'يعرض الإصدار الذي تعمل عليه هذه العقدة ونتيجة آخر بحث عن إصدارات بعيدة',
  currentVersion: 'الإصدار الحالي',
  latestVersion: 'أحدث إصدار',
  buildSequence: 'رقم البناء',
  lastCheckedAt: 'آخر فحص',
  checkInterval: 'الفاصل بين الفحوصات',
  checkIntervalHours: 'يفحص تلقائيًا كل {hours} ساعة',
  checkIntervalMinutes: 'يفحص تلقائيًا كل {minutes} دقيقة',
  checkIntervalSeconds: 'يفحص تلقائيًا كل {seconds} ثانية',
  checkNow: 'فحص التحديثات الآن',
  checking: 'جارٍ الفحص',

  stateNotConfiguredLabel: 'لم يتم إعداد مصدر إصدارات بعيدة',
  stateNotConfiguredDetail:
    'لا يوجد في هذه العقدة مصدر إصدارات بعيدة، لذلك فإن الفحص التلقائي والتنبيهات معطّلة. لا يمكن تحديد وجود إصدار أحدث.',
  stateUnknownLabel: 'حالة الإصدار غير معروفة',
  stateUnknownDetail:
    'لم يصل الفحص عن الإصدارات إلى نتيجة: {reason}. قبل نجاح أحد الفحوصات لا يمكن تحديد وجود إصدار أحدث.',
  stateNeverCheckedDetail:
    'مصدر الإصدارات البعيدة مُعد في هذه العقدة، لكن لم يُنفَّذ أي فحص بعد. لا يمكن تحديد وجود إصدار أحدث.',
  stateUpToDateLabel: 'الإصدار الحالي هو الأحدث',
  stateUpToDateDetail: 'أكد الفحص البعيد عدم توفر إصدار أحدث.',
  stateAvailableLabel: 'يتوفر إصدار أحدث',
  stateAvailableDetail:
    'أكد الفحص البعيد أن الإصدار {version} متاح ويمكن تثبيته.',

  bannerTitle: 'يتوفر إصدار أحدث',
  bannerMandatory: 'تحديث إلزامي',
  bannerOptional: 'تحديث اختياري',
  bannerNotesTitle: 'ملاحظات الإصدار',
  bannerNoNotes: 'لم يصاحب هذا الإصدار أي ملاحظات.',
  installNow: 'الترقية الآن',

  confirmInstallTitle: 'تأكيد الترقية إلى {version}',
  confirmInstallBody:
    'تقوم الترقية بتنزيل الملف التنفيذي الجديد والتحقق منه واستبدال الملف التنفيذي الحالي ثم إعادة تشغيل الخدمة. تتوقف الطلبات لفترة وجيزة أثناء إعادة التشغيل. هل تريد المتابعة؟',
  confirmInstallButton: 'تأكيد الترقية',
  installStartFailed: 'تعذر بدء مهمة التثبيت: {reason}',

  installCardTitle: 'تقدّم التثبيت',
  installCardHint: 'يعرض حالة آخر مهمة تثبيت والخطوات التي نفذت',
  installCardEmpty: 'لا توجد في هذه العقدة أي مهمة تثبيت مسجلة بعد.',
  jobVersion: 'الإصدار الهدف',
  jobMode: 'طريقة التثبيت',
  jobModeUnknown: 'طريقة تثبيت غير معروفة',
  jobStatus: 'حالة المهمة',
  jobStatusPending: 'في انتظار البدء',
  jobStatusDownloading: 'يجري تنزيل الملف التنفيذي',
  jobStatusReadyToRestart: 'في انتظار إعادة تشغيل الخدمة',
  jobStatusUpgrading: 'يجري استبدال الملف التنفيذي',
  jobStatusSuccess: 'نجحت الترقية',
  jobStatusFailed: 'فشلت الترقية',
  jobStatusRolledBack: 'تم الرجوع إلى الإصدار السابق',
  jobStatusUnknown: 'حالة غير معروفة',
  jobStartedAt: 'وقت البدء',
  jobStepsTitle: 'الخطوات المنفذة',
  jobStepsEmpty: 'لم تنتج المهمة أي خطوة بعد.',
  jobErrorTitle: 'سبب الفشل',
  jobSuccessNotice: 'اكتملت الترقية وأصبح الإصدار الجديد ساريًا.',
  jobRestartScheduled: 'اكتملت الترقية وستعيد الخدمة التشغيل بعد قليل.',
  modeArtifact: 'تنزيل الملف التنفيذي وتثبيته',
  modeSource: 'البناء من الشيفرة المصدرية المحلية ثم التثبيت',

  notificationsTitle: 'تنبيهات الترقية',
  notificationsHint:
    'يعرض سجلات التنبيهات التي أُنشئت عند اكتشاف إصدار جديد',
  notificationsEmpty: 'لا توجد تنبيهات ترقية بانتظار المعالجة.',
  notificationsLoadFailed: 'تعذر قراءة قائمة التنبيهات: {reason}',
  notificationVersion: 'الإصدار',
  notificationStatus: 'حالة التنبيه',
  notificationStartedAt: 'وقت الإنشاء',
  notificationCompletedAt: 'وقت المعالجة',
  notificationRetryCount: 'عدد المحاولات',
  notificationPending: 'بانتظار المعالجة',
  notificationHandled: 'تمت المعالجة',

  sourceCardTitle: 'البناء من الشيفرة المصدرية المحلية والترقية',
  sourceCardHint:
    'يبني الملف التنفيذي من الشيفرة الموجودة على هذا الجهاز ويثبّته. لا يغيّر البناء شجرة العمل الحالية.',
  sourceRefLabel: 'مرجع الشيفرة المصدرية',
  sourceRefPlaceholder: 'اتركه فارغًا لاستخدام شجرة العمل الحالية',
  sourceBuildNow: 'البناء من الشيفرة المصدرية والترقية',
  confirmSourceBuildTitle: 'تأكيد البناء من الشيفرة المصدرية والترقية',
  confirmSourceBuildBody:
    'يسحب البناء شجرة عمل منفصلة وينشئ الملف التنفيذي ويستبدل الإصدار الحالي ثم يعيد تشغيل الخدمة. يستهلك المعالج والقرص في هذا الجهاز. هل تريد المتابعة؟',
  confirmSourceBuildButton: 'تأكيد البناء والترقية',
  sourceBuildNoVersion:
    'الإصدار المراد تثبيته غير معروف بعد. نفّذ فحص الإصدارات أولًا.',
  sourceBuildFailed: 'تعذر بدء مهمة البناء: {reason}',
}
