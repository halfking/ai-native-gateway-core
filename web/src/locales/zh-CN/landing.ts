// landing.ts — 落地页文案（未登录首页）。
//
// 2026-10-24 改写（impeccable craft-floor / STYLE 轮）：
// 原稿是典型的 AI 落地页腔 —— 三段式营销串（`内核开源 · 企业级 · 私有部署`）、
// 每段都恰好三件事、形容词（高可用体系 / 智能 / 护盾）替代表情、
// 破折号代替标点、每条描述都是「XX + YY + ZZ」的工整排比。
// 改写原则见仓库根目录 DESIGN.md §7「Voice」。
//
// 事实纪律：这一版只保留**产品确实做到**的事（Apache 2.0 / 40 MB 镜像 /
// 200 并发 P99 < 500 ms / RLS 行级安全 / 43 轮审计 L1=0 / k3s + Docker /
// 三套入向协议 / 15 天试用）。没有加任何新的能力承诺。
export default {
  // 只在 PublicPageShell 页脚用到（品牌名 + 本项），已不再作为标题上方的
  // 小标签渲染（craft-floor 硬禁 kicker/eyebrow）。
  kicker: 'Apache 2.0 开源内核',
  brandTitle: 'AI-Native 组织核心网关',
  title: '把公司里所有大模型调用，收进一个能管、能查、能关的入口',
  subtitle:
    'OpenAI、Anthropic、Responses 三套协议接进同一个网关，按租户和模型选路，记账、审计、限流都在这一处。部署在你自己的机器上，数据不出门。',
  featuresTitle: '核心能力',
  featuresSubtitle: '接入、选路、记账、审计，都在这一个入口里',
  // 能力导轨（右栏）的六条：每条都能被验证，不写形容词。
  heroPoints: [
    '内核 Apache 2.0 开源',
    '数据面 Go，镜像 40 MB',
    '并发 200，P99 低于 500 ms',
    '三套入向协议统一',
    'PostgreSQL RLS 行级隔离',
    'k3s 或 Docker 私有部署',
  ],
  features: {
    smartRouting: {
      title: '按租户和任务选路',
      description: '按租户、模型、任务类型自动选路。上游出问题，探测到就换下一个，凭据指纹池负责轮换。',
    },
    safety: {
      title: '提示注入与敏感数据',
      description: 'LLM-as-judge 检测提示词注入（v1 只观测、不拦截），敏感字段按规则脱敏。',
      badge: 'beta',
    },
    cache: {
      title: '缓存与成本',
      description: '前缀稳定化加语义缓存，KV Cache 命中率高起来，token 的开销就下来了。',
    },
    agent: {
      title: 'Agent 与 MCP 接入',
      description: 'Agent 注册中心、A2A 协议、MCP 工具托管与协议转换，从代理 LLM 变成编排 Agent。',
      badge: '即将上线',
    },
    observability: {
      title: '每条请求都留痕',
      description: '请求日志、路由决策审计、OTel 链路追踪、SIEM/CEF 事件导出。出事能查到来龙去脉。',
    },
    billing: {
      title: '用量和钱对得上',
      description: '套餐、积分、三池钱包（订阅 / 信用 / 充值）。租户自己就能查用量，不用问管理员。',
    },
    multiProtocol: {
      title: '三套协议一个入口',
      description: 'OpenAI Chat、Anthropic Messages、Responses 入向统一。客户端照旧写，网关不用改。',
    },
    multiTenant: {
      title: '租户之间不串',
      description: 'PostgreSQL RLS 行级安全，43 轮审计 L1=0，每个租户独立策略和配额。',
    },
  },
  advantagesTitle: '为什么自己部署',
  advantagesSubtitle: '数据要留在自己家里，账也要自己看得懂',
  advantages: {
    openSource: {
      title: '内核开源',
      description: '数据面、控制面、鉴权、路由调度全部开源，Apache 2.0。改得动，也审得清。',
    },
    private: {
      title: '私有部署',
      description: '装在你自己的机器上，k3s 和 Docker 两种形态，不依赖任何外部服务。',
    },
    antiBan: {
      title: '上游封了不慌',
      description: '多凭据轮换加主动探测：某个凭据被封，自动换下一个继续跑。',
    },
    perf: {
      title: '扛得住的 Go 数据面',
      description: '原生 Go，镜像 40 MB，并发 200 时 P99 仍低于 500 ms，SSE 流式转发稳定。',
    },
  },
  footer: '内核 Apache 2.0 开源，部署在你自己的机器上。',
  roadmap: {
    title: '产品演进路线',
    subtitle: '从 LLM 数据面走到企业 Agent 网关',
    v31: {
      phase: 'v3.1',
      title: 'API Hub 资产中心 + MCP 工具托管',
      description: 'LLM 端点、MCP 服务与 Agent 统一登记，开发者自助发现与复用。',
    },
    v32: {
      phase: 'v3.2',
      title: '注入拦截正式版 + SIEM 对接 + SpecBoost',
      description: '提示词注入从只观测升级到可拦截，敏感数据脱敏，API 描述自动富集以提高 Function Calling 准确率。',
    },
    v40: {
      phase: 'v4.0',
      title: 'Agent 注册中心 + A2A 协议网关',
      description: '智能体之间互相委派任务，OpenClaw 与业务 Agent 走同一个入口接进来。',
    },
    v50: {
      phase: 'v5.0',
      title: '行业方案正式版',
      description: '客服、HR、销售、物流四套模板，装上就能用。',
    },
  },
  // 2026-07-21: guest-nav aria + 顶部 7 个下载/激活链接（App.vue L197-203）。
  guestNavAria: '产品导航',
  navDownload: '下载',
  navSetup: '安装激活',
  navActivate: '在线激活',
  navLicense: '许可状态',
  navAgreement: '用户协议',
  navSupport: '技术支持',
  // 2026-07-21: 落地页主 CTA（LandingView.vue L109-119）。
  ctaLogin: '登录控制面',
  ctaDownload: '下载安装包',
  ctaActivate: '激活 License',
  ctaAgreement: '查看用户协议',
}
