<script setup lang="ts">
// UnifiedRequestSessionDrawer — dual-mode request/session detail shell.
// 2026-08-28: 实时流泳道点击 → 抽屉首屏。
//
// 数据来源（与 request_logs body 列已迁移到 request_logs_bodies 一致）：
//   - getRequestLogDetail (/api/logs/:id)：metadata 全量（token/cost/provider/
//     session_id/task_id/...）+ request_body/response_body/outbound_body
//     由 admin/logs.go 的 fetchRequestBodies/fetchRequestOutboundBody
//     从 request_logs_bodies_hot (heap) → request_logs_bodies (columnar)
//     二阶段读取。后端不识别 omit_body，因此前端 Phase A/Phase B
//     拆分不再有意义，一次调用即可拿到完整 payload。
//   - getUnifiedRequestDetail (/api/admin/request-detail/:id)：memory/file
//     → request_logs → session_turns 多层回退的 unified facade。
//     与 /api/logs/:id 数据重复，主要用作 source 标签（memory/file/
//     request_logs/session_turns）和 in_flight/persisted 持久化阶段。
//   - getSessionSnapshot：会话级快照（标题、分析结果、最后模型/供应商）。
import { computed, ref, watch } from 'vue'
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

watch(
  () => props.requestId,
  async (id) => {
    activeRequestId.value = id
    log.value = null
    unified.value = null
    sessionSnap.value = null
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
    // 单次请求拿到 metadata + body（两个端点都无视 omit_body），
    // Promise.all 并行拉取，失败一方降级（catch → null），由另一方兜底。
    const [u, meta] = await Promise.all([
      getUnifiedRequestDetail(id).catch(() => null),
      getRequestLogDetail(id).catch(() => null),
    ])
    unified.value = u
    log.value = meta
    if (!u && !meta) {
      error.value = '请求详情未找到'
      return
    }
    const sid = log.value?.gw_session_id || unified.value?.meta.gw_session_id
    if (sid) {
      void getSessionSnapshot(sid)
        .then((snap) => { sessionSnap.value = snap as Record<string, unknown> })
        .catch(() => { sessionSnap.value = null })
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
  activeRequestId.value ? `在独立页面打开请求 ${activeRequestId.value}` : '',
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
        <!-- 2026-08-28: 抽屉内新增「打开全页」入口，跳到独立 /request-detail 页 -->
        <button
          v-if="activeRequestId"
          type="button"
          class="btn btn-sm"
          :title="openFullscreenTitle"
          @click="openActiveInFullscreen"
        >
          打开全页
        </button>
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
.raw-pre {
  font-size: 11px; white-space: pre-wrap; word-break: break-word;
  background: var(--bg-subtle); padding: 10px; border-radius: 6px;
}
</style>
