<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { clearAll } from '../store'
import { logout as apiLogout } from '../api/auth'

const { t } = useI18n()
const router = useRouter()

function goHome() {
  router.push('/')
}

async function logout() {
  try { await apiLogout() } catch { /* ignore */ }
  clearAll()
  router.push('/login')
}
</script>

<template>
  <div class="forbidden-page">
    <div class="forbidden-card">
      <div class="forbidden-icon" aria-hidden="true">🔒</div>
      <h1>{{ t('forbidden.title') }}</h1>
      <p class="subtitle">{{ t('forbidden.subtitle') }}</p>
      <div class="actions">
        <button class="btn btn-primary" @click="goHome">{{ t('forbidden.goDashboard') }}</button>
        <button class="btn btn-ghost" @click="logout">{{ t('forbidden.switchAccount') }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.forbidden-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg);
}
.forbidden-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 48px 40px;
  text-align: center;
  max-width: 480px;
}
.forbidden-icon {
  font-size: 64px;
  margin-bottom: 16px;
}
h1 {
  font-size: 24px;
  margin: 0 0 12px;
  color: var(--text);
}
.subtitle {
  color: var(--muted);
  font-size: 14px;
  line-height: 1.6;
  margin-bottom: 28px;
}
.actions {
  display: flex;
  gap: 12px;
  justify-content: center;
}
</style>
