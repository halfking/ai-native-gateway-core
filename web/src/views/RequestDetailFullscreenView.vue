<script setup lang="ts">
// RequestDetailFullscreenView — fullscreen shell for request/session detail.
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { surfaceError } from '../i18n/errorSurface'
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
import { ArrowLeft } from '@element-plus/icons-vue'
import './request-detail-fullscreen.css'

type ViewMode = 'request' | 'session-turns'

// ★ 2026-10-27 第 25 增量：页头的「←」是**纯图标按钮**，此前**没有可访问名**
//   （无 aria-label / title）⇒ 读屏用户不知道它是什么。
//   ⇒ 复用已有的 8 语种共享键 `common.button.back`，不新造文案、不硬编码中文。
const { t } = useI18n()

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
  waterfall, attempts, loadMeta, onSectionNeed, dispose,
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
  // The route may have changed while metadata was loading. Do not start a
  // body/waterfall request for the stale id after loadMeta has been
  // invalidated by the newer route watcher.
  if (id !== requestId.value) return
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

function replaceRequestDetailRoute(
  requestId: string,
  mode: ViewMode,
  tab: DetailSection,
) {
  return router.replace({
    name: 'request-detail',
    params: { requestId },
    // Request-detail authorization comes from the authenticated tenant context,
    // not a URL query. Keep this route contract deliberately narrow so stale
    // session/list query state cannot leak into a request-detail transition.
    query: { mode, tab },
  })
}

async function onSelectTurn(rid: string) {
  if (rid === requestId.value) return
  await replaceRequestDetailRoute(rid, 'session-turns', section.value)
}

function openAsRequest(rid: string) {
  viewMode.value = 'request'
  void replaceRequestDetailRoute(rid, 'request', section.value)
}

function switchMode(mode: ViewMode) {
  if (mode === 'session-turns' && !sessionId.value) return
  viewMode.value = mode
  void replaceRequestDetailRoute(requestId.value, mode, section.value)
}

function gotoSection(s: DetailSection) {
  section.value = s
  viewMode.value = 'request'
  void replaceRequestDetailRoute(requestId.value, 'request', s)
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
    exportError.value = surfaceError(e)
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
// sessions.duration_ms —— 877 起每轮刷新的已流逝时长。
// undefined = 后端没给（不是 0ms）；0 是"瞬时完成"，两者语义不同。
const snapDurationMs = computed(() => {
  const n = sessionSnap.value?.duration_ms
  return typeof n === 'number' ? n : undefined
})
</script>

<template>
  <div class="rdf" data-testid="request-detail-fullscreen">
    <header class="top">
      <!-- ★ 第 25 增量：纯图标按钮补可访问名（复用 common.button.back） -->
      <button
        type="button"
        class="btn btn-sm"
        :aria-label="t('common.button.back')"
        :title="t('common.button.back')"
        @click="goBack"
      ><ArrowLeft /></button>
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
      <!-- ★ 第 25 增量：**删掉「关闭」** —— 它与左上角的「←」调的是同一个
           `goBack`（两个入口做同一件事）⇒ 页头可见动作 5 → 4。 -->
    </header>

    <div v-if="exportError" class="banner err">导出失败：{{ exportError }}</div>

    <div v-if="metaLoading" class="banner">加载元数据…</div>
    <div v-else-if="metaError" class="banner err">{{ metaError }}</div>

    <template v-if="!metaLoading && (log || unified || (viewMode === 'session-turns' && sessionId) || !metaError)">
      <SessionSummaryBar
        v-if="sessionId && viewMode === 'request'"
        :session-id="sessionId"
        :title="snapTitle"
        :summary="snapSummary"
        :total-turns="snapTurns"
        :total-cost="snapCost"
        :summary-generated-at="snapGeneratedAt"
        :duration-ms="snapDurationMs"
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
          @goto="gotoSection"
        />
      </div>
    </template>
  </div>
</template>
