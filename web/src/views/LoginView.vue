<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { login, getAuthMe } from '../api'
import { setApiKey, setJwtToken, setUserInfo } from '../store'
import ServiceLandingPage from '../components/ServiceLandingPage.vue'

const router = useRouter()
const username = ref('admin')
const password = ref('')
const loading = ref(false)
const error = ref('')

const features = [
  { title: '多租户路由', description: '按租户、模型与凭证智能选路，支持 override 与免费资源池。' },
  { title: 'MaaS 账户', description: '套餐、充值、用量与账单，面向租户自助管理。' },
  { title: '凭证指纹池', description: '多机出站 IP 与 TLS 指纹，降低供应商封号风险。' },
  { title: '会话上下文', description: '跨请求上下文关联，支持任务级追踪与审计。' },
  { title: '提供商治理', description: '多 Provider 接入、定价管理与路由全景可视化。' },
  { title: 'OpenAI 兼容', description: '标准 Chat Completions API，无缝替换上游。' },
]

const advantages = [
  { title: 'Go 生产数据面', description: '71/184/252 多节点统一部署，无 Python 遗留' },
  { title: '抗封号', description: 'UA 漂移、utls 指纹与并发槽位自动调优' },
  { title: '平台 SSO', description: 'Casdoor JWT 登录管理面，API Key 兼容运维' },
  { title: '可观测', description: '请求日志、审计与路由决策全链路可查' },
]

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
      setJwtToken(resp.access_token)
      if (resp.user) {
        setUserInfo(resp.user)
      } else {
        try {
          const me = await getAuthMe()
          setUserInfo(me)
        } catch { /* ignore */ }
      }
    } else if (resp.api_key) {
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
  <div class="login-shell">
    <div class="login-shell__main">
      <ServiceLandingPage
        kicker="LLM Gateway"
        title="开轩 MaaS 与大模型路由网关"
        subtitle="统一接入多家 LLM 提供商，按租户与策略智能路由。登录后管理密钥、用量、定价与路由策略。"
        :hero-points="['多租户', '智能路由', 'MaaS 计费', 'OpenAI 兼容']"
        :features="features"
        :advantages="advantages"
        advantages-subtitle="生产级 LLM 基础设施"
        footer-text="开轩 LLM Gateway · llmgo.kxpms.cn"
        accent="#6366f1"
        :hide-cta="true"
      />
    </div>
    <aside id="login-form" class="login-shell__aside">
      <div class="login-card">
        <div class="login-logo">
          <svg width="40" height="40" viewBox="0 0 32 32" aria-hidden="true">
            <circle cx="16" cy="16" r="14" fill="#6366f1" />
            <text x="16" y="21" text-anchor="middle" font-size="16" fill="white"
              font-family="Arial,sans-serif" font-weight="bold">G</text>
          </svg>
        </div>
        <h2>登录控制面</h2>
        <p class="subtitle">开轩 MaaS 管理后台</p>
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
          <button class="btn btn-primary login-submit" type="submit" :disabled="loading">
            {{ loading ? '登录中…' : '登录' }}
          </button>
        </form>
      </div>
    </aside>
  </div>
</template>

<style scoped>
.login-shell {
  display: grid;
  grid-template-columns: 1fr 380px;
  min-height: 100vh;
  background: #0f1117;
}

.login-shell__main {
  overflow-y: auto;
  border-right: 1px solid #2d3139;
}

.login-shell__aside {
  position: sticky;
  top: 0;
  height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: #0f1117;
}

.login-card {
  background: var(--card, #1a1d27);
  border: 1px solid var(--border, #2d3139);
  border-radius: 12px;
  padding: 32px 28px;
  width: 100%;
  max-width: 340px;
}

.login-logo { text-align: center; margin-bottom: 12px; }
h2 { text-align: center; font-size: 18px; margin-bottom: 4px; color: #e8eaed; }
.subtitle { text-align: center; color: var(--muted, #6b7280); font-size: 13px; margin-bottom: 20px; }
.login-submit {
  width: 100%;
  justify-content: center;
  margin-top: 8px;
}

@media (max-width: 960px) {
  .login-shell {
    grid-template-columns: 1fr;
  }
  .login-shell__aside {
    position: static;
    height: auto;
    border-top: 1px solid #2d3139;
  }
}
</style>
