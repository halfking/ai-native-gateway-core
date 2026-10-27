<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{
  title: string
  subtitle?: string
  maxWidth?: string
}>()

const { t } = useI18n()
</script>

<template>
  <div class="pub-page" :style="maxWidth ? { '--pub-max': maxWidth } : undefined">
    <header v-if="title" class="pub-page__hero">
      <!-- 2026-10-24：这里原来有一个 `kicker` 小标签挂在标题上方
           （ForbiddenView 传 "403"、MaintainUnavailableView 传 "503"）。
           craft-floor 硬禁「标题上方的 kicker/eyebrow」——没有 brief 能赎回来。
           状态码本身是真信息，所以把它折进标题里，由标题自己扛。 -->
      <h1 class="pub-page__title">{{ title }}</h1>
      <p v-if="subtitle" class="pub-page__sub">{{ subtitle }}</p>
      <slot name="hero-extra" />
    </header>
    <div class="pub-page__body">
      <slot />
    </div>
    <footer class="pub-page__foot">
      <span>{{ t('landing.brandTitle') }} · {{ t('landing.kicker') }}</span>
      <div class="pub-page__foot-links">
        <router-link to="/download">{{ t('public.layout.download') }}</router-link>
        <router-link to="/activate">{{ t('public.download.activateLink') }}</router-link>
        <router-link to="/support">{{ t('public.layout.support') }}</router-link>
      </div>
    </footer>
  </div>
</template>

<style scoped>
.pub-page {
  --pub-max: 960px;
  --pub-accent: var(--accent);
  --pub-bg: var(--bg);
  --pub-panel: var(--card);
  --pub-border: var(--border);
  --pub-text: var(--text);
  --pub-muted: var(--muted);
  /* 2026-09-13 P1c: dvh fallback（移动端地址栏收起时不留缝） */
  min-height: calc(100vh - 69px);
  min-height: calc(100dvh - 69px);
  padding: 16px 16px 32px;
  background:
    radial-gradient(ellipse 80% 50% at 50% -10%, color-mix(in srgb, var(--accent) 14%, transparent), transparent),
    var(--pub-bg);
  color: var(--pub-text);
}

.pub-page__hero {
  max-width: var(--pub-max);
  margin: 0 auto 24px;
  text-align: center;
}

.pub-page__title {
  margin: 0 0 8px;
  font-size: clamp(1.5rem, 3vw, 2rem);
  font-weight: 700;
  line-height: 1.2;
}

.pub-page__sub {
  margin: 0 auto;
  max-width: 52ch;
  font-size: 14px;
  line-height: 1.6;
  color: var(--pub-muted);
}

.pub-page__body {
  max-width: var(--pub-max);
  margin: 0 auto;
}

.pub-page__foot {
  max-width: var(--pub-max);
  margin: 32px auto 0;
  padding-top: 16px;
  border-top: 1px solid var(--pub-border);
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  font-size: 13px;
  color: var(--pub-muted);
}

.pub-page__foot-links {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}

.pub-page__foot-links a {
  color: var(--pub-accent);
  text-decoration: none;
  font-weight: 500;
}

.pub-page__foot-links a:hover {
  text-decoration: underline;
}

:deep(.pub-card) {
  margin-bottom: 16px;
  border: 1px solid var(--pub-border);
  border-radius: 12px;
  background: var(--pub-panel);
}

:deep(.pub-card .el-card__body) {
  color: var(--pub-text);
}

:deep(.pub-steps) {
  margin: 0 0 16px;
  padding-left: 16px;
  color: var(--pub-muted);
  font-size: 14px;
  line-height: 1.65;
}

:deep(.pub-actions) {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  align-items: center;
  margin-top: 16px;
}

:deep(.pub-code) {
  display: block;
  margin-top: 8px;
  padding: 8px 12px;
  border-radius: 8px;
  font-size: 12px;
  font-family: ui-monospace, monospace;
  color: var(--border);
  background: var(--overlay-strong);
  word-break: break-all;
}

:deep(.pub-link-row) {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
  align-items: center;
  margin-top: 12px;
}

:deep(.pub-git-box) {
  margin: 16px 0;
  padding: 12px 16px;
  border: 1px solid color-mix(in srgb, var(--accent) 35%, transparent);
  border-radius: 10px;
  background: color-mix(in srgb, var(--accent) 8%, transparent);
  text-align: left;
}

:deep(.pub-git-box a) {
  color: var(--accent-h);
  word-break: break-all;
}

:deep(.el-descriptions) {
  --el-descriptions-table-border: var(--pub-border);
}

:deep(.el-descriptions__label),
:deep(.el-descriptions__content) {
  background: var(--pub-panel) !important;
  color: var(--pub-text) !important;
}

/* 2026-09-13 P1c：--pub-max 960px 在 <768 降为全宽 + 12px 内边距（只做
   增量 media，不改桌面态） */
@media (max-width: 768px) {
  .pub-page {
    --pub-max: 100%;
    padding: 16px 12px 24px;
  }
}
</style>
