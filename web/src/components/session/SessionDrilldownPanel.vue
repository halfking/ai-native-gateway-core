<script setup lang="ts">
/**
 * SessionDrilldownPanel — 会话与统计 tab 下钻容器（V3.3-OBS OBS-FE5, 26 号 §5）
 *
 * 二级结构：在线会话列表（OnlineSessionsPanel）→ 会话详情轮次时间线
 * （SessionTurnsTimeline，含内联子请求树）。
 *
 * 数据均为手动加载（进入时拉一次 + 手动刷新/游标加载更多），无轮询，
 * 因此不需要 probeStreamStore 式 visibility 门控（页面不可见时零后台流量）。
 *
 * 设计约束：颜色只用 var(--kx-*)。
 */
import { onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import OnlineSessionsPanel from './OnlineSessionsPanel.vue'
import SessionCatalogPanel from './SessionCatalogPanel.vue'
import SessionTurnsTimeline from './SessionTurnsTimeline.vue'
import TurnDigestDrawer from './TurnDigestDrawer.vue'
import { useTurnTitleSummary } from '../../composables/useTurnTitleSummary'
import { openRequestDetailPage } from '../../utils/openRequestDetailPage'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

type ListMode = 'online' | 'catalog'

function queryString(key: string): string {
  const raw = route.query[key]
  return typeof raw === 'string' ? raw : ''
}

const listMode = ref<ListMode>(queryString('slist') === 'catalog' ? 'catalog' : 'online')
const selectedSessionId = ref<string | null>(queryString('session') || null)
const digestOpen = ref(false)
const digestTurnNo = ref<number | null>(null)
// 2026-09-05 audit F2-#9: pass per-turn title/summary (turns list, cached per
// session) into the drawer — the detail response never carries them.
const { ensureTurnTitleSummary, lookupTurnTitleSummary } = useTurnTitleSummary()
const digestTitle = ref('')
const digestSummary = ref('')

function clearDigestState() {
  digestOpen.value = false
  digestTurnNo.value = null
  digestTitle.value = ''
  digestSummary.value = ''
}

function writeQuery(next: { session?: string | null; slist?: ListMode | null }) {
  const query: Record<string, string | string[]> = {}
  for (const [key, value] of Object.entries(route.query)) {
    if (value == null) continue
    query[key] = value as string | string[]
  }
  if (next.slist) query.slist = next.slist
  else delete query.slist
  if (next.session) query.session = next.session
  else delete query.session
  void router.replace({ query })
}

function openSession(sessionId: string) {
  clearDigestState()
  selectedSessionId.value = sessionId
  writeQuery({ session: sessionId, slist: listMode.value === 'catalog' ? 'catalog' : null })
}

function backToList() {
  clearDigestState()
  selectedSessionId.value = null
  writeQuery({ session: null, slist: listMode.value === 'catalog' ? 'catalog' : null })
}

function setListMode(mode: ListMode) {
  listMode.value = mode
  writeQuery({ session: selectedSessionId.value, slist: mode === 'catalog' ? 'catalog' : null })
}

onMounted(() => {
  const session = queryString('session')
  if (session) selectedSessionId.value = session
})

watch(
  () => route.query.session,
  (raw) => {
    const next = typeof raw === 'string' && raw ? raw : null
    if (next === selectedSessionId.value) return
    clearDigestState()
    selectedSessionId.value = next
  },
)

function openRequest(payload: { requestId: string }) {
  openRequestDetailPage(payload.requestId, { mode: 'session-turns' })
}

function showDigest(payload: { turnNumber: number }) {
  if (!selectedSessionId.value) return
  const sessionId = selectedSessionId.value
  digestTurnNo.value = payload.turnNumber
  digestTitle.value = ''
  digestSummary.value = ''
  digestOpen.value = true
  void ensureTurnTitleSummary(sessionId).then(() => {
    // Ignore the resolved lookup if the user already switched turns/sessions.
    if (selectedSessionId.value !== sessionId || digestTurnNo.value !== payload.turnNumber) return
    const item = lookupTurnTitleSummary(sessionId, payload.turnNumber)
    digestTitle.value = item.title
    digestSummary.value = item.summary
  })
}

function closeDigest() {
  clearDigestState()
}
</script>

<template>
  <div class="sdp" data-testid="session-drilldown-panel">
    <template v-if="!selectedSessionId">
      <div class="sdp-mode" role="tablist">
        <button
          type="button"
          class="sdp-mode-btn"
          :class="{ 'sdp-mode-btn--active': listMode === 'online' }"
          data-testid="slist-online"
          @click="setListMode('online')"
        >
          {{ t('sessions.catalog.online') }}
        </button>
        <button
          type="button"
          class="sdp-mode-btn"
          :class="{ 'sdp-mode-btn--active': listMode === 'catalog' }"
          data-testid="slist-catalog"
          @click="setListMode('catalog')"
        >
          {{ t('sessions.catalog.directory') }}
        </button>
      </div>
      <OnlineSessionsPanel v-if="listMode === 'online'" @select="openSession" />
      <SessionCatalogPanel v-else @select="openSession" />
    </template>
    <template v-else>
      <div class="sdp-detail">
        <div class="sdp-back-row">
          <button type="button" class="sdp-back" @click="backToList">
            &larr; {{ t('sessions.catalog.backToList') }}
          </button>
        </div>
        <SessionTurnsTimeline
          :session-id="selectedSessionId"
          :key="selectedSessionId"
          @open-request="openRequest"
          @show-digest="showDigest"
        />
        <TurnDigestDrawer
          v-if="selectedSessionId"
          v-model="digestOpen"
          :session-id="selectedSessionId"
          :turn-no="digestTurnNo"
          :title="digestTitle"
          :summary="digestSummary"
          @close="closeDigest"
        />
      </div>
    </template>
  </div>
</template>

<style scoped>
.sdp {
  margin-bottom: 20px;
}
.sdp-mode {
  display: inline-flex;
  margin-bottom: 10px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  overflow: hidden;
}
.sdp-mode-btn {
  border: 0;
  background: var(--kx-surface);
  color: var(--kx-text);
  padding: 6px 14px;
  font-size: 13px;
  cursor: pointer;
}
.sdp-mode-btn--active {
  background: var(--kx-primary);
  color: var(--on-primary);
}
.sdp-back-row {
  display: flex;
  margin-bottom: 10px;
}
.sdp-back {
  font-size: 13px;
  padding: 4px 12px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-text);
  cursor: pointer;
}
.sdp-back:hover {
  border-color: var(--kx-primary);
  color: var(--kx-primary);
}
</style>
