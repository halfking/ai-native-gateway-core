/**
 * 选择控件点开时压掉软键盘。
 *
 * Android WebView 上：若搜索框等 input 仍 focus，点 `<select>` 会同时弹出
 * 软键盘，把原生选项列表遮掉一半（需求点名过）。iOS 原生 select 走转盘，
 * 通常无此问题，但 blur 多余一次无害。
 *
 * 在 capture 阶段于 pointerdown/touchstart/mousedown 上对 select 先 blur
 * 当前焦点，再让系统弹出选项。
 */
export function installSelectKeyboardSuppress(root: ParentNode = document): () => void {
  const onPick = (event: Event) => {
    const target = event.target
    if (!(target instanceof HTMLSelectElement)) return
    const active = document.activeElement
    if (active instanceof HTMLElement && active !== target) active.blur()
  }
  root.addEventListener('pointerdown', onPick, true)
  root.addEventListener('touchstart', onPick, true)
  root.addEventListener('mousedown', onPick, true)
  return () => {
    root.removeEventListener('pointerdown', onPick, true)
    root.removeEventListener('touchstart', onPick, true)
    root.removeEventListener('mousedown', onPick, true)
  }
}
