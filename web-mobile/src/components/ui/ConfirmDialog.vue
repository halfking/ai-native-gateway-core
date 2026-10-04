<script setup lang="ts">
// ConfirmDialog.vue — 确认框（最上层弹层；系统返回只关确认框，17 §8-2）。
import { onBeforeUnmount, onMounted } from 'vue'
import { useHyperOverlay } from '../../composables/useHyperOverlay'
import { useTitleStore } from '../../stores/titleStore'
import { t } from '../../i18n'

const props = withDefaults(
  defineProps<{
    title: string
    body?: string
    confirmText?: string
    cancelText?: string
    danger?: boolean
  }>(),
  { body: '', danger: false },
)

const emit = defineEmits<{ (e: 'confirm'): void; (e: 'cancel'): void }>()

const titleStore = useTitleStore()
const prevOverlay = titleStore.overlay
const prevInherited = titleStore.inherited

const overlay = useHyperOverlay({
  title: props.title,
  onClose: () => emit('cancel'),
})

onMounted(() => {
  // 无标题弹层继承语义由调用方保证；确认框显式带标题。
  titleStore.setOverlay(props.title, prevOverlay)
})

onBeforeUnmount(() => {
  titleStore.setOverlay(prevOverlay, prevInherited)
})

function onConfirm(): void {
  emit('confirm')
}
</script>

<template>
  <Teleport to="body">
    <div class="m-confirm-backdrop" @click="overlay.close()"></div>
    <div class="m-confirm" role="alertdialog" aria-modal="true" :aria-label="props.title">
      <p class="m-confirm__title">{{ props.title }}</p>
      <p v-if="props.body" class="m-confirm__body">{{ props.body }}</p>
      <div class="m-confirm__actions">
        <button type="button" class="m-btn m-btn--ghost" @click="overlay.close()">
          {{ props.cancelText ?? t('common.cancel') }}
        </button>
        <button
          type="button"
          class="m-btn"
          :class="{ 'm-btn--danger': props.danger }"
          @click="onConfirm"
        >
          {{ props.confirmText ?? t('common.confirm') }}
        </button>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.m-confirm-backdrop {
  position: fixed;
  inset: 0;
  z-index: var(--app-z-toast);
  background: rgb(13 20 35 / 46%);
}

.m-confirm {
  position: fixed;
  left: 50%;
  top: 50%;
  transform: translate(-50%, -50%);
  z-index: calc(var(--app-z-toast) + 1);
  width: min(320px, calc(100vw - 3rem));
  background: var(--app-surface);
  border-radius: var(--app-radius-lg);
  box-shadow: var(--app-shadow-dialog);
  padding: var(--app-space-4);
}

.m-confirm__title {
  font-weight: 600;
  font-size: 1rem;
  margin-bottom: var(--app-space-2);
}

.m-confirm__body {
  font-size: 0.875rem;
  color: var(--app-text-secondary);
  margin-bottom: var(--app-space-4);
}

.m-confirm__actions {
  display: flex;
  gap: var(--app-space-2);
}

.m-confirm__actions .m-btn {
  flex: 1;
  min-height: 48px;
}
</style>
