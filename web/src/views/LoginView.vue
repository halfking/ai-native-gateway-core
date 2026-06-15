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
  { title: '统一路由', description: '多模型、多供应商智能路由，按成本、延迟与可用性择优。' },
  { title: '租户与密钥', description: 'MaaS 账户、API 密钥与用量配额分租户隔离管理。' },
  { title: '指纹与伪装', description: 'UA/TLS 指纹池与凭据并发槽，降低供应商封禁风险。' },
  { title: '审计可观测', description: '请求日志、会话上下文与路由决策全链路可追溯。' },
  { title: '定价与套餐', description: '模型定价、免费资源池与充值订单一体化。' },
  { title: '高可用部署', description: '56/71/184/252 多节点 least_conn，生产级出口冗余。' },
]

const advantages = [
  { title: 'Go 数据面', description: '全量 Go 重写，生产验证稳定' },
  { title: '开轩 MaaS', description: '对外 llm.kxpms.cn 统一入口' },
  { title: '自动调优', description: '凭据峰值统计与槽位建议' },
  { title: 'Casdoor SSO', description: 'JWT 与 API Key 并存过渡' },
]

function scrollToLogin() {
  document.getElementById('login-form')?.scrollIntoView({ behavior: 'smooth', block: 'center' })
}

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
        subtitle="统一接入国内外大模型，提供路由、计费、审计与多租户治理。"
        :hero-points="['智能路由', '多租户', '用量审计', '高可用']"
        :features="features"
        :advantages="advantages"
        advantages-subtitle="生产环境验证的企业级 LLM 网关"
        cta-label="前往登录"
        footer-text="开轩 LLM Gateway · MaaS 控制面"
        accent="#6366f1"
        hide-cta
        @login="scrollToLogin"
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
