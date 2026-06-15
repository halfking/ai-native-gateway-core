<script setup lang="ts">
import ServiceLandingPage from '../components/ServiceLandingPage.vue';

const features = [
  { title: '多租户路由', description: '按租户、模型与凭证智能选路，支持 override 与免费资源池。' },
  { title: 'MaaS 账户', description: '套餐、充值、用量与账单，面向租户自助管理。' },
  { title: '凭证指纹池', description: '多机出站 IP 与 TLS 指纹，降低供应商封号风险。' },
  { title: '会话上下文', description: '跨请求上下文关联，支持任务级追踪与审计。' },
  { title: '提供商治理', description: '多 Provider 接入、定价管理与路由全景可视化。' },
  { title: 'OpenAI 兼容', description: '标准 Chat Completions API，无缝替换上游。' },
];

const advantages = [
  { title: 'Go 生产数据面', description: '71/184/252 多节点统一部署，无 Python 遗留' },
  { title: '抗封号', description: 'UA 漂移、utls 指纹与并发槽位自动调优' },
  { title: '平台 SSO', description: 'Casdoor JWT 登录管理面，API Key 兼容运维' },
  { title: '可观测', description: '请求日志、审计与路由决策全链路可查' },
];
</script>

<template>
  <div class="llmgo-landing">
    <ServiceLandingPage
      kicker="LLM Gateway"
      title="开轩 MaaS 与大模型路由网关"
      subtitle="统一接入多家 LLM 提供商，按租户与策略智能路由。登录后管理密钥、用量、定价与路由策略。"
      :hero-points="['多租户', '智能路由', 'MaaS 计费', 'OpenAI 兼容']"
      :features="features"
      :advantages="advantages"
      advantages-subtitle="生产级 LLM 基础设施"
      footer-text="开轩 LLM Gateway · llmgo.kxpms.cn"
      accent="#6366f1"
      :hide-cta="true"
    />
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
</style>
