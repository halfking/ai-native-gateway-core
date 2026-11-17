<script setup lang="ts">
// StageTreeLegend.vue —— 请求阶段树的**图例**（2026-11-17 设计对齐轮）
//
// ★ 为什么要有图例（设计第 4 轮）：
//   阶段树有**七种**状态，其中两种都是灰（「未测量」＝没有信号、
//   「未到达」＝还没跑到）⇒ 不给图例，读者只能靠猜；
//   而这两种灰的含义**相反**（一个说「系统没做」，一个说「还没到」），
//   猜错就把「没数据」读成「系统没做这一步」。
//   ⇒ 图例把七态的字形 + 两条轨的方向写出来。

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { FULL_GLYPH, COMPACT_GLYPH, LEGEND_ORDER } from '../../lib/stageTreeGlyphs'

const { t } = useI18n()

const props = withDefaults(defineProps<{ compact?: boolean }>(), { compact: false })

/** 七态图例项（字形与状态一一对应；★ 文案逐字面量 t()）。
 *
 *  ★★ 字形来自**唯一真源** `lib/stageTreeGlyphs.ts`（设计规则 15）：
 *   紧凑模式下 done / pending / unmeasured 的字形**换成空串**（不是透明化）——
 *   透明化会让 ✓ 仍留在 DOM / 无障碍文本里 ⇒ 屏幕阅读器仍读出「完成」，
 *   而画面上什么都没有 ⇒ **图例与画面对不上**（2026-11-18 实测到的线上缺陷 F1）。
 *   ⇒ 图例与瓦片共用同一张字形表 ⇒ 同一条规定只有一份 ⇒ 不会再漂移。 */
const LEGEND = computed(() =>
  LEGEND_ORDER.map((state) => ({
    state,
    glyph: (props.compact ? COMPACT_GLYPH : FULL_GLYPH)[state] ?? '',
  })),
)

function stateLabel(state: string): string {
  switch (state) {
    case 'done':
      return t('liveStageTree.state.done')
    case 'active':
      return t('liveStageTree.state.active')
    case 'pending':
      return t('liveStageTree.state.pending')
    case 'skipped':
      return t('liveStageTree.state.skipped')
    case 'failed':
      return t('liveStageTree.state.failed')
    case 'warned':
      return t('liveStageTree.state.warned')
    case 'unmeasured':
      return t('liveStageTree.state.unmeasured')
    default:
      return ''
  }
}
</script>

<template>
  <div class="legend" data-testid="stage-tree-legend">
    <span v-for="item in LEGEND" :key="item.state" class="legend__item">
      <i class="legend__glyph" :class="`is-${item.state}`">{{ item.glyph }}</i>
      <span>{{ stateLabel(item.state) }}</span>
    </span>
    <span class="legend__sep"></span>
    <span class="legend__rail">
      <i></i>
      <span>{{ t('liveStageTree.legend.sendRail') }}</span>
    </span>
    <span class="legend__rail is-recv">
      <i></i>
      <span>{{ t('liveStageTree.legend.recvRail') }}</span>
    </span>
  </div>
</template>

<style scoped>
.legend {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  padding: 4px 12px;
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-surface);
  color: var(--kx-muted);
  font-size: var(--kx-text-2xs);
}

.legend__item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.legend__glyph {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 4px;
  border: 1px solid var(--kx-border);
  background: var(--kx-bg-container);
  font-style: normal;
  font-size: 10px;
}

.legend__glyph.is-done {
  color: var(--kx-success);
  border-color: var(--kx-success);
}
.legend__glyph.is-active {
  color: var(--kx-primary);
  border-color: var(--kx-primary);
}
.legend__glyph.is-failed {
  color: var(--kx-danger);
  border-color: var(--kx-danger);
}
.legend__glyph.is-warned {
  color: var(--kx-warning);
  border-color: var(--kx-warning);
}
.legend__glyph.is-skipped,
.legend__glyph.is-unmeasured,
.legend__glyph.is-pending {
  color: var(--kx-muted);
  border-style: dashed;
}

.legend__sep {
  width: 1px;
  height: 12px;
  background: var(--kx-border);
}

.legend__rail {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}

.legend__rail i {
  display: inline-block;
  width: 20px;
  height: 0;
  border-top: 2px solid var(--kx-primary);
}

.legend__rail.is-recv i {
  border-top-color: var(--kx-warning);
}
</style>
