<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { ApiKey } from '../api'
import { formatApiKeyLabel } from '../utils/apiKey'

const props = defineProps<{
  keys: ApiKey[]
  loading?: boolean
  error?: string
}>()

const emit = defineEmits<{
  select: [id: number]
}>()

const router = useRouter()
const route = useRoute()

const redirectPath = computed(() => {
  const r = route.path
  return r.startsWith('/chat') ? '/chat' : r
})

function goKeys(action?: string) {
  const query: Record<string, string> = { redirect: redirectPath.value }
  if (action) query.action = action
  void router.push({ path: '/keys', query })
}
</script>

<template>
  <div class="key-picker card">
    <h3 class="key-picker__title">选择 API 密钥</h3>
    <p class="key-picker__hint">
      对话需要完整的 <code>sk-*</code> 密钥。请从下方选择一把可用密钥；若密钥无法还原，请重新签发。
    </p>
    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <ul class="key-picker__list">
      <li v-for="k in keys" :key="k.id" class="key-picker__item">
        <div class="key-picker__meta">
          <span class="key-picker__label">{{ formatApiKeyLabel(k) }}</span>
          <span class="key-picker__sub">
            {{ k.tenant_id }} · {{ k.owner_user || '—' }}
          </span>
        </div>
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="loading"
          @click="emit('select', k.id)"
        >
          {{ loading ? '加载中…' : '使用此密钥' }}
        </button>
      </li>
    </ul>
    <div v-if="!keys.length" class="key-picker__empty">
      没有可用的密钥。
    </div>
    <div class="key-picker__actions">
      <button type="button" class="btn btn-ghost btn-sm" @click="goKeys('create')">
        签发新密钥
      </button>
      <button type="button" class="btn btn-ghost btn-sm" @click="goKeys()">
        前往 API 密钥管理
      </button>
    </div>
  </div>
</template>

<style scoped>
.key-picker {
  padding: 16px 18px;
  margin-bottom: 12px;
  border: 1px solid rgba(99, 102, 241, 0.35);
  background: rgba(99, 102, 241, 0.08);
}

.key-picker__title {
  margin: 0 0 6px;
  font-size: 15px;
}

.key-picker__hint {
  margin: 0 0 12px;
  font-size: 13px;
  color: var(--muted);
  line-height: 1.5;
}

.key-picker__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.key-picker__item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border-radius: 8px;
  border: 1px solid var(--border);
  background: var(--bg);
}

.key-picker__meta {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.key-picker__label {
  font-size: 14px;
  font-weight: 500;
}

.key-picker__sub {
  font-size: 12px;
  color: var(--muted);
}

.key-picker__empty {
  font-size: 13px;
  color: var(--muted);
  padding: 8px 0;
}

.key-picker__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 12px;
}
</style>
