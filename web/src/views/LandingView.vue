<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import ServiceLandingPage from '../components/ServiceLandingPage.vue'
import { SITE_LOGO_SIZE, SITE_TITLE } from '../config/brand'
import { detectTheme, logoSrc } from '../theme'

const { t, tm } = useI18n()
const router = useRouter()
const brandLogo = ref(logoSrc())
let logoObserver: MutationObserver | null = null

onMounted(() => {
  brandLogo.value = logoSrc(detectTheme())
  logoObserver = new MutationObserver(() => { brandLogo.value = logoSrc(detectTheme()) })
  logoObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
})
onUnmounted(() => logoObserver?.disconnect())

const heroPoints = computed(() => tm('landing.heroPoints') as string[])

const features = computed(() => [
  {
    icon: '01',
    title: t('landing.features.smartRouting.title'),
    description: t('landing.features.smartRouting.description'),
  },
  {
    icon: '02',
    title: t('landing.features.safety.title'),
    description: t('landing.features.safety.description'),
    badge: t('landing.features.safety.badge'),
  },
  {
    icon: '03',
    title: t('landing.features.cache.title'),
    description: t('landing.features.cache.description'),
  },
  {
    icon: '04',
    title: t('landing.features.agent.title'),
    description: t('landing.features.agent.description'),
    badge: t('landing.features.agent.badge'),
  },
  {
    icon: '05',
    title: t('landing.features.observability.title'),
    description: t('landing.features.observability.description'),
  },
  {
    icon: '06',
    title: t('landing.features.billing.title'),
    description: t('landing.features.billing.description'),
  },
  {
    icon: '07',
    title: t('landing.features.multiProtocol.title'),
    description: t('landing.features.multiProtocol.description'),
  },
  {
    icon: '08',
    title: t('landing.features.multiTenant.title'),
    description: t('landing.features.multiTenant.description'),
  },
])

const advantages = computed(() => [
  {
    title: t('landing.advantages.openSource.title'),
    description: t('landing.advantages.openSource.description'),
  },
  {
    title: t('landing.advantages.private.title'),
    description: t('landing.advantages.private.description'),
  },
  {
    title: t('landing.advantages.antiBan.title'),
    description: t('landing.advantages.antiBan.description'),
  },
  {
    title: t('landing.advantages.perf.title'),
    description: t('landing.advantages.perf.description'),
  },
])

function openLogin() {
  router.replace({ path: '/', query: { ...router.currentRoute.value.query, login: '1' } })
}
</script>

<template>
  <div class="llmgo-landing">
    <ServiceLandingPage
      :brand="SITE_TITLE"
      :logo-src="brandLogo"
      :logo-size="SITE_LOGO_SIZE"
      :kicker="t('landing.kicker')"
      :title="t('landing.title')"
      :subtitle="t('landing.subtitle')"
      :hero-points="heroPoints"
      :features="features"
      :features-title="t('landing.featuresTitle')"
      :features-subtitle="t('landing.featuresSubtitle')"
      :advantages="advantages"
      :advantages-title="t('landing.advantagesTitle')"
      :advantages-subtitle="t('landing.advantagesSubtitle')"
      :footer-text="t('landing.footer')"
      :cta-label="t('landing.ctaLogin')"
      :secondary-cta-label="t('landing.ctaDownload')"
      secondary-cta-href="/maintain/download"
      :hide-cta="false"
      @login="openLogin"
    >
      <template #hero-extra>
        <div class="llmgo-landing__extra-links">
          <a href="/maintain/activate">{{ t('landing.ctaActivate') }}</a>
          <a href="/maintain/setup">{{ t('landing.navSetup') }}</a>
          <a href="/customer/update-activate">{{ t('landing.ctaAgreement') }}</a>
          <a href="/user-agreement.html" target="_blank" rel="noopener">完整协议 ↗</a>
        </div>
      </template>
      <section class="llmgo-roadmap">
        <header class="llmgo-roadmap__head">
          <span class="llmgo-roadmap__eyebrow">ROADMAP</span>
          <h2>{{ t('landing.roadmap.title') }}</h2>
          <p>{{ t('landing.roadmap.subtitle') }}</p>
        </header>
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
      </section>
    </ServiceLandingPage>
  </div>
</template>

<style scoped>
.llmgo-landing {
  min-height: 100%;
  width: 100%;
}

.llmgo-landing__extra-links {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-start;
  gap: 14px;
  margin-top: 16px;
}

.llmgo-landing__extra-links a {
  color: #1e4fd6;
  font-size: 13px;
  font-weight: 650;
  text-decoration: none;
}

.llmgo-landing__extra-links a:hover {
  text-decoration: underline;
}

.llmgo-roadmap {
  padding: 8px clamp(28px, 5vw, 72px) 48px;
  width: 100%;
  max-width: none;
  margin: 0;
  box-sizing: border-box;
  text-align: left;
}

.llmgo-roadmap__head {
  margin-bottom: 18px;
  text-align: left;
}

.llmgo-roadmap__eyebrow {
  display: block;
  color: #1e4fd6;
  font-size: 11px;
  font-weight: 750;
  letter-spacing: 0.13em;
  text-transform: uppercase;
}

.llmgo-roadmap__head h2 {
  margin: 8px 0 6px;
  font-family: "Outfit", "Noto Sans SC", "PingFang SC", sans-serif;
  font-size: clamp(1.4rem, 2.4vw, 1.75rem);
  letter-spacing: -0.03em;
  color: #152033;
  text-align: left;
}

.llmgo-roadmap__head p {
  margin: 0;
  color: #5b6b82;
  font-size: 14px;
  line-height: 1.6;
  text-align: left;
}

.llmgo-roadmap__list {
  display: grid;
  gap: 12px;
  margin: 0;
  padding: 0;
  list-style: none;
  width: 100%;
}

.llmgo-roadmap__list li {
  display: grid;
  grid-template-columns: 88px minmax(0, 1fr);
  gap: 4px 16px;
  padding: 16px 18px;
  border: 1px solid #dce3ee;
  border-radius: 12px;
  background: rgba(255, 255, 255, 0.88);
  box-shadow: 0 10px 32px rgba(30, 45, 75, 0.04);
  text-align: left;
}

.llmgo-roadmap__phase {
  grid-row: span 2;
  align-self: start;
  width: fit-content;
  padding: 4px 10px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 700;
  color: #1e4fd6;
  background: #eaf0ff;
  white-space: nowrap;
}

.llmgo-roadmap__list strong {
  font-size: 14px;
  font-weight: 650;
  color: #152033;
  letter-spacing: -0.02em;
}

.llmgo-roadmap__list span:last-child {
  font-size: 13px;
  line-height: 1.55;
  color: #5b6b82;
}

@media (max-width: 640px) {
  .llmgo-roadmap__list li {
    grid-template-columns: 1fr;
    gap: 6px;
  }

  .llmgo-roadmap__phase {
    grid-row: auto;
  }
}
</style>
