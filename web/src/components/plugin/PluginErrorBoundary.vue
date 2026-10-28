<script setup lang="ts">
import { onErrorCaptured, ref } from 'vue'
import { useI18n } from 'vue-i18n'

defineProps<{ pluginId: string }>()
const { t } = useI18n()
const err = ref<Error | null>(null)

onErrorCaptured((e) => {
  err.value = e instanceof Error ? e : new Error(String(e))
  return false
})

function retry() {
  err.value = null
}
</script>

<template>
  <div class="plugin-error-boundary">
    <div v-if="err" class="plugin-error-boundary__panel" role="alert">
      <p>{{ t('plugin.error.boundary', { id: pluginId }) }}</p>
      <p class="plugin-error-boundary__detail">{{ err.message }}</p>
      <button type="button" class="btn btn-secondary" @click="retry">{{ t('plugin.error.retry') }}</button>
    </div>
    <slot v-else />
  </div>
</template>

<style scoped>
.plugin-error-boundary__panel { padding: 12px; border: 1px solid var(--danger); border-radius: 6px; }
.plugin-error-boundary__detail { color: var(--text-secondary); font-size: 14px; }
</style>
