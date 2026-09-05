<script setup lang="ts">
// UnifiedRequestSessionDrawer — dual-mode request/session detail shell.
// 2026-08-28: 实时流泳道点击 → 抽屉首屏。
// 2026-09-05 (F2-#5): loading/state 迁移到共享组合式函数
//   useRequestDetailLoader（per-request 45s 缓存 + loadSeq 竞态守卫 +
//   resetTransientState + ensureBodies 以 requestId 为键），修复抽屉自管
//   加载的三类缺陷：并发覆盖（bodyless unified 覆盖已拉取的完整 body）、
//   换轮不清态（A 轮正文串到 B 轮视图）、bodiesLoading 粘滞。
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
//     fire-and-forget，由组合式函数以 loadSeq + AbortSignal 防止快速切换串写。
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  useRequestDetailLoader,
  REQUEST_DETAIL_NOT_FOUND,
} from '../../composables/useRequestDetailLoader'
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

// State (log/unified/sessionSnap/warnings/bodies*) lives in the shared
// loader. loadMeta resets transient state per request and bumps the seq,
// so an in-flight response from request A can never land in request B's
// view, and bodiesLoading cannot get stuck after a switch.
const loader = useRequestDetailLoader()
const {
  metaLoading: loading,
  metaError,
  metaWarnings: warnings,
  log,
  unified,
  sessionSnap,
  sessionId,
  activeRequestId,
  requestBody,
  responseBody,
  outboundBody,
  bodiesLoading,
  loadMeta,
  ensureBodies,
  dispose,
} = loader

// metaError carries the REQUEST_DETAIL_NOT_FOUND sentinel for the
// "both endpoints empty" case; the drawer renders it localized.
const error = computed(() =>
  metaError.value === REQUEST_DETAIL_NOT_FOUND
    ? t('requestDetail.drawer.notFound')
    : metaError.value,
)

const hasRouting = computed(() => !!log.value?.routing_attempts?.attempts?.length)

// Body 优先级（unified > log）与多源回退语义由组合式函数的
// requestBody/responseBody/outboundBody computed 提供，与迁移前一致。

// Only these tabs need full bodies; overview stays on the
// request_preview/response_preview fallback (no body fetch).
function tabNeedsBodies(current: Tab): boolean {
  return current === 'chat' || current === 'compress' || current === 'raw'
}

async function loadActiveTabBodies() {
  const id = activeRequestId.value
  if (!id || !tabNeedsBodies(tab.value)) return
  // ensureBodies is keyed by requestId inside the composable: it re-checks
  // activeRequestId after the fetch and records per-request body state in
  // the per-request cache, so a turn switch can neither reuse the previous
  // turn's bodies nor apply a stale fetch to the new view.
  await ensureBodies(id)
}

watch(
  () => props.requestId,
  async (id) => {
    viewMode.value = props.initialViewMode
    tab.value = props.initialTraceOpen ? 'flow' : 'overview'
    if (!id) return
    await loadMeta(id)
    // The prop may have changed again while loadMeta was in flight — do not
    // start a body fetch for a request that is no longer active.
    if (id !== activeRequestId.value) return
    await loadActiveTabBodies()
  },
  { immediate: true },
)

// Switching to a body-consuming tab triggers the phased body fetch.
watch(tab, () => {
  void loadActiveTabBodies()
})

onUnmounted(() => dispose())

async function onSelectTurn(requestId: string, _turn: number) {
  // Turn switch inside the session pane goes through the same path as the
  // props watcher: loadMeta clears the previous turn's log/unified/snap/
  // warnings and body state before fetching, so turn B never renders
  // turn A's body (the old self-managed guard saw the residue and marked
  // the new turn's bodies as already loaded).
  await loadMeta(requestId)
  if (requestId !== activeRequestId.value) return
  await loadActiveTabBodies()
}

function openAsRequest(requestId: string) {
  viewMode.value = 'request'
  void (async () => {
    await loadMeta(requestId)
    if (requestId !== activeRequestId.value) return
    await loadActiveTabBodies()
  })()
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
