// landing.ts — Landing page copy (guest homepage).
//
// 2026-10-25 rewrite (impeccable craft-floor / STYLE pass).
// Rebuilt from the zh-CN / en-US samples. The previous draft was still the
// English marketing text: "Core Open Source · Enterprise-Grade · Private
// Deployment", adjectives standing in for facts, em dashes doing the job of
// punctuation, and every description a tidy "X + Y + Z".
// Rules live in DESIGN.md §7 "Voice" at the repo root.
//
// Fact discipline: this version claims only what the product actually does
// (Apache 2.0 / 40 MB image / 200 concurrent with P99 under 500 ms / RLS
// row-level security / 43 audit rounds with zero L1 findings / k3s and Docker
// / three inbound protocols / 15-day trial). No new capability is implied.
//
// RTL note: this file carries no directional control characters (U+202B to
// U+202E). The browser derives direction from the page, not from the copy.
export default {
  // Rendered only in the PublicPageShell footer (brand name + this). It is no
  // longer used as a small label above the heading (craft-floor bans kickers).
  kicker: 'نواة مفتوحة المصدر برخصة Apache 2.0',
  brandTitle: 'بوابة المؤسسات المبنية على الذكاء الاصطناعي',
  // already says that. It says what the thing does instead.
  title:
    'بوابة واحدة لكل استدعاءات نماذج الذكاء الاصطناعي في مؤسستك، ترى فيها الاستهلاك وتحتسبه وتوقفه',
  subtitle:
    'بروتوكولات OpenAI وAnthropic وResponses تدخل في البوابة نفسها. التوجيه حسب المستأجر والنموذج، والفوترة والتدقيق وحدود المعدل في مكان واحد. تعمل على أجهزتك الخاصة، فلا يغادر البيانات الشبكة.',
  featuresTitle: 'ما تفعله البوابة',
  featuresSubtitle: 'الاستقبال والتوجيه والفوترة والتدقيق، كلها في مدخل واحد',
  // The six capability facts on the right-hand rail. Every one is checkable.
  heroPoints: [
    'النواة برخصة Apache 2.0',
    'مستوى البيانات بلغة Go، حجم الصورة 40 ميغابايت',
    '200 طلب متزامن، وزمن P99 أقل من 500 مللي ثانية',
    'ثلاثة بروتوكولات واردة، ومنفذ واحد',
    'عزل على مستوى الصف عبر PostgreSQL RLS',
    'k3s أو Docker، على أجهزتك الخاصة',
  ],
  features: {
    smartRouting: {
      title: 'التوجيه حسب المستأجر ونوع المهمة',
      description:
        'يُختار المسار من المستأجر والنموذج ونوع المهمة. حين يتعطل مزوّد، تكتشفه الفحوص الخلفية فينتقل الطلب إلى المزوّد التالي، وتتكفل مجموعة بصمات بيانات الاعتماد بالتناوب.',
    },
    safety: {
      title: 'حقن التعليمات والبيانات الحساسة',
      description:
        'يرصد LLM-as-judge حقن التعليمات في النماذج، والإصدار v1 يراقب فقط ولا يوقف، وتُخفى الحقول الحساسة وفق قواعد معيّنة.',
      badge: 'beta',
    },
    cache: {
      title: 'التخزين المؤقت والتكلفة',
      description:
        'تثبيت البادئات مع تخزين دلالي. كلما ارتفعت نسبة إصابة KV Cache قلت عدد الرموز التي تُحتسب.',
    },
    agent: {
      title: 'دخول الوكلاء وأدوات MCP',
      description:
        'سجل الوكلاء وبروتوكول A2A واستضافة أدوات MCP وتحويل البروتوكولات. ينتقل المنتج من مجرد تمرير الطلبات إلى نموذج LLM إلى تنسيق الوكلاء.',
      badge: 'قريبًا',
    },
    observability: {
      title: 'كل طلب يترك أثرًا',
      description:
        'سجلات الطلبات، وتدقيق قرارات التوجيه، وتتبّع OTel، وتصدير الأحداث بصيغة SIEM وCEF. عند وقوع عطل يمكن تتبّع ما جرى.',
    },
    billing: {
      title: 'الاستخدام يطابق الفاتورة',
      description:
        'خطط وأرصدة ومحفظة بثلاثة مجمعات: اشتراك، ورصيد، وشحن. يطّلع المستأجرون على استهلاكهم بأنفسهم بدل سؤال المسؤول.',
    },
    multiProtocol: {
      title: 'منفذ واحد، ثلاثة بروتوكولات',
      description:
        'يُوحَّد OpenAI Chat وAnthropic Messages وResponses عند الدخول. لا يتغير عميلك ولا تتغير البوابة.',
    },
    multiTenant: {
      title: 'المستأجرون لا يختلطون',
      description:
        'أمان على مستوى الصف عبر PostgreSQL RLS، و43 جولة تدقيق بلا أي ملاحظة من المستوى الأول، وسياسة وحصة مستقلة لكل مستأجر.',
    },
  },
  advantagesTitle: 'لماذا تنشره بنفسك',
  advantagesSubtitle: 'البيانات تبقى عندك، والفاتورة تبقى مفهومة',
  advantages: {
    openSource: {
      title: 'النواة مفتوحة',
      description:
        'مستوى البيانات، ومستوى التحكم، والمصادقة، ومجدول التوجيه كلها منشورة برخصة Apache 2.0. تستطيع تعديلها وتستطيع تدقيقها.',
    },
    private: {
      title: 'أجهزتك الخاصة',
      description: 'يُثبَّت على أجهزتك، بشكل k3s أو Docker، دون الاعتماد على أي خدمة خارجية.',
    },
    antiBan: {
      title: 'الحظر ليس انقطاعًا',
      description:
        'تناوب بيانات الاعتماد مع الفحص النشط: إذا حُظر أحدها، تتولى البيانات التالية المهمة.',
    },
    perf: {
      title: 'مستوى بيانات يتحمّل الضغط',
      description:
        'Go أصلي، حجم الصورة 40 ميغابايت، ويبقى زمن P99 أقل من 500 مللي ثانية عند 200 طلب متزامن، مع تمرير SSE مستقر.',
    },
  },
  footer: 'نواة مفتوحة برخصة Apache 2.0، تعمل على أجهزتك.',
  roadmap: {
    title: 'إلى أين يتجه المشروع',
    subtitle: 'من مستوى بيانات لنماذج LLM إلى بوابة لوكلاء المؤسسات',
    v31: {
      phase: 'v3.1',
      title: 'مركز أصول API Hub واستضافة أدوات MCP',
      description:
        'تُسجَّل نقاط نهاية LLM وخدمات MCP والوكلاء في مكان واحد، فيعثر عليها المطورون ويعيدون استخدامها من تلقاء أنفسهم.',
    },
    v32: {
      phase: 'v3.2',
      title: 'حظر الحقن، والتكامل مع SIEM، وSpecBoost',
      description:
        'ينتقل رصد حقن التعليمات من المراقبة إلى الحظر، وتُخفى البيانات الحساسة، وتُثرى وصفات واجهات API تلقائيًا لرفع دقة استدعاء الدوال.',
    },
    v40: {
      phase: 'v4.0',
      title: 'سجل الوكلاء وبوابة بروتوكول A2A',
      description: 'يفوّض الوكلاء المهام فيما بينهم، ويدخل OpenClaw ووكيل المؤسسة من المدخل نفسه.',
    },
    v50: {
      phase: 'v5.0',
      title: 'حلول قطاعية متاحة',
      description: 'قوالب لخدمة العملاء والموارد البشرية والمبيعات واللوجستيات. ثبّتها فتعمل.',
    },
  },
  // 2026-07-21: guest-nav aria + top download/activation links (App.vue L197-203).
  guestNavAria: 'تنقّل المنتج',
  navDownload: 'تنزيل',
  navSetup: 'تثبيت وتفعيل',
  navActivate: 'تفعيل عبر الإنترنت',
  navLicense: 'حالة الترخيص',
  navAgreement: 'اتفاقية المستخدم',
  navSupport: 'الدعم',
  // 2026-07-21: landing primary CTAs (LandingView.vue L109-119).
  ctaLogin: 'تسجيل الدخول',
  ctaDownload: 'نزّل حزمة التثبيت',
  ctaActivate: 'فعّل الترخيص',
  ctaAgreement: 'اطلع على الاتفاقية',
}
