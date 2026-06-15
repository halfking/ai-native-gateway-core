<script setup lang="ts">
import { useRouter } from 'vue-router'
import { clearAll } from '../store'

const router = useRouter()

function goHome() {
  router.push('/')
}

function logout() {
  clearAll()
  router.push('/login')
}
</script>

<template>
  <div class="forbidden-page">
    <div class="forbidden-card">
      <div class="forbidden-icon">🔒</div>
      <h1>403 - 访问被拒绝</h1>
      <p class="subtitle">此页面仅限超级管理员访问。您的租户管理员账户没有访问权限。</p>
      <div class="actions">
        <button class="btn btn-primary" @click="goHome">返回仪表盘</button>
        <button class="btn btn-ghost" @click="logout">切换账户</button>
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
