<script setup lang="ts">
// RequestDetailFullscreenView — fullscreen shell for request/session detail.
import { computed, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  useRequestDetailLoader,
  type DetailSection,
} from '../composables/useRequestDetailLoader'
import SessionTurnsSyncPane from '../components/detail/SessionTurnsSyncPane.vue'
import RequestDetailSectionHost from '../components/detail/RequestDetailSectionHost.vue'
import SessionSummaryBar from '../components/SessionSummaryBar.vue'
import { statusToneClass } from '../components/detail/statusTone'
import { downloadSessionDetailMarkdown } from '../utils/sessionDetailExport'
import './request-detail-fullscreen.css'

type ViewMode = 'request' | 'session-turns'

const SECTIONS: [DetailSection, string][] = [
  ['overview', '概览'],
  ['chat', '对话'],
  ['waterfall', '调度瀑布'],
  ['attempts', '路由与重试'],
  ['flow', '流程 Trace'],
  ['compress', '压缩脱敏'],
  ['attachments', '附件/媒体'],
  ['raw', '原始 JSON'],
]

const route = useRoute()
const router = useRouter()
const loader = useRequestDetailLoader()
const {
  metaLoading, metaError, log, unified, sessionSnap, sessionId,
  requestBody, responseBody, outboundBody, waterfallLoading, waterfallError,
  waterfall, waterfallSource, attempts, loadMeta, onSectionNeed, dispose,
} = loader

const requestId = computed(() => String(route.params.requestId || '').trim())
const viewMode = ref<ViewMode>('request')
const section = ref<DetailSection>('overview')
const exporting = ref(false)
const exportError = ref('')

watch(() => route.query.mode, (m) => {
  if (m === 'session-turns') viewMode.value = 'session-turns'
  else if (m === 'request') viewMode.value = 'request'
}, { immediate: true })

watch(() => route.query.tab, (t) => {
  const key = String(t || '')
  if (SECTIONS.some(([k]) => k === key)) section.value = key as DetailSection
}, { immediate: true })

watch(requestId, async (id) => {
  if (!id) return
  await loadMeta(id)
  await onSectionNeed(id, viewMode.value === 'session-turns' ? 'chat' : section.value)
}, { immediate: true })

watch(section, (s) => {
  if (viewMode.value === 'request' && requestId.value) void onSectionNeed(requestId.value, s)
})

watch(viewMode, (mode) => {
  const id = requestId.value
  if (!id) return
  void onSectionNeed(id, mode === 'session-turns' ? 'chat' : section.value)
})

onUnmounted(() => dispose())

async function onSelectTurn(rid: string) {
  if (rid === requestId.value) return
  await router.replace({
    name: 'request-detail',
    params: { requestId: rid },
    query: { ...route.query, mode: 'session-turns' },
  })
}

function openAsRequest(rid: string) {
  viewMode.value = 'request'
  void router.replace({
    name: 'request-detail',
    params: { requestId: rid },
    query: { tab: section.value, mode: 'request' },
  })
}

function switchMode(mode: ViewMode) {
  if (mode === 'session-turns' && !sessionId.value) return
  viewMode.value = mode
  void router.replace({ query: { ...route.query, mode } })
}

function goBack() {
  if (window.history.length > 1) router.back()
  else void router.push('/request-logs')
}

async function copyId() {
  try { await navigator.clipboard.writeText(requestId.value) } catch { /* ignore */ }
}

function openSession() {
  const sid = sessionId.value
  if (sid) void router.push(`/admin/sessions/${encodeURIComponent(sid)}`)
}

function gotoSection(s: DetailSection) {
  section.value = s
  viewMode.value = 'request'
  void router.replace({ query: { ...route.query, mode: 'request', tab: s } })
}

function onSummaryUpdated(snap: Record<string, unknown>) {
  sessionSnap.value = snap
}

async function exportSessionMd() {
  const sid = sessionId.value
  if (!sid || exporting.value) return
  exporting.value = true
  exportError.value = ''
  try {
    await downloadSessionDetailMarkdown({
      sessionId: sid,
      title: typeof snapTitle.value === 'string' ? snapTitle.value : undefined,
      summary: snapSummary.value,
      totalTurns: snapTurns.value,
      totalCostUsd: snapCost.value,
    })
  } catch (e: unknown) {
    exportError.value = e instanceof Error ? e.message : String(e)
  } finally {
    exporting.value = false
  }
}

const statusLabel = computed(
  () => log.value?.request_status ?? unified.value?.meta.request_status ?? '—',
)
const snapTitle = computed(() => {
  const t = sessionSnap.value?.title
  return typeof t === 'string' ? t : (log.value?.session_title || undefined)
})
const snapSummary = computed(() => {
  const s = sessionSnap.value?.summary
  return typeof s === 'string' ? s : undefined
})
const snapTurns = computed(() => {
  const n = sessionSnap.value?.total_turns
  return typeof n === 'number' ? n : undefined
})
const snapCost = computed(() => {
  const n = sessionSnap.value?.total_cost_usd
  return typeof n === 'number' ? n : undefined
})
const snapGeneratedAt = computed(() => {
  const s = sessionSnap.value?.summary_generated_at
  return typeof s === 'string' ? s : undefined
})
</script>

<template>
  <div class="rdf" data-testid="request-detail-fullscreen">
    <header class="top">
      <button type="button" class="btn btn-sm" @click="goBack">←</button>
      <div class="id-block">
        <code>{{ requestId || '—' }}</code>
        <span class="status-pill" :class="statusToneClass(statusLabel, 'pill')">{{ statusLabel }}</span>
        <span v-if="sessionId" class="meta">session: {{ sessionId }}</span>
        <span v-if="unified" class="meta">{{ unified.source }} · {{ unified.persistence }}</span>
      </div>
      <div class="mode-seg">
        <button type="button" class="btn btn-sm" :class="{ 'btn-primary': viewMode === 'request' }" @click="switchMode('request')">单请求</button>
        <button type="button" class="btn btn-sm" :class="{ 'btn-primary': viewMode === 'session-turns' }" :disabled="!sessionId" @click="switchMode('session-turns')">会话轮次</button>
      </div>
      <button type="button" class="btn btn-sm" @click="copyId">复制</button>
      <button
        v-if="sessionId"
        type="button"
        class="btn btn-sm"
        :disabled="exporting"
        data-testid="export-session-md"
        @click="exportSessionMd"
      >{{ exporting ? '导出中…' : '导出 MD' }}</button>
      <button v-if="sessionId" type="button" class="btn btn-sm" @click="openSession">打开会话</button>
      <button type="button" class="btn btn-sm" @click="goBack">关闭</button>
    </header>

    <div v-if="exportError" class="banner err">导出失败：{{ exportError }}</div>

    <div v-if="metaLoading" class="banner">加载元数据…</div>
    <div v-else-if="metaError && !log && !unified" class="banner err">{{ metaError }}</div>

    <template v-else>
      <SessionSummaryBar
        v-if="sessionId && viewMode === 'request'"
        :session-id="sessionId"
        :title="snapTitle"
        :summary="snapSummary"
        :total-turns="snapTurns"
        :total-cost="snapCost"
        :summary-generated-at="snapGeneratedAt"
        @summary-updated="onSummaryUpdated"
      />

      <div
        v-if="viewMode === 'session-turns' && sessionId"
        class="session-shell"
        data-testid="session-sync-shell"
      >
        <SessionTurnsSyncPane
          :session-id="sessionId"
          :active-request-id="requestId"
          :request-body="requestBody"
          :response-body="responseBody"
          @select-request="onSelectTurn"
          @open-as-request="openAsRequest"
        />
      </div>

      <div v-else class="body">
        <nav class="nav">
          <button
            v-for="[key, label] in SECTIONS"
            :key="key"
            type="button"
            class="nav-btn"
            :class="{ active: section === key }"
            @click="gotoSection(key)"
          >{{ label }}</button>
        </nav>
        <RequestDetailSectionHost
          :section="section"
          :request-id="requestId"
          :log="log"
          :unified="unified"
          :session-snap="sessionSnap"
          :session-id="sessionId"
          :request-body="requestBody"
          :response-body="responseBody"
          :outbound-body="outboundBody"
          :waterfall="waterfall"
          :attempts="attempts"
          :waterfall-loading="waterfallLoading"
          :waterfall-error="waterfallError"
          :waterfall-source="waterfallSource"
          @goto="gotoSection"
        />
      </div>
    </template>
  </div>
</template>
