<script setup lang="ts">
// PullRefreshContainer — 通用下拉刷新滚动容器（非列表页用，如总览）。
// 自任滚动根；内容经默认插槽提供。注册进 Hyper.scroll 由调用方决定
// （页面 useHyperPage 传 scrollRoots = 本容器 ref）。
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { usePullToRefreshGesture } from '@/composables/usePullToRefreshGesture'

const props = defineProps<{
  onRefresh: () => Promise<void>
  /**
   * **真实滚动宿主**（默认：本容器自身）。
   *
   * ⚠️ 下拉刷新的「到顶」判据读的就是这个元素（`scroller.scrollTop <= 2`）。
   * 当插槽内容自带滚动宿主（内层 `.page` / `HyperList`）时**必须**显式传
   * 真正的那个，否则判据读的是一个**永远不滚**的容器：它的 scrollTop 恒为 0
   * ⇒ 页面滚到任何位置之后，**任何向下拖动**都会被下拉刷新抢走并
   * `preventDefault()` ⇒ 页面能滑下去、却**滑不回来**（2026-10-28 总览页缺陷）。
   */
  scroller?: () => HTMLElement | null
}>()

const rootRef = ref<HTMLElement | null>(null)

const gesture = usePullToRefreshGesture({
  onRefresh: props.onRefresh,
  getScroller: () => props.scroller?.() ?? rootRef.value,
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
