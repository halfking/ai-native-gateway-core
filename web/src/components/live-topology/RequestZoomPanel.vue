<script setup lang="ts">
// RequestZoomPanel.vue —— 「点击请求行」弹出的内联摘要浮层（2026-10-27 恢复）
//
// 交互是**两步**（owner 2026-10-27 口径）：点行 → 本浮层弹出**摘要**；
// 浮层里的「查看全景」→ 全页详情 /request-detail/:id 开新窗。
// 2026-10-23 曾把点行改成直接开新页（跳过摘要），属主实测后要求改回旧版两步流。
//
// 形态纪律：内联覆盖面板，**不是弹窗** —— 拓扑图仍在视野里（本仓 ui-audit
// 对弹层有形态契约，且弹窗会遮挡拓扑）。内容本体在 RequestDetailView
// （与全页详情路由共享，两处字段必然一致）；本组件只负责覆盖层外观
// （定位/关闭/全景入口）。
//
// 样式：抬升只用 --kx-shadow-md、**不带闭合描边** —— craft-floor 把
// 「同一表面既描边又投影」钉成幽灵卡片（V1），仓内浮层先例
// （ApiKeySelectModal 等）同款都是纯抬升。图标用 @element-plus/icons-vue
// SVG（craft-floor 禁字形当图标）；裸 <svg viewBox> 不带 width/height 会被
// 浏览器按包含块宽度渲染，必须显式定尺寸。

import { useI18n } from 'vue-i18n'
import { Close, TopRight } from '@element-plus/icons-vue'
import type { ActionEvent } from '../../composables/liveStreamStore'
import type { PanelEntry } from '../../composables/useLiveTopology'
import RequestDetailView from './RequestDetailView.vue'

withDefaults(
  defineProps<{
    entry: PanelEntry
    decisions: ActionEvent[]
    /** 浮层锚在哪一侧的角落（跟随点击的那一列：左栏点击锚左下、右栏锚右下）。 */
    side?: 'left' | 'right'
  }>(),
  { side: 'right' },
)

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'open-page', entry: PanelEntry): void
}>()

const { t } = useI18n()
</script>

<template>
  <section class="zoom" :class="{ 'zoom--left': side === 'left' }" role="region" :aria-label="t('liveTopology.zoom.title')">
    <header class="zoom__head">
      <span class="zoom__title">{{ t('liveTopology.zoom.title') }}</span>
      <span class="zoom__actions">
        <button
          type="button"
          class="zoom__open"
          data-testid="zoom-open-page"
          :aria-label="t('liveTopology.zoom.openPage')"
          :title="t('liveTopology.zoom.openPage')"
          @click="emit('open-page', entry)"
        ><TopRight />{{ t('liveTopology.zoom.openPage') }}</button>
        <button
          type="button"
          class="zoom__close"
          :aria-label="t('liveTopology.zoom.close')"
          :title="t('liveTopology.zoom.close')"
          @click="emit('close')"
        ><Close /></button>
      </span>
    </header>
    <RequestDetailView :entry="entry" :decisions="decisions" />
  </section>
</template>

<style scoped>
.zoom {
  position: absolute;
  right: 12px;
  bottom: 12px;
  z-index: 5;
  width: min(460px, calc(100% - 24px));
  max-height: calc(100% - 24px);
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 8px 12px;
  border-radius: 12px;
  background: color-mix(in srgb, var(--kx-surface) 97%, transparent);
  box-shadow: var(--kx-shadow-md);
  overflow-y: auto;
}

/* 左栏形态：锚到本列左下角（与右栏的右下锚互为镜像 —— 浮层长在点击侧）。 */
.zoom--left {
  right: auto;
  left: 12px;
}

.zoom__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 4px;
}

.zoom__title {
  font-size: var(--kx-font-md, 14px);
  font-weight: 700;
  color: var(--kx-text);
}

.zoom__actions {
  display: flex;
  align-items: center;
  gap: 4px;
}

.zoom__open {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: transparent;
  color: var(--kx-text);
  font-size: 11px;
  line-height: 1.4;
  cursor: pointer;
  white-space: nowrap;
}

.zoom__open:hover {
  color: var(--kx-primary);
  border-color: var(--kx-primary);
}

.zoom__close {
  border: 0;
  background: transparent;
  color: var(--kx-muted);
  cursor: pointer;
  padding: 2px 4px;
}

.zoom__close:hover {
  color: var(--kx-danger);
}

/* ── 图标尺寸：element-plus 图标是裸 <svg viewBox>，必须显式定尺寸 ── */
.zoom__open > svg { width: 11px; height: 11px; }
.zoom__close > svg { width: 12px; height: 12px; }
</style>
