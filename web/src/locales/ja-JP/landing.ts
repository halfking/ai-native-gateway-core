// landing.ts — Landing page copy (guest homepage).
//
// 2026-07-05: Updated to neutral, global positioning.
export default {
  kicker: 'Core Open Source · Enterprise-Grade · Private Deployment',
  title: 'LLM Gateway — Enterprise Open Source AI Gateway',
  subtitle: 'Core open source enterprise AI gateway. Unified governance, global LLM access, compliance and data sovereignty — all core open source.',
  featuresTitle: 'Core Capabilities',
  featuresSubtitle: 'Covering key aspects from access to operations',
  heroPoints: [
    'Core Open Source · Apache 2.0',
    'Enterprise Governance',
    'Global LLM Access',
    'Data Security Shield',
    'AI Session Asset Management',
    'Private Deployment',
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
      description: 'Request logs, routing decision audit, OTel tracing, SIEM/CEF event export, enterprise compliance ready.',
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
  advantagesSubtitle: 'Enterprise AI gateway for global enterprises',
  advantages: {
    openSource: {
      title: 'Core Open Source',
      description: 'Data plane, control plane, authentication, routing scheduler all open source. Zero black box, enterprise auditable, customizable, and evolvable',
    },
    private: {
      title: 'Private Deployment',
      description: 'Fully private deployment, data stays in enterprise, k3s + Docker dual form, zero external dependencies',
    },
    antiBan: {
      title: 'High Availability System',
      description: 'Multi-credential rotation + intelligent probing + automatic failover, ensuring service continuity',
    },
    perf: {
      title: 'Go High-Performance Data Plane',
      description: 'Native Go implementation, 40MB lightweight image, 200 concurrency P99 < 500ms, SSE streaming stable relay',
    },
  },
  footer: 'LLM Gateway · Core Open Source · Enterprise Deployment · Data Sovereignty',
  ariaPoints: 'Key Highlights',
  roadmap: {
    title: 'Product Roadmap',
    subtitle: 'From LLM data plane to enterprise Agent gateway, continuous build',
    v31: {
      phase: 'v3.1',
      title: 'API Hub Asset Center + MCP Tool Hosting',
      description: 'Unified registration of LLM endpoints, MCP services and Agents, developer self-service discovery and reuse.',
    },
    v32: {
      phase: 'v3.2',
      title: 'Security Shield GA + SIEM Integration + SpecBoost',
      description: 'Prompt injection blocking, sensitive data masking, API description intelligent enrichment to improve Function Calling accuracy.',
    },
    v40: {
      phase: 'v4.0',
      title: 'Agent Registry + A2A Protocol Gateway',
      description: 'Cross-agent task delegation and orchestration, unified access to OpenClaw and business Agents.',
    },
    v50: {
      phase: 'v5.0',
      title: 'Industry Solution GA',
      description: 'Four industry templates for customer service, HR, sales, logistics, out-of-the-box agent solutions.',
    },
  },

  brandTitle: 'AI-Native 组织核心网关',

  brandSubtitle: '开轩启圭 · 私有化部署',

  activateBanner: {
    title: '网关尚未激活',
    desc: '检测到本实例未绑定有效 License。点击右侧按钮打开激活向导，可在线激活或走离线流程。',
    action: '立即激活',
  },

  downloadCta: {
    title: '私有化部署 · 5 分钟上手',
    subtitle: '多平台离线包，无需注册即可下载；安装后免费试用 15 天',
    download: '立即下载',
    support: '支持开源',
    activate: '已有安装包？去激活',
    note: '捐赠完全自愿，不影响下载与功能使用',
  },

  deployFlow: {
    title: '下载 · 安装 · 激活 · 登录',
    subtitle: '与 License 激活向导打通的私有化部署全流程',
    steps: {
      download: {
        title: '下载离线安装包',
        description: '按平台选择 tar.gz / 安装器，无需注册即可获取限时下载链接。',
        action: '前往下载页',
      },
      install: {
        title: '安装到目标环境',
        description: '解压后在服务器执行安装器，完成 k3s / Docker 部署与基础配置。',
        hint: 'llm-gw-installer install --target /opt/kx-gateway',
      },
      activate: {
        title: '激活 License',
        description: '在线输入 License Key 或走离线 activation.req → activation.resp 流程，支持 15 天试用。',
        action: '打开激活向导',
        offlineAction: '离线激活门户',
      },
      login: {
        title: '登录控制面',
        description: '激活完成后使用管理员账号登录，进入租户治理、路由与可观测控制台。',
        action: '登录控制面',
      },
    },
    notes: [
      '下载与捐赠完全解耦，跳过捐赠不影响任何功能',
      '离线环境请使用「离线激活门户」提交 activation.req',
      '激活向导路径：/activate · 与安装器 CLI 命令互通',
    ],
  },

  guestNavAria: '产品导航',

  navDownload: '下载',

  navSetup: '安装激活',

  navActivate: '在线激活',

  navLicense: '许可状态',

  navAgreement: '用户协议',

  navSupport: '技术支持',

  ctaLogin: '登录控制面',

  ctaDownload: '下载安装包',

  ctaActivate: '激活 License',

  ctaAgreement: '查看用户协议',
}
