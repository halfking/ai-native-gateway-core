<script setup lang="ts">
// UnifiedRequestSessionDrawer — dual-mode request/session detail shell.
// 2026-08-28: 实时流泳道点击 → 抽屉首屏。
//
// 数据来源（与 request_logs body 列已迁移到 request_logs_bodies 一致）：
//   - getRequestLogDetail (/api/logs/:id)：metadata 全量（token/cost/provider/
//     session_id/task_id/...）；首屏 omitBody 只取 metadata，对话/压缩/原始
//     JSON tab 切入时由 ensureBodies 按需补拉完整 request/response/outbound
//     body（后端 fetchRequestBodies 从 request_logs_bodies_hot (heap) →
//     request_logs_bodies (columnar) 二阶段读取）。
//   - getUnifiedRequestDetail (/api/admin/request-detail/:id)：memory/file
//     → request_logs → session_turns 多层回退的 unified facade。
//     与 /api/logs/:id 数据重复，主要用作 source 标签（memory/file/
//     request_logs/session_turns）和 in_flight/persisted 持久化阶段。
//   - getSessionSnapshot：会话级快照（标题、分析结果、最后模型/供应商），
//     fire-and-forget，带 loadSeq + AbortSignal 防止快速切换时串写。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ApiError } from '../../api/_core'
import { getRequestLogDetail, type RequestLogDetail } from '../../api/logs'
import {
  getUnifiedRequestDetail,
  type UnifiedRequestDetail,
} from '../../api/requestDetail'
import { getSessionSnapshot } from '../../api/sessions_v2'
import RequestOverviewPanel from './RequestOverviewPanel.vue'
import ConversationMessagesPanel from './ConversationMessagesPanel.vue'
import FlowTimingPanel from './FlowTimingPanel.vue'
import CompressionRedactionPanel from './CompressionRedactionPanel.vue'
import SessionTurnsSyncPane from './SessionTurnsSyncPane.vue'
import { formatJson } from './messageHelpers'
import { openRequestDetailPage } from '../../utils/openRequestDetailPage'

export type DetailMode = 'request' | 'session-turns'

const { t } = useI18n()

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
const sessionSnap = ref<Record<string, unknown> | null>(null)
const activeRequestId = ref<string | null>(null)
const warnings = ref<string[]>([])

const sessionId = computed(
  () => log.value?.gw_session_id || unified.value?.meta.gw_session_id || null,
)

// Body 优先级：unified（admin/request-detail，memory→request_logs→session_turns 多层回退）>
//   log（/api/logs/:id，来自 request_logs_bodies_hot/_bodies）。
// unified 在 in_flight（memory/file）路径下 body 才唯一可信；persisted 路径下
// 与 log 同源（都是 request_logs_bodies），互为备份。
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

// loadSeq：请求切换序号。快照/正文都是 fire-and-forget 异步，快速切换
// A→B 时，A 的晚到响应不得写入 B 的视图（与 useRequestDetailLoader 同款守卫）。
let loadSeq = 0
let snapshotAbortController: AbortController | null = null
// bodiesLoadedFor 记录哪个请求已拿到完整 body，切 tab 不重复拉取。
let bodiesLoadedFor = ''
const bodiesLoading = ref(false)

watch(
  () => props.requestId,
  async (id) => {
    activeRequestId.value = id
    loadSeq++
    if (snapshotAbortController) {
      snapshotAbortController.abort()
      snapshotAbortController = null
    }
    bodiesLoadedFor = ''
    log.value = null
    unified.value = null
    sessionSnap.value = null
    error.value = ''
    warnings.value = []
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
  warnings.value = []
  const currentSeq = loadSeq
  try {
    // 首屏 metadata-only（omitBody）：抽屉默认停在概览，QA 卡片用
    // request_preview/response_preview 兜底；对话/压缩/原始JSON tab 切入时
    // 再由 ensureBodies 按需拉完整正文，避免每次点击色块都打列存冷路径。
    // 两端点并行拉取，失败一方降级（catch → null）由另一方兜底，
    // 失败原因收集到 warnings 显式提示。
    const [u, meta] = await Promise.all([
      getUnifiedRequestDetail(id, { omitBody: true }).catch((e: unknown) => {
        recordEndpointFailure('admin/request-detail', e)
        return null
      }),
      getRequestLogDetail(id, { omitBody: true }).catch((e: unknown) => {
        recordEndpointFailure('/api/logs/:id', e)
        return null
      }),
    ])
    if (currentSeq !== loadSeq) return
    unified.value = u
    log.value = meta
    if (!u && !meta) {
      error.value = t('requestDetail.drawer.notFound')
      return
    }
    const sid = log.value?.gw_session_id || unified.value?.meta.gw_session_id
    if (sid) {
      snapshotAbortController = new AbortController()
      const signal = snapshotAbortController.signal
      void getSessionSnapshot(sid, { signal })
        .then((snap) => {
          if (currentSeq === loadSeq && !signal.aborted) {
            sessionSnap.value = snap as Record<string, unknown>
          }
        })
        .catch((e: unknown) => {
          if (signal.aborted) return
          recordEndpointFailure('sessions/:id/snapshot', e)
          sessionSnap.value = null
        })
    }
  } catch (e: unknown) {
    if (currentSeq === loadSeq) {
      error.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (currentSeq === loadSeq) loading.value = false
  }
}

// ensureBodies 按需加载完整正文（不带 omitBody），供对话/压缩/原始JSON tab。
async function ensureBodies(id: string) {
  if (!id || bodiesLoadedFor === id) return
  if (unified.value?.bodies || log.value?.request_body) {
    bodiesLoadedFor = id
    return
  }
  const seq = loadSeq
  bodiesLoading.value = true
  try {
    const [fullLog, fullUnified] = await Promise.all([
      getRequestLogDetail(id).catch(() => null),
      getUnifiedRequestDetail(id).catch(() => null),
    ])
    if (seq !== loadSeq || id !== activeRequestId.value) return
    if (fullLog) log.value = fullLog
    if (fullUnified) unified.value = fullUnified
    if (fullLog || fullUnified) bodiesLoadedFor = id
  } finally {
    if (seq === loadSeq) bodiesLoading.value = false
  }
}

// 切到需要正文的 tab 时按需加载（概览用 preview 兜底，不触发）。
watch([tab, activeRequestId], () => {
  if (tab.value === 'chat' || tab.value === 'compress' || tab.value === 'raw') {
    const id = activeRequestId.value
    if (id) void ensureBodies(id)
  }
})

function recordEndpointFailure(endpoint: string, e: unknown) {
  const detail = e instanceof ApiError
    ? `HTTP ${e.status} ${e.message}`
    : e instanceof Error
      ? e.message
      : String(e)
  warnings.value.push(`${endpoint}: ${detail}`)
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

// 2026-08-28: 把当前抽屉中展示的请求在新标签页里打开全屏详情页
function openActiveInFullscreen() {
  const id = activeRequestId.value
  if (!id) return
  const targetTab = mapDrawerTabToFullscreen(tab.value)
  openRequestDetailPage(id, {
    mode: viewMode.value === 'session-turns' ? 'session-turns' : 'request',
    tab: viewMode.value === 'session-turns' ? undefined : targetTab,
  })
}

// 抽屉 tab → 全屏 page tab 映射（保持用户体验一致）
function mapDrawerTabToFullscreen(t: Tab): string {
  if (t === 'routing') return 'attempts'
  return t
}

const openFullscreenTitle = computed(() =>
  activeRequestId.value ? t('requestDetail.drawer.openInNewTab', { id: activeRequestId.value }) : '',
)
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
        <h3>{{ t('requestDetail.drawer.title') }}</h3>
        <div class="mode-seg">
          <button
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': viewMode === 'request' }"
            @click="viewMode = 'request'"
          >
            {{ t('requestDetail.drawer.single') }}
          </button>
          <button
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': viewMode === 'session-turns' }"
            :disabled="!sessionId"
            @click="switchToSession"
          >
            {{ t('requestDetail.drawer.turns') }}
          </button>
        </div>
        <!-- 2026-08-28: 抽屉内新增「打开全页」入口，跳到独立 /request-detail 页 -->
        <button
          v-if="activeRequestId"
          type="button"
          class="btn btn-sm"
          :title="openFullscreenTitle"
          @click="openActiveInFullscreen"
        >
          {{ t('requestDetail.drawer.openFullPage') }}
        </button>
        <button class="btn btn-sm" type="button" @click="emit('close')">{{ t('requestDetail.drawer.close') }}</button>
      </div>

      <div v-if="loading" class="drawer-loading">{{ t('requestDetail.drawer.loading') }}</div>
      <div v-else-if="error && !log && !unified" class="drawer-error">{{ error }}</div>

      <template v-else>
        <p v-if="error" class="drawer-error drawer-error-inline">{{ error }}</p>
        <!-- 2026-08-28: 部分端点失败时显式列出，便于排查（默认 Promise.all
             catch → null 会静默降级，用户感知不到端点失败）。 -->
        <ul v-if="warnings.length" class="drawer-warnings" data-testid="drawer-warnings">
          <li v-for="(w, i) in warnings" :key="i">{{ w }}</li>
        </ul>

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
              v-for="tabKey in ([
                'overview',
                'chat',
                'flow',
                'compress',
                'routing',
                'raw',
              ] as Tab[])"
              :key="tabKey"
              type="button"
              class="btn btn-sm"
              :class="{ 'btn-primary': tab === tabKey }"
              :disabled="tabKey === 'routing' && !hasRouting"
              @click="tab = tabKey"
            >
              {{ t(`requestDetail.drawer.tabs.${tabKey}`) }}
            </button>
            <button
              v-if="sessionId"
              type="button"
              class="btn btn-sm"
              @click="emit('filterSession', sessionId!)"
            >
              {{ t('requestDetail.drawer.filterSessions') }}
            </button>
          </div>

          <div class="drawer-body-scroll">
            <div v-if="bodiesLoading" class="drawer-loading">
              {{ t('requestDetail.drawer.loading') }}
            </div>
            <RequestOverviewPanel
              v-if="tab === 'overview'"
              :log="log"
              :unified="unified"
              :request-body="requestBody"
              :response-body="responseBody"
              :session-snap="sessionSnap"
            />
            <ConversationMessagesPanel
              v-else-if="tab === 'chat'"
              :body="requestBody"
              :response-body="responseBody"
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
.drawer-warnings {
  list-style: none; margin: 0 0 8px; padding: 8px 12px;
  border: 1px solid color-mix(in srgb, var(--kx-warning) 50%, transparent);
  background: color-mix(in srgb, var(--kx-warning) 10%, transparent);
  border-radius: 6px; font-size: 11px; color: var(--text);
}
.drawer-warnings li { font-family: var(--font-mono, ui-monospace, monospace); word-break: break-all; }
.raw-pre {
  font-size: 11px; white-space: pre-wrap; word-break: break-word;
  background: var(--bg-subtle); padding: 10px; border-radius: 6px;
}
</style>
