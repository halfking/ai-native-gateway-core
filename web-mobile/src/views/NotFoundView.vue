<script setup lang="ts">
// NotFoundView — 404 兜底（根页 fallback 前的最后防线）。
import { t } from '@/i18n'
import { appBase } from '@/utils/base'

function backHome(): void {
  window.location.assign(appBase())
}
</script>

<template>
  <div class="page nf">
    <h1 class="nf__title">{{ t('common.notFound') }}</h1>
    <p class="nf__hint">{{ t('common.notFoundHint') }}</p>
    <button type="button" class="btn btn--primary" @click="backHome">{{ t('common.backHome') }}</button>
  </div>
</template>

<style scoped>
.nf {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--app-space-3);
  text-align: center;
}

.nf__title {
  font-size: 1.125rem;
  font-weight: 600;
}

.nf__hint {
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}
</style>
