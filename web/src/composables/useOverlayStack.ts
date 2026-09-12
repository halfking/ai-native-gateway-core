// Module-scoped registry of open overlay layers (AppModal / AppDrawer).
//
// ESC must close only the TOPMOST overlay: previously every stacked instance
// listened for Escape independently and a single keypress closed the whole
// stack at once (e.g. ProvidersView credential drawer with a stacked edit
// modal lost both layers and their state). Each instance registers on open,
// unregisters on close, and only the layer at the top of the stack may act
// on Escape — audit R20, 2026-09-13.
const stack: number[] = []
let seq = 0

export function nextOverlayLayerId(): number {
  return ++seq
}

export function pushOverlayLayer(id: number): void {
  if (!stack.includes(id)) stack.push(id)
}

export function popOverlayLayer(id: number): void {
  const idx = stack.indexOf(id)
  if (idx >= 0) stack.splice(idx, 1)
}

export function isTopmostOverlayLayer(id: number): boolean {
  return stack[stack.length - 1] === id
}

/** Testing helper: observed stack depth. */
export function overlayStackDepth(): number {
  return stack.length
}
