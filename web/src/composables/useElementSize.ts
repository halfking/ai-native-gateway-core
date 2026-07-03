// useElementSize — track an element's rendered width reactively.
import { ref, onMounted, onBeforeUnmount, type Ref } from 'vue'

export function useElementSize(target: Ref<HTMLElement | null | undefined>) {
  const width = ref(0)
  let observer: ResizeObserver | null = null

  function update() {
    const el = target.value
    if (!el) return
    const rect = el.getBoundingClientRect()
    width.value = Math.floor(rect.width)
  }

  onMounted(() => {
    update()
    const el = target.value
    if (!el) return
    if (typeof ResizeObserver !== 'undefined') {
      observer = new ResizeObserver(() => update())
      observer.observe(el)
    } else {
      window.addEventListener('resize', update)
    }
  })

  onBeforeUnmount(() => {
    if (observer) {
      observer.disconnect()
      observer = null
    }
    window.removeEventListener('resize', update)
  })

  return { width }
}
