<script setup lang="ts">
// RequestActionsView — 批 203（2026-10-17）
// 🟡 P2 requestActions 孤儿接前端：request_id 维度的会话动作时间线
//
// 端点：GET /api/admin/requests/{id}/actions（admin 中间件，handler.go:1078）
// 17 跳 line 1064：「只查 Redis LIST by request_id」by-design 平台共享（不分租户）
//
// 视图：用户输入 request_id（路径参数 + URL query 兼容）→ 拉 actions 数组
// 11 字段（来自 liveactions.ActionEvent + detail 展平）
import { ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getRequestActions, type RequestAction } from '../api/requestActions'

const route = useRoute()
const router = useRouter()

const loading = ref(false)
const error = ref('')
const requestId = ref<string>(typeof route.params.id === 'string' ? route.params.id : '')
const actions = ref<RequestAction[]>([])
const count = ref(0)

async function load() {
  if (!requestId.value.trim()) {
    error.value = '请输入 request_id'
    return
  }
  loading.value = true
  error.value = ''
  try {
    const res = await getRequestActions(requestId.value.trim())
    actions.value = res.actions
    count.value = res.count
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    actions.value = []
    count.value = 0
  } finally {
    loading.value = false
  }
}

// URL 同步：path 参数变化时自动重新加载
watch(
  () => route.params.id,
  (newId) => {
    if (typeof newId === 'string' && newId) {
      requestId.value = newId
      void load()
    }
  },
  { immediate: true },
)

function formatTimestamp(s: string | undefined | null): string {
  if (!s) return '—'
  return s.replace('T', ' ').replace(/\.\d+Z$/, '').replace('Z', '')
}

function isUnknownKey(k: string): boolean {
  return !['request_id', 'seq', 'action', 'ts', 'model', 'credential_id', 'error_kind', 'retry_seq', 'retry'].includes(k)
}

function extraKeys(a: RequestAction): string[] {
  return Object.keys(a).filter(isUnknownKey)
}

function submitNewId() {
  if (!requestId.value.trim()) return
  void router.push({ name: 'request-actions', params: { id: requestId.value.trim() } })
}
</script>

<template>
  <div class="request-actions-view">
    <header class="ra-header">
      <h1 class="ra-title">请求动作时间线</h1>
    </header>

    <form class="ra-form" @submit.prevent="submitNewId">
      <label class="ra-label">
        <span>request_id</span>
        <input
          v-model="requestId"
          class="ra-input"
          type="text"
          placeholder="req-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
          :disabled="loading"
        />
      </label>
      <button type="submit" class="btn btn-primary btn-sm" :disabled="loading || !requestId.trim()">
        {{ $t('common.terms.query') }}
      </button>
    </form>

    <p v-if="error" class="ra-error">{{ error }}</p>

    <div v-if="loading" class="ra-loading">{{ $t('common.feedback.loading') }}</div>

    <template v-else-if="actions.length > 0">
      <header class="ra-result-header">
        <span>共 {{ count }} 条动作</span>
        <span class="ra-request-id">request_id: <code>{{ requestId }}</code></span>
      </header>

      <ol class="ra-timeline">
        <li v-for="a in actions" :key="`${a.seq}-${a.ts}`" class="ra-event">
          <div class="ra-event-head">
            <span class="ra-seq">#{{ a.seq }}</span>
            <code class="ra-action">{{ a.action }}</code>
            <span class="ra-ts">{{ formatTimestamp(a.ts) }}</span>
          </div>
          <div class="ra-event-body">
            <div v-if="a.model" class="ra-row"><span class="ra-row-label">model</span><code>{{ a.model }}</code></div>
            <div v-if="a.credential_id" class="ra-row"><span class="ra-row-label">credential_id</span><code>{{ a.credential_id }}</code></div>
            <div v-if="a.error_kind" class="ra-row"><span class="ra-row-label">error_kind</span><code>{{ a.error_kind }}</code></div>
            <div v-if="a.retry" class="ra-row"><span class="ra-row-label">retry</span>{{ a.retry_seq ? `第 ${a.retry_seq} 次重试` : $t('common.button.yes') }}</div>
            <div v-for="k in extraKeys(a)" :key="`${a.seq}-${k}`" class="ra-row">
              <span class="ra-row-label">{{ k }}</span>
              <span>{{ String(a[k] ?? '—') }}</span>
            </div>
          </div>
        </li>
      </ol>
    </template>

    <p v-else-if="!loading && count === 0 && !error" class="ra-empty">
      暂无动作记录
    </p>
  </div>
</template>

<style scoped>
.request-actions-view { padding: var(--space-4); }
.ra-header { margin-bottom: var(--space-3); }
.ra-title { font-size: var(--text-xl); font-weight: var(--font-semibold); }
.ra-form { display: flex; gap: var(--space-2); align-items: flex-end; margin-bottom: var(--space-3); }
.ra-label { display: flex; flex-direction: column; gap: var(--space-1); flex: 1; }
.ra-label span { font-size: var(--text-sm); color: var(--text-muted); }
.ra-input { padding: var(--space-2); border: 1px solid var(--border); border-radius: var(--radius); background: var(--bg-surface); color: var(--text); font-family: var(--kx-font-mono, monospace); font-size: var(--text-sm); }
.ra-input:focus { outline: none; border-color: var(--kx-primary); }
.ra-error { color: var(--danger); padding: var(--space-2); background: var(--danger-bg); border-radius: var(--radius); margin-bottom: var(--space-3); }
.ra-loading { color: var(--text-muted); padding: var(--space-3); }
.ra-empty { color: var(--text-muted); padding: var(--space-3); font-style: italic; }
.ra-result-header { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: var(--space-3); }
.ra-request-id { color: var(--text-muted); font-size: var(--text-sm); }
.ra-timeline { list-style: none; margin: 0; padding: 0; }
.ra-event { background: var(--bg-surface); border: 1px solid var(--border); border-radius: var(--radius); padding: var(--space-3); margin-bottom: var(--space-2); }
.ra-event-head { display: flex; align-items: baseline; gap: var(--space-2); margin-bottom: var(--space-2); }
.ra-seq { color: var(--text-muted); font-family: var(--kx-font-mono, monospace); font-size: var(--text-sm); }
.ra-action { background: var(--kx-primary-soft, var(--bg-subtle)); color: var(--kx-primary); padding: 2px 6px; border-radius: 4px; font-size: var(--text-sm); }
.ra-ts { color: var(--text-muted); font-size: var(--text-sm); margin-left: auto; }
.ra-event-body { display: flex; flex-direction: column; gap: var(--space-1); }
.ra-row { display: grid; grid-template-columns: 140px 1fr; gap: var(--space-2); font-size: var(--text-sm); }
.ra-row-label { color: var(--text-muted); }
</style>
