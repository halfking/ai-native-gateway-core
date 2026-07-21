<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import LanguageSelector from './LanguageSelector.vue'

defineEmits<{ login: [] }>()

const { t } = useI18n()
const route = useRoute()

const navLinks = [
  { path: '/', labelKey: 'public.layout.home' },
  { path: '/download', labelKey: 'public.layout.download' },
  { path: '/support', labelKey: 'public.layout.support' },
  { path: '/offline-activation', labelKey: 'public.layout.offline' },
  { path: '/activate', labelKey: 'public.download.activateLink' },
]

function isNavActive(path: string) {
  if (path === '/') {
    return route.path === '/'
  }
  return route.path === path || route.path.startsWith(`${path}/`)
}
</script>

<template>
  <header class="guest-header">
    <router-link to="/" class="guest-brand">
      <img
        src="/logo-icon-dark.png"
        width="40"
        height="40"
        :alt="t('landing.brandTitle')"
        class="guest-brand-img"
      />
      <span class="guest-brand-text">
        <span class="guest-brand-title">{{ t('landing.brandTitle') }}</span>
        <span class="guest-brand-sub">{{ t('landing.brandSubtitle') }}</span>
      </span>
    </router-link>

    <nav class="guest-nav" :aria-label="t('public.layout.navAria')">
      <router-link
        v-for="link in navLinks"
        :key="link.path"
        :to="link.path"
        class="guest-nav__link"
        :class="{ 'guest-nav__link--active': isNavActive(link.path) }"
      >
        {{ t(link.labelKey) }}
      </router-link>
    </nav>

    <div class="guest-header-right">
      <LanguageSelector />
      <button type="button" class="btn btn-primary btn-sm guest-login-btn" @click="$emit('login')">
        {{ t('login.submit') }}
      </button>
    </div>
  </header>
</template>

<style scoped>
.guest-header {
  display: flex;
  align-items: center;
  gap: 20px;
  padding: 14px 24px;
  border-bottom: 1px solid var(--border);
  background: var(--sidebar);
  position: sticky;
  top: 0;
  z-index: 20;
}

.guest-brand {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
  text-decoration: none;
  color: inherit;
  min-width: 0;
}

.guest-brand-img {
  display: block;
  width: 40px;
  height: 40px;
  flex-shrink: 0;
}

.guest-brand-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.guest-brand-title {
  font-size: 15px;
  font-weight: 700;
  line-height: 1.25;
  color: var(--text);
  white-space: nowrap;
}

.guest-brand-sub {
  font-size: 11px;
  font-weight: 500;
  line-height: 1.3;
  color: var(--text-secondary);
  white-space: nowrap;
}

.guest-nav {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 1;
  min-width: 0;
  overflow-x: auto;
  scrollbar-width: none;
}

.guest-nav::-webkit-scrollbar {
  display: none;
}

.guest-nav__link {
  flex-shrink: 0;
  padding: 6px 12px;
  border-radius: 8px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
  text-decoration: none;
  transition: color 0.15s ease, background 0.15s ease;
}

.guest-nav__link:hover {
  color: var(--text);
  background: rgba(255, 255, 255, 0.06);
}

.guest-nav__link--active {
  color: var(--accent);
  background: rgba(99, 102, 241, 0.12);
}

.guest-header-right {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
  margin-left: auto;
}

.guest-login-btn {
  flex-shrink: 0;
}

@media (max-width: 960px) {
  .guest-brand-sub {
    display: none;
  }

  .guest-nav {
    display: none;
  }
}

@media (max-width: 640px) {
  .guest-header {
    padding: 12px 16px;
    gap: 12px;
  }

  .guest-brand-title {
    font-size: 13px;
    max-width: 160px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
}
</style>
