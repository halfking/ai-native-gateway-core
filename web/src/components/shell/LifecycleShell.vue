<script setup lang="ts">
// 2026-07-21: 公开访问（未登录）时的顶部水平导航。
// 与 ai-native-maintain 的 LifecycleShell 同形；链接到 /maintain/* 同源子 SPA。
// 主壳（App.vue）通过 <slot name="actions" /> 注入"登录"按钮；
// 默认 `<slot />` 渲染 <RouterView />（即 LandingView）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import LanguageSelector from '../LanguageSelector.vue'
import ThemeToggle from '../ThemeToggle.vue'
import { detectTheme, logoSrc } from '../../theme'
import { SITE_LOGO_SIZE, SITE_TITLE } from '../../config/brand'
import { PUBLIC_NAV_LINKS } from '../../config/navLinks'

const { t } = useI18n()
const brandLogo = ref(logoSrc(detectTheme()))

let observer: MutationObserver | null = null

onMounted(() => {
  brandLogo.value = logoSrc(detectTheme())
  observer = new MutationObserver(() => { brandLogo.value = logoSrc(detectTheme()) })
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
})

onUnmounted(() => observer?.disconnect())

const links = computed(() =>
  PUBLIC_NAV_LINKS.map((l) => ({ path: l.path, label: t(l.labelKey, l.path) })),
)
</script>

<template>
  <div class="app-shell">
    <a class="skip-link" href="#main-content">{{ t('app.nav.skip') }}</a>
    <header class="topbar">
      <a class="brand" href="/" :title="SITE_TITLE">
        <img
          class="brand-logo"
          :src="brandLogo"
          :width="SITE_LOGO_SIZE"
          :height="SITE_LOGO_SIZE"
          :alt="SITE_TITLE"
        />
        <span class="brand-title">{{ SITE_TITLE }}</span>
      </a>
      <div class="topbar-right">
        <nav class="topnav" :aria-label="t('app.nav.mainAria')">
          <a
            v-for="link in links"
            :key="link.path"
            :href="link.path"
            class="topnav-link"
          >{{ link.label }}</a>
        </nav>
        <ThemeToggle />
        <LanguageSelector />
        <slot name="actions" />
      </div>
    </header>
    <main id="main-content" class="main-content">
      <slot />
    </main>
    <footer class="footer">
      <span>{{ t('app.footer.left') }}</span>
      <span>{{ t('app.footer.right') }}<a href="/customer/update-activate">{{ t('app.footer.feedbackLink') }}</a></span>
    </footer>
  </div>
</template>

<style scoped>
.app-shell {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  background: var(--kx-bg, var(--bg));
  color: var(--kx-text, var(--text));
}
.skip-link {
  position: absolute;
  left: -9999px;
  top: 0;
  background: var(--kx-primary, var(--accent));
  color: #fff;
  padding: 8px 14px;
  border-radius: 6px;
  z-index: 9999;
}
.skip-link:focus { left: 12px; top: 12px; }

.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px 28px;
  border-bottom: 1px solid var(--kx-border, var(--border));
  background: var(--kx-surface-soft, var(--card));
  backdrop-filter: blur(12px);
  position: sticky;
  top: 0;
  z-index: 100;
}
.brand {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  text-decoration: none;
  color: var(--kx-text, var(--text));
}
.brand-logo {
  width: 44px;
  height: 44px;
  border-radius: 10px;
  object-fit: contain;
  background: var(--kx-surface, var(--card));
}
.brand-title {
  font-size: 14px;
  font-weight: 700;
  letter-spacing: -0.01em;
  line-height: 1.3;
}
.topbar-right {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.topnav {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-right: 6px;
}
.topnav-link {
  display: inline-flex;
  align-items: center;
  padding: 6px 12px;
  border-radius: 6px;
  font-size: 13px;
  color: var(--kx-muted, var(--muted));
  text-decoration: none;
  transition: background 0.15s ease, color 0.15s ease;
}
.topnav-link:hover {
  color: var(--kx-primary, var(--accent));
  background: var(--kx-primary-soft, var(--bg-subtle));
}
.topnav-link.router-link-active,
.topnav-link.is-active {
  color: var(--kx-primary, var(--accent));
  background: var(--kx-primary-soft, var(--bg-subtle));
}
.main-content {
  flex: 1;
  width: 100%;
  display: block;
}
.footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 18px 28px;
  border-top: 1px solid var(--kx-border, var(--border));
  font-size: 12px;
  color: var(--kx-muted, var(--muted));
  background: var(--kx-surface-soft, var(--card));
}
.footer a { color: var(--kx-primary, var(--accent)); text-decoration: none; }
.footer a:hover { text-decoration: underline; }

@media (max-width: 720px) {
  .topbar { flex-direction: column; align-items: stretch; }
  .topbar-right { justify-content: flex-start; }
  .topnav { flex-wrap: wrap; }
}
</style>
