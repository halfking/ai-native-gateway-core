<script setup lang="ts">
import { ref } from 'vue'
import { t } from '@/i18n'
import AppIcon from './AppIcon.vue'
import FocusLayer from './FocusLayer.vue'

const props = defineProps<{ title: string; heading?: string }>()
const active = ref(false)
</script>

<template>
  <div class="data-table-focus">
    <div v-if="!active || heading" class="data-table-focus__toolbar">
      <h3 v-if="heading" class="data-table-focus__heading">{{ heading }}</h3>
      <button
        v-if="!active"
        type="button"
        class="data-table-focus__button"
        :aria-label="t('common.focusView')"
        @click="active = true"
      >
        <AppIcon name="expand" :size="16" />
        <span>{{ t('common.focusView') }}</span>
      </button>
    </div>
    <FocusLayer v-model:active="active" :title="props.title">
      <div class="data-table-focus__content">
        <slot />
      </div>
    </FocusLayer>
  </div>
</template>

<style scoped>
.data-table-focus__button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  min-height: 48px;
  flex-shrink: 0;
  padding: 0 var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 0.8125rem;
  cursor: pointer;
}

.data-table-focus__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
  min-height: 48px;
  margin-bottom: var(--app-space-2);
}

.data-table-focus__heading {
  min-width: 0;
  margin: 0;
  font-size: var(--app-section-title-size);
  font-weight: 600;
}

.data-table-focus__content {
  min-width: 0;
}
</style>
