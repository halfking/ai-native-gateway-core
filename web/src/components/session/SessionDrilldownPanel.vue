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
import { ref } from 'vue'
import OnlineSessionsPanel from './OnlineSessionsPanel.vue'
import SessionTurnsTimeline from './SessionTurnsTimeline.vue'
import TurnDigestDrawer from './TurnDigestDrawer.vue'
import { useTurnTitleSummary } from '../../composables/useTurnTitleSummary'
import { openRequestDetailPage } from '../../utils/openRequestDetailPage'

const selectedSessionId = ref<string | null>(null)
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

function openSession(sessionId: string) {
  clearDigestState()
  selectedSessionId.value = sessionId
}

function backToList() {
  clearDigestState()
  selectedSessionId.value = null
}

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
      <OnlineSessionsPanel @select="openSession" />
    </template>
    <template v-else>
      <div class="sdp-detail">
        <div class="sdp-back-row">
          <button type="button" class="sdp-back" @click="backToList">
            &larr; 返回在线会话列表
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
