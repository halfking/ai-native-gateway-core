<script setup lang="ts">
// Sheet.vue — 全屏/半屏 Sheet（UI 规范 02 §5 + 03 §3：手机弹窗全屏化）。
// 接 BackDispatcher（系统返回只关 Sheet）；无标题时继承下层标题（06 §2）。
import { onBeforeUnmount, onMounted } from 'vue'
import Icon from '../Icon.vue'
import { useHyperOverlay } from '../../composables/useHyperOverlay'
import { useTitleStore } from '../../stores/titleStore'
import { t } from '../../i18n'

const props = withDefaults(
  defineProps<{
    /** null=无标题（顶栏继承下层快照标题）。 */
    title?: string | null
    /** 关闭前确认（脏表单守卫）。 */
    beforeClose?: () => boolean
    fullscreen?: boolean
  }>(),
  { title: null, fullscreen: true },
)

const emit = defineEmits<{ (e: 'close'): void }>()

const titleStore = useTitleStore()
const prevOverlayTitle = titleStore.overlay
const prevInherited = titleStore.inherited

const overlay = useHyperOverlay({
  title: props.title ?? null,
  beforeClose: props.beforeClose,
  onClose: () => emit('close'),
})

onMounted(() => {
  // 标题继承链：Sheet 显式标题 > 下层快照（把当前顶栏标题作为 inherited 上浮）。
  const lowerTitle = titleStore.registered ?? prevOverlayTitle ?? null
  titleStore.setOverlay(props.title ?? null, props.title ? null : lowerTitle)
})

onBeforeUnmount(() => {
  titleStore.setOverlay(prevOverlayTitle, prevInherited)
})
</script>

<template>
  <Teleport to="body">
    <div
      class="m-sheet-backdrop"
      data-overlay="sheet"
      @click="overlay.close()"
    ></div>
    <div
      class="m-sheet"
      :class="{ 'm-sheet--fullscreen': props.fullscreen }"
      role="dialog"
      aria-modal="true"
      :aria-label="props.title ?? undefined"
    >
      <header class="m-sheet__bar">
        <button type="button" class="m-topbar__btn" :aria-label="t('common.close')" @click="overlay.close()">
          <Icon name="close" />
        </button>
        <div class="m-topbar__title">{{ props.title ?? '' }}</div>
        <div class="m-topbar__side"><slot name="action" /></div>
      </header>
      <div class="m-sheet__body">
        <slot />
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.m-sheet-backdrop {
  position: fixed;
  inset: 0;
  z-index: var(--app-z-sheet);
  background: rgb(13 20 35 / 42%);
}

.m-sheet {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: calc(var(--app-z-sheet) + 1);
  background: var(--app-bg);
  border-radius: var(--app-radius-lg) var(--app-radius-lg) 0 0;
  box-shadow: var(--app-shadow-dialog);
  max-height: calc(100% - 2.5rem);
  display: flex;
  flex-direction: column;
}

.m-sheet--fullscreen {
  top: 0;
  max-height: none;
  height: 100%;
  height: 100dvh;
  border-radius: 0;
  padding-top: var(--app-safe-top);
  padding-bottom: var(--app-safe-bottom);
}

.m-sheet__bar {
  display: flex;
  align-items: center;
  gap: var(--app-space-1);
  height: var(--app-topbar-height);
  flex: none;
  border-bottom: 1px solid var(--app-border-subtle);
  background: var(--app-header-bg);
}

.m-sheet__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
  touch-action: pan-y;
  padding: var(--app-space-3) var(--app-space-content-x);
}
</style>
