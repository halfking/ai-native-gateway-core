<script setup lang="ts">
import ServiceLandingPage from '../components/ServiceLandingPage.vue';

const features = [
  {
    icon: '🧭',
    title: '智能路由与凭据池',
    description: '按租户、模型与任务类型自动选路；多凭据指纹池 + 自适应探测，故障秒级切换、封号率趋零。',
  },
  {
    icon: '🔐',
    title: '调用安全护盾',
    description: 'LLM-as-judge 提示词注入检测（v1 可观测模式）+ 敏感数据脱敏规划，企业级合规防线。',
    badge: 'beta',
  },
  {
    icon: '⚡',
    title: '缓存对齐与降本',
    description: 'Prompt 前缀稳定化 + 语义缓存，最大化 KV Cache 命中率，降低 Token 算力开销。',
  },
  {
    icon: '🤖',
    title: 'Agent 与 MCP 网关',
    description: 'Agent 注册中心、A2A 协议、MCP 工具托管与协议转换——从 LLM 代理升级为智能体编排入口。',
    badge: '即将上线',
  },
  {
    icon: '📊',
    title: '全链路可观测',
    description: '请求日志、路由决策审计、OTel 链路追踪、SIEM/CEF 事件导出，等保 2.0 与 GDPR 就绪。',
  },
  {
    icon: '💳',
    title: 'MaaS 计费体系',
    description: '套餐 + 积分 + 三池钱包（订阅 / 信用 / 充值），面向租户自助的完整商业化闭环。',
  },
  {
    icon: '🌐',
    title: '多协议兼容',
    description: 'OpenAI Chat / Anthropic Messages / Responses 三套入向统一归一，国产与海外模型无缝接入。',
  },
  {
    icon: '🏗️',
    title: '多租户隔离',
    description: 'PostgreSQL RLS 行级安全 + 43 轮审计 L1=0，租户间数据零泄漏，每租户独立策略与配额。',
  },
];

const advantages = [
  { icon: '🇨🇳', title: '中国本地化', description: '全中文界面、国产模型优先、支付宝/微信支付接入、等保合规模板' },
  { icon: '🔒', title: '私有化部署', description: '完全私有部署，数据不出企业，k3s + Docker 双形态，零外部依赖' },
  { icon: '🛡️', title: '抗封号体系', description: '50+ UA 轮换 + utls TLS 指纹池 + 11 浏览器 profile + 5 分钟自动轮换' },
  { icon: '⚡', title: 'Go 高性能数据面', description: '原生 Go 实现，40MB 轻量镜像，200 并发 P99 < 500ms，SSE 流式稳定中继' },
];
</script>

<template>
  <div class="llmgo-landing">
    <ServiceLandingPage
      kicker="Enterprise AI & Agent Gateway"
      title="开轩企业 AI 与智能体网关"
      subtitle="从大模型代理到智能体编排的统一入口。统一接入多家 LLM，智能路由、安全护盾、缓存降本、全链路审计——让企业的每一次 AI 调用可控、可观测、可计费。"
      :hero-points="['智能路由', '调用安全', '缓存降本', 'Agent 就绪', '全链路审计', 'MaaS 计费']"
      :features="features"
      :advantages="advantages"
      advantages-title="差异化优势"
      advantages-subtitle="海外厂商给不了的能力"
      footer-text="开轩 LLM Gateway · llmgo.kxpms.cn · 私有部署 · 中国本地化"
      accent="#6366f1"
      :hide-cta="true"
    />

    <!-- 路线图预告区块 -->
    <section class="llmgo-roadmap">
      <div class="llmgo-roadmap__inner">
        <h2 class="llmgo-roadmap__title">产品演进路线</h2>
        <p class="llmgo-roadmap__sub">从 LLM 数据面到企业 Agent 网关，持续构建</p>
        <ol class="llmgo-roadmap__list">
          <li>
            <span class="llmgo-roadmap__phase">v3.1 · 2026 Q3</span>
            <strong>API Hub 资产中心 + MCP 工具托管</strong>
            <span>统一登记 LLM 端点、MCP 服务与 Agent，开发者自助发现与复用。</span>
          </li>
          <li>
            <span class="llmgo-roadmap__phase">v3.2 · 2026 Q4</span>
            <strong>安全护盾 GA + SIEM 对接 + SpecBoost</strong>
            <span>提示词注入拦截、敏感数据脱敏、API 描述智能富集提升 Function Calling 准确率。</span>
          </li>
          <li>
            <span class="llmgo-roadmap__phase">v4.0 · 2027 Q1</span>
            <strong>Agent 注册中心 + A2A 协议网关</strong>
            <span>跨智能体任务委派与编排，OpenClaw 与业务 Agent 统一接入。</span>
          </li>
          <li>
            <span class="llmgo-roadmap__phase">v5.0 · 2027 Q3</span>
            <strong>行业方案 GA</strong>
            <span>客服、HR、销售、物流四大行业模板，开箱即用的智能体方案。</span>
          </li>
        </ol>
      </div>
    </section>
  </div>
</template>

<style scoped>
.llmgo-landing {
  min-height: 100vh;
  overflow-y: auto;
  background: var(--kx-bg-page, #0f1117);
}

.llmgo-landing :deep(.kx-landing) {
  background: var(--page-bg, #0f1117);
  color: var(--text, #e8eaed);
}

.llmgo-landing :deep(.kx-landing__card),
.llmgo-landing :deep(.kx-landing__adv-flow li) {
  background: var(--panel, #1a1d27);
  border-color: var(--border, #2a2d3a);
}

.llmgo-landing :deep(.kx-landing__points li) {
  background: var(--panel, #1a1d27);
  border-color: var(--border, #2a2d3a);
}

/* 路线图预告区块 */
.llmgo-roadmap {
  padding: 32px 16px 48px;
  max-width: 960px;
  margin: 0 auto;
  width: 100%;
}

.llmgo-roadmap__inner {
  padding: 24px;
  border: 1px solid var(--border, #2a2d3a);
  border-radius: 14px;
  background: var(--panel, #1a1d27);
}

.llmgo-roadmap__title {
  margin: 0 0 4px;
  font-size: 20px;
  font-weight: 600;
  color: #e8eaed;
}

.llmgo-roadmap__sub {
  margin: 0 0 20px;
  font-size: 14px;
  color: #6b7280;
}

.llmgo-roadmap__list {
  display: grid;
  gap: 14px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.llmgo-roadmap__list li {
  display: grid;
  grid-template-columns: 140px 1fr;
  gap: 4px 16px;
  padding: 12px 0;
  border-top: 1px solid var(--border, #2a2d3a);
}

.llmgo-roadmap__list li:first-child {
  border-top: none;
}

.llmgo-roadmap__phase {
  grid-row: span 2;
  align-self: start;
  padding: 4px 10px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 600;
  color: #a5b4fc;
  background: rgba(99, 102, 241, 0.12);
  white-space: nowrap;
}

.llmgo-roadmap__list strong {
  font-size: 14px;
  font-weight: 600;
  color: #e8eaed;
}

.llmgo-roadmap__list span {
  font-size: 13px;
  line-height: 1.55;
  color: #9aa0a6;
}

@media (max-width: 600px) {
  .llmgo-roadmap__list li {
    grid-template-columns: 1fr;
    gap: 4px;
  }

  .llmgo-roadmap__phase {
    grid-row: auto;
    display: inline-block;
    width: fit-content;
  }
}
</style>
