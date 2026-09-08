# AI Native Gateway

> ليست مجرد وكيل. **مستوى إدارة أصلي للذكاء الاصطناعي ومنصة حوكمة للجلسات** لعصر الوكلاء وVibe Coding.

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8.svg)](https://golang.org)
[![Version](https://img.shields.io/badge/version-2.5.3-green.svg)](CHANGELOG.md)

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja.md) | [Deutsch](README.de.md) | [Français](README.fr.md) | [Español](README.es.md) | **العربية**

[البدء السريع](#quick-start) • [لماذا أصلي للذكاء الاصطناعي](#why-ai-native) • [مستوى الإدارة](#management-plane) • [حوكمة الجلسة](#session-governance) • [معاينة](#feature-preview) • [المقارنة](#comparison) • [الهندسة](docs/architecture.md)

---

<a id="why-ai-native"></a>

## لماذا بوابة أصلية للذكاء الاصطناعي؟

بوابة API التقليدية تدير HTTP. وكيل LLM العام يجعل «بدّل النموذج وأبقِ الـ SDK» ممكناً.
عندما يصبح الذكاء الاصطناعي بنية إنتاج، تصبح المشكلات الصعبة مختلفة:

1. **السيطرة** — من يستدعي، أي نموذج عمل، كم كلّف، وهل تجاوزت الجلسة حدود المستأجر
2. **الإكمال** — جلسات الوكلاء تمتد عشرات الأدوار عبر نماذج وبيانات اعتماد؛ لا يجوز أن يختفي السياق
3. **التتبع** — تحتاج المحادثة كاملة وقرار التوجيه، لا سطراً واحداً من access log

تعامل AI Native Gateway **الجلسات وبيانات الاعتماد والمستأجرين والتكلفة والامتثال** ككائنات من الدرجة الأولى: ثنائي Go واحد بواجهة إدارة بـ 8 لغات، تبقى البيانات داخل محيطك، وتبقى البروتوكولات متوافقة مع OpenAI / Anthropic / Gemini / Responses.

| | بوابة API تقليدية | وكيل LLM عام | **AI Native Gateway** |
|---|---|---|---|
| وحدة الحوكمة | طلب / مسار | استدعاء نموذج واحد | **جلسة + مستأجر + اعتماد** |
| التوجيه | موازنة أعلى | fallback / round-robin | **طبقتان: النموذج ثم الاعتماد** |
| الرصد | مقاييس + سجلات | عدّادات الرموز | **بانوراما توجيه + إعادة تشغيل جلسة + دفتر** |
| تعدد المستأجرين | إضافة / مساحة أسماء | طبقة التطبيق | **RLS في PostgreSQL على 38+ جدولاً** |
| النشر | أجزاء كثيرة | SaaS أو سكربتات | **ثنائي واحد + خاص 100٪** |

**تناسب**: جلسات Vibe Coding / وكلاء IDE الطويلة · أساطيل وكلاء على مخرج مشترك · MaaS / إعادة بيع (باقات + أرصدة + معززات) · إقامة البيانات.

---

## ✨ أربع قيم أساسية

| القيمة | ما يحصل عليه العميل |
|--------|---------------------|
| **أمان** | حواجز الذكاء الاصطناعي · DLP · اعتراض مضمّن · حوكمة Vibe Coding · SIEM/SOAR |
| **ثبات** | تنسيق متعدد السحابة · قاطع دائرة حسّاس للصحة · هدف SLA ‏99.9٪ |
| **تكلفة منخفضة** | تخزين دلالي مؤقت · توجيه تلقائي · قياس الرموز · ضغط المطالبات · مجمع مجاني |
| **تكامل مؤسسي** | تدقيق كامل السلسلة · RLS متعدد المستأجرين · دفتر MaaS · MCP / API Hub (خارطة طريق) |

## 🏗️ ثلاثة أعمدة للمنتج

```
┌────────────────────────┬────────────────────────┬────────────────────────┐
│   Control              │   Govern               │   Secure               │
├────────────────────────┼────────────────────────┼────────────────────────┤
│ ✅ قياس الرموز          │ 🔨 أصول API Hub        │ 🔨 Model Armor         │
│ ✅ توجيه ذكي + لاصق     │ 🔨 اكتشاف تلقائي       │ 🔨 تنقيح SDP           │
│ ✅ تخزين دلالي + Funnel │ 🔨 إثراء SpecBoost     │ 🔨 مطالبات عدائية      │
│ ✅ تدقيق + OTel         │ ✅ RLS متعدد المستأجرين │ ✅ SIEM/SOAR           │
│ ✅ فوترة MaaS           │ ✅ دورة حياة الجلسة    │ ✅ إخفاء الأسرار       │
└────────────────────────┴────────────────────────┴────────────────────────┘
```

✅ = مُسلَّم &nbsp;·&nbsp; 🔨 = خارطة طريق

## 🎯 مصفوفة القدرات

| الطبقة | ما تحصل عليه |
|--------|--------------|
| **البروتوكول** | OpenAI / Anthropic / Gemini / Responses + ترحيل SSE مع فحوصات سلامة |
| **التوجيه** | طبقتان (نموذج → اعتماد) + جلسات لاصقة + تبديل حسّاس للصحة؛ تقييم cost/quality في وضع shadow حالياً |
| **الإدارة** | Vue SPA مضمّنة (8 لغات) + إعداد ساخن (~5 ث) + مصفوفة اعتمادات + بانوراما + MaaS |
| **حوكمة الجلسة** | ربط لاصق + ضغط مسبق + إعادة تشغيل كاملة + ملخص + دورة حياة من 4 مستويات |
| **تعدد المستأجرين** | نفق هوية (IP/MAC/ClientID افتراضي) + مجمعات اعتماد + RLS على 38+ جدولاً |
| **الحركة** | حدود TPM/RPM + تخزين دلالي + ضغط مطالبات + نوافذ منزلقة |
| **التدقيق** | تدقيق كامل السلسلة + DLQ + تراجع إلى القرص + OTel + Prometheus |
| **الاعتمادات** | اعتمادات متعددة + مجمع بصمات (50+ UA · 35 Accept-Language · 11 uTLS) + سبر تكيّفي |
| **النشر** | نسختان (Docker + k3s NodePort) تشتركان في مخطط PostgreSQL واحد |

انظر [Architecture](docs/architecture.md).

---

<a id="management-plane"></a>

## 🎛️ مستوى الإدارة

معظم البوابات تترك «الإدارة» في ملفات إعداد أو SaaS خارجي. هنا يسير مستوى التحكم في نفس العملية مع مستوى البيانات: افتح `/admin` لتغيير سياسة، وفحص جلسة، وتشغيل مستأجر، ومطابقة التكلفة.

- **سياسة ساخنة**: أنواع العمل وأوزان التوجيه والحدود وعتبات الضغط خلال نحو 5 ثوانٍ — بلا إعادة تشغيل
- **تشغيل الاعتمادات**: مصفوفة 19×18، P95 / نجاح ساعة واحدة، فتحات التزامن
- **توجيه قابل للرصد**: L1 يصنّف 10 أنواع عمل → درجة بـ 6 أبعاد → قفل ملف؛ L2 تراجع طبقة → جولة فوترة → P2C → تنفيذ / قطع
- **مستأجرون + MaaS**: مستخدمون / مفاتيح / حصص في مكان واحد؛ باقات + أرصدة + معززات؛ CNY + USD؛ عينة إنتاج: 14,208 طلباً / 726 مليون رمز / 295.50$ في 7 أيام على مستأجر واحد
- **كتالوج التكلفة**: 1,045 عرضاً و410 نموذجاً بتغطية 100٪
- **8 لغات للواجهة**: en / zh-CN / zh-TW / ja / de / fr / es / ar

---

<a id="session-governance"></a>

## 🧠 حوكمة الجلسة

الوكيل العام يعامل كل استدعاء كحدث معزول. في عصر الوكلاء، «الطلب» غالباً قفزة واحدة في محادثة طويلة. هذه البوابة تحكم **الجلسة**.

- **ربط لاصق**: تثبيت الجلسة على اعتماد حتى يبقى السياق بعد تبديل النموذج أو تجاوز الفشل
- **خط معالجة مسبقة**: خام → تنقيح → ضغط؛ عند نحو 80٪ من نافذة المزوّد الحقيقية يُضغط قبل الإرسال
- **بقاء الجلسات الطويلة**: إعادة محاولة / تبديل اعتماد / تبديل نموذج حسب فئة الخطأ؛ بعد البايت الأول يُحفظ اتساق التدفق؛ نبضات أثناء الانتظار
- **تدقيق وإعادة تشغيل**: مطالبة النظام والاستجابة كاملتين (أكثر من 13,000 جلسة في الإنتاج)
- **بيانات وصفية**: 10 أنواع عمل، مشروع، عنوان، ملخص
- **نفق الهوية**: IP/MAC/ClientID افتراضي ينسب حركة الوكلاء على مخرج مشترك إلى مستأجر؛ يبقى RLS سارياً
- **دورة حياة من 4 مستويات**: ساخن 0–7 أيام / دافئ 7–30 / بارد 30–90 / منتهٍ >90، مع معاينة قبل الأرشفة

---

<a id="feature-preview"></a>

## 🎛️ معاينة المنتج

كل الوحدات أدناه مُسلَّمة. اللقطات من نشر محلي حي (1728×1050 بعد تحميل البيانات كاملة).

### 1. مراقب الاعتمادات

![Credential Monitor](docs/assets/screenshots/credential-monitor.png)

19 اعتماداً × 18 نموذجاً. P95 ونجاح ساعة واحدة وفتحات التزامن بنظرة واحدة.

### 2. بانوراما التوجيه

![Routing Panorama](docs/assets/screenshots/routing-panorama.png)

خريطة حرارية مهمة × نموذج ومخطط Sankey لأكثر من 14,000 طلب حي (مهمة → نموذج → مزوّد).

### 3. سجلات الطلبات — طب شرعي للجلسة

![Request Detail](docs/assets/screenshots/request-detail.png)

إعادة تشغيل السؤال والجواب، شلال التوزيع، أثر التوجيه/إعادة المحاولة، ضغط/إخفاء، إحصاءات الرموز والتخزين. تُحفظ المفاتيح كـ `sk-****`.

### 4. التدفق الحي · أنواع العمل · المجمع المجاني

![Dashboard Request Stream](docs/assets/screenshots/dashboard-request-stream.png)

![Work Types](docs/assets/screenshots/work-types.png)

![Free Pool](docs/assets/screenshots/free-pool.png)

قيد التنفيذ / p50 / p95 حسب الطابور. عشرة أنواع عمل قابلة للضبط. مجمعات Groq / Google AI Studio / OpenRouter / SiliconFlow / Zhipu.

### 5. دورة الحياة · فوترة المستأجر · كتالوج الأسعار

مستويات ساخن/دافئ/بارد/منتهٍ مع معاينة قبل الأرشفة. يغطي MaaS الكتالوج والباقات والاستهلاك والمحفظة والدفتر.

---

<a id="quick-start"></a>

## 🚀 البدء السريع

### الخيار أ: Docker Compose (موصى به، أقل من 10 دقائق)

```bash
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git ai-native-gateway
cd ai-native-gateway
# مرآة GitHub: git clone https://github.com/halfking/ai-native-gateway-core.git

cp .env.quickstart.example .env
docker compose -f docker-compose.quickstart.yml up -d
curl http://localhost:8781/healthz
open http://localhost:8781/admin
```

### الخيار ب: البناء من المصدر

```bash
go build -o gateway ./cmd/gateway
./gateway --config=configs/local.yaml
curl http://localhost:8781/healthz
```

للمطوّرين: `./scripts/install-githooks.sh --pre-commit` و `./scripts/install-githooks.sh`.

انظر [Getting Started](docs/getting-started.md).

---

## أوضاع النشر

| الوضع | الوصف | الحالة |
|-------|--------|--------|
| **Docker Compose** | حزمة كاملة مع PostgreSQL / Redis | ✅ للتقييم |
| **ثنائي + systemd** | إنتاج Linux | ✅ مثبّت متوفر |
| **Kubernetes** | Deployment + ConfigMap + Service | ⚠️ مستوى اختبار (الإنتاج الداخلي على k3s) |

البدء السريع يستخدم **نفس الثنائي ونفس المخطط** كالإنتاج. الانتقال للإنتاج يعني PostgreSQL 14+ / Redis 7+ مُدارَين وTLS.

انظر [Production Deployment](docs/06-deployment/).

---

<a id="comparison"></a>

## 📐 التموضع والمقارنة

| البعد | بوابة ذكاء اصطناعي عامة | AI Native Gateway |
|-------|-------------------------|-------------------|
| **النشر / البيانات** | SaaS أو بيانات تغادر المحيط | خاص 100٪ |
| **الحوكمة** | سجلات طلبات | جلسة لاصقة + إعادة تشغيل + ضغط + 4 مستويات |
| **التوجيه** | fallback / round-robin | طبقتان + حسّاس للصحة + ظاهر في الإدارة |
| **الفوترة** | استخدام (USD) | باقات + أرصدة + معززات (شركات صينية صغيرة، Alipay) |
| **الأعلى** | قلة من الكبار | كتالوج واسع + نماذج صينية + محلية + مجمع مجاني |
| **البصمات** | أساسي | 50+ UA · 35 Accept-Language · 11 uTLS |
| **تدقيق المستأجر** | قياسي | RLS على 38+ جدولاً · تدقيقات L1 = 0 |
| **بوابة أدوات MCP** | جزئي | تسليم كامل مستهدف الربع الثالث 2026 |

| الميزة | AI Native Gateway | LiteLLM | OmniRoute | Portkey | Kong AI |
|--------|-------------------|---------|-----------|---------|---------|
| **النشر** | استضافة ذاتية خاصة | SaaS + OSS | استضافة ذاتية (Node) | SaaS | OSS |
| **تعدد المستأجرين** | RLS أصلي في PG | أساسي | عقدة واحدة | كامل (SaaS) | إضافات |
| **واجهة الإدارة** | Vue SPA مضمّنة (8 لغات) | CLI | واجهة ويب | واجهة SaaS | Kong Manager |
| **الإقامة** | خاص 100٪ | حسب الوضع | خاص 100٪ | سحابة | قابل للاستضافة الذاتية |
| **الرخصة** | Apache 2.0 | MIT | المصدر الأعلى | ملكية | Apache 2.0 |

**اخترنا** للإقامة، وتعدد المستأجرين على مستوى قاعدة البيانات، والطب الشرعي للجلسة، ومستوى إدارة في ثنائي واحد.
**اختر غيرنا** لتغطية 100+ مزوّد (LiteLLM)، أو Node + قاعدة مضمّنة (OmniRoute)، أو SaaS بلا تشغيل (Portkey)، أو بوابة API عامة + LLM (Kong).

انظر [المقارنة التفصيلية](docs/comparison.md).

---

## خارطة الطريق

**الآن (v2.5.x)**: توافق البروتوكولات · RLS · توجيه طبقتين + جلسات لاصقة · ضغط / إعادة تشغيل / دورة حياة · إدارة Vue · Docker Compose

**التالي (3–6 أشهر)**: ترقية توجيه cost/quality من shadow إلى خيار افتراضي · Helm إنتاجي · قوالب Grafana · إعادة تشغيل القرارات

**استكشاف**: بوابة أدوات MCP (الربع الثالث 2026) · A2A · مشغّل Kubernetes

انظر [ROADMAP.md](ROADMAP.md).

---

## 📚 التوثيق

| الفئة | المستندات |
|-------|-----------|
| بدء / هندسة / API | [getting-started](docs/getting-started.md) · [architecture](docs/architecture.md) · [API](docs/03-design/01-architecture/architecture/API.md) |
| مقارنة / نظرة | [comparison](docs/comparison.md) · [PROJECT_OVERVIEW](docs/PROJECT_OVERVIEW.md) · [INDEX](docs/INDEX.md) |
| دليل الجلسة | [session-management](docs/04-implementation/deliverables/user-guide/session-management.md) |
| مستودع مزدوج / أمن / قانوني | [REPO-MIRROR-POLICY](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md) · [SECURITY.md](SECURITY.md) · [disguise-compliance](docs/02-resources/compliance/legal/disguise-compliance.md) |
| بحث | [A2A](docs/03-design/01-architecture/architecture/a2a-spec-2027.md) · [Armor/SDP](docs/03-design/01-architecture/architecture/armor-sdp-feasibility.md) |

---

## 🔀 استراتيجية المستودعين

| Remote | URL | الغرض |
|--------|-----|--------|
| `codeup` (origin) | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | التطوير اليومي |
| `github` | `git@github.com:halfking/ai-native-gateway-core.git` | مرآة عامة |

```bash
git push              # → codeup
git push github       # → github (فحص صارم بـ 49 قاعدة، يُحظر عند الإصابة)
```

انظر [سياسة المرآة](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md).

## 🤝 المساهمة

انظر [CONTRIBUTING.md](CONTRIBUTING.md). تغييرات تعدد المستأجرين يجب أن تمرّ بـ `lint-tenant-scope-llmgw` / `lint-pg-rls` / `lint-otel-tenant`.

## 🔐 الأمن

الثغرات: [SECURITY.md](SECURITY.md). فحص المرآة العامة: سياسة المستودعين. قائمة التنكّر البيضاء: [disguise-compliance](docs/02-resources/compliance/legal/disguise-compliance.md).

## الرخصة

[Apache License 2.0](LICENSE). احتفظ بإشعارات حقوق النشر و[NOTICE](NOTICE) عند إعادة التوزيع.

---

بُني بـ ❤️ من مجتمع AI Native Gateway
