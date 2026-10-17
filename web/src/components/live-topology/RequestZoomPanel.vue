<script setup lang="ts">
// RequestZoomPanel.vue —— 「点击放大」的请求详情（2026-10-18 改为薄包装）
//
// 内联覆盖式放大面板：不是弹窗、不是新页面 —— 拓扑图仍在视野里，放大的
// 是这条请求的细节。内容本体在 RequestDetailView（与全页详情路由共享，
// 两处字段必然一致）；本组件只负责覆盖层外观（定位/关闭/全页打开入口）。
//
// 「全页打开」是 owner 2026-10-18 要求：详情可以整页形式打开
// （/live/request/:id），覆盖面板里的 ↗ 与面板行的 ↗ 走同一条路由。

import { useI18n } from 'vue-i18n'
import type { ActionEvent } from '../../composables/liveStreamStore'
import type { PanelEntry } from '../../composables/useLiveTopology'
import RequestDetailView from './RequestDetailView.vue'

defineProps<{
  entry: PanelEntry
  decisions: ActionEvent[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'open-page', entry: PanelEntry): void
}>()

const { t } = useI18n()
</script>

<template>
  <section class="zoom" role="region" :aria-label="t('liveTopology.zoom.title')">
    <header class="zoom__head">
      <span class="zoom__title">{{ t('liveTopology.zoom.title') }}</span>
      <span class="zoom__actions">
        <button
          type="button"
          class="zoom__page"
          data-testid="zoom-open-page"
          :aria-label="t('liveTopology.zoom.openPage')"
          :title="t('liveTopology.zoom.openPage')"
          @click="emit('open-page', entry)"
        >↗</button>
        <button type="button" class="zoom__close" :aria-label="t('liveTopology.zoom.close')" @click="emit('close')">✕</button>
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
  padding: 10px 12px;
  border: 1px solid var(--kx-primary);
  border-radius: 12px;
  background: color-mix(in srgb, var(--kx-surface) 97%, transparent);
  box-shadow: var(--kx-shadow-md);
  overflow-y: auto;
}

.zoom__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.zoom__title {
  font-size: var(--kx-font-md, 14px);
  font-weight: 700;
  color: var(--kx-text);
}

.zoom__actions {
  display: flex;
  align-items: center;
  gap: 6px;
}

.zoom__page,
.zoom__close {
  border: 0;
  background: transparent;
  color: var(--kx-muted);
  cursor: pointer;
  font-size: 13px;
  padding: 2px 4px;
}

.zoom__page:hover {
  color: var(--kx-primary);
}

.zoom__close:hover {
  color: var(--kx-danger);
}
</style>
