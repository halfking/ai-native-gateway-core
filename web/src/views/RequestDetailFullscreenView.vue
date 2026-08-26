<script setup lang="ts">
// RequestDetailFullscreenView — fullscreen shell for request/session detail.
import { computed, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  useRequestDetailLoader,
  type DetailSection,
} from '../composables/useRequestDetailLoader'
import RequestOverviewPanel from '../components/detail/RequestOverviewPanel.vue'
import ConversationMessagesPanel from '../components/detail/ConversationMessagesPanel.vue'
import FlowTimingPanel from '../components/detail/FlowTimingPanel.vue'
import CompressionRedactionPanel from '../components/detail/CompressionRedactionPanel.vue'
import SessionTurnsSyncPane from '../components/detail/SessionTurnsSyncPane.vue'
import RequestWaterfallPanel from '../components/detail/RequestWaterfallPanel.vue'
import MultimodalAttachmentsPanel from '../components/detail/MultimodalAttachmentsPanel.vue'
import { formatJson } from '../components/detail/messageHelpers'

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
const {
  metaLoading,
  metaError,
  log,
  unified,
  sessionId,
  requestBody,
  responseBody,
  outboundBody,
  waterfallLoading,
  waterfallError,
  waterfall,
  waterfallSource,
  attempts,
  loadMeta,
  onSectionNeed,
  dispose,
} = useRequestDetailLoader()

const requestId = computed(() => String(route.params.requestId || '').trim())
const viewMode = ref<ViewMode>('request')
const section = ref<DetailSection>('overview')

watch(
  () => route.query.mode,
  (m) => {
    if (m === 'session-turns') viewMode.value = 'session-turns'
    else if (m === 'request') viewMode.value = 'request'
  },
  { immediate: true },
)

watch(
  () => route.query.tab,
  (t) => {
    const key = String(t || '')
    if (SECTIONS.some(([k]) => k === key)) section.value = key as DetailSection
  },
  { immediate: true },
)

watch(
  requestId,
  async (id) => {
    if (!id) return
    await loadMeta(id)
    await onSectionNeed(id, section.value)
  },
  { immediate: true },
)

watch(section, (s) => {
  const id = requestId.value
  if (id) void onSectionNeed(id, s)
})

onUnmounted(() => dispose())

async function onSelectTurn(rid: string) {
  if (rid === requestId.value) return
  await router.replace({
    name: 'request-detail',
    params: { requestId: rid },
    query: { ...route.query, mode: 'session-turns', tab: section.value },
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
  try {
    await navigator.clipboard.writeText(requestId.value)
  } catch { /* ignore */ }
}

function openSession() {
  const sid = sessionId.value
  if (sid) void router.push(`/admin/sessions/${encodeURIComponent(sid)}`)
}

const statusLabel = computed(
  () => log.value?.request_status ?? unified.value?.meta.request_status ?? '—',
)
</script>

<template>
  <div class="rdf" data-testid="request-detail-fullscreen">
    <header class="top">
      <button type="button" class="btn btn-sm" @click="goBack">←</button>
      <div class="id-block">
        <code>{{ requestId || '—' }}</code>
        <span class="meta">{{ statusLabel }}</span>
        <span v-if="sessionId" class="meta">session: {{ sessionId }}</span>
        <span v-if="unified" class="meta">
          {{ unified.source }} · {{ unified.persistence }}
        </span>
      </div>
      <div class="mode-seg">
        <button
          type="button"
          class="btn btn-sm"
          :class="{ 'btn-primary': viewMode === 'request' }"
          @click="switchMode('request')"
        >单请求</button>
        <button
          type="button"
          class="btn btn-sm"
          :class="{ 'btn-primary': viewMode === 'session-turns' }"
          :disabled="!sessionId"
          @click="switchMode('session-turns')"
        >会话轮次</button>
      </div>
      <button type="button" class="btn btn-sm" @click="copyId">复制</button>
      <button
        v-if="sessionId"
        type="button"
        class="btn btn-sm"
        @click="openSession"
      >打开会话</button>
      <button type="button" class="btn btn-sm" @click="goBack">关闭</button>
    </header>

    <div v-if="metaLoading" class="banner">加载元数据…</div>
    <div v-else-if="metaError && !log && !unified" class="banner err">
      {{ metaError }}
    </div>

    <div v-else class="body" :class="{ 'body--session': viewMode === 'session-turns' }">
      <nav class="nav">
        <button
          v-for="[key, label] in SECTIONS"
          :key="key"
          type="button"
          class="nav-btn"
          :class="{ active: section === key }"
          @click="section = key"
        >{{ label }}</button>
      </nav>

      <aside
        v-if="viewMode === 'session-turns' && sessionId"
        class="turns"
      >
        <SessionTurnsSyncPane
          timeline-only
          :session-id="sessionId"
          :active-request-id="requestId"
          :request-body="requestBody"
          :response-body="responseBody"
          @select-request="onSelectTurn"
          @open-as-request="openAsRequest"
        />
      </aside>

      <main class="main">
        <RequestOverviewPanel
          v-if="section === 'overview'"
          :log="log"
          :unified="unified"
        />
        <ConversationMessagesPanel
          v-else-if="section === 'chat'"
          :body="requestBody"
        />
        <RequestWaterfallPanel
          v-else-if="section === 'waterfall'"
          :selected="waterfall"
          :attempts="attempts"
          :loading="waterfallLoading"
          :error="waterfallError"
          :source="waterfallSource"
        />
        <RequestWaterfallPanel
          v-else-if="section === 'attempts'"
          :selected="null"
          :attempts="attempts"
          :loading="waterfallLoading"
          :error="waterfallError"
        />
        <FlowTimingPanel
          v-else-if="section === 'flow'"
          :request-id="requestId"
        />
        <CompressionRedactionPanel
          v-else-if="section === 'compress'"
          :session-id="sessionId"
          :request-id="requestId"
          :request-body="requestBody"
          :outbound-body="outboundBody"
          :response-body="responseBody"
        />
        <MultimodalAttachmentsPanel
          v-else-if="section === 'attachments'"
          :request-id="requestId"
          :request-body="requestBody"
          :attachments="log?.attachments"
        />
        <pre v-else class="raw">{{
          formatJson({
            unified,
            log_meta: log,
            waterfall,
            request_body: requestBody,
            outbound_body: outboundBody,
            response_body: responseBody,
          })
        }}</pre>
      </main>
    </div>
  </div>
</template>

<style scoped>
.rdf {
  display: flex; flex-direction: column; min-height: calc(100vh - 48px);
  background: var(--bg, var(--kx-bg));
}
.top {
  display: flex; flex-wrap: wrap; align-items: center; gap: 8px;
  padding: 10px 14px; border-bottom: 1px solid var(--border);
  background: var(--bg-card, var(--card));
}
.id-block { flex: 1; min-width: 180px; display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.id-block code { font-size: 12px; word-break: break-all; }
.meta { font-size: 11px; color: var(--muted); }
.mode-seg { display: flex; gap: 4px; }
.banner { padding: 16px; }
.banner.err { color: var(--danger); }
.body {
  display: grid; grid-template-columns: 140px 1fr; flex: 1; min-height: 0;
}
.body--session { grid-template-columns: 140px minmax(200px, 280px) 1fr; }
.nav {
  display: flex; flex-direction: column; gap: 2px; padding: 10px 8px;
  border-right: 1px solid var(--border); background: var(--bg-card, var(--card));
}
.nav-btn {
  text-align: left; border: none; background: transparent; padding: 8px 10px;
  border-radius: 6px; cursor: pointer; font-size: 12px; color: var(--text);
}
.nav-btn.active { background: var(--primary); color: #fff; }
.turns {
  border-right: 1px solid var(--border); overflow: auto; min-height: 0;
}
.main { padding: 14px; overflow: auto; min-height: 0; }
.raw {
  font-size: 11px; white-space: pre-wrap; word-break: break-word;
  background: var(--bg-subtle); padding: 10px; border-radius: 6px;
}
</style>
