// landing.ts — Landing page copy (guest homepage).
//
// 2026-10-24 rewrite (impeccable craft-floor / STYLE pass).
// The previous draft read like a generated landing page: a three-item
// marketing string, adjectives standing in for facts, em dashes doing the
// job of punctuation, and every description a tidy "X + Y + Z".
// Rules live in DESIGN.md §7 "Voice" at the repo root.
//
// Fact discipline: this version claims only what the product actually does
// (Apache 2.0 / 40 MB image / 200 concurrent with P99 under 500 ms / RLS
// row-level security / 43 audit rounds with zero L1 findings / k3s and Docker
// / three inbound protocols / 15-day trial). No new capability is implied.
export default {
  // Rendered only in the PublicPageShell footer (brand name + this). It is no
  // longer used as a small label above the heading (craft-floor bans kickers).
  kicker: 'Apache 2.0 core',
  brandTitle: 'AI-Native Organization Core Gateway',
  // already says that. It says what the thing does instead.
  title: 'One gateway for every model your company calls, where you can see it, meter it, and turn it off',
  subtitle:
    'OpenAI, Anthropic and Responses protocols land in the same gateway. Routing by tenant and model, metering, audit and rate limits in one place. It runs on your own machines, so the traffic never leaves.',
  featuresTitle: 'What it does',
  featuresSubtitle: 'Intake, routing, metering and audit, all in the same entry point',
  // The six capability facts on the right-hand rail. Every one is checkable.
  heroPoints: [
    'Apache 2.0 core',
    'Go data plane, 40 MB image',
    '200 concurrent, P99 under 500 ms',
    'Three inbound protocols, one endpoint',
    'Row-level isolation with PostgreSQL RLS',
    'k3s or Docker, on your own hardware',
  ],
  features: {
    smartRouting: {
      title: 'Routing by tenant and by task',
      description:
        'Picks a route from tenant, model and task type. When an upstream starts failing, a probe notices and traffic moves to the next one; a credential fingerprint pool handles rotation.',
    },
    safety: {
      title: 'Prompt injection and sensitive data',
      description:
        'LLM-as-judge detects prompt injection (v1 observes only, it does not block) and sensitive fields get masked by rule.',
      badge: 'beta',
    },
    cache: {
      title: 'Cache and cost',
      description:
        'Prefix stabilization plus semantic caching. A higher KV Cache hit rate means fewer tokens billed.',
    },
    agent: {
      title: 'Agent and MCP ingress',
      description:
        'Agent registry, A2A protocol, MCP tool hosting and protocol translation. It moves from proxying an LLM to orchestrating agents.',
      badge: 'Coming soon',
    },
    observability: {
      title: 'Every request leaves a trail',
      description:
        'Request logs, routing decision audit, OTel traces, SIEM and CEF event export. When something breaks you can walk it back.',
    },
    billing: {
      title: 'Usage that matches the bill',
      description:
        'Plans, credits and a three-pool wallet (subscription, credit, top-up). Tenants check their own usage instead of asking an admin.',
    },
    multiProtocol: {
      title: 'One endpoint, three protocols',
      description:
        'OpenAI Chat, Anthropic Messages and Responses are normalized on the way in. The client code does not change and neither does the gateway.',
    },
    multiTenant: {
      title: 'Tenants stay separate',
      description:
        'PostgreSQL RLS row-level security, 43 audit rounds with zero L1 findings, independent policy and quota per tenant.',
    },
  },
  advantagesTitle: 'Why run it yourself',
  advantagesSubtitle: 'The data stays on your side, and the bill stays readable',
  advantages: {
    openSource: {
      title: 'Open core',
      description:
        'Data plane, control plane, auth and routing scheduler are all open source under Apache 2.0. You can change it, and you can audit it.',
    },
    private: {
      title: 'Your own hardware',
      description:
        'Installed on your machines, in k3s or Docker form, with no dependency on an external service.',
    },
    antiBan: {
      title: 'A ban is not an outage',
      description:
        'Credential rotation plus active probing: when one credential gets banned, the next one takes over.',
    },
    perf: {
      title: 'A data plane that holds up',
      description:
        'Native Go, 40 MB image, P99 still under 500 ms at 200 concurrent requests, stable SSE relay.',
    },
  },
  footer: 'Open core under Apache 2.0, running on your own machines.',
  roadmap: {
    title: 'Where this is going',
    subtitle: 'From an LLM data plane to a gateway for enterprise agents',
    v31: {
      phase: 'v3.1',
      title: 'API Hub asset center and MCP tool hosting',
      description:
        'LLM endpoints, MCP services and agents get registered in one place, so developers can find and reuse them without asking.',
    },
    v32: {
      phase: 'v3.2',
      title: 'Injection blocking, SIEM integration, SpecBoost',
      description:
        'Prompt injection detection moves from observing to blocking, sensitive data gets masked, and API descriptions are enriched automatically to improve Function Calling accuracy.',
    },
    v40: {
      phase: 'v4.0',
      title: 'Agent registry and A2A protocol gateway',
      description:
        'Agents delegate tasks to each other, and OpenClaw plus in-house agents arrive through the same door.',
    },
    v50: {
      phase: 'v5.0',
      title: 'Industry solutions, generally available',
      description:
        'Templates for support, HR, sales and logistics. Install one and it works.',
    },
  },
  // 2026-07-21: guest-nav aria + top download/activation links (App.vue L197-203).
  guestNavAria: 'Product navigation',
  navDownload: 'Download',
  navSetup: 'Install and activate',
  navActivate: 'Activate',
  navLicense: 'License',
  navAgreement: 'User Agreement',
  navSupport: 'Support',
  // 2026-07-21: landing primary CTAs (LandingView.vue L109-119).
  ctaLogin: 'Sign in',
  ctaDownload: 'Download',
  ctaActivate: 'Activate license',
  ctaAgreement: 'View agreement',
}
