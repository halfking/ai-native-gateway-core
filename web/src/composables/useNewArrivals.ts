// useNewArrivals.ts —— 「新到达」行标记（/live 左右请求面板共用，2026-10-28）
//
// 实时流里新请求到达时，只有**新出现的那几行**做入场动画，其余行纹丝
// 不动 —— 否则整列表每次刷新都重放动画，一条新请求会被放大成「整个
// 面板都在动」，动画从信息变成噪声。
//
// 判定语义（与 SSE 数据更新同构：新请求 prepend 到列表头部）：
//  · **首帧不标**：组件挂载 / 数据源切换时整页都是「新」的 ⇒ 全闪 = 噪声，
//    那是数据初始化不是活动；
//  · **头对头扫描**：从列表头扫到上一帧的头部 id 为止，中间的都是新增
//    （不需要记住全部历史 id —— 列表本身有界，历史记忆会让 Set 无限涨）；
//  · **上一帧头部已滚出列表**（大换血 / 窗口重置）：只标头部最多 40 条，
//    不把整页标新；
//  · 标记存活 lifetimeMs 后自动清除（入场动画 300ms，标记多留一点余量）。
//
// reduced-motion：本 composable 只负责标记，动画由 CSS 层负责并在
// prefers-reduced-motion 下中和（见面板样式）—— 标记本身无副作用。

import { onBeforeUnmount, ref, watch, type Ref } from 'vue'

export interface NewArrivals {
  /** 当前「新到达」的 id 集合（响应式；组件卸载时自然回收）。 */
  fresh: Ref<ReadonlySet<string>>
}

/** 兜底上限：上一帧头部滚出列表时最多标记的头部条数。 */
const MASS_TURNOVER_MARK_CAP = 40

export function useNewArrivals(
  ids: () => readonly string[],
  lifetimeMs = 700,
): NewArrivals {
  const fresh = ref<ReadonlySet<string>>(new Set<string>())
  let primed = false
  let lastHead: string | null = null
  const timers: number[] = []

  function track(list: readonly string[]): void {
    if (!list.length) {
      lastHead = null
      return
    }
    if (!primed) {
      primed = true
      lastHead = list[0]
      return
    }
    if (list[0] === lastHead) return
    // 从头扫到上一帧的头部 id；找不到（大换血）则只标头部一段。
    let stop = list.indexOf(lastHead as string)
    if (stop < 0) stop = Math.min(list.length, MASS_TURNOVER_MARK_CAP)
    lastHead = list[0]
    if (stop <= 0) return
    const added = list.slice(0, stop)
    const next = new Set(fresh.value)
    for (const id of added) next.add(id)
    fresh.value = next
    timers.push(
      window.setTimeout(() => {
        const after = new Set(fresh.value)
        for (const id of added) after.delete(id)
        fresh.value = after
      }, lifetimeMs),
    )
  }

  watch(ids, track, { immediate: true })

  onBeforeUnmount(() => {
    for (const handle of timers) window.clearTimeout(handle)
  })

  return { fresh }
}
