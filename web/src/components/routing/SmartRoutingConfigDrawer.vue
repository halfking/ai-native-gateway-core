<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import SmartRoutingConfigPanel from './SmartRoutingConfigPanel.vue'
import { Close } from '@element-plus/icons-vue'

defineProps<{
  open: boolean
  taskType?: string
}>()

const emit = defineEmits<{
  close: []
}>()

const { t } = useI18n()
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="smart-drawer-overlay" @click.self="emit('close')">
      <aside
        class="smart-drawer"
        role="dialog"
        :aria-label="t('routingDefault.title')"
      >
        <header class="smart-drawer-head">
          <div>
            <h3>{{ t('routingDefault.title') }}</h3>
            <p class="hint">{{ t('routing.dashboard.overview.smartConfigHint') }}</p>
          </div>
          <button type="button" class="close" :aria-label="t('routingDefault.actions.cancel')" @click="emit('close')"><Close /></button>
        </header>
        <div class="smart-drawer-body">
          <SmartRoutingConfigPanel :initial-task-type="taskType || ''" compact />
        </div>
      </aside>
    </div>
  </Teleport>
</template>

<style scoped>
.smart-drawer-overlay {
  position: fixed;
  inset: 0;
  background: var(--overlay-strong);
  z-index: 70;
  display: flex;
  justify-content: flex-end;
}
.smart-drawer {
  width: min(960px, 100vw);
  height: 100%;
  background: var(--card);
  box-shadow: var(--kx-shadow-md-left);
  display: flex;
  flex-direction: column;
}
.smart-drawer-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
}
.smart-drawer-head h3 {
  margin: 0 0 2px;
  font-size: 15px;
}
.hint {
  margin: 0;
  font-size: 11px;
  color: var(--muted);
}
.close {
  margin-left: auto;
  border: none;
  background: transparent;
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  color: var(--muted);
}
.close:hover { color: var(--text); }
.smart-drawer-body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 12px 16px 16px;
}

/* ── 2026-10-27 第 17 增量（AI 痕迹门 V3）：图标尺寸 ──
   element-plus 图标是裸 <svg viewBox="0 0 1024 1024">，**不带 width/height**
   ⇒ 浏览器按包含块宽度渲染（会撑爆布局）⇒ 必须显式定尺寸。
   尺寸沿用原字形的 font-size 观感（字形墨迹 ≈ 0.7em）。 */
.close > svg { width: 14px; height: 14px; }
</style>
