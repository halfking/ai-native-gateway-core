<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { clearAll } from '../store'
import { logout as apiLogout } from '../api/auth'
import PublicPortalLayout from '../components/PublicPortalLayout.vue'

const { t } = useI18n()
const router = useRouter()

function goHome() {
  router.push('/')
}

async function logout() {
  try { await apiLogout() } catch { /* ignore */ }
  clearAll()
  router.push('/')
}
</script>

<template>
  <PublicPortalLayout
    :title="t('forbidden.title')"
    :subtitle="t('forbidden.subtitle')"
    kicker="403"
  >
    <el-card shadow="never" class="pub-card forbidden-card">
      <div class="forbidden-icon" aria-hidden="true">🔒</div>
      <div class="actions">
        <el-button type="primary" @click="goHome">{{ t('forbidden.goDashboard') }}</el-button>
        <el-button @click="logout">{{ t('forbidden.switchAccount') }}</el-button>
      </div>
    </el-card>
  </PublicPortalLayout>
</template>

<style scoped>
.forbidden-card {
  text-align: center;
  padding: 24px 16px 32px;
}
.forbidden-icon {
  font-size: 56px;
  margin-bottom: 16px;
}
.actions {
  display: flex;
  gap: 12px;
  justify-content: center;
  flex-wrap: wrap;
  margin-top: 8px;
}
</style>
