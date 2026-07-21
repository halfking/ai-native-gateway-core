<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { store } from '../../store'

const emit = defineEmits<{
  'user-info': []
  'change-password': []
  logout: []
}>()

const { t } = useI18n()

const open = ref(false)
const dropdownStyle = ref<Record<string, string>>({})
const triggerRef = ref<HTMLElement | null>(null)

const displayName = computed(() => store.userInfo?.display_name || store.userInfo?.username || '')
const roleLabel = computed(() => {
  const role = store.userInfo?.role
  return role ? t(`app.role.${role}`) : ''
})

function syncPosition() {
  const el = triggerRef.value
  if (!el || typeof window === 'undefined') return
  const rect = el.getBoundingClientRect()
  dropdownStyle.value = {
    position: 'fixed',
    top: `${rect.bottom + 6}px`,
    right: `${Math.max(8, window.innerWidth - rect.right)}px`,
    left: 'auto',
    zIndex: '1001',
  }
}

function toggle() {
  open.value = !open.value
  if (open.value) nextTick(() => syncPosition())
}

function close() {
  open.value = false
}

function onUserInfo() {
  close()
  emit('user-info')
}

function onChangePassword() {
  close()
  emit('change-password')
}

function onLogout() {
  close()
  emit('logout')
}

function handleOutside(event: MouseEvent) {
  const target = event.target as HTMLElement | null
  if (!target) return
  if (target.closest('.user-menu')) return
  close()
}

function onWindowChange() {
  if (open.value) syncPosition()
}

onMounted(() => {
  document.addEventListener('click', handleOutside)
  window.addEventListener('resize', onWindowChange)
  window.addEventListener('scroll', onWindowChange, true)
})

onBeforeUnmount(() => {
  document.removeEventListener('click', handleOutside)
  window.removeEventListener('resize', onWindowChange)
  window.removeEventListener('scroll', onWindowChange, true)
})
</script>

<template>
  <div v-if="store.userInfo" class="user-menu">
    <button
      ref="triggerRef"
      type="button"
      class="user-menu__trigger"
      :aria-expanded="open"
      aria-haspopup="menu"
      @click.stop="toggle"
    >
      <span class="user-menu__name">{{ displayName }}</span>
      <span v-if="roleLabel" class="user-menu__role">{{ roleLabel }}</span>
      <span class="user-menu__chevron" aria-hidden="true">▾</span>
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        class="user-menu__dropdown"
        :style="dropdownStyle"
        role="menu"
        @click.stop
      >
        <button type="button" class="user-menu__item" role="menuitem" @click="onUserInfo">
          {{ t('app.userMenu.profile') }}
        </button>
        <button
          v-if="store.jwtToken"
          type="button"
          class="user-menu__item"
          role="menuitem"
          @click="onChangePassword"
        >
          {{ t('app.userMenu.changePassword') }}
        </button>
        <div class="user-menu__sep" role="separator" />
        <button type="button" class="user-menu__item user-menu__item--danger" role="menuitem" @click="onLogout">
          {{ t('app.userMenu.logout') }}
        </button>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.user-menu {
  position: relative;
}

.user-menu__trigger {
  display: inline-flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 1px;
  padding: 6px 10px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--kx-text, var(--text));
  cursor: pointer;
  font-family: inherit;
  transition: background 0.15s ease;
  max-width: min(28vw, 200px);
}

.user-menu__trigger:hover,
.user-menu__trigger[aria-expanded='true'] {
  background: var(--kx-primary-soft, var(--bg-subtle));
}

.user-menu__name {
  font-size: 12px;
  font-weight: 600;
  line-height: 1.2;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
}

.user-menu__role {
  font-size: 10px;
  color: var(--kx-muted, var(--muted));
  line-height: 1.2;
  white-space: nowrap;
}

.user-menu__chevron {
  display: none;
}

@media (min-width: 769px) {
  .user-menu__trigger {
    flex-direction: row;
    align-items: center;
    gap: 6px;
  }
  .user-menu__chevron {
    display: inline;
    font-size: 10px;
    opacity: 0.7;
  }
}

.user-menu__dropdown {
  min-width: 168px;
  padding: 6px;
  background: var(--kx-surface, var(--card));
  border: 1px solid var(--kx-border, var(--border));
  border-radius: 12px;
  box-shadow: var(--kx-shadow-md, 0 16px 40px rgba(0, 0, 0, 0.1));
  display: flex;
  flex-direction: column;
  gap: 2px;
  animation: user-menu-enter 0.14s ease both;
}

.user-menu__item {
  display: block;
  width: 100%;
  padding: 8px 10px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--kx-text, var(--text));
  font-size: 13px;
  font-weight: 500;
  text-align: start;
  cursor: pointer;
  font-family: inherit;
  transition: background 0.15s ease, color 0.15s ease;
}

.user-menu__item:hover {
  background: var(--kx-primary-soft, var(--bg-subtle));
  color: var(--kx-primary, var(--accent));
}

.user-menu__item--danger:hover {
  color: #f87171;
}

.user-menu__sep {
  height: 1px;
  margin: 4px 6px;
  background: var(--kx-border, var(--border));
}

@keyframes user-menu-enter {
  from { opacity: 0; transform: translateY(-4px); }
  to { opacity: 1; transform: translateY(0); }
}

@media (max-width: 768px) {
  .user-menu__role {
    display: none;
  }
}
</style>
