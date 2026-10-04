<script setup lang="ts">
// EmptyState.vue — 空态（非错误：数据为空）。
import { t } from '../../i18n'
defineProps<{ message?: string }>()
</script>

<template>
  <div class="m-state">{{ message ?? t('common.empty') }}</div>
</template>
