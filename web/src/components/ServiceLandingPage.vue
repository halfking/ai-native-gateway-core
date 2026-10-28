<script setup lang="ts">
import { computed } from 'vue'

export interface LandingFeature {
  title: string
  description: string
  /** 徽标标记，例如「即将上线」/「beta」。 */
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
    footerText: 'LLM Gateway：核心开源，中国本地化，私有部署',
    hideCta: false,
  },
)

const emit = defineEmits<{ login: [] }>()

/**
 * 2026-10-24：这里原来把 heroPoints 截前 3 条渲染成「1、2、3 编号的流程」。
 * 三处问题：① 编号本身不承载信息（craft-floor 禁装饰性序号）；
 * ② 凭空截断 6 条里的 3 条，读者会以为只有三个能力；③ 右侧整块是装饰，
 * 右边两个渐变光球 + 编号流程，没有一个字是产品事实。
 *
 * 现在改成一条「能力导轨」：全部 heroPoints 按原顺序列出，用一根细线串起来。
 * 线只是排版手段（让一列短语读起来是一组），不承载信息编号。
 */
const railPoints = computed(() => props.heroPoints)
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

      <!-- 能力导轨：替代原来的「装饰光球 + 1/2/3 编号流程」。 -->
      <aside v-if="railPoints.length" class="kx-landing__rail" aria-label="capabilities">
        <ol class="kx-landing__rail-list">
          <li v-for="point in railPoints" :key="point" class="kx-landing__rail-item">
            {{ point }}
          </li>
        </ol>
      </aside>
    </section>

    <section class="kx-landing__features">
      <header class="kx-landing__section-head">
        <h2 class="kx-landing__section-title">{{ featuresTitle }}</h2>
        <p class="kx-landing__section-sub">{{ featuresSubtitle }}</p>
      </header>
      <!-- 2026-10-24：原来这里是「图标块 + 标题 + 文字」同尺寸卡片网格
           （craft-floor 的懒默认）。现在去掉图标块与卡片壳，改成清单式排版：
           标题、说明、一条细分隔线。信息量不变，读起来像一份说明而不是模板。 -->
      <div class="kx-landing__feature-list">
        <article v-for="item in features" :key="item.title" class="kx-landing__feature">
          <div class="kx-landing__feature-head">
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
      <!-- 同上：去掉 01/02 序号与卡片壳。 -->
      <ol class="kx-landing__adv-flow">
        <li v-for="item in advantages" :key="item.title" class="kx-landing__adv-item">
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
  /* 2026-10-24：原来是 `"Noto Sans SC", "PingFang SC", ...` 与
     `"Outfit", "Noto Sans SC", ...`。这两个名字**从未被 @font-face 加载过**
     —— 引用了一个不存在的字体，运行时直接落回平台 sans。
     现在改吃全局令牌（style.css 里的自托管 IBM Plex Sans + 系统中日韩）。 */
  --landing-font: var(--kx-font-sans);
  --landing-display: var(--kx-font-sans);
  --landing-pad-x: clamp(28px, 5vw, 72px);
  color: var(--landing-text);
  font-family: var(--landing-font);
  background: var(--landing-bg);
  min-height: 100%;
  width: 100%;
  box-sizing: border-box;
}

.kx-landing__hero {
  display: grid;
  grid-template-columns: minmax(0, 1.25fr) minmax(240px, 0.75fr);
  gap: clamp(32px, 6vw, 80px);
  align-items: center;
  justify-content: stretch;
  width: 100%;
  /* 2026-09-13 P1c: dvh fallback */
  min-height: calc(100vh - 64px);
  min-height: calc(100dvh - 64px);
  margin: 0;
  padding: clamp(36px, 5vw, 64px) var(--landing-pad-x) clamp(40px, 5vw, 72px);
  box-sizing: border-box;
  animation: kx-landing-enter 0.45s var(--kx-motion-ease-out) both;
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
  margin-bottom: 24px;
}
.kx-landing__brand-mark {
  width: 60px;
  height: 60px;
  border-radius: 12px;
  display: grid;
  place-items: center;
  color: var(--on-primary);
  /* 2026-10-24：去掉 145° 渐变与彩色柔影。品牌字块是实心色块就够了，
     渐变 + 阴影是「让元素浮起来」的装饰，不是信息。字重也收到 700
     （craft-floor：只允许 400/500/600/700，750/800 是合成出来的假字重）。 */
  font-weight: 700;
  font-size: 18px;
  font-family: var(--landing-display);
  background: var(--landing-primary);
}
.kx-landing__brand-logo {
  width: 60px;
  height: 60px;
  object-fit: contain;
  flex-shrink: 0;
}
.kx-landing__brand-text {
  min-width: 0;
}
.kx-landing__brand strong {
  display: block;
  line-height: 1.35;
  text-align: left;
  font-family: var(--landing-display);
  font-size: clamp(14px, 2vw, 17px);
  font-weight: 700;
  letter-spacing: -0.02em;
  max-width: min(52vw, 420px);
}

.kx-landing__title {
  /* 上方留白多于下方：标题自己扛重量，上面不再挂 kicker / eyebrow 小标签
     （craft-floor 硬禁：「没有哪份 brief 能把它赎回来」）。 */
  margin: 0 0 12px;
  font-family: var(--landing-display);
  font-size: clamp(2.2rem, 5vw, 3.6rem);
  font-weight: 700;
  line-height: 1.08;
  letter-spacing: -0.04em;
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
  gap: 12px;
  margin-top: 24px;
}

.kx-landing__cta,
.kx-landing__cta-secondary {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 44px;
  padding: 0 16px;
  border-radius: var(--radius);
  font-size: 14px;
  font-weight: 600;
  text-decoration: none;
  transition: transform 0.16s var(--kx-motion-ease-out), background 0.16s var(--kx-motion-ease-out), border-color 0.16s var(--kx-motion-ease-out);
}
.kx-landing__cta {
  border: 0;
  color: var(--on-primary);
  background: var(--landing-primary);
  cursor: pointer;
}
.kx-landing__cta:hover { background: var(--landing-primary-hover); transform: translateY(-1px); }
/* 2026-09-29 暗色修复：背景原为 var(--on-primary)，而 --on-primary → --kx-text-on-primary
→   #ffffff 在明暗两套皮肤里都是同一个纯白（style.css L28 / L168），于是暗色下「下载安装包」
   变成一块刺眼白底 + 深色文字的大色块。改用本组件已声明、随皮肤切换的 --landing-surface
   （→ --surface-elevated：亮色 rgba(255,255,255,.88)，暗色 rgba(26,34,45,.92)）。 */
.kx-landing__cta-secondary {
  color: var(--landing-text);
  background: var(--landing-surface);
  border: 1px solid var(--landing-border);
}
.kx-landing__cta-secondary:hover { transform: translateY(-1px); border-color: var(--info-bd); }

/* ── 能力导轨 ──
   替代原来的装饰光球 + 编号流程。一根细线把短语串成一组，
   线只管排版，不管编号。 */
.kx-landing__rail {
  align-self: stretch;
  display: flex;
  align-items: center;
  min-height: 260px;
  padding-left: 24px;
  border-left: 1px solid var(--landing-border);
}
.kx-landing__rail-list {
  list-style: none;
  margin: 0;
  padding: 0;
  width: 100%;
  display: grid;
  gap: 12px;
}
.kx-landing__rail-item {
  position: relative;
  font-size: 14px;
  line-height: 1.5;
  color: var(--landing-text);
  padding-left: 16px;
}
/* 每个短语前一个小方点：形状是「这是列表项」的第二信号，
   不靠颜色区分。 */
.kx-landing__rail-item::before {
  content: '';
  position: absolute;
  left: 0;
  top: 0.55em;
  width: 5px;
  height: 5px;
  border-radius: 1px;
  background: var(--landing-primary);
  opacity: 0.55;
}

.kx-landing__features,
.kx-landing__advantages {
  padding: 12px var(--landing-pad-x) 48px;
  width: 100%;
  max-width: none;
  margin: 0;
  box-sizing: border-box;
  text-align: left;
  animation: kx-landing-enter 0.5s var(--kx-motion-ease-out) both;
  animation-delay: 0.06s;
}

.kx-landing__section-head {
  margin-bottom: 24px;
  text-align: left;
}
.kx-landing__section-title {
  margin: 0 0 4px;
  font-family: var(--landing-display);
  font-size: clamp(1.4rem, 2.4vw, 1.75rem);
  font-weight: 700;
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

/* 功能清单：两栏，靠一条细线分隔，不用卡片壳。 */
.kx-landing__feature-list {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  column-gap: 40px;
  row-gap: 0;
  width: 100%;
  border-top: 1px solid var(--landing-border);
}
.kx-landing__feature {
  padding: 24px 0 24px;
  border-bottom: 1px solid var(--landing-border);
  text-align: left;
}
.kx-landing__feature-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 4px;
}
.kx-landing__feature h3 {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  letter-spacing: -0.01em;
  text-align: left;
}
.kx-landing__badge {
  flex-shrink: 0;
  padding: 2px 8px;
  border-radius: 6px;
  font-size: 11px;
  font-weight: 600;
  color: var(--warning-dark);
  background: var(--warning-bg);
}
.kx-landing__feature p {
  margin: 0;
  font-size: 13px;
  line-height: 1.65;
  color: var(--landing-muted);
  text-align: left;
}

.kx-landing__adv-flow {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 24px 40px;
  margin: 0;
  padding: 0;
  list-style: none;
  width: 100%;
  border-top: 1px solid var(--landing-border);
}
.kx-landing__adv-item {
  display: grid;
  gap: 4px;
  padding: 24px 0 24px;
  text-align: left;
}
.kx-landing__adv-item strong {
  font-size: 14px;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.kx-landing__adv-item span {
  font-size: 13px;
  line-height: 1.65;
  color: var(--landing-muted);
}

.kx-landing__footer {
  margin-top: auto;
  padding: 24px var(--landing-pad-x) 24px;
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

@media (max-width: 1024px) {
  .kx-landing__hero {
    grid-template-columns: 1fr;
    gap: 24px;
    min-height: auto;
    padding-top: 24px;
  }
  .kx-landing__rail {
    min-height: 0;
    padding-left: 16px;
  }
}

/* 2026-09-13 P1c：补 768px 档。hero 在 <=960 已单列化（768-959 区间表现
   无问题），此档仅做间距/装饰微调，不改布局结构。 */
@media (max-width: 768px) {
  .kx-landing__hero {
    gap: 16px;
    padding-top: 16px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .kx-landing__hero,
  .kx-landing__features,
  .kx-landing__advantages {
    animation: none !important;
  }
}

/* 2026-10-27 第 26 增量：空间移动的 reduced-motion 路径（impeccable animate.md:77）。
   颜色 / 透明度反馈保留（那是「有意义的状态变化」），只把**空间移动**停下来。 */
@media (prefers-reduced-motion: reduce) {
  .kx-landing__cta {
    transition-property: background, border-color;
  }
  .kx-landing__cta-secondary:hover { transform: none; }
  .kx-landing__cta-secondary {
    transition-property: background, border-color;
  }
  .kx-landing__cta-secondary:hover { transform: none; }
}
</style>
