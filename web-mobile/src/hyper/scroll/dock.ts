// DockCoordinator 简化实现 — UI规范 07 §4 + 17 §4-R7。
// 统一 viewport CSS px；safe-area 已含在顶栏实测下沿时不再加第二次；
// 旋转/分屏/字号走 ResizeObserver + visualViewport。优先 CSS sticky，
// 本模块只负责 effectiveTop 的计算与跟踪（禁止写死"顶栏 48 + Tab 40"）。

export function computeEffectiveTop(headerEl: HTMLElement | null): number {
  if (!headerEl) return 0
  const bottom = headerEl.getBoundingClientRect().bottom
  return Math.max(0, bottom)
}

/** tableTop = effectiveTop + 所属吸顶 Tab 实际高度 + 区域工具栏高度（R7：无 Tab 取 0）。 */
export function computeTableTop(effectiveTop: number, tabHeightPx: number, toolbarHeightPx: number): number {
  return effectiveTop + tabHeightPx + toolbarHeightPx
}

/** 目标 pane 至少一个剩余可见视口高度才渲染切换（07 §5 / R7 精确式）。 */
export function hasResidualViewport(viewportHeightPx: number, tabTopPx: number, toolbarHeightPx: number): boolean {
  return viewportHeightPx - tabTopPx - toolbarHeightPx > 0
}
