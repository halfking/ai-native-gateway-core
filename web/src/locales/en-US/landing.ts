// landing.ts — Landing page copy for the public (logged-out) home view.
// Mirrors the props that LandingView passes to ServiceLandingPage plus the
// extra "Roadmap" section that lives directly in LandingView's template.
//
// Keys use camelCase nested objects so vue-i18n's `t()` interpolation and
// `t('landing.features.X.title', { ... })` substitution both work.
export default {
  kicker: 'AI-Native · Enterprise Governance',
  title: 'AI-Native Organization Core Gateway',
  subtitle: [
    'Make AI a native capability of your organization.',
    'Open core + enterprise enhancement + Vibe Coding governance + session asset-ification.',
  ],
  featuresTitle: 'Core Capabilities',
  featuresSubtitle: 'Covering the full chain from access to operations',
  heroPoints: [
    'Enterprise AI Entry',
    'Vibe Coding Governance',
    'AI Session Assets',
    'Enterprise Knowledge',
    'Data Security Shield',
    'Private Deployment',
  ],
  features: {
    smartRouting: {
      title: 'Enterprise AI Entry',
      description:
        '1045 Offers / 410 Models / 100% Coverage. Unified access to global LLMs and AI tools with smart routing and sticky binding. Every AI call is controllable, observable, billable, and governable.',
    },
    safety: {
      title: 'Vibe Coding Governance',
      description:
        'Code quality +35% · Security vulnerabilities -78% · Tech debt -60%. Full-lifecycle AI-assisted coding governance with complete quality assurance from prompts to code output.',
    },
    cache: {
      title: 'AI Session Assets',
      description: '13,000+ sessions archived · Onboarding -40%. Transform every AI conversation into searchable, reusable enterprise knowledge assets, sharing wisdom across teams.',
    },
    agent: {
      title: 'Enterprise Knowledge Deposition',
      description: 'Knowledge auto-grows · Cross-generation inheritance. Models change, Agents change, but enterprise memory persists. Build a knowledge foundation independent of specific tools.',
    },
    observability: {
      title: 'Data Security Shield',
      description: 'Injection blocking 98% · Compliance risk 0. LLM-as-judge prompt injection detection, sensitive data masking, SIEM/SOAR integration, MLPS 2.0 and GDPR ready.',
    },
    billing: {
      title: 'Multi-credential Fingerprint Pool + Anti-ban',
      description: '50+ UA · 35 lang · 11 utls · 5min rotation. Adaptive probing with sub-second failover, near-zero ban rate, ensuring service continuity.',
    },
    multiProtocol: {
      title: 'Fully Private Deployment',
      description: '184 k3s + 71 docker · 10-minute installation. Data stays in-house, zero external dependencies, open-source architecture and compliance requirements.',
    },
    multiTenant: {
      title: 'Enterprise Management + MaaS',
      description: 'Plans + credits + top-up · 60%+ cheaper than SaaS. Complete commercialization loop with multi-tenant isolation, PostgreSQL RLS row-level security L1=0.',
    },
  },
  advantagesTitle: 'Differentiated Advantages',
  advantagesSubtitle: 'What global vendors cannot offer',
  advantages: {
    local: {
      title: 'China Localization',
      description: 'Full Chinese UI, open-source model priority, Alipay / WeChat Pay, MLPS-compliant templates',
    },
    private: {
      title: 'Private Deployment',
      description: 'Fully on-prem, data never leaves the enterprise, k3s + Docker dual modes, zero external dependencies',
    },
    antiBan: {
      title: 'Anti-ban System',
      description: '50+ UA rotation + utls TLS fingerprint pool + 11 browser profiles + 5-minute auto rotation',
    },
    perf: {
      title: 'Go High-performance Dataplane',
      description: 'Native Go, 40MB lightweight image, 200 concurrent P99 < 500ms, stable SSE streaming relay',
    },
  },
  footer: 'Kaixuan LLM Gateway · [GATEWAY_DOMAIN] · Private deployment · China localization',
  ariaPoints: 'Highlight points',
  roadmap: {
    title: 'Product Evolution Roadmap',
    subtitle: 'From LLM dataplane to enterprise Agent gateway — built continuously',
    v31: {
      phase: 'Step 1',
      title: 'API Hub Asset Center + MCP Tool Hosting',
      description:
        'Unified registration of LLM endpoints, MCP services, and Agents. Developer self-service discovery and reuse. Credential SLA guarantee + semantic cache API.',
    },
    v32: {
      phase: 'Step 2',
      title: 'Model Armor Security Shield + SIEM Integration',
      description:
        'Prompt injection blocking GA, sensitive data masking (SDP), SIEM/SOAR integration. SpecBoost smart enrichment to improve Function Calling accuracy.',
    },
    v40: {
      phase: 'Step 3',
      title: 'Agent Registry + A2A Protocol Gateway',
      description:
        'Cross-agent task delegation and orchestration, OpenClaw and business Agents unified entry. Auto asset discovery + compliance GA + SpecBoost GA.',
    },
    v50: {
      phase: 'Step 4',
      title: 'Industry Solutions GA + External Customers',
      description:
        'Customer service, HR, sales, logistics industry templates — out-of-the-box agent solutions. 2 external customers + documentation system + billing online.',
    },
  },
}
