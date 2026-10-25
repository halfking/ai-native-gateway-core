// public.ts — Public portal copy (download / donation / offline activation).
//
// 2026-10-25 rewrite (impeccable craft-floor / STYLE pass): this file was
// still entirely English. It is now written in Arabic, and the decorative
// "·" separator, the em dashes standing in for punctuation and the
// triple-adjective lists are gone.
//
// RTL note: no directional control characters (U+202B to U+202E) are used
// here. The page direction comes from the document, not from the copy.
export default {
  download: {
    activateLink: 'معالج التفعيل',
  },
  support: {
    emailPlaceholder: "you{'@'}company.com",
  },
  layout: {
    download: 'تنزيل',
    support: 'ادعمنا',
  },
  agreement: {
    title: 'اتفاقية المستخدم وإشعاراته',
    intro: 'مرحبًا بك في بوابة المؤسسات المبنية على الذكاء الاصطناعي. تُشارك النواة مفتوحة المصدر، وتُسلَّم النسخة القياسية كما هي للتشغيل الداخلي. اقرأ ما يلي قبل المتابعة:',
    points: [
      'الحقوق والواجبات: أنت مسؤول عن التثبيت والإعداد والنسخ الاحتياطي والاستخدام المطابق للقوانين داخل بيئتك. نقدّم البرنامج والوثائق، ولا نضمن نتائج الأعمال ولا جودة النماذج ولا جاهزية الأطراف الثالثة.',
      'جمع البيانات: لا تُجمع محتويات الأعمال افتراضيًا. يغطي القياس الاختياري الإصدار ومعرّف المثيل وحالة الترخيص والمقاييس المجمّعة، ولا يغطي التلقينات أو نص المحادثات أو مفاتيح API أو البيانات التي تحدّد شخصًا مباشرة.',
      'المشاركة مفتوحة المصدر: النسخة دون تخصيص مشاركة من المجتمع بلا SLA تجاري. للطلبات المخصّصة تواصل معنا عبر قناة الدعم.',
      'إخلاء المسؤولية: يُقدّم البرنامج كما هو. لا نتحمل مسؤولية الخسائر غير المباشرة أو فترات التوقف أو فقدان البيانات أو نتائج قرارات الذكاء الاصطناعي. قيّم المخاطر واحمِ نفسك بما يلزم.',
    ],
    disclaimer: 'الشروط الكاملة في الاتفاقية كاملة. وضع علامة يعني أنك قرأت النطاق أعلاه وفهمته.',
    checkbox: 'قرأتُ وأوافق على',
    fullLink: '«اتفاقية المستخدم وتفويض معالجة البيانات»',
    accept: 'وافق وتابع',
  },
  opAgreement: {
    downloadTitle: 'اقرأ {title} قبل التنزيل',
    activateTitle: 'اقرأ {title} قبل التفعيل',
  },
}
