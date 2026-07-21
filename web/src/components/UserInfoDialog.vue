<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { store } from '../store'

const props = defineProps<{
  modelValue: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
}>()

const { t } = useI18n()

const user = computed(() => store.userInfo)

const roleLabel = computed(() => {
  const role = user.value?.role
  return role ? t(`app.role.${role}`) : '—'
})

function close() {
  emit('update:modelValue', false)
}
</script>

<template>
  <Teleport to="body">
    <div v-if="modelValue && user" class="user-info-overlay" @click.self="close">
      <div class="user-info-dialog" role="dialog" aria-modal="true" :aria-label="t('app.userMenu.profile')">
        <header class="user-info-dialog__header">
          <h2 class="user-info-dialog__title">{{ t('app.userMenu.profile') }}</h2>
          <button type="button" class="user-info-dialog__close" aria-label="Close" @click="close">×</button>
        </header>
        <dl class="user-info-dialog__body">
          <div class="user-info-row">
            <dt>{{ t('app.userInfo.displayName') }}</dt>
            <dd>{{ user.display_name || '—' }}</dd>
          </div>
          <div class="user-info-row">
            <dt>{{ t('app.userInfo.username') }}</dt>
            <dd>{{ user.username || '—' }}</dd>
          </div>
          <div class="user-info-row">
            <dt>{{ t('app.userInfo.email') }}</dt>
            <dd>{{ user.email || '—' }}</dd>
          </div>
          <div class="user-info-row">
            <dt>{{ t('app.userInfo.role') }}</dt>
            <dd>{{ roleLabel }}</dd>
          </div>
          <div class="user-info-row">
            <dt>{{ t('app.userInfo.tenant') }}</dt>
            <dd>{{ user.tenant_id || '—' }}</dd>
          </div>
        </dl>
        <footer class="user-info-dialog__footer">
          <button type="button" class="btn btn-primary btn-sm" @click="close">{{ t('app.userInfo.close') }}</button>
        </footer>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.user-info-overlay {
  position: fixed;
  inset: 0;
  z-index: 1100;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  background: rgba(0, 0, 0, 0.45);
}

.user-info-dialog {
  width: min(100%, 420px);
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  box-shadow: 0 24px 48px rgba(0, 0, 0, 0.25);
}

.user-info-dialog__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 18px 8px;
}

.user-info-dialog__title {
  margin: 0;
  font-size: 16px;
  font-weight: 700;
  color: var(--text);
}

.user-info-dialog__close {
  border: 0;
  background: transparent;
  color: var(--muted);
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 6px;
}

.user-info-dialog__close:hover {
  background: var(--bg-subtle, rgba(255, 255, 255, 0.05));
  color: var(--text);
}

.user-info-dialog__body {
  margin: 0;
  padding: 8px 18px 16px;
}

.user-info-row {
  display: grid;
  grid-template-columns: 96px 1fr;
  gap: 8px 12px;
  padding: 8px 0;
  border-bottom: 1px solid var(--border);
}

.user-info-row:last-child {
  border-bottom: 0;
}

.user-info-row dt {
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}

.user-info-row dd {
  margin: 0;
  font-size: 13px;
  color: var(--text);
  word-break: break-word;
}

.user-info-dialog__footer {
  display: flex;
  justify-content: flex-end;
  padding: 8px 18px 16px;
}
</style>
