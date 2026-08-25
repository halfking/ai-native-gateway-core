<script setup lang="ts">
import { computed } from 'vue'

export interface LandingFeature {
  title: string
  description: string
  /** Short glyph / index mark (decorative). */
  icon?: string
  /** Optional badge tag, e.g. "即将上线" / "beta". */
  badge?: string
}

export interface LandingAdvantage {
  title: string
  description: string
  icon?: string
}

const props = withDefaults(
  defineProps<{
    brand?: string
    brandMark?: string
    /** When set, shows product logo instead of the decorative brand mark. */
    logoSrc?: string
    logoSize?: number
    kicker: string
    title: string
    subtitle: string
    heroPoints?: string[]
    features: LandingFeature[]
    featuresTitle?: string
    featuresSubtitle?: string
    advantages?: LandingAdvantage[]
    advantagesTitle?: string
    advantagesSubtitle?: string
    ctaLabel?: string
    secondaryCtaLabel?: string
    secondaryCtaHref?: string
    footerText?: string
    hideCta?: boolean
  }>(),
  {
    brand: 'AI Native 组织核心网关',
    brandMark: 'K',
    logoSize: 60,
    heroPoints: () => [],
    featuresTitle: '核心能力',
    featuresSubtitle: '覆盖从接入到运营的关键环节',
    advantages: () => [],
    advantagesTitle: '为什么选择 LLM Gateway',
    advantagesSubtitle: '面向有中国业务需求的全球企业',
    ctaLabel: '登录控制台',
    secondaryCtaLabel: '',
    secondaryCtaHref: '',
    footerText: 'LLM Gateway · 核心开源 · 中国本地化 · 私有部署',
    hideCta: false,
  },
)

const emit = defineEmits<{ login: [] }>()

const pipeline = computed(() =>
  props.heroPoints.slice(0, 3).map((point, index) => ({
    n: String(index + 1),
    title: point,
  })),
)
</script>

<template>
  <div class="kx-landing">
    <section class="kx-landing__hero">
      <div class="kx-landing__hero-copy">
        <div class="kx-landing__brand" aria-label="brand">
          <img
            v-if="logoSrc"
            class="kx-landing__brand-logo"
            :src="logoSrc"
            :width="logoSize"
            :height="logoSize"
            alt="开轩启圭"
          />
          <span v-else class="kx-landing__brand-mark" aria-hidden="true">{{ brandMark }}</span>
          <span class="kx-landing__brand-text">
            <strong>{{ brand }}</strong>
          </span>
        </div>
        <p class="kx-landing__kicker">{{ kicker }}</p>
        <h1 class="kx-landing__title">{{ title }}</h1>
        <p class="kx-landing__subtitle">{{ subtitle }}</p>
        <div v-if="!hideCta" class="kx-landing__actions">
          <button type="button" class="kx-landing__cta" @click="emit('login')">
            {{ ctaLabel }} <span aria-hidden="true">→</span>
          </button>
          <a
            v-if="secondaryCtaLabel && secondaryCtaHref"
            class="kx-landing__cta-secondary"
            :href="secondaryCtaHref"
          >
            {{ secondaryCtaLabel }}
          </a>
        </div>
        <slot name="hero-extra" />
      </div>

      <div class="kx-landing__hero-art" aria-hidden="true">
        <div class="kx-landing__orb kx-landing__orb--one" />
        <div class="kx-landing__orb kx-landing__orb--two" />
        <div class="kx-landing__pipeline">
          <div
            v-for="(step, index) in pipeline"
            :key="step.title"
            class="kx-landing__pipeline-step"
            :class="{ 'is-active': index === 0 }"
          >
            <span>{{ step.n }}</span>
            <div>
              <strong>{{ step.title }}</strong>
            </div>
          </div>
        </div>
      </div>
    </section>

    <section class="kx-landing__features">
      <header class="kx-landing__section-head">
        <span class="kx-landing__eyebrow">CAPABILITIES</span>
        <h2 class="kx-landing__section-title">{{ featuresTitle }}</h2>
        <p class="kx-landing__section-sub">{{ featuresSubtitle }}</p>
      </header>
      <div class="kx-landing__feature-grid">
        <article v-for="(item, index) in features" :key="item.title" class="kx-landing__card">
          <div class="kx-landing__card-mark" aria-hidden="true">
            {{ item.icon || String(index + 1).padStart(2, '0') }}
          </div>
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
        <span class="kx-landing__eyebrow">WHY US</span>
        <h2 class="kx-landing__section-title">{{ advantagesTitle }}</h2>
        <p class="kx-landing__section-sub">{{ advantagesSubtitle }}</p>
      </header>
      <ol class="kx-landing__adv-flow">
        <li v-for="(item, index) in advantages" :key="item.title">
          <span class="kx-landing__adv-index" aria-hidden="true">{{ String(index + 1).padStart(2, '0') }}</span>
          <strong>{{ item.title }}</strong>
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
/* Full-bleed guest homepage: edge-to-edge, content left-aligned (no centered column). */
.kx-landing {
  --landing-bg: var(--kx-bg);
  --landing-surface: var(--surface-elevated);
  --landing-text: var(--kx-text);
  --landing-muted: var(--kx-muted);
  --landing-border: var(--border);
  --landing-primary: var(--accent);
  --landing-primary-hover: var(--accent-dark);
  --landing-primary-soft: var(--info-bg);
  --landing-font: "Noto Sans SC", "PingFang SC", "Hiragino Sans GB", "Segoe UI", sans-serif;
  --landing-display: "Outfit", "Noto Sans SC", "PingFang SC", sans-serif;
  --landing-pad-x: clamp(28px, 5vw, 72px);
  color: var(--landing-text);
  font-family: var(--landing-font);
  background:
    radial-gradient(ellipse 70% 45% at 100% -10%, var(--info-bg), transparent 55%),
    radial-gradient(ellipse 50% 40% at 0% 100%, var(--success-bg), transparent 50%),
    var(--landing-bg);
  min-height: 100%;
  width: 100%;
  box-sizing: border-box;
}

.kx-landing__hero {
  display: grid;
  grid-template-columns: minmax(0, 1.2fr) minmax(260px, 0.8fr);
  gap: clamp(32px, 6vw, 80px);
  align-items: center;
  justify-content: stretch;
  width: 100%;
  min-height: calc(100vh - 64px);
  margin: 0;
  padding: clamp(36px, 5vw, 64px) var(--landing-pad-x) clamp(40px, 5vw, 72px);
  box-sizing: border-box;
  animation: kx-landing-enter 0.45s ease both;
}

.kx-landing__hero-copy {
  text-align: left;
  justify-self: start;
  max-width: 42rem;
  width: 100%;
}

.kx-landing__brand {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 22px;
}
.kx-landing__brand-mark {
  width: 60px;
  height: 60px;
  border-radius: 12px;
  display: grid;
  place-items: center;
  color: var(--on-primary);
  font-weight: 800;
  font-size: 18px;
  font-family: var(--landing-display);
  background: linear-gradient(145deg, var(--accent), var(--accent-darker));
  box-shadow: 0 8px 20px color-mix(in srgb, var(--accent) 18%, transparent);
}
.kx-landing__brand-logo {
  width: 60px;
  height: 60px;
  border-radius: 12px;
  object-fit: contain;
  flex-shrink: 0;
  background: var(--surface-elevated);
  box-shadow: 0 8px 20px var(--kx-shadow-md);
}
.kx-landing__brand-text {
  min-width: 0;
}
.kx-landing__brand strong,
.kx-landing__brand small {
  display: block;
  line-height: 1.1;
  text-align: left;
}
.kx-landing__brand strong {
  font-family: var(--landing-display);
  font-size: clamp(14px, 2vw, 17px);
  font-weight: 700;
  line-height: 1.35;
  letter-spacing: -0.02em;
  max-width: min(52vw, 420px);
}
.kx-landing__brand small {
  margin-top: 4px;
  color: var(--landing-muted);
  font-size: 11px;
  letter-spacing: 0.1em;
  text-transform: uppercase;
}

.kx-landing__kicker,
.kx-landing__eyebrow {
  margin: 0;
  color: var(--landing-primary);
  font-size: 11px;
  font-weight: 750;
  letter-spacing: 0.13em;
  text-transform: uppercase;
  text-align: left;
}

.kx-landing__title {
  margin: 12px 0 14px;
  font-family: var(--landing-display);
  font-size: clamp(2.2rem, 5vw, 3.6rem);
  font-weight: 700;
  line-height: 1.06;
  letter-spacing: -0.045em;
  white-space: pre-line;
  text-align: left;
}

.kx-landing__subtitle {
  margin: 0;
  max-width: 54ch;
  color: var(--landing-muted);
  font-size: 16px;
  line-height: 1.7;
  text-align: left;
}

.kx-landing__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-start;
  gap: 14px;
  margin-top: 28px;
}

.kx-landing__cta,
.kx-landing__cta-secondary {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 44px;
  padding: 0 18px;
  border-radius: 8px;
  font-size: 14px;
  font-weight: 650;
  text-decoration: none;
  transition: transform 0.16s ease, background 0.16s ease, border-color 0.16s ease;
}
.kx-landing__cta {
  border: 0;
  color: var(--on-primary);
  background: var(--landing-primary);
  box-shadow: 0 8px 18px var(--info-bg);
  cursor: pointer;
}
.kx-landing__cta:hover { background: var(--landing-primary-hover); transform: translateY(-1px); }
.kx-landing__cta-secondary {
  color: var(--landing-text);
  background: var(--on-primary);
  border: 1px solid var(--landing-border);
}
.kx-landing__cta-secondary:hover { transform: translateY(-1px); border-color: var(--info-bd); }

.kx-landing__hero-art {
  position: relative;
  min-height: 320px;
  width: 100%;
  display: grid;
  place-items: center;
  overflow: hidden;
  border-radius: 0;
  border: 0;
  background: linear-gradient(155deg, var(--info-bg) 0%, var(--info-bg) 48%, var(--success-bg) 100%);
  align-self: stretch;
}
.kx-landing__orb {
  position: absolute;
  border-radius: 999px;
  filter: blur(2px);
  animation: kx-orb-drift 8s ease-in-out infinite;
}
.kx-landing__orb--one {
  width: 240px;
  height: 240px;
  top: -50px;
  right: -20px;
  background: var(--info-bg);
}
.kx-landing__orb--two {
  width: 160px;
  height: 160px;
  bottom: -45px;
  left: 20px;
  background: color-mix(in srgb, var(--success) 14%, transparent);
  animation-delay: -3s;
}
.kx-landing__pipeline {
  position: relative;
  z-index: 1;
  width: min(88%, 360px);
  display: grid;
  gap: 10px;
}
.kx-landing__pipeline-step {
  display: grid;
  grid-template-columns: 28px 1fr;
  gap: 12px;
  align-items: center;
  padding: 12px 14px;
  border-radius: 12px;
  background: var(--surface-elevated);
  border: 1px solid var(--surface-elevated);
  box-shadow: 0 8px 24px var(--kx-shadow-sm);
}
.kx-landing__pipeline-step span {
  width: 28px;
  height: 28px;
  border-radius: 8px;
  display: grid;
  place-items: center;
  background: var(--landing-primary-soft);
  color: var(--landing-primary);
  font-size: 12px;
  font-weight: 700;
  font-family: var(--landing-display);
}
.kx-landing__pipeline-step strong {
  display: block;
  font-size: 13px;
  letter-spacing: -0.02em;
  text-align: left;
}
.kx-landing__pipeline-step.is-active {
  border-color: var(--info-bd);
  background: var(--on-primary);
}
.kx-landing__pipeline-step.is-active span {
  background: var(--landing-primary);
  color: var(--on-primary);
}

.kx-landing__features,
.kx-landing__advantages {
  padding: 12px var(--landing-pad-x) 48px;
  width: 100%;
  max-width: none;
  margin: 0;
  box-sizing: border-box;
  text-align: left;
  animation: kx-landing-enter 0.5s ease both;
  animation-delay: 0.06s;
}

.kx-landing__section-head {
  margin-bottom: 18px;
  text-align: left;
}
.kx-landing__section-title {
  margin: 8px 0 6px;
  font-family: var(--landing-display);
  font-size: clamp(1.4rem, 2.4vw, 1.75rem);
  letter-spacing: -0.03em;
  text-align: left;
}
.kx-landing__section-sub {
  margin: 0;
  color: var(--landing-muted);
  font-size: 14px;
  line-height: 1.6;
  text-align: left;
}

.kx-landing__feature-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 14px;
  width: 100%;
}

.kx-landing__card {
  padding: 20px;
  border: 1px solid var(--landing-border);
  border-radius: 12px;
  background: var(--landing-surface);
  box-shadow: 0 10px 32px var(--kx-shadow-sm);
  transition: border-color 0.15s ease, transform 0.15s ease;
  text-align: left;
}
.kx-landing__card:hover {
  border-color: var(--info-bd);
  transform: translateY(-2px);
}
.kx-landing__card-mark {
  width: 36px;
  height: 36px;
  margin-bottom: 12px;
  border-radius: 10px;
  display: grid;
  place-items: center;
  background: var(--landing-primary-soft);
  color: var(--landing-primary);
  font-size: 12px;
  font-weight: 700;
  font-family: var(--landing-display);
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
  font-weight: 650;
  letter-spacing: -0.02em;
  text-align: left;
}
.kx-landing__badge {
  flex-shrink: 0;
  padding: 3px 8px;
  border-radius: 6px;
  font-size: 11px;
  font-weight: 650;
  color: var(--warning-dark);
  background: var(--warning-bg);
}
.kx-landing__card p {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
  color: var(--landing-muted);
  text-align: left;
}

.kx-landing__adv-flow {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 12px;
  margin: 0;
  padding: 0;
  list-style: none;
  width: 100%;
}
.kx-landing__adv-flow li {
  display: grid;
  gap: 6px;
  padding: 18px;
  border: 1px solid var(--landing-border);
  border-radius: 12px;
  background: var(--on-primary);
  text-align: left;
}
.kx-landing__adv-index {
  width: fit-content;
  color: var(--landing-primary);
  font-family: var(--landing-display);
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.08em;
}
.kx-landing__adv-flow strong {
  font-size: 14px;
  letter-spacing: -0.02em;
}
.kx-landing__adv-flow span:last-child {
  font-size: 12px;
  line-height: 1.55;
  color: var(--landing-muted);
}

.kx-landing__footer {
  margin-top: auto;
  padding: 22px var(--landing-pad-x) 28px;
  border-top: 1px solid var(--landing-border);
  text-align: left;
  width: 100%;
  box-sizing: border-box;
}
.kx-landing__footer p {
  margin: 0;
  color: var(--muted);
  font-size: 12px;
  text-align: left;
}

@keyframes kx-landing-enter {
  from { opacity: 0; transform: translateY(12px); }
  to { opacity: 1; transform: translateY(0); }
}
@keyframes kx-orb-drift {
  0%, 100% { transform: translate3d(0, 0, 0); }
  50% { transform: translate3d(-8px, 10px, 0); }
}

@media (max-width: 960px) {
  .kx-landing__hero {
    grid-template-columns: 1fr;
    gap: 28px;
    min-height: auto;
    padding-top: 28px;
  }
  .kx-landing__hero-art {
    min-height: 240px;
    border-radius: 16px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .kx-landing__hero,
  .kx-landing__features,
  .kx-landing__advantages,
  .kx-landing__orb {
    animation: none !important;
  }
}
</style>
