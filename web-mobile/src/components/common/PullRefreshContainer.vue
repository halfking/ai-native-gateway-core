<script setup lang="ts">
// PullRefreshContainer — 通用下拉刷新滚动容器（非列表页用，如总览）。
// 自任滚动根；内容经默认插槽提供。注册进 Hyper.scroll 由调用方决定
// （页面 useHyperPage 传 scrollRoots = 本容器 ref）。
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { usePullToRefreshGesture } from '@/composables/usePullToRefreshGesture'

const props = defineProps<{
  onRefresh: () => Promise<void>
}>()

const rootRef = ref<HTMLElement | null>(null)

const gesture = usePullToRefreshGesture({
  onRefresh: props.onRefresh,
  getScroller: () => rootRef.value,
})

defineExpose({ rootRef })

let unbind: (() => void) | null = null

onMounted(() => {
  if (rootRef.value) unbind = gesture.bind(rootRef.value)
})

onBeforeUnmount(() => {
  unbind?.()
  unbind = null
})
</script>

<template>
  <div ref="rootRef" class="ptr-container">
    <div class="ptr-container__indicator" :style="{ height: `${gesture.visualOffset.value}px` }" aria-live="polite">
      <span class="ptr-container__label">{{ gesture.label.value }}</span>
    </div>
    <slot />
  </div>
</template>

<style scoped>
.ptr-container {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
  overscroll-behavior-y: contain;
  touch-action: pan-y pinch-zoom;
}

.ptr-container__indicator {
  overflow: hidden;
  display: flex;
  align-items: center;
  justify-content: center;
  height: 0;
}

.ptr-container__label {
  font-size: 0.75rem;
  color: var(--app-text-muted);
  white-space: nowrap;
}
</style>
