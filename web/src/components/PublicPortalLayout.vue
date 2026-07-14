<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

const { t } = useI18n()
const router = useRouter()

const links = [
  { path: '/', labelKey: 'public.layout.home' },
  { path: '/download', labelKey: 'public.layout.download' },
  { path: '/support', labelKey: 'public.layout.support' },
  { path: '/offline-activation', labelKey: 'public.layout.offline' },
  { path: '/activate', labelKey: 'public.download.activateLink' },
]
</script>

<template>
  <div class="pub-layout">
    <header class="pub-header">
      <router-link to="/" class="pub-brand">KX Gateway</router-link>
      <nav class="pub-nav">
        <router-link
          v-for="link in links"
          :key="link.path"
          :to="link.path"
          class="pub-nav__link"
          active-class="pub-nav__link--active"
        >
          {{ t(link.labelKey) }}
        </router-link>
      </nav>
    </header>
    <main class="pub-main">
      <slot />
    </main>
    <footer class="pub-footer">
      <span>LLM Gateway · {{ t('landing.kicker') }}</span>
      <el-button link type="primary" @click="router.push('/support')">{{ t('public.layout.support') }}</el-button>
    </footer>
  </div>
</template>

<style scoped>
.pub-layout {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  background: linear-gradient(180deg, #0f172a 0%, #1e293b 40%, #f8fafc 40%);
}
.pub-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 1rem 2rem;
  color: #e2e8f0;
}
.pub-brand {
  font-weight: 700;
  font-size: 1.125rem;
  color: #fff;
  text-decoration: none;
}
.pub-nav {
  display: flex;
  gap: 1.25rem;
  flex-wrap: wrap;
}
.pub-nav__link {
  color: #94a3b8;
  text-decoration: none;
  font-size: 0.9rem;
}
.pub-nav__link--active,
.pub-nav__link:hover {
  color: #fff;
}
.pub-main {
  flex: 1;
  max-width: 960px;
  width: 100%;
  margin: 0 auto;
  padding: 2rem 1.5rem 3rem;
}
.pub-footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 1rem 2rem;
  border-top: 1px solid #e2e8f0;
  background: #fff;
  color: #64748b;
  font-size: 0.875rem;
}
</style>
