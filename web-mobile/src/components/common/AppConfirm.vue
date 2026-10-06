<script setup lang="ts">
// AppConfirm — 顶层确认框（06 §5 优先级 3：专注层上的确认框先关确认框）。
import { t } from '@/i18n'
import AppSheet from './AppSheet.vue'

withDefaults(
  defineProps<{
    modelValue: boolean
    title: string
    body?: string
    confirmLabel?: string
    cancelLabel?: string
    danger?: boolean
  }>(),
  { body: undefined, danger: false },
)

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  confirm: []
  cancel: []
}>()

function onConfirm(): void {
  emit('confirm')
  emit('update:modelValue', false)
}

function onCancel(): void {
  emit('cancel')
  emit('update:modelValue', false)
}
</script>

<template>
  <AppSheet
    :model-value="modelValue"
    presentation="modal"
    :title="title"
    @update:model-value="(v: boolean) => emit('update:modelValue', v)"
  >
    <p v-if="body" class="confirm__body">{{ body }}</p>
    <!-- 默认 slot（2026-10-06 新增）：确认框需要承载**必填输入**时用。
         NodesView 的 set/clear-manual-disabled 要求 reason 非空（后端空串直接
         400，admin/credential_monitor.go:1785-1792），靠 body 传纯文本满足不了。
         放在 actions 之前 ⇒ 输入在按钮上方，符合移动端表单阅读顺序。 -->
    <slot />
    <div class="confirm__actions">
      <button type="button" class="btn" @click="onCancel">{{ cancelLabel ?? t('common.cancel') }}</button>
      <button type="button" class="btn" :class="danger ? 'btn--danger' : 'btn--primary'" @click="onConfirm">
        {{ confirmLabel ?? t('common.confirm') }}
      </button>
    </div>
  </AppSheet>
</template>

<style scoped>
.confirm__body {
  color: var(--app-text-secondary);
  font-size: 0.875rem;
  margin-bottom: var(--app-space-4);
}

.confirm__actions {
  display: flex;
  gap: var(--app-space-2);
}

.confirm__actions .btn {
  flex: 1;
}
</style>
