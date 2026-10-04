<script setup lang="ts">
// FocusWorkspace.vue — 专注工作区（UI 规范 07 §8–§9）：重要表格的全页查看与编辑。
// 单实例 Teleport portal；背景 inert；停靠表头；可横滚；退出/返回/Esc 三入口等效。
// 进入不复制数据：调用方以默认插槽把**同一份**区域组件渲染进来（原位留占位由调用方负责）。
// 关闭有两条路径且必须同效：①退出钮/Esc → 本组件 exit()；②系统返回 →
// BackDispatcher → runtime.close()。后者不经本组件，故订阅 runtime 状态，
// current() 变 null 时同样 emit('exit') 让调用方清 ref——否则覆盖层滞留、
// 背景 inert 已解但层还挡住交互（2026-10-04 首部署实测）。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import Icon from '../Icon.vue'
import { focusWorkspace, type FocusHandle } from '../../runtime/focusWorkspace'
import { useTitleStore } from '../../stores/titleStore'
import { t } from '../../i18n'

const props = defineProps<{ handle: FocusHandle }>()
const emit = defineEmits<{ (e: 'exit'): void }>()

const runtime = focusWorkspace()
const titleStore = useTitleStore()
const prevOverlay = titleStore.overlay
const prevInherited = titleStore.inherited

const toolOpen = ref(false)
let exited = false

function exit(): void {
  runtime.close()
  emit('exit')
}

// 订阅运行时：外部路径（系统返回）关闭时同步通知调用方卸载本层。
const unsubscribe = runtime.subscribe(() => {
  if (runtime.current() === null && !exited) {
    exited = true
    emit('exit')
  }
})

watch(
  () => props.handle,
  (h) => {
    if (h) {
      titleStore.setOverlay(h.title, null)
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  unsubscribe()
  titleStore.setOverlay(prevOverlay, prevInherited)
})

const stateLabel = computed(() => t('common.exitFocus'))
</script>

<template>
  <Teleport to="body">
    <div class="m-focus" role="dialog" aria-modal="true" :aria-label="props.handle.title">
      <header class="m-focus__bar">
        <button type="button" class="m-topbar__btn" :aria-label="stateLabel" @click="exit">
          <Icon name="close" />
        </button>
        <div class="m-topbar__title">{{ props.handle.title }}</div>
        <div class="m-topbar__side">
          <button
            type="button"
            class="m-topbar__btn"
            :aria-expanded="toolOpen"
            @click="toolOpen = !toolOpen"
          >
            <Icon name="settings" />
          </button>
        </div>
      </header>
      <div class="m-focus__body">
        <!-- 唯一可滚动/横移区域：停靠表头由 .m-table th sticky 承担 -->
        <slot />
      </div>
      <div v-if="toolOpen" class="m-focus__tools">
        <slot name="tools" />
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.m-focus {
  position: fixed;
  inset: 0;
  z-index: var(--app-z-focus);
  background: var(--app-bg);
  padding-top: var(--app-safe-top);
  padding-bottom: var(--app-safe-bottom);
  display: flex;
  flex-direction: column;
}

.m-focus__bar {
  display: flex;
  align-items: center;
  gap: var(--app-space-1);
  height: var(--app-topbar-height);
  flex: none;
  border-bottom: 1px solid var(--app-border);
  background: var(--app-header-bg);
}

.m-focus__body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  -webkit-overflow-scrolling: touch;
  touch-action: pan-x pan-y;
  padding: var(--app-space-2);
}

.m-focus__tools {
  position: absolute;
  right: var(--app-space-3);
  bottom: calc(var(--app-space-4) + var(--app-safe-bottom));
  background: var(--app-surface-raised);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-lg);
  box-shadow: var(--app-shadow-focus);
  padding: var(--app-space-2);
  min-width: 180px;
}
</style>
