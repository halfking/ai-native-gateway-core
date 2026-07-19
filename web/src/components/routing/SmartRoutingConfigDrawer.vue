<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import SmartRoutingConfigPanel from './SmartRoutingConfigPanel.vue'

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
          <button type="button" class="close" :aria-label="t('routingDefault.actions.cancel')" @click="emit('close')">×</button>
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
  background: rgba(15, 23, 42, 0.4);
  z-index: 70;
  display: flex;
  justify-content: flex-end;
}
.smart-drawer {
  width: min(960px, 100vw);
  height: 100%;
  background: var(--bg-card, #fff);
  box-shadow: -12px 0 32px rgba(0, 0, 0, 0.16);
  display: flex;
  flex-direction: column;
}
.smart-drawer-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border, #e5e7eb);
}
.smart-drawer-head h3 {
  margin: 0 0 2px;
  font-size: 15px;
}
.hint {
  margin: 0;
  font-size: 11px;
  color: var(--muted, #6b7280);
}
.close {
  margin-left: auto;
  border: none;
  background: transparent;
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  color: var(--muted, #6b7280);
}
.close:hover { color: var(--text, #111); }
.smart-drawer-body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 12px 16px 16px;
}
</style>
