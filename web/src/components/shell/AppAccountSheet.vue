<script setup lang="ts">
/**
 * AppAccountSheet — compact 账户面板（docs/UI规范/00 §5.2 · H2，参考规范 02 §5）。
 *
 * ## 存在理由
 *
 * 规范禁止在 compact 顶栏并排「语言 + 主题 + 健康灯 + 帮助 + 用户下拉」——
 * 那是桌面专业壳的密度，320px 宽放不下且触控目标互相挤占。本面板把 compact
 * 上的系统设置收成一件事：**「我」**。它对标微信/支付宝的「我」页，
 * 而不是桌面 dropdown 的缩小版。
 *
 * ## 分层
 *
 * - 组件只负责呈现与 emit，**不做**登出/健康探测等业务动作 —— 那些仍由
 *   `App.vue` 编排（与既有 `UserMenuDropdown` 同样的分工），不复制逻辑。
 * - 通过 Hyper 的 `presentOverlay` 登记到 `OverlayRegistry`，所以
 *   Android 系统返回与 Esc 会先关它，而不是穿透到背景路由。
 *
 * ## 无障碍
 *
 * `role="dialog"` + `aria-modal`；打开时把焦点移入、Tab 圈闭，关闭后交还触发控件。
 * 每项都是真实 `<button>`，状态不只靠颜色表达。
 */
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useFocusTrap } from '../../composables/useFocusTrap'
import { lockBodyScroll, unlockBodyScroll } from '../../composables/useScrollLock'
import { store, isSuperAdmin } from '../../store'
import { back, overlays, presentOverlay } from '../../lib/shell/hyper'
import LanguageSelector from '../LanguageSelector.vue'
import ThemeToggle from '../ThemeToggle.vue'

const props = withDefaults(
  defineProps<{
    modelValue: boolean
    /** 只读健康态：'ok' | 'down' | 'unknown'。由 App.vue 探测后传入。 */
    health?: 'ok' | 'down' | 'unknown'
  }>(),
  { health: 'unknown' },
)

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  logout: []
  help: []
  openAdmin: []
}>()

const { t } = useI18n()
const panelRef = ref<HTMLElement | null>(null)
const trap = useFocusTrap(panelRef)

const OVERLAY_ID = 'hyper-account-sheet'

/**
 * 关闭。
 *
 * 注销覆盖层是**当场**做的，不等父组件把 modelValue 传回来。
 * 否则会留下一个空窗：层已经 emit 关闭、但还挂在栈里，此时按返回会命中
 * 一个「已死」的条目 —— 要么重复触发 close，要么被脏守卫拦住不响应。
 * unregister 幂等，所以与 watch 里的那次注销不冲突。
 */
function close(): void {
  overlays.unregister(OVERLAY_ID)
  emit('update:modelValue', false)
}

function onKeydown(e: KeyboardEvent): void {
  if (e.key === 'Escape') {
    e.stopPropagation()
    close()
    return
  }
  trap.trapTab(e)
}

watch(
  () => props.modelValue,
  (open) => {
    if (typeof document === 'undefined') return
    if (open) {
      lockBodyScroll()
      document.addEventListener('keydown', onKeydown)
      // 登记到 Hyper 覆盖层栈：系统返回 / Esc 只关最上层，不穿透到背景路由
      presentOverlay({
        id: OVERLAY_ID,
        presentation: 'sheet',
        title: t('hyper.account.title'),
        dismissible: true,
        dirty: false,
        close,
      })
      void nextTick(() => trap.activate())
    } else {
      unlockBodyScroll()
      document.removeEventListener('keydown', onKeydown)
      // 父组件从外部关闭时（点遮罩/关闭钮之外的路）也要注销。
      // 走 close() 的路径已经注销过了，这里是幂等的兜底。
      overlays.unregister(OVERLAY_ID)
    }
  },
  // ★ immediate 是必需的，不是可选优化：
  //   父组件可能以 modelValue=true 首次挂载本组件（深链直接开面板、
  //   KeepAlive 重新激活）。没有 immediate，watch 不会跑，覆盖层就永远
  //   没登记 —— Android 系统返回会**穿透**到背景路由，把整个面板页退掉。
  { immediate: true },
)

onBeforeUnmount(() => {
  if (typeof document === 'undefined') return
  if (props.modelValue) unlockBodyScroll()
  document.removeEventListener('keydown', onKeydown)
})

const healthText = () => {
  if (props.health === 'ok') return t('hyper.account.healthy')
  if (props.health === 'down') return t('hyper.account.unhealthy')
  return t('hyper.account.unknown')
}
</script>

<template>
  <div v-if="modelValue" class="account-sheet" role="dialog" aria-modal="true" :aria-label="t('hyper.account.title')">
    <div class="account-sheet__mask-backdrop" @click="close" />

    <section ref="panelRef" class="account-sheet__panel">
      <header class="account-sheet__header">
        <div class="account-sheet__identity">
          <div class="account-sheet__name">{{ store.userInfo?.display_name || store.userInfo?.username || '—' }}</div>
          <div class="account-sheet__role">{{ store.userInfo?.role || '—' }}</div>
        </div>
        <button
          type="button"
          class="account-sheet__close"
          :aria-label="t('hyper.account.close')"
          @click="close"
        >
          ✕
        </button>
      </header>

      <ul class="account-sheet__list">
        <li class="account-sheet__row">
          <span class="account-sheet__row-label">{{ t('hyper.account.language') }}</span>
          <LanguageSelector />
        </li>
        <li class="account-sheet__row">
          <span class="account-sheet__row-label">{{ t('hyper.account.theme') }}</span>
          <ThemeToggle />
        </li>
        <li class="account-sheet__row">
          <span class="account-sheet__row-label">{{ t('hyper.account.health') }}</span>
          <span
            class="account-sheet__health"
            :data-state="health"
            role="status"
            aria-live="polite"
          >{{ healthText() }}</span>
        </li>
        <li v-if="isSuperAdmin()" class="account-sheet__row">
          <button type="button" class="account-sheet__action" @click="emit('openAdmin')">
            {{ t('hyper.account.adminEntry') }}
          </button>
        </li>
        <li class="account-sheet__row">
          <button type="button" class="account-sheet__action" @click="emit('help')">
            {{ t('hyper.account.help') }}
          </button>
        </li>
        <li class="account-sheet__row">
          <button
            type="button"
            class="account-sheet__action account-sheet__action--danger"
            @click="emit('logout')"
          >
            {{ t('hyper.account.logout') }}
          </button>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.account-sheet {
  position: fixed;
  inset: 0;
  /* 高于底栏(30)与抽屉(40)：账户面板是从底栏/顶栏唤起的最后一层 */
  z-index: 50;
  display: flex;
  align-items: flex-end;
  justify-content: center;
}

.account-sheet__mask-backdrop {
  position: absolute;
  inset: 0;
  background: var(--overlay-medium);
}

.account-sheet__panel {
  position: relative;
  width: 100%;
  max-width: 520px;
  max-height: 90vh;
  max-height: 90dvh;
  display: flex;
  flex-direction: column;
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  /* 圆角只在上沿：这是自底向上的 Sheet */
  border-radius: 12px 12px 0 0;
  /* 安全区只在此处消费一次 */
  padding-bottom: env(safe-area-inset-bottom, 0px);
}

.account-sheet__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  padding: 16px;
  padding-top: calc(16px + env(safe-area-inset-top, 0px));
  border-bottom: 1px solid var(--kx-border);
}

.account-sheet__name {
  font-size: 16px;
  font-weight: 600;
}

.account-sheet__role {
  font-size: 12px;
  color: var(--kx-muted);
  margin-top: 2px;
}

.account-sheet__close {
  /* R1（2026-10-04 吸收）：≥48px。此前 44，而同一组件的 `__row` / `__action`
     本来就是 48 —— 同一个 Sheet 里两种触控尺寸，是自相矛盾不是设计。 */
  min-width: 48px;
  min-height: 48px;
  background: none;
  border: 0;
  color: var(--kx-muted);
  font-size: 16px;
  cursor: pointer;
}

.account-sheet__list {
  list-style: none;
  margin: 0;
  padding: 4px 0;
  overflow: auto;
  min-height: 0;
}

.account-sheet__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 48px;
  padding: 0 16px;
}

.account-sheet__row-label {
  font-size: 15px;
}

.account-sheet__health[data-state='ok'] {
  color: var(--kx-success);
}
.account-sheet__health[data-state='down'] {
  color: var(--kx-danger);
}
.account-sheet__health[data-state='unknown'] {
  color: var(--kx-muted);
}

.account-sheet__action {
  width: 100%;
  min-height: 48px;
  display: flex;
  align-items: center;
  background: none;
  border: 0;
  color: inherit;
  font: inherit;
  font-size: 15px;
  text-align: start;
  cursor: pointer;
}

.account-sheet__action--danger {
  color: var(--kx-danger);
}

.account-sheet__action:focus-visible,
.account-sheet__close:focus-visible {
  outline: 2px solid var(--kx-primary);
  outline-offset: -2px;
}
</style>
