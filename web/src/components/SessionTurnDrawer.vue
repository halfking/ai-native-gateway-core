<script setup lang="ts">
// SessionTurnDrawer.vue — V2-P4 (2026-07-24)
// Right-side drawer showing full turn payload. Six tabs: request,
// response, compression diagnostics, meta, governance, attachments. Attachment links open via
// signed URL (admin endpoint, short-lived).

import { onBeforeUnmount, ref, watch } from 'vue'
import { getSessionTurn } from '../api/sessions_v2'
import { headers } from '../api/_core'

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
const error = ref('')
const turn = ref<TurnDetail | null>(null)
const tab = ref<'request' | 'response' | 'compression' | 'meta' | 'governance' | 'attachments'>(
  'request'
)
let requestSeq = 0
let controller: AbortController | null = null

watch(
  () => [props.sessionId, props.turnNo] as const,
  async ([sessionId, n]) => {
    const seq = ++requestSeq
    controller?.abort()
    controller = null
    turn.value = null
    tab.value = 'request'
    error.value = ''
    if (n == null) return
    controller = new AbortController()
    loading.value = true
    try {
      const value = (await getSessionTurn(sessionId, n, { signal: controller.signal })) as TurnDetail
      if (seq !== requestSeq) return
      turn.value = value
    } catch (e) {
      if (seq !== requestSeq || (e instanceof DOMException && e.name === 'AbortError')) return
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (seq === requestSeq) loading.value = false
    }
  },
  { immediate: true }
)

onBeforeUnmount(() => {
  requestSeq++
  controller?.abort()
})

function attachmentURL(att: Attachment): string {
  if (!att.object) return ''
  return `/api/attachments/${att.object.split('/').map(encodeURIComponent).join('/')}`
}

async function openAttachment(att: Attachment) {
  const url = attachmentURL(att)
  if (!url) return
  try {
    const response = await fetch(url, { headers: headers('GET'), credentials: 'same-origin' })
    if (!response.ok) throw new Error(`附件下载失败（${response.status}）`)
    const blob = await response.blob()
    const objectURL = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = objectURL
    link.download = att.name || 'attachment'
    document.body.appendChild(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(objectURL)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
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
    <div v-else-if="error" class="error" role="alert">{{ error }}</div>
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
            <button v-if="attachmentURL(att)" type="button" class="attachment-link" @click="openAttachment(att)">{{ att.name }}</button>
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
.error { color: #b42318; background: #fff1f0; border: 1px solid #f3b4b0; padding: 10px 12px; border-radius: 6px; margin: 12px; }
.muted { color: #6b7280; font-size: 12px; }
.att-list { list-style: none; padding: 0; }
.attachment-link { color: #2563eb; background: transparent; border: 0; padding: 0; cursor: pointer; text-decoration: underline; }
</style>
