<script setup lang="ts">
// UnifiedRequestSessionDrawer — dual-mode request/session detail shell.
import { computed, ref, watch } from 'vue'
import { getRequestLogDetail, type RequestLogDetail } from '../../api/logs'
import {
  getUnifiedRequestDetail,
  type UnifiedRequestDetail,
} from '../../api/requestDetail'
import RequestOverviewPanel from './RequestOverviewPanel.vue'
import ConversationMessagesPanel from './ConversationMessagesPanel.vue'
import FlowTimingPanel from './FlowTimingPanel.vue'
import CompressionRedactionPanel from './CompressionRedactionPanel.vue'
import SessionTurnsSyncPane from './SessionTurnsSyncPane.vue'
import { formatJson } from './messageHelpers'

export type DetailMode = 'request' | 'session-turns'

const props = withDefaults(defineProps<{
  requestId: string | null
  mode?: 'default' | 'request-logs'
  initialTraceOpen?: boolean
  stackLevel?: 'default' | 'nested'
  initialViewMode?: DetailMode
}>(), {
  mode: 'default',
  initialTraceOpen: false,
  stackLevel: 'default',
  initialViewMode: 'request',
})

const emit = defineEmits<{
  close: []
  filterSession: [sessionId: string]
  openRequest: [requestId: string]
  sessionTitleChanged: [{ taskId: string; sessionId: string | null; title: string | null }]
}>()

type Tab = 'overview' | 'chat' | 'flow' | 'compress' | 'routing' | 'raw'

const viewMode = ref<DetailMode>(props.initialViewMode)
const tab = ref<Tab>(props.initialTraceOpen ? 'flow' : 'overview')
const loading = ref(false)
const error = ref('')
const log = ref<RequestLogDetail | null>(null)
const unified = ref<UnifiedRequestDetail | null>(null)
const activeRequestId = ref<string | null>(null)

const sessionId = computed(
  () => log.value?.gw_session_id || unified.value?.meta.gw_session_id || null,
)

const requestBody = computed(
  () => unified.value?.bodies?.request_body ?? log.value?.request_body ?? null,
)
const responseBody = computed(
  () => unified.value?.bodies?.response_body ?? log.value?.response_body ?? null,
)
const outboundBody = computed(
  () => unified.value?.bodies?.outbound_body ?? log.value?.outbound_body ?? null,
)

const hasRouting = computed(() => !!log.value?.routing_attempts?.attempts?.length)

watch(
  () => props.requestId,
  async (id) => {
    activeRequestId.value = id
    log.value = null
    unified.value = null
    error.value = ''
    viewMode.value = props.initialViewMode
    tab.value = props.initialTraceOpen ? 'flow' : 'overview'
    if (!id) return
    await loadRequest(id)
  },
  { immediate: true },
)

async function loadRequest(id: string) {
  loading.value = true
  error.value = ''
  try {
    const [u, meta] = await Promise.all([
      getUnifiedRequestDetail(id).catch(() => null),
      getRequestLogDetail(id, { omitBody: true }).catch(() => null),
    ])
    unified.value = u
    log.value = meta
    if (!u && !meta) {
      error.value = '请求详情未找到'
      return
    }
    // Fill bodies: prefer unified; else full log detail.
    if (u?.bodies) {
      // already have bodies
    } else if (meta) {
      try {
        const full = await getRequestLogDetail(id)
        log.value = { ...meta, ...full }
      } catch {
        /* meta-only ok */
      }
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function onSelectTurn(requestId: string, _turn: number) {
  activeRequestId.value = requestId
  await loadRequest(requestId)
}

function openAsRequest(requestId: string) {
  viewMode.value = 'request'
  activeRequestId.value = requestId
  void loadRequest(requestId)
  emit('openRequest', requestId)
}

function switchToSession() {
  if (sessionId.value) viewMode.value = 'session-turns'
}
</script>

<template>
  <div
    v-if="requestId"
    class="drawer-backdrop"
    :class="{ 'drawer-backdrop--nested': stackLevel === 'nested' }"
    @click="emit('close')"
  >
    <div class="drawer-panel card drawer-panel-wide" @click.stop>
      <div class="drawer-header">
        <h3>请求/会话详情</h3>
        <div class="mode-seg">
          <button
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': viewMode === 'request' }"
            @click="viewMode = 'request'"
          >
            单请求
          </button>
          <button
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': viewMode === 'session-turns' }"
            :disabled="!sessionId"
            @click="switchToSession"
          >
            会话轮次
          </button>
        </div>
        <button class="btn btn-sm" type="button" @click="emit('close')">关闭</button>
      </div>

      <div v-if="loading" class="drawer-loading">加载中…</div>
      <div v-else-if="error && !log && !unified" class="drawer-error">{{ error }}</div>

      <template v-else>
        <p v-if="error" class="drawer-error drawer-error-inline">{{ error }}</p>

        <template v-if="viewMode === 'session-turns' && sessionId">
          <SessionTurnsSyncPane
            :session-id="sessionId"
            :active-request-id="activeRequestId"
            :request-body="requestBody"
            :response-body="responseBody"
            @select-request="onSelectTurn"
            @open-as-request="openAsRequest"
          />
        </template>

        <template v-else>
          <div class="tab-row">
            <button
              v-for="t in ([
                ['overview', '概览'],
                ['chat', '对话'],
                ['flow', '流程'],
                ['compress', '压缩与脱敏'],
                ['routing', '路由'],
                ['raw', '原始JSON'],
              ] as [Tab, string][])"
              :key="t[0]"
              type="button"
              class="btn btn-sm"
              :class="{ 'btn-primary': tab === t[0] }"
              :disabled="t[0] === 'routing' && !hasRouting"
              @click="tab = t[0]"
            >
              {{ t[1] }}
            </button>
            <button
              v-if="sessionId"
              type="button"
              class="btn btn-sm"
              @click="emit('filterSession', sessionId!)"
            >
              筛选会话
            </button>
          </div>

          <div class="drawer-body-scroll">
            <RequestOverviewPanel v-if="tab === 'overview'" :log="log" :unified="unified" />
            <ConversationMessagesPanel
              v-else-if="tab === 'chat'"
              :body="requestBody"
            />
            <FlowTimingPanel v-else-if="tab === 'flow'" :request-id="activeRequestId" />
            <CompressionRedactionPanel
              v-else-if="tab === 'compress'"
              :session-id="sessionId"
              :request-id="activeRequestId"
              :request-body="requestBody"
              :outbound-body="outboundBody"
              :response-body="responseBody"
            />
            <pre v-else-if="tab === 'routing'" class="raw-pre">{{
              formatJson(log?.routing_attempts)
            }}</pre>
            <pre v-else class="raw-pre">{{
              formatJson({
                unified,
                request_body: requestBody,
                outbound_body: outboundBody,
                response_body: responseBody,
              })
            }}</pre>
          </div>
        </template>
      </template>
    </div>
  </div>
</template>

<style scoped>
.drawer-backdrop {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.45);
  z-index: 2000; display: flex; justify-content: flex-end;
}
.drawer-backdrop--nested { z-index: 3100; }
.drawer-panel-wide {
  width: min(920px, 96vw); height: 100%; overflow: auto;
  background: var(--bg-card, var(--card)); padding: 16px; box-sizing: border-box;
}
.drawer-header {
  display: flex; align-items: center; gap: 12px; margin-bottom: 12px;
}
.drawer-header h3 { margin: 0; flex: 1; font-size: 16px; }
.mode-seg { display: flex; gap: 4px; }
.tab-row { display: flex; flex-wrap: wrap; gap: 6px; margin: 10px 0; }
.drawer-body-scroll { max-height: calc(100vh - 140px); overflow: auto; }
.drawer-loading, .drawer-error { padding: 16px; }
.drawer-error { color: var(--danger); }
.drawer-error-inline { font-size: 12px; margin-bottom: 8px; }
.raw-pre {
  font-size: 11px; white-space: pre-wrap; word-break: break-word;
  background: var(--bg-subtle); padding: 10px; border-radius: 6px;
}
</style>
