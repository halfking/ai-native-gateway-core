<script setup lang="ts">
import { computed } from 'vue';

export interface LandingFeature {
  title: string;
  description: string;
  /** Emoji or short glyph shown as a leading icon (optional, purely decorative). */
  icon?: string;
  /** Optional badge tag, e.g. "即将上线" / "beta". Renders as a small pill top-right. */
  badge?: string;
}

export interface LandingAdvantage {
  title: string;
  description: string;
  /** Optional emoji glyph prefix. */
  icon?: string;
}

const props = withDefaults(
  defineProps<{
    kicker: string;
    title: string;
    subtitle: string;
    heroPoints?: string[];
    features: LandingFeature[];
    advantages?: LandingAdvantage[];
    advantagesTitle?: string;
    advantagesSubtitle?: string;
    ctaLabel?: string;
    footerText?: string;
    accent?: string;
    hideCta?: boolean;
  }>(),
  {
    heroPoints: () => [],
    advantages: () => [],
    advantagesTitle: '竞争优势',
    advantagesSubtitle: '差异化能力，面向企业生产场景',
    ctaLabel: '登录开始使用',
    footerText: '开轩启圭 · 企业智能工作台',
    accent: '',
    hideCta: true,
  },
);

const emit = defineEmits<{ login: [] }>();

const accentStyle = computed(() =>
  props.accent ? { '--landing-accent': props.accent } : {},
);
</script>

<template>
  <div class="kx-landing" :style="accentStyle">
    <section class="kx-landing__hero">
      <p class="kx-landing__kicker">{{ kicker }}</p>
      <h1 class="kx-landing__title">{{ title }}</h1>
      <p class="kx-landing__subtitle">{{ subtitle }}</p>
      <ul v-if="heroPoints.length" class="kx-landing__points" aria-label="核心亮点">
        <li v-for="point in heroPoints" :key="point">{{ point }}</li>
      </ul>
      <button v-if="!hideCta" type="button" class="kx-landing__cta" @click="emit('login')">
        {{ ctaLabel }}
      </button>
      <slot name="hero-extra" />
    </section>

    <section class="kx-landing__features">
      <header class="kx-landing__section-head">
        <h2 class="kx-landing__section-title">核心能力</h2>
        <p class="kx-landing__section-sub">覆盖从接入到运营的关键环节</p>
      </header>
      <div class="kx-landing__feature-grid">
        <article v-for="item in features" :key="item.title" class="kx-landing__card">
          <span v-if="item.icon" class="kx-landing__card-icon" aria-hidden="true">{{ item.icon }}</span>
          <div class="kx-landing__card-head">
            <h3>{{ item.title }}</h3>
            <span v-if="item.badge" class="kx-landing__badge">{{ item.badge }}</span>
          </div>
          <p>{{ item.description }}</p>
        </article>
      </div>
    </section>

    <section v-if="advantages.length" class="kx-landing__advantages">
      <header class="kx-landing__section-head">
        <h2 class="kx-landing__section-title">{{ advantagesTitle }}</h2>
        <p class="kx-landing__section-sub">{{ advantagesSubtitle }}</p>
      </header>
      <ol class="kx-landing__adv-flow">
        <li v-for="item in advantages" :key="item.title">
          <strong><span v-if="item.icon" class="kx-landing__adv-icon" aria-hidden="true">{{ item.icon }}</span>{{ item.title }}</strong>
          <span>{{ item.description }}</span>
        </li>
      </ol>
    </section>

    <slot />

    <footer class="kx-landing__footer">
      <p>{{ footerText }}</p>
    </footer>
  </div>
</template>

<style scoped>
.kx-landing {
  --landing-accent: #6366f1;
  --kx-bg-page: #0f1117;
  --kx-bg-card: #1a1d27;
  --kx-text-primary: #e8eaed;
  --kx-text-secondary: #9aa0a6;
  --kx-text-muted: #6b7280;
  --kx-text-inverse: #fff;
  --kx-border: #2d3139;
  display: flex;
  flex-direction: column;
  min-height: 100vh;
}

.kx-landing__hero {
  padding: 32px 16px 24px;
  max-width: 880px;
  margin: 0 auto;
  width: 100%;
}

.kx-landing__kicker {
  margin: 0 0 8px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--landing-accent);
}

.kx-landing__title {
  margin: 0 0 12px;
  font-size: clamp(26px, 4vw, 38px);
  font-weight: 700;
  line-height: 1.18;
  letter-spacing: -0.03em;
}

.kx-landing__subtitle {
  margin: 0 0 16px;
  max-width: 56ch;
  font-size: 15px;
  line-height: 1.65;
  color: var(--kx-text-secondary);
}

.kx-landing__points {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 0 0 20px;
  padding: 0;
  list-style: none;
}

.kx-landing__points li {
  padding: 4px 12px;
  border: 1px solid var(--kx-border);
  border-radius: 999px;
  font-size: 13px;
  color: var(--kx-text-muted);
  background: var(--kx-bg-card);
}

.kx-landing__cta {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 42px;
  padding: 0 20px;
  border: none;
  border-radius: 10px;
  background: var(--landing-accent);
  color: var(--kx-text-inverse);
  font-size: 15px;
  font-weight: 600;
  cursor: pointer;
}

.kx-landing__features,
.kx-landing__advantages {
  padding: 24px 16px;
  max-width: 960px;
  margin: 0 auto;
  width: 100%;
}

.kx-landing__section-head { margin-bottom: 16px; }

.kx-landing__section-title {
  margin: 0 0 4px;
  font-size: 20px;
  font-weight: 600;
}

.kx-landing__section-sub {
  margin: 0;
  font-size: 14px;
  color: var(--kx-text-muted);
}

.kx-landing__feature-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 12px;
}

.kx-landing__card {
  position: relative;
  padding: 18px 16px 16px;
  border: 1px solid var(--kx-border);
  border-radius: 12px;
  background: var(--kx-bg-card);
  overflow: hidden;
  transition: transform 0.18s ease, border-color 0.18s ease, box-shadow 0.18s ease;
}

.kx-landing__card::before {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 2px;
  background: linear-gradient(90deg, var(--landing-accent), transparent);
  opacity: 0.7;
}

.kx-landing__card:hover {
  transform: translateY(-3px);
  border-color: var(--landing-accent);
  box-shadow: 0 8px 24px -8px rgba(0, 0, 0, 0.5);
}

.kx-landing__card-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  margin: 0 0 10px;
  border-radius: 8px;
  font-size: 17px;
  line-height: 1;
  background: color-mix(in srgb, var(--landing-accent) 14%, transparent);
}

.kx-landing__card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin: 0 0 8px;
}

.kx-landing__card h3 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}

.kx-landing__badge {
  flex-shrink: 0;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  line-height: 1.4;
  color: var(--landing-accent);
  background: color-mix(in srgb, var(--landing-accent) 16%, transparent);
  border: 1px solid color-mix(in srgb, var(--landing-accent) 30%, transparent);
}

.kx-landing__card p {
  margin: 0;
  font-size: 13px;
  line-height: 1.55;
  color: var(--kx-text-muted);
}

.kx-landing__adv-flow {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.kx-landing__adv-flow li {
  display: grid;
  gap: 4px;
  padding: 14px;
  border: 1px solid var(--kx-border);
  border-radius: 10px;
  background: var(--kx-bg-card);
  transition: border-color 0.18s ease, transform 0.18s ease;
}

.kx-landing__adv-flow li:hover {
  border-color: color-mix(in srgb, var(--landing-accent) 50%, var(--kx-border));
  transform: translateY(-2px);
}

.kx-landing__adv-flow strong {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  color: var(--landing-accent);
}

.kx-landing__adv-icon {
  font-size: 15px;
  line-height: 1;
}

.kx-landing__adv-flow span {
  font-size: 12px;
  line-height: 1.45;
  color: var(--kx-text-muted);
}

.kx-landing__footer {
  margin-top: auto;
  padding: 16px;
  border-top: 1px solid var(--kx-border);
  text-align: center;
}

.kx-landing__footer p {
  margin: 0;
  font-size: 12px;
  color: var(--kx-text-muted);
}
</style>
