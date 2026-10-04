<script setup lang="ts">
/**
 * HyperLoadMore — 连续加载的尾部控件（docs/UI规范/00 §5.2 · H3，参考规范 07 §3 / 13 §1）。
 *
 * 本组件**只负责表达状态与收集触发**，不持有数据、不发请求。
 * 请求与累积由 `createHyperPages` 负责；两者通过 props + emit 连接。
 * 这样列表的加载语义只有一处实现，尾部控件不可能与数据层说谎。
 *
 * ## 自动加载的四条约束（缺一条就会在真机上出问题）
 *
 * 1. **只有有下一页且非 busy 才请求。** 其余情况一律不触发 ——
 *    「触发条件」写成 `isIntersecting` 就等于把请求放大成每帧一次。
 * 2. **root 必须是 sentinel 的祖先。** 传 `null` 会退化成「相对视口判断」，
 *    在自定义滚动宿主里会把屏外几百像素的 sentinel 判成可见。
 * 3. **单轮自动填屏最多 3 页。** 首屏不足一屏时 IntersectionObserver 会连续命中，
 *    不设上限会一路把后端打穿。超限后降级为「继续加载」按钮。
 * 4. **sentinel 滚出视口顶部不算接近底部。** 只看 `isIntersecting` 会把
 *    「已经滚过去了」当成「快到底了」。
 *
 * ## 无 IntersectionObserver 时
 *
 * 降级为「继续加载」按钮，**不是**一个永远不触发的空页。
 * 旧 WebView / 测试环境 / 某些内嵌容器都可能没有这个 API。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ContinuousState } from '../../lib/shell/hyper/hyperPages'

const props = withDefaults(
  defineProps<{
    /** 与 `createHyperPages` 的 state 同名同义。 */
    state: ContinuousState
    /** 是否还有下一页。与 `hasMore` 同源，不要另算。 */
    hasMore: boolean
    /** 已加载行数，用于「已全部加载 N 条」。 */
    loadedCount: number
    /**
     * 观察容器的底边提前量（px）。规范值 240：sentinel 进入底部 240px 就预加载。
     */
    rootMarginBottom?: number
    /** 单轮自动填屏页数上限。规范值 3（设计值，不是实测结论）。 */
    maxAutoFillPages?: number
    /**
     * 滚动宿主。**必须是 sentinel 的祖先**，否则判定失真。
     * 不传表示由视口滚动（默认）。
     */
    scrollHost?: HTMLElement | null
  }>(),
  {
    rootMarginBottom: 240,
    maxAutoFillPages: 3,
    scrollHost: null,
  },
)

const emit = defineEmits<{
  (e: 'load-more'): void
  (e: 'retry'): void
  /** 查询条件变化时通知宿主：自动填屏配额已重置。 */
  (e: 'auto-fill-reset'): void
}>()

const { t } = useI18n()

const sentinel = ref<HTMLElement | null>(null)
/** 本轮已自动触发的次数。手动加载或重试会把它清零。 */
const autoFired = ref(0)

/** 环境是否提供 IntersectionObserver。 */
const observerAvailable = typeof IntersectionObserver !== 'undefined'

/** 自动填屏配额是否还在。 */
const autoQuotaLeft = computed(() => autoFired.value < props.maxAutoFillPages)

/** 忙态：不允许任何新触发。 */
const isBusy = computed(() => props.state === 'loadingNext' || props.state === 'refreshing')

/** 当前状态下是否**允许**再要一页。exhausted / failed / paused 一律不允许。 */
const canTrigger = computed(() => props.hasMore && props.state === 'idle')

/** 是否走自动触发。任一条件不满足就降级为按钮。 */
const autoTriggerEnabled = computed(() => observerAvailable && autoQuotaLeft.value && canTrigger.value)

let observer: IntersectionObserver | null = null

function teardown() {
  observer?.disconnect()
  observer = null
}

function setupObserver() {
  teardown()
  if (!sentinel.value) return
  // 模板里 `sentinel 存在 ⟺ autoTriggerEnabled`（那个 v-if 就是这条等价式的执行点），
  // 所以下面这行在**当前模板下不可达**。保留它只有一个理由：
  // 万一有人把模板改成「该降级时仍渲染 sentinel」，观察会挂在一个不该发请求的节点上，
  // 而症状是「偶发多发请求」——极难定位。留一道会被变异验证盯着的显式检查。
  if (!autoTriggerEnabled.value) return

  observer = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue
        // ★ 约束 4：从视口顶部滚过去的 sentinel 不算「接近底部」。
        // rootMargin 只向下扩展，向上滚过去时 top 为负。
        if (entry.boundingClientRect.top < 0) continue
        fireAuto()
      }
    },
    {
      root: props.scrollHost,
      rootMargin: `0px 0px ${props.rootMarginBottom}px 0px`,
      threshold: 0,
    },
  )
  observer.observe(sentinel.value)
}

function fireAuto() {
  // 约束 1：只有有下一页且非 busy 才请求。放在触发点再查一次，
  // 因为 observe 之后 state 可能已经变了。
  if (!canTrigger.value || isBusy.value) return
  autoFired.value += 1
  emit('load-more')
}

/** 宿主在查询条件变化时调用：重置自动填屏配额并重新观察。 */
function resetAutoFill() {
  autoFired.value = 0
  teardown()
  setupObserver()
  emit('auto-fill-reset')
}

function onManual() {
  // 手动加载说明用户在主动推进，配额重置 —— 下一屏仍可自动填。
  autoFired.value = 0
  emit('load-more')
}

function onRetry() {
  autoFired.value = 0
  emit('retry')
}

onMounted(setupObserver)
onBeforeUnmount(teardown)

// 四个输入合成**一个** watch：state / hasMore / scrollHost / 自动填屏配额。
// 拆成两个 watch 会让一次状态变化触发两次 setupObserver，
// 多建一个 observer 再立刻拆掉 —— 功能无害，但白白制造了一个「短暂并存」窗口。
//
// ★ flush: 'post' 是必需的，不是调优。默认的 'pre' 在 DOM 更新**之前**跑：
// 状态从 loadingNext 回到 idle 时，sentinel 还没重新挂上，sentinel.value 仍是 null，
// setupObserver 会静默早退 —— 表现为「列表加载完第一页后，自动加载永久失效」，
// 滚到底没有任何反应，也没有任何报错。
watch(
  () => [props.state, props.hasMore, props.scrollHost, autoQuotaLeft.value] as const,
  setupObserver,
  { flush: 'post' },
)

defineExpose({ resetAutoFill })
</script>

<template>
  <div class="hyper-load-more" :data-state="state">
    <!-- 没有更多：不再观察、不再触发 -->
    <p v-if="state === 'exhausted'" class="hyper-load-more__end" role="status">
      {{ t('hyper.list.allLoaded', { count: loadedCount }) }}
    </p>

    <!-- 失败：停自动重试，保留已加载行，出重试 -->
    <div v-else-if="state === 'failed'" class="hyper-load-more__failed">
      <span class="hyper-load-more__failed-text">{{ t('hyper.list.loadFailed') }}</span>
      <button type="button" class="hyper-load-more__btn" @click="onRetry">
        {{ t('hyper.list.retry') }}
      </button>
    </div>

    <!--
      暂停：宿主停止观察时（页面隐藏 / KeepAlive 停用）给一个回来的入口。
      这里仍然发 load-more 而不是自定义事件 —— 组件只收集触发，
      「要不要先 resume」是宿主的事，不该由尾部控件替他决定。
    -->
    <div v-else-if="state === 'paused'" class="hyper-load-more__paused">
      <button type="button" class="hyper-load-more__btn" @click="onManual">
        {{ t('hyper.list.loadMore') }}
      </button>
    </div>

    <!-- 忙：只出状态，不出可点控件（防重复请求） -->
    <div v-else-if="isBusy" class="hyper-load-more__busy" role="status" aria-live="polite">
      {{ state === 'refreshing' ? t('hyper.list.refreshing') : t('hyper.list.loadingMore') }}
    </div>

    <!-- 空闲：自动（sentinel）或手动（按钮） -->
    <template v-else>
      <button
        v-if="!autoTriggerEnabled"
        type="button"
        class="hyper-load-more__btn hyper-load-more__btn--manual"
        @click="onManual"
      >
        {{ t('hyper.list.loadMore') }}
      </button>
      <!--
        sentinel 是纯几何触发点，不可达、不抢焦点。
        无 IntersectionObserver 或自动配额用尽时它整个不渲染（v-if），
        这样「有没有手动入口」可以被直接断言，而不是靠 CSS 切换可见性。
      -->
      <div v-else ref="sentinel" class="hyper-load-more__sentinel" aria-hidden="true" />
    </template>
  </div>
</template>

<style scoped>
.hyper-load-more {
  display: flex;
  align-items: center;
  justify-content: center;
  /* R1（2026-10-04 吸收）：≥48px。此处原本 44px。 */
  min-height: 48px;
  padding: 8px 0;
  color: var(--kx-muted);
  font-size: 13px;
}

.hyper-load-more__end,
.hyper-load-more__busy,
.hyper-load-more__failed,
.hyper-load-more__paused {
  display: flex;
  align-items: center;
  gap: 8px;
}

.hyper-load-more__btn {
  /* R1（2026-10-04 吸收）：≥48px。此处原本 44px。 */
  min-height: 48px;
  padding: 0 16px;
  background: var(--kx-bg);
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  color: var(--kx-text);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}

/* 焦点可见性：底栏/尾部控件在键盘与开关控制下必须看得见焦点环 */
.hyper-load-more__btn:focus-visible {
  outline: 2px solid var(--kx-primary);
  outline-offset: 2px;
}

.hyper-load-more__sentinel {
  width: 100%;
  height: 1px;
}
</style>
