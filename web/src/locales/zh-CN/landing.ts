// landing.ts — 落地页文案（未登录首页）。对应 LandingView 传给 ServiceLandingPage 的 props，
// 以及 LandingView 模板内自带的"路线图"区块。
//
// 2026-07-05: 更新为当前 official-deploy 项目的实际内容（匹配 LandingView.vue）。
export default {
  kicker: '核心开源 · 中国本地化 · 企业级',
  title: 'LLM Gateway — 面向全球市场的开源 AI 网关',
  subtitle: '唯一兼具核心开源与深度中国本地化的 AI 网关。企业级治理、全球 LLM 接入、合规与数据主权 — 全部核心开源。',
  featuresTitle: '核心能力',
  featuresSubtitle: '覆盖从接入到运营的关键环节',
  heroPoints: [
    '核心开源 · Apache 2.0',
    '中国本地化 · 等保 2.0',
    '企业级 AI 入口',
    'Vibe Coding 治理',
    'AI 会话资产化',
    '数据安全护盾',
  ],
  features: {
    smartRouting: {
      title: '智能路由与凭据池',
      description: '按租户、模型与任务类型自动选路；多凭据指纹池 + 自适应探测，故障秒级切换、封号率趋零。',
    },
    safety: {
      title: '调用安全护盾',
      description: 'LLM-as-judge 提示词注入检测（v1 可观测模式）+ 敏感数据脱敏规划，企业级合规防线。',
      badge: 'beta',
    },
    cache: {
      title: '缓存对齐与降本',
      description: 'Prompt 前缀稳定化 + 语义缓存，最大化 KV Cache 命中率，降低 Token 算力开销。',
    },
    agent: {
      title: 'Agent 与 MCP 网关',
      description: 'Agent 注册中心、A2A 协议、MCP 工具托管与协议转换——从 LLM 代理升级为智能体编排入口。',
      badge: '即将上线',
    },
    observability: {
      title: '全链路可观测',
      description: '请求日志、路由决策审计、OTel 链路追踪、SIEM/CEF 事件导出，等保 2.0 与 GDPR 就绪。',
    },
    billing: {
      title: 'MaaS 计费体系',
      description: '套餐 + 积分 + 三池钱包（订阅 / 信用 / 充值），面向租户自助的完整商业化闭环。',
    },
    multiProtocol: {
      title: '多协议兼容',
      description: 'OpenAI Chat / Anthropic Messages / Responses 三套入向统一归一，开源与商业模型无缝接入。',
    },
    multiTenant: {
      title: '多租户隔离',
      description: 'PostgreSQL RLS 行级安全 + 43 轮审计 L1=0，租户间数据零泄漏，每租户独立策略与配额。',
    },
  },
  advantagesTitle: '为什么选择 LLM Gateway',
  advantagesSubtitle: '面向有中国业务需求的全球企业',
  advantages: {
    local: {
      title: '深度中国本地化',
      description: '全中文界面、国产开源 LLM 优先对接、支付宝/微信支付接入、等保 2.0 合规模板、国内云基础设施就绪',
    },
    private: {
      title: '私有化部署',
      description: '完全私有部署，数据不出企业，k3s + Docker 双形态，零外部依赖',
    },
    antiBan: {
      title: '抗封号体系',
      description: '50+ UA 轮换 + utls TLS 指纹池 + 11 浏览器 profile + 5 分钟自动轮换',
    },
    perf: {
      title: 'Go 高性能数据面',
      description: '原生 Go 实现，40MB 轻量镜像，200 并发 P99 < 500ms，SSE 流式稳定中继',
    },
  },
  footer: 'LLM Gateway · llmgateway.internal.example.com · 核心开源 · 中国本地化 · 私有部署',
  ariaPoints: '核心亮点',
  roadmap: {
    title: '产品演进路线',
    subtitle: '从 LLM 数据面到企业 Agent 网关，持续构建',
    v31: {
      phase: 'v3.1 · 2026 Q3',
      title: 'API Hub 资产中心 + MCP 工具托管',
      description: '统一登记 LLM 端点、MCP 服务与 Agent，开发者自助发现与复用。',
    },
    v32: {
      phase: 'v3.2 · 2026 Q4',
      title: '安全护盾 GA + SIEM 对接 + SpecBoost',
      description: '提示词注入拦截、敏感数据脱敏、API 描述智能富集提升 Function Calling 准确率。',
    },
    v40: {
      phase: 'v4.0 · 2027 Q1',
      title: 'Agent 注册中心 + A2A 协议网关',
      description: '跨智能体任务委派与编排，OpenClaw 与业务 Agent 统一接入。',
    },
    v50: {
      phase: 'v5.0 · 2027 Q3',
      title: '行业方案 GA',
      description: '客服、HR、销售、物流四大行业模板，开箱即用的智能体方案。',
    },
  },
}
