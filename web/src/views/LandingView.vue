<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ServiceLandingPage from '../components/ServiceLandingPage.vue'

const { t, tm } = useI18n()

const heroPoints = computed(() => tm('landing.heroPoints') as string[])

const features = computed(() => [
  {
    icon: '🧭',
    title: t('landing.features.smartRouting.title'),
    description: t('landing.features.smartRouting.description'),
  },
  {
    icon: '🔐',
    title: t('landing.features.safety.title'),
    description: t('landing.features.safety.description'),
    badge: t('landing.features.safety.badge'),
  },
  {
    icon: '⚡',
    title: t('landing.features.cache.title'),
    description: t('landing.features.cache.description'),
  },
  {
    icon: '🤖',
    title: t('landing.features.agent.title'),
    description: t('landing.features.agent.description'),
    badge: t('landing.features.agent.badge'),
  },
  {
    icon: '📊',
    title: t('landing.features.observability.title'),
    description: t('landing.features.observability.description'),
  },
  {
    icon: '💳',
    title: t('landing.features.billing.title'),
    description: t('landing.features.billing.description'),
  },
  {
    icon: '🌐',
    title: t('landing.features.multiProtocol.title'),
    description: t('landing.features.multiProtocol.description'),
  },
  {
    icon: '🏗️',
    title: t('landing.features.multiTenant.title'),
    description: t('landing.features.multiTenant.description'),
  },
])

const advantages = computed(() => [
  { 
    icon: '📖', 
    title: t('landing.advantages.local.title'), 
    description: t('landing.advantages.local.description') 
  },
  { 
    icon: '🔒', 
    title: t('landing.advantages.private.title'), 
    description: t('landing.advantages.private.description') 
  },
  { 
    icon: '🛡️', 
    title: t('landing.advantages.antiBan.title'), 
    description: t('landing.advantages.antiBan.description') 
  },
  { 
    icon: '⚡', 
    title: t('landing.advantages.perf.title'), 
    description: t('landing.advantages.perf.description') 
  },
])
</script>

<template>
  <div class="llmgo-landing">
    <ServiceLandingPage
      :kicker="t('landing.kicker')"
      :title="t('landing.title')"
      :subtitle="t('landing.subtitle')"
      :hero-points="heroPoints"
      :features="features"
      :advantages="advantages"
      :advantages-title="t('landing.advantagesTitle')"
      :advantages-subtitle="t('landing.advantagesSubtitle')"
      :footer-text="t('landing.footer')"
      accent="#6366f1"
      :hide-cta="true"
    />

    <!-- 路线图预告区块 -->
    <section class="llmgo-roadmap">
      <div class="llmgo-roadmap__inner">
        <h2 class="llmgo-roadmap__title">{{ t('landing.roadmap.title') }}</h2>
        <p class="llmgo-roadmap__sub">{{ t('landing.roadmap.subtitle') }}</p>
        <ol class="llmgo-roadmap__list">
          <li>
            <span class="llmgo-roadmap__phase">{{ t('landing.roadmap.v31.phase') }}</span>
            <strong>{{ t('landing.roadmap.v31.title') }}</strong>
            <span>{{ t('landing.roadmap.v31.description') }}</span>
          </li>
          <li>
            <span class="llmgo-roadmap__phase">{{ t('landing.roadmap.v32.phase') }}</span>
            <strong>{{ t('landing.roadmap.v32.title') }}</strong>
            <span>{{ t('landing.roadmap.v32.description') }}</span>
          </li>
          <li>
            <span class="llmgo-roadmap__phase">{{ t('landing.roadmap.v40.phase') }}</span>
            <strong>{{ t('landing.roadmap.v40.title') }}</strong>
            <span>{{ t('landing.roadmap.v40.description') }}</span>
          </li>
          <li>
            <span class="llmgo-roadmap__phase">{{ t('landing.roadmap.v50.phase') }}</span>
            <strong>{{ t('landing.roadmap.v50.title') }}</strong>
            <span>{{ t('landing.roadmap.v50.description') }}</span>
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
