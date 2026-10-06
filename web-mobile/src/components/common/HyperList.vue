<script setup lang="ts" generic="T">
// HyperList — 连续加载列表容器（UI规范 07 §1–§3 落地件）。
// 自任滚动宿主（默认 scrollId 'main'）；下拉刷新 + sentinel 预载 +
// 分页状态脚注；失败保旧、手动重试；「已加载 X / 总计 Y」。
// 页面用本组件做主滚动区时，不要再向 useHyperPage 传 scrollRoots。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Hyper, type ContinuousListController } from '@/hyper'
import { usePullToRefreshGesture } from '@/composables/usePullToRefreshGesture'
import { t } from '@/i18n'
import AppStateView from './AppStateView.vue'

const props = withDefaults(
  defineProps<{
    controller: ContinuousListController<T>
    itemKey: (item: T) => string
    onRefresh?: () => Promise<void>
    scrollId?: string
    refreshable?: boolean
    emptyHint?: string
  }>(),
  { scrollId: 'main', refreshable: true, emptyHint: undefined },
)

const rootRef = ref<HTMLElement | null>(null)
const sentinelRef = ref<HTMLElement | null>(null)
const tick = ref(0)

const state = computed(() => {
  void tick.value
  return props.controller.state
})
const items = computed(() => {
  void tick.value
  return props.controller.items
})
const loadedCount = computed(() => {
  void tick.value
  return props.controller.loadedCount
})
const total = computed(() => {
  void tick.value
  return props.controller.total
})

/**
 * 失败提示按**失败来源**分文案（2026-10-04）。
 *
 * 为什么不能所有失败都显示 `common.errorHint`（"请检查网络后重试"）：
 *   实测 /nodes 的失败是服务端 500（admin/credential_monitor.go:657 查询超时 15s），
 *   页面却让用户去查手机网络 —— 用户会照着错误方向排查半天，而真正的故障在服务器。
 *
 * 判据用 `status`：`_core.ts` 的 ApiError 对网络层失败给 status 0，
 * 对 HTTP 失败给真实状态码。0 = 用户侧，其余（≥500）= 服务端。
 * 拿不到 status（不认识的对象）时退回中性文案，不猜。
 */
const errorText = computed(() => {
  void tick.value
  const err = props.controller.error as { status?: unknown; message?: unknown } | null
  const status = typeof err?.status === 'number' ? err.status : null
  if (status === null) return t('common.loadFailedTapRetry')
  if (status === 0) return t('common.errorHint')
  if (status >= 500) return t('common.errorHintServer')
  return t('common.loadFailedTapRetry')
})

const gesture = usePullToRefreshGesture({
  onRefresh: async () => {
    if (props.onRefresh) await props.onRefresh()
    props.controller.refresh()
  },
  getScroller: () => rootRef.value,
  enabled: () => props.refreshable,
})

// ---- sentinel 预载（07 §3：距底 240px） ----
let observer: IntersectionObserver | null = null

function isScreenFilled(): boolean {
  const root = rootRef.value
  return !!root && root.scrollHeight > root.clientHeight + 4
}

function syncObserver(): void {
  const sentinel = sentinelRef.value
  const root = rootRef.value
  if (!sentinel || !root) return
  if (!observer) {
    if (typeof IntersectionObserver === 'undefined') return
    observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          props.controller.loadNext()
        }
      },
      { root, rootMargin: '0px 0px 240px 0px' },
    )
  }
  observer.disconnect()
  if (state.value === 'exhausted' || state.value === 'loadFailed') return
  observer.observe(sentinel)
}

watch(state, () => syncObserver())

let cleanup: (() => void) | null = null

onMounted(() => {
  const root = rootRef.value
  if (!root) return
  const unbindGesture = gesture.bind(root)
  const releaseHost = Hyper.scroll.register({
    id: props.scrollId,
    axis: 'y',
    getEl: () => rootRef.value,
  })
  const unsubscribe = props.controller.subscribe(() => {
    tick.value++
    syncObserver()
  })
  cleanup = () => {
    unsubscribe()
    releaseHost()
    unbindGesture()
  }
  if (state.value === 'idle') {
    props.controller.loadFirst('initial')
  }
  void props.controller.autoFill(isScreenFilled)
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
  cleanup?.()
  cleanup = null
  // ★ 2026-10-06：卸载时 dispose controller —— 取消在途请求 + 递增 revision，
  //   否则 fetchPage 的 await 续体会回来改已卸载组件的状态。详见 continuousList.dispose。
  props.controller.dispose()
})

const showEmpty = computed(() => state.value === 'exhausted' && loadedCount.value === 0)
const hasContent = computed(() => loadedCount.value > 0)
</script>

<template>
  <div ref="rootRef" class="hyper-list" :data-refreshing="gesture.machine.state === 'refreshing' ? 'on' : undefined">
    <div
      class="hyper-list__ptr"
      :class="{ 'hyper-list__ptr--armed': gesture.machine.state === 'armed' }"
      :style="{ height: `${gesture.visualOffset.value}px` }"
      aria-live="polite"
    >
      <span class="hyper-list__ptr-label">{{ gesture.label.value }}</span>
    </div>

    <AppStateView v-if="state === 'initialLoading' && !hasContent" :loading="true" :skeleton-rows="6" />
    <AppStateView v-else-if="state === 'loadFailed' && !hasContent" :error="errorText" @retry="controller.retry()" />
    <AppStateView v-else-if="showEmpty" :empty="true">
      <template #empty-hint>
        <span class="hyper-list__empty-hint">{{ emptyHint }}</span>
      </template>
    </AppStateView>

    <template v-else>
      <div class="hyper-list__inner">
        <slot name="header" />
        <div class="card-list">
          <div
            v-for="item in items"
            :key="itemKey(item)"
            :data-hyper-anchor="itemKey(item)"
            class="hyper-list__row"
          >
            <slot name="item" :item="item" :index="0" />
          </div>
        </div>

        <div ref="sentinelRef" class="hyper-list__sentinel" aria-hidden="true" />

        <footer class="hyper-list__footer">
          <span v-if="state === 'loadingNext'" class="hyper-list__footnote">{{ t('common.loading') }}</span>
          <button
            v-else-if="state === 'loadMoreFailed'"
            type="button"
            class="btn btn--sm hyper-list__retry"
            @click="controller.retry()"
          >
            {{ t('common.loadFailedTapRetry') }}
          </button>
          <span v-else-if="state === 'exhausted'" class="hyper-list__footnote">
            {{ t('common.allLoaded', { n: loadedCount }) }}
          </span>
          <span v-else-if="total !== undefined" class="hyper-list__footnote num">
            {{ t('common.loadedOfTotal', { loaded: loadedCount, total }) }}
          </span>
        </footer>
      </div>
    </template>
  </div>
</template>

<style scoped>
.hyper-list {
  position: relative;
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
  overscroll-behavior-y: contain;
  touch-action: pan-y pinch-zoom;
}

.hyper-list__ptr {
  overflow: hidden;
  display: flex;
  align-items: center;
  justify-content: center;
  height: 0;
}

.hyper-list__ptr-label {
  font-size: 0.75rem;
  color: var(--app-text-muted);
  white-space: nowrap;
}

.hyper-list__inner {
  padding: var(--app-space-content-y) var(--app-space-content-x) var(--app-space-4);
  max-width: 720px;
  margin: 0 auto;
  width: 100%;
}

.hyper-list__row {
  display: block;
}

.hyper-list__sentinel {
  height: 1px;
}

.hyper-list__footer {
  display: flex;
  justify-content: center;
  padding: var(--app-space-3) 0 var(--app-space-2);
}

.hyper-list__footnote {
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.hyper-list__empty-hint {
  font-size: 0.75rem;
  color: var(--app-text-muted);
}
</style>
