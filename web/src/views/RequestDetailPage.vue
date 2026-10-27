<script setup lang="ts">
// RequestDetailPage.vue —— 请求详情**全页**（2026-10-18 owner 要求：
// 请求信息处加链接，可全页形式打开请求详情页）。
//
// 路由：/live/request/:requestId（详情页从 /live 面板进入，非导航项，
// navCoverage 按孤儿登记）。内容与内联放大面板共享 RequestDetailView
// —— 同一请求在两处的字段必然一致。
//
// 数据语义：实时 store 是**内存滚动窗口**，刷新/直链时请求可能已不在
// 窗口内 —— 找不到就如实说「不在当前实时窗口内」，绝不渲染空壳冒充。
// 刻意读原始 store（不经过滤开关）：从面板点开被隐藏的探测请求也有效。

import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useLiveStream } from '../composables/useLiveStream'
import { useLiveTopology } from '../composables/useLiveTopology'
import RequestDetailView from '../components/live-topology/RequestDetailView.vue'

const route = useRoute()
const { t } = useI18n()
// 建立 SSE 连接（引用计数单例）：深链/刷新进入时 store 可能是空的，
// 首帧 initial_data 会回放最近窗口内的请求 —— 该请求还在回放窗口里，
// 详情就能真渲染；滚出窗口则如实走「不在实时窗口内」分支。
const { initialDataReceived } = useLiveStream()
const topo = useLiveTopology()

const requestId = computed(() => String(route.params.requestId ?? ''))
const entry = computed(() => topo.panelEntryFor(requestId.value))
const decisions = computed(() => (entry.value ? topo.decisionsFor(requestId.value) : []))
/** SSE 已联通但仍未见到首帧：这是「还在等」不是「没有」——分开说。 */
const waitingFirstFrame = computed(() => !entry.value && !initialDataReceived.value)
</script>

<template>
  <div class="rdp" data-testid="request-detail-page">
    <header class="rdp__head">
      <router-link class="rdp__back" to="/live" data-testid="request-detail-back">
        ‹ {{ t('liveTopology.page.back') }}
      </router-link>
      <h2 class="rdp__title">{{ t('liveTopology.zoom.title') }}</h2>
    </header>

    <div v-if="entry" class="rdp__body">
      <RequestDetailView :entry="entry" :decisions="decisions" />
    </div>

    <!-- 首帧未到 ≠ 没有：SSE 刚连上还在等回放，先说「正在取」而不是判死。 -->
    <div v-else-if="waitingFirstFrame" class="rdp__missing" data-testid="request-detail-waiting">
      <p class="rdp__missing-hint">{{ t('liveTopology.page.waiting') }}</p>
    </div>

    <!-- 找不到 ≠ 空详情：实时窗口已滚过去（或直链进入）要明说，
         否则用户会以为「这条请求没有详情」。 -->
    <div v-else class="rdp__missing" data-testid="request-detail-missing">
      <p class="rdp__missing-title">{{ t('liveTopology.page.notFoundTitle') }}</p>
      <p class="rdp__missing-hint">{{ t('liveTopology.page.notFoundHint') }}</p>
      <p class="rdp__missing-id">{{ requestId }}</p>
    </div>
  </div>
</template>

<style scoped>
.rdp {
  display: flex;
  flex-direction: column;
  gap: 8px;
  height: 100%;
  min-height: 0;
  padding: 12px 16px;
  overflow-y: auto;
}

.rdp__head {
  display: flex;
  align-items: baseline;
  gap: 12px;
  flex: none;
}

.rdp__back {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-primary);
  text-decoration: none;
  white-space: nowrap;
}

.rdp__back:hover {
  text-decoration: underline;
}

.rdp__title {
  margin: 0;
  font-size: 16px;
  color: var(--kx-text);
}

.rdp__body {
  max-width: 860px;
  padding: 12px 12px;
  border: 1px solid var(--kx-border);
  border-radius: 12px;
  background: var(--kx-surface);
}

.rdp__missing {
  max-width: 560px;
  padding: 16px 16px;
  border: 1px dashed var(--kx-border);
  border-radius: 12px;
  background: var(--kx-surface);
}

.rdp__missing-title {
  margin: 0 0 4px;
  font-size: var(--kx-font-md, 14px);
  color: var(--kx-text);
}

.rdp__missing-hint {
  margin: 0;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-muted);
}

.rdp__missing-id {
  margin: 8px 0 0;
  font-size: 11px;
  color: var(--kx-muted);
  font-family: monospace;
  word-break: break-all;
}
</style>
