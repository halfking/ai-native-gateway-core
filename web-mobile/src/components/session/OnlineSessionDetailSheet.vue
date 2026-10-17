<script setup lang="ts">
// OnlineSessionDetailSheet — 在线会话详情（直拉 /sessions/{id}/turns）。
//
// 为什么不靠 /turns?search=：turns/sessions 的 search 对大量活跃 gw_* id
// 常无命中（列表空、搜索框却已填）——详情链路在真机 CDP 上表现为「点了
// 等于进空页」。本组件绕过列表搜索，按 session_id 直取轮次树。

import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import AppSheet from '@/components/common/AppSheet.vue'
import FocusLayer from '@/components/common/FocusLayer.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchSessionTurns,
  latencyOf,
  type SessionTurnTreeItem,
} from '@/api/turnsSessions'
import type { OnlineSession } from '@/api/sessions'

const props = defineProps<{
  modelValue: boolean
  session: OnlineSession | null
}>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

const router = useRouter()
const turns = ref<SessionTurnTreeItem[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const focusOpen = ref(false)

async function loadTurns(sessionId: string): Promise<void> {
  loading.value = true
  error.value = null
  turns.value = []
  try {
    const resp = await fetchSessionTurns(sessionId, { limit: 50 })
    turns.value = resp.turns ?? []
  } catch (err) {
    // ApiError.detail 才是后端原文；message 有时是笼统 network_error。
    const ae = err as { detail?: string; message?: string; status?: number }
    error.value = ae.detail || ae.message || t('common.error')
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.modelValue, props.session?.session_id] as const,
  ([open, id]) => {
    if (open && id) void loadTurns(id)
    if (!open) {
      turns.value = []
      error.value = null
      focusOpen.value = false
    }
  },
)

function latencyText(turn: SessionTurnTreeItem): string {
  const ms = latencyOf(turn)
  return ms == null ? t('turns.latencyUnknown') : `${ms}ms`
}

function openJourney(requestId?: string): void {
  if (!requestId) return
  emit('update:modelValue', false)
  void router.push({ path: `/journey/${encodeURIComponent(requestId)}` })
}

function openInTurnsPage(): void {
  const id = props.session?.session_id
  if (!id) return
  emit('update:modelValue', false)
  void router.push({ path: '/turns', query: { search: id } })
}

const open = computed({
  get: () => props.modelValue,
  set: (v: boolean) => emit('update:modelValue', v),
})
</script>

<template>
  <AppSheet
    v-model="open"
    presentation="sheet"
    :title="session?.title || session?.session_id || t('online.detailTitle')"
  >
    <template v-if="session">
      <div class="osd data-card">
        <div class="card-field">
          <span>{{ t('online.sessionId') }}</span>
          <span class="card-field__value mono">{{ session.session_id }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('online.model') }}</span>
          <span class="card-field__value">{{ session.last_model || t('online.unknownModel') }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('online.provider') }}</span>
          <span class="card-field__value">
            {{ session.last_provider_id === undefined ? '—' : `#${session.last_provider_id}` }}
          </span>
        </div>
        <div class="card-field">
          <span>{{ t('online.latency') }}</span>
          <span class="card-field__value num">
            {{ session.last_latency_ms === undefined ? '—' : `${session.last_latency_ms}ms` }}
          </span>
        </div>
        <div v-if="session.last_active_at" class="card-field">
          <span>{{ t('online.lastActiveLabel') }}</span>
          <span class="card-field__value">{{ relativeTime(session.last_active_at) }}</span>
        </div>
      </div>

      <div class="osd__toolbar">
        <button type="button" class="osd__btn" @click="focusOpen = true">
          {{ t('online.focusTurns') }}
        </button>
        <button type="button" class="osd__btn osd__btn--ghost" @click="openInTurnsPage">
          {{ t('online.openInTurns') }}
        </button>
      </div>

      <p v-if="loading" class="osd__msg">{{ t('common.loading') }}</p>
      <p v-else-if="error" class="osd__msg osd__msg--err">{{ error }}</p>
      <p v-else-if="turns.length === 0" class="osd__msg">{{ t('turns.noTurns') }}</p>
      <ul v-else class="osd__turns">
        <li v-for="tn in turns" :key="tn.turn_number" class="osd__turn">
          <div class="osd__turn-head">
            <span>#{{ tn.turn_number }}</span>
            <span>{{ tn.model || '—' }}</span>
            <span>{{ latencyText(tn) }}</span>
          </div>
          <p class="osd__turn-status">{{ tn.status }}</p>
          <button
            v-if="tn.request_id"
            type="button"
            class="osd__journey"
            @click="openJourney(tn.request_id)"
          >
            {{ t('turns.openJourney') }}
          </button>
        </li>
      </ul>
    </template>
  </AppSheet>

  <FocusLayer v-model:active="focusOpen" :title="session?.title || session?.session_id || t('online.detailTitle')">
    <p v-if="loading" class="osd__msg">{{ t('common.loading') }}</p>
    <p v-else-if="error" class="osd__msg osd__msg--err">{{ error }}</p>
    <p v-else-if="turns.length === 0" class="osd__msg">{{ t('turns.noTurns') }}</p>
    <ul v-else class="osd__turns osd__turns--focus">
      <li v-for="tn in turns" :key="`f-${tn.turn_number}`" class="osd__turn">
        <div class="osd__turn-head">
          <span>#{{ tn.turn_number }}</span>
          <span>{{ tn.model || '—' }}</span>
          <span>{{ latencyText(tn) }}</span>
        </div>
        <p class="osd__turn-status">{{ tn.status }}</p>
        <button
          v-if="tn.request_id"
          type="button"
          class="osd__journey"
          @click="openJourney(tn.request_id)"
        >
          {{ t('turns.openJourney') }}
        </button>
      </li>
    </ul>
  </FocusLayer>
</template>

<style scoped>
.osd {
  margin-bottom: var(--app-space-3);
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  word-break: break-all;
}
.osd__toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-3);
}
.osd__btn {
  min-height: 48px;
  padding: 0 14px;
  border-radius: var(--app-radius);
  border: 1px solid var(--app-border);
  background: var(--app-primary);
  color: #fff;
  font: inherit;
  font-weight: 600;
}
.osd__btn--ghost {
  background: var(--app-surface);
  color: var(--app-text);
}
.osd__msg {
  margin: var(--app-space-2) 0;
  font-size: 13px;
  color: var(--app-text-muted);
}
.osd__msg--err {
  color: var(--app-danger);
}
.osd__turns {
  list-style: none;
  margin: 0;
  padding: 0;
}
.osd__turns--focus {
  padding: var(--app-space-3);
}
.osd__turn {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-2);
  background: var(--app-surface);
}
.osd__turn-head {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
}
.osd__turn-status {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
}
.osd__journey {
  margin-top: 8px;
  min-height: 48px;
  padding: 0 12px;
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: transparent;
  font: inherit;
  color: var(--app-primary);
}
</style>
