<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{
  title: string
  subtitle?: string
  kicker?: string
  maxWidth?: string
}>()

const { t } = useI18n()
</script>

<template>
  <div class="pub-page" :style="maxWidth ? { '--pub-max': maxWidth } : undefined">
    <header v-if="title" class="pub-page__hero">
      <p v-if="kicker" class="pub-page__kicker">{{ kicker }}</p>
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
        <a href="/user-agreement.html" target="_blank" rel="noopener">{{ t('public.layout.userNotice') }}</a>
      </div>
    </footer>
  </div>
</template>

<style scoped>
.pub-page {
  --pub-max: 960px;
  --pub-accent: #6366f1;
  --pub-bg: #0f1117;
  --pub-panel: #1a1d27;
  --pub-border: #2a2d3a;
  --pub-text: #e8eaed;
  --pub-muted: #94a3b8;
  min-height: calc(100vh - 69px);
  padding: 1.25rem 1rem 2rem;
  background:
    radial-gradient(ellipse 80% 50% at 50% -10%, rgba(99, 102, 241, 0.14), transparent),
    var(--pub-bg);
  color: var(--pub-text);
}

.pub-page__hero {
  max-width: var(--pub-max);
  margin: 0 auto 1.5rem;
  text-align: center;
}

.pub-page__kicker {
  margin: 0 0 8px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--pub-accent);
}

.pub-page__title {
  margin: 0 0 10px;
  font-size: clamp(1.5rem, 3vw, 2rem);
  font-weight: 700;
  line-height: 1.2;
}

.pub-page__sub {
  margin: 0 auto;
  max-width: 52ch;
  font-size: 15px;
  line-height: 1.6;
  color: var(--pub-muted);
}

.pub-page__body {
  max-width: var(--pub-max);
  margin: 0 auto;
}

.pub-page__foot {
  max-width: var(--pub-max);
  margin: 2rem auto 0;
  padding-top: 1rem;
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
  gap: 14px;
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
  margin-bottom: 1rem;
  border: 1px solid var(--pub-border);
  border-radius: 12px;
  background: var(--pub-panel);
}

:deep(.pub-card .el-card__body) {
  color: var(--pub-text);
}

:deep(.pub-steps) {
  margin: 0 0 1rem;
  padding-left: 1.25rem;
  color: var(--pub-muted);
  font-size: 14px;
  line-height: 1.65;
}

:deep(.pub-actions) {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  align-items: center;
  margin-top: 1rem;
}

:deep(.pub-code) {
  display: block;
  margin-top: 8px;
  padding: 10px 12px;
  border-radius: 8px;
  font-size: 12px;
  font-family: ui-monospace, monospace;
  color: #cbd5e1;
  background: rgba(15, 23, 42, 0.75);
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
  margin: 1rem 0;
  padding: 14px 16px;
  border: 1px solid rgba(99, 102, 241, 0.35);
  border-radius: 10px;
  background: rgba(99, 102, 241, 0.08);
  text-align: left;
}

:deep(.pub-git-box a) {
  color: #a5b4fc;
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
</style>
