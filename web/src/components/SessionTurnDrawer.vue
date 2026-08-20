<script setup lang="ts">
// SessionTurnDrawer.vue — V2-P4 (2026-07-24)
// Right-side drawer showing full turn payload. Six tabs: request,
// response, compression diagnostics, meta, governance, attachments. Attachment links open via
// signed URL (admin endpoint, short-lived).

import { ref, watch } from 'vue'
import { getSessionTurn } from '../api/sessions_v2'

interface Attachment {
  att_id: string
  name: string
  size: number
  mime?: string
  object?: string
}

interface TurnDetail {
  request?: unknown
  response?: unknown
  compression?: unknown
  meta?: unknown
  governance?: unknown
  attachments?: Attachment[]
  model?: string
  cost_usd?: number
}

const props = defineProps<{ sessionId: string; turnNo: number | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const loading = ref(false)
const turn = ref<TurnDetail | null>(null)
const tab = ref<'request' | 'response' | 'compression' | 'meta' | 'governance' | 'attachments'>(
  'request'
)

watch(
  () => [props.sessionId, props.turnNo] as const,
  async ([sessionId, n]) => {
    if (n == null) {
      turn.value = null
      return
    }
    loading.value = true
    try {
      turn.value = (await getSessionTurn(sessionId, n)) as TurnDetail
    } catch (e) {
      console.error('load turn failed', e)
      turn.value = null
    } finally {
      loading.value = false
    }
  },
  { immediate: true }
)

function attachmentURL(att: Attachment): string {
  if (!att.object) return ''
  return `/api/attachments/${att.object.split('/').map(encodeURIComponent).join('/')}`
}

function openAttachment(att: Attachment) {
  const url = attachmentURL(att)
  if (!url) return
  window.open(url, '_blank', 'noopener,noreferrer')
}

function stringify(v: unknown): string {
  if (v === undefined || v === null) return ''
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}
</script>

<template>
  <el-drawer
    :model-value="turnNo != null"
    direction="rtl"
    size="70%"
    :with-header="true"
    @close="emit('close')"
  >
    <template #header>
      <span>
        Turn #{{ turnNo }} &middot;
        {{ turn?.model || '' }} &middot;
        ${{
          typeof turn?.cost_usd === 'number' ? turn.cost_usd.toFixed(4) : '0'
        }}
      </span>
    </template>
    <div v-if="loading" class="loading">加载中&hellip;</div>
    <el-tabs v-else-if="turn" v-model="tab">
      <el-tab-pane label="请求" name="request">
        <pre>{{ stringify(turn.request) }}</pre>
      </el-tab-pane>
      <el-tab-pane label="回复" name="response">
        <pre>{{ stringify(turn.response) }}</pre>
      </el-tab-pane>
      <el-tab-pane label="压缩诊断" name="compression">
        <pre>{{ stringify(turn.compression) }}</pre>
      </el-tab-pane>
      <el-tab-pane label="元数据" name="meta">
        <pre>{{ stringify(turn.meta) }}</pre>
      </el-tab-pane>
      <el-tab-pane label="治理" name="governance">
        <pre>{{ stringify(turn.governance) }}</pre>
      </el-tab-pane>
      <el-tab-pane label="附件" name="attachments">
        <el-empty
          v-if="!turn.attachments || turn.attachments.length === 0"
          description="无附件"
        />
        <ul v-else class="att-list">
          <li v-for="att in turn.attachments" :key="att.att_id">
            <a v-if="attachmentURL(att)" :href="attachmentURL(att)" target="_blank" rel="noopener noreferrer">{{ att.name }}</a>
            <span v-else class="muted">{{ att.name }}（暂无下载路径）</span>
            <span class="muted">
              &middot; {{ (att.size / 1024).toFixed(1) }} KB
            </span>
          </li>
        </ul>
      </el-tab-pane>
    </el-tabs>
    <div v-else class="loading">无数据</div>
  </el-drawer>
</template>

<style scoped>
pre {
  background: #f9fafb;
  padding: 12px;
  border-radius: 6px;
  max-height: 70vh;
  overflow: auto;
  font-size: 12px;
}
.loading { color: #6b7280; padding: 24px; }
.muted { color: #6b7280; font-size: 12px; }
.att-list { list-style: none; padding: 0; }
.att-list li { padding: 6px 0; }
</style>
