<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { login, getAuthMe } from '../api'
import { setApiKey, setJwtToken, setUserInfo } from '../store'

const router   = useRouter()
const username = ref('admin')
const password = ref('')
const loading  = ref(false)
const error    = ref('')

async function handleLogin() {
  error.value = ''
  if (!username.value || !password.value) {
    error.value = '请输入用户名和密码'
    return
  }
  loading.value = true
  try {
    const resp = await login(username.value, password.value)
    if (resp.access_token) {
      // JWT-based login: store token + user info
      setJwtToken(resp.access_token)
      if (resp.user) {
        setUserInfo(resp.user)
      } else {
        // Fetch user info if not in response
        try {
          const me = await getAuthMe()
          setUserInfo(me)
        } catch { /* ignore */ }
      }
    } else if (resp.api_key) {
      // Legacy API-key login
      setApiKey(resp.api_key)
    }
    router.push('/')
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '登录失败'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-logo">
        <svg width="40" height="40" viewBox="0 0 32 32">
          <circle cx="16" cy="16" r="14" fill="#6366f1"/>
          <text x="16" y="21" text-anchor="middle" font-size="16" fill="white"
            font-family="Arial,sans-serif" font-weight="bold">G</text>
        </svg>
      </div>
      <h2>LLM Gateway</h2>
      <p class="subtitle">开轩 MaaS 控制面</p>

      <div v-if="error" class="alert alert-danger">{{ error }}</div>

      <form @submit.prevent="handleLogin">
        <div class="form-group">
          <label>用户名</label>
          <input v-model="username" type="text" placeholder="admin" autocomplete="username" />
        </div>
        <div class="form-group">
          <label>密码</label>
          <input v-model="password" type="password" placeholder="••••••••" autocomplete="current-password" />
        </div>
        <button class="btn btn-primary" style="width:100%;justify-content:center;margin-top:8px"
          type="submit" :disabled="loading">
          {{ loading ? '登录中…' : '登录' }}
        </button>
      </form>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg);
}
.login-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 36px 32px;
  width: 360px;
}
.login-logo { text-align: center; margin-bottom: 12px; }
h2 { text-align: center; font-size: 20px; margin-bottom: 4px; }
.subtitle { text-align: center; color: var(--muted); font-size: 13px; margin-bottom: 24px; }
</style>
