// landing.ts — Landing page copy (guest homepage).
//
// 2026-07-05: Updated to match current official-deploy project content (LandingView.vue).
export default {
  kicker: 'Core Open Source · China Localization · Enterprise-Grade',
  title: 'LLM Gateway — Open Source AI Gateway for Global Markets',
  subtitle: 'The only AI gateway combining core open source with deep China localization. Enterprise governance, global LLM access, compliance and data sovereignty — all core open source.',
  featuresTitle: 'Core Capabilities',
  featuresSubtitle: 'Covering key aspects from access to operations',
  heroPoints: [
    'Core Open Source · Apache 2.0',
    'China Localization · Djbh 2.0',
    'Enterprise AI Gateway',
    'Vibe Coding Governance',
    'AI Session Asset Management',
    'Data Security Shield',
  ],
  features: {
    smartRouting: {
      title: 'Smart Routing & Credential Pool',
      description: 'Auto-routing by tenant, model and task type; multi-credential fingerprint pool + adaptive probing, failover in seconds, near-zero ban rate.',
    },
    safety: {
      title: 'Call Security Shield',
      description: 'LLM-as-judge prompt injection detection (v1 observability mode) + sensitive data masking planning, enterprise compliance defense.',
      badge: 'beta',
    },
    cache: {
      title: 'Cache Alignment & Cost Reduction',
      description: 'Prompt prefix stabilization + semantic caching, maximize KV Cache hit rate, reduce token compute overhead.',
    },
    agent: {
      title: 'Agent & MCP Gateway',
      description: 'Agent registry, A2A protocol, MCP tool hosting and protocol conversion — upgrade from LLM proxy to agent orchestration gateway.',
      badge: 'Coming Soon',
    },
    observability: {
      title: 'Full-Chain Observability',
      description: 'Request logs, routing decision audit, OTel tracing, SIEM/CEF event export, Djbh 2.0 and GDPR ready.',
    },
    billing: {
      title: 'MaaS Billing System',
      description: 'Plan + credits + three-pool wallet (subscription / credit / recharge), complete commercialization loop for tenant self-service.',
    },
    multiProtocol: {
      title: 'Multi-Protocol Compatibility',
      description: 'OpenAI Chat / Anthropic Messages / Responses three inbound protocols unified, seamless access to open source and commercial models.',
    },
    multiTenant: {
      title: 'Multi-Tenant Isolation',
      description: 'PostgreSQL RLS row-level security + 43 rounds of audit L1=0, zero data leakage between tenants, independent policy and quota per tenant.',
    },
  },
  advantagesTitle: 'Why Choose LLM Gateway',
  advantagesSubtitle: 'For global enterprises with China business needs',
  advantages: {
    local: {
      title: 'Deep China Localization',
      description: 'Full Chinese interface, domestic open source LLM priority access, Alipay/WeChat Pay integration, Djbh 2.0 compliance templates, domestic cloud infrastructure ready',
    },
    private: {
      title: 'Private Deployment',
      description: 'Fully private deployment, data stays in enterprise, k3s + Docker dual form, zero external dependencies',
    },
    antiBan: {
      title: 'Anti-Ban System',
      description: '50+ UA rotation + utls TLS fingerprint pool + 11 browser profiles + 5-minute auto-rotation',
    },
    perf: {
      title: 'Go High-Performance Data Plane',
      description: 'Native Go implementation, 40MB lightweight image, 200 concurrency P99 < 500ms, SSE streaming stable relay',
    },
  },
  footer: 'LLM Gateway · llmgateway.internal.example.com · Core Open Source · China Localization · Private Deployment',
  ariaPoints: 'Key Highlights',
  roadmap: {
    title: 'Product Roadmap',
    subtitle: 'From LLM data plane to enterprise Agent gateway, continuous build',
    v31: {
      phase: 'v3.1 · 2026 Q3',
      title: 'API Hub Asset Center + MCP Tool Hosting',
      description: 'Unified registration of LLM endpoints, MCP services and Agents, developer self-service discovery and reuse.',
    },
    v32: {
      phase: 'v3.2 · 2026 Q4',
      title: 'Security Shield GA + SIEM Integration + SpecBoost',
      description: 'Prompt injection blocking, sensitive data masking, API description intelligent enrichment to improve Function Calling accuracy.',
    },
    v40: {
      phase: 'v4.0 · 2027 Q1',
      title: 'Agent Registry + A2A Protocol Gateway',
      description: 'Cross-agent task delegation and orchestration, unified access to OpenClaw and business Agents.',
    },
    v50: {
      phase: 'v5.0 · 2027 Q3',
      title: 'Industry Solution GA',
      description: 'Four industry templates for customer service, HR, sales, logistics, out-of-the-box agent solutions.',
    },
  },
}
