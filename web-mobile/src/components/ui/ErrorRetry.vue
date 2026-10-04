<script setup lang="ts">
// ErrorRetry.vue — 加载失败：保旧数据 + 错误提示 + 手动重试（UI 规范 13 §3）。
import { t } from '../../i18n'

defineProps<{ message?: string }>()
const emit = defineEmits<{ (e: 'retry'): void }>()
</script>

<template>
  <div class="m-state">
    <p>{{ message ?? t('common.error') }}</p>
    <button type="button" class="m-btn m-btn--ghost retry-btn" @click="emit('retry')">
      {{ t('common.retry') }}
    </button>
  </div>
</template>

<style scoped>
.retry-btn {
  margin-top: var(--app-space-2);
  min-height: 48px;
  padding: 0 var(--app-space-5);
}
</style>
