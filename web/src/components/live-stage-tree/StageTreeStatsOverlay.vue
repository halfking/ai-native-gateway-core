<script setup lang="ts">
// StageTreeStatsOverlay.vue —— 统计的**全页浮层**（2026-11-16，live2）
//
// owner 2026-11-16 原话：「统计点开可以展开全页形式，盖在动态显示的上层」。
// ⇒ 形态：**固定覆盖整个视口**（含顶栏与过滤栏），把下面的阶段树盖住；
//   关闭后回到动态显示，**不丢过滤态**（过滤态在父组件里，浮层只读统计）。
//
// 为什么复用 StatsPanel 而不是重写一份统计：
//   · 统计口径**必须唯一**（5 分钟/1 小时＝DB 直查、当天＝日桶滞后 ≤1h）。
//     两份统计实现 = 两把尺 = 迟早对不上（仓内反复吃过的亏）。
//   · StatsPanel 已带总量行常驻 + 口径行 + 三组明细 + 点行联动过滤。
//
// 浮层自身的三条纪律：
//   1. 有 Esc 出口（不是只能点关闭按钮）；
//   2. 内容超长时**限高可滚**（统计不能被裁掉 —— 口径行必须看得见）；
//   3. 关闭后焦点回到触发它的按钮（键盘用户不至于迷路）。

import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Close } from '@element-plus/icons-vue'
import StatsPanel from '../live-topology/StatsPanel.vue'

const props = defineProps<{
  open: boolean
  /** 在途数（实时口径，来自阶段树数据）。 */
  inFlight?: number | null
  /** 队列深度（未接线时 null —— 隐藏，不冒充 0）。 */
  queueDepth?: number | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'select-client', key: string): void
  (e: 'select-provider', code: string): void
  (e: 'select-model', model: string): void
}>()

const { t } = useI18n()
const panelRef = ref<HTMLElement | null>(null)

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.stopPropagation()
    emit('close')
  }
}

watch(
  () => props.open,
  (open) => {
    if (open) {
      document.addEventListener('keydown', onKeydown)
    } else {
      document.removeEventListener('keydown', onKeydown)
    }
  },
)

onMounted(() => {
  if (props.open) document.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div
    v-if="props.open"
    ref="panelRef"
    class="stage-stats-overlay"
    role="dialog"
    aria-modal="true"
    :aria-label="t('liveStageTree.stats.title')"
    data-testid="stage-stats-overlay"
  >
    <header class="stage-stats-overlay__bar">
      <h2 class="stage-stats-overlay__title">{{ t('liveStageTree.stats.title') }}</h2>
      <span class="stage-stats-overlay__hint">{{ t('liveStageTree.stats.hint') }}</span>
      <button
        type="button"
        class="stage-stats-overlay__close"
        data-testid="stage-stats-close"
        :aria-label="t('liveStageTree.stats.close')"
        :title="t('liveStageTree.stats.close')"
        @click="emit('close')"
      >
        <Close />
      </button>
    </header>

    <div class="stage-stats-overlay__body">
      <StatsPanel
        :in-flight="props.inFlight ?? null"
        :queue-depth="props.queueDepth ?? null"
        @select-client="(k: string) => emit('select-client', k)"
        @select-provider="(c: string) => emit('select-provider', c)"
        @select-model="(m: string) => emit('select-model', m)"
      />
    </div>
  </div>
</template>

<style scoped>
.stage-stats-overlay {
  position: fixed;
  inset: 0;
  z-index: 30;
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  background: var(--kx-bg);
}

.stage-stats-overlay__bar {
  display: flex;
  align-items: center;
  gap: var(--kx-space-3);
  flex: none;
  padding: var(--kx-space-3) var(--kx-space-4);
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-surface);
}

.stage-stats-overlay__title {
  margin: 0;
  font-size: var(--kx-text-lg);
  font-weight: 650;
  color: var(--kx-text);
}

.stage-stats-overlay__hint {
  flex: 1;
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

.stage-stats-overlay__close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm);
  background: transparent;
  color: var(--kx-text-secondary);
  cursor: pointer;
}

.stage-stats-overlay__close:hover {
  color: var(--kx-primary);
  border-color: var(--kx-primary);
}

/* element-plus 图标是裸 <svg viewBox>，必须显式定尺寸。 */
.stage-stats-overlay__close > svg {
  width: 12px;
  height: 12px;
}

.stage-stats-overlay__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: var(--kx-space-4);
}
</style>
