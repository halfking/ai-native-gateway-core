<script setup lang="ts">
// AppStateView — 13 §1 可区分状态集的区块级组合：initialLoading / error /
// empty 三态与内容互斥，避免"骨架→空白→数据"三连跳。骨架 >150ms 才显示
// 由调用方控制（或直接接受首帧骨架——快查页可接受）。
import { t } from '@/i18n'
import AppIcon from './AppIcon.vue'

withDefaults(
  defineProps<{
    loading?: boolean
    error?: string | null
    empty?: boolean
    loadingText?: string
    skeletonRows?: number
  }>(),
  { loading: false, error: null, empty: false, skeletonRows: 4 },
)

defineEmits<{ retry: [] }>()
</script>

<template>
  <div v-if="loading" class="state-view" aria-busy="true" :aria-label="loadingText ?? t('common.loading')">
    <div v-for="i in skeletonRows" :key="i" class="state-view__row">
      <div class="skeleton state-view__avatar" />
      <div class="state-view__lines">
        <div class="skeleton state-view__line" :style="{ width: 40 + ((i * 17) % 45) + '%' }" />
        <div class="skeleton state-view__line state-view__line--sm" :style="{ width: 25 + ((i * 23) % 40) + '%' }" />
      </div>
    </div>
  </div>

  <div v-else-if="error" class="state-view state-view--center">
    <AppIcon name="alert" :size="28" class="state-view__icon" />
    <p class="state-view__text">{{ t('common.error') }}</p>
    <p class="state-view__hint">{{ error }}</p>
    <button type="button" class="btn btn--sm" @click="$emit('retry')">
      {{ t('common.retry') }}
    </button>
  </div>

  <div v-else-if="empty" class="state-view state-view--center">
    <p class="state-view__text">{{ t('common.empty') }}</p>
    <slot name="empty-hint" />
  </div>

  <slot v-else />
</template>

<style scoped>
.state-view {
  width: 100%;
}

.state-view--center {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--app-space-2);
  padding: var(--app-space-6) var(--app-space-4);
  color: var(--app-text-muted);
  text-align: center;
}

.state-view__row {
  display: flex;
  gap: var(--app-space-3);
  padding: var(--app-space-3) 0;
}

.state-view__avatar {
  width: 36px;
  height: 36px;
  border-radius: 50%;
  flex-shrink: 0;
}

.state-view__lines {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 8px;
  justify-content: center;
}

.state-view__line {
  height: 12px;
}

.state-view__line--sm {
  height: 10px;
  opacity: 0.7;
}

.state-view__icon {
  color: var(--app-text-muted);
}

.state-view__text {
  font-size: 0.875rem;
  color: var(--app-text-secondary);
}

.state-view__hint {
  font-size: 0.75rem;
  color: var(--app-text-muted);
  word-break: break-all;
  max-width: 100%;
}
</style>
