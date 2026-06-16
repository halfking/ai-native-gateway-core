<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { ApiKey } from '../api'
import { formatApiKeyLabel } from '../utils/apiKey'

const props = defineProps<{
  visible: boolean
  keys: ApiKey[]
  unrevealableIds?: Set<number>
  loading?: boolean
  error?: string
  reason?: 'session-forbidden' | 'manual'
  selectedId?: number | null
}>()

const emit = defineEmits<{
  select: [id: number]
  close: []
}>()

const router = useRouter()
const route = useRoute()

const title = computed(() =>
  props.reason === 'session-forbidden' ? '会话密钥不匹配' : '选择 API 密钥',
)

const hint = computed(() =>
  props.reason === 'session-forbidden'
    ? '此对话绑定的网关会话属于另一把 API 密钥。请选择拥有该会话的密钥，系统将自动重发上一条消息。'
    : '对话需要完整的 sk-* 密钥。请从下方选择一把可用密钥。',
)

const redirectPath = computed(() => {
  const r = route.path
  return r.startsWith('/chat') ? '/chat' : r
})

function isUnrevealable(id: number): boolean {
  return props.unrevealableIds?.has(id) ?? false
}

function goKeys(action?: string) {
  const query: Record<string, string> = { redirect: redirectPath.value }
  if (action) query.action = action
  void router.push({ path: '/keys', query })
}
</script>

<template>
  <Teleport to="body">
    <div
      v-if="visible"
      class="modal-backdrop"
      :class="{ 'modal-backdrop--blocking': reason === 'session-forbidden' }"
      @click.self="reason !== 'session-forbidden' && emit('close')"
    >
      <div class="modal-panel card" role="dialog" aria-modal="true" :aria-labelledby="'key-modal-title'">
        <div class="modal-panel__head">
          <h3 id="key-modal-title" class="modal-panel__title">{{ title }}</h3>
          <button
            v-if="reason !== 'session-forbidden'"
            type="button"
            class="modal-panel__close"
            aria-label="关闭"
            @click="emit('close')"
          >
            ×
          </button>
        </div>
        <p class="modal-panel__hint">{{ hint }}</p>
        <div v-if="error" class="alert alert-danger">{{ error }}</div>
        <ul class="key-list">
          <li v-for="k in keys" :key="k.id" class="key-list__item" :class="{ 'key-list__item--disabled': isUnrevealable(k.id) }">
            <div class="key-list__meta">
              <span class="key-list__label">
                {{ formatApiKeyLabel(k) }}
                <span v-if="isUnrevealable(k.id)" class="key-list__badge">无法还原</span>
              </span>
              <span class="key-list__sub">{{ k.tenant_id }} · {{ k.owner_user || '—' }}</span>
            </div>
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :class="{ 'btn-ghost': selectedId === k.id }"
              :disabled="loading || isUnrevealable(k.id)"
              @click="emit('select', k.id)"
            >
              {{
                loading
                  ? '加载中…'
                  : isUnrevealable(k.id)
                    ? '不可选'
                    : selectedId === k.id
                      ? '当前密钥'
                      : '使用此密钥'
              }}
            </button>
          </li>
        </ul>
        <div v-if="!keys.length" class="key-list__empty">
          没有可用的密钥。请先申请或创建 API 密钥。
        </div>
        <div class="modal-panel__actions">
          <button type="button" class="btn btn-ghost btn-sm" @click="goKeys('create')">签发新密钥</button>
          <button type="button" class="btn btn-ghost btn-sm" @click="goKeys()">前往 API 密钥管理</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.modal-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  background: rgba(0, 0, 0, 0.55);
  backdrop-filter: blur(2px);
}

.modal-backdrop--blocking {
  cursor: default;
}

.modal-panel {
  width: min(520px, 100%);
  max-height: min(85vh, 640px);
  overflow-y: auto;
  padding: 18px 20px;
  border: 1px solid rgba(99, 102, 241, 0.45);
  background: var(--card, #1a1d27);
  box-shadow: 0 16px 48px rgba(0, 0, 0, 0.45);
}

.modal-panel__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}

.modal-panel__title {
  margin: 0;
  font-size: 16px;
}

.modal-panel__close {
  border: none;
  background: transparent;
  color: var(--muted);
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  padding: 0 4px;
}

.modal-panel__hint {
  margin: 0 0 14px;
  font-size: 13px;
  color: var(--muted);
  line-height: 1.55;
}

.key-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.key-list__item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border-radius: 8px;
  border: 1px solid var(--border);
  background: var(--bg);
}

.key-list__meta {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.key-list__label {
  font-size: 14px;
  font-weight: 500;
}

.key-list__sub {
  font-size: 12px;
  color: var(--muted);
}

.key-list__item--disabled {
  opacity: 0.72;
}

.key-list__badge {
  margin-left: 6px;
  font-size: 11px;
  font-weight: 500;
  color: #f59e0b;
}

.key-list__empty {
  font-size: 13px;
  color: var(--muted);
  padding: 8px 0;
}

.modal-panel__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 14px;
}
</style>
