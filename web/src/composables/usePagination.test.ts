// usePagination.test.ts — 派生状态与翻页动作语义（与被替换的手写实现对齐）
import { describe, expect, it, vi } from 'vitest'
import { ref, type Ref } from 'vue'
import { usePagination } from './usePagination'

function setup(total = 0, page = 1, pageSize = 50) {
  const pageRef: Ref<number> = ref(page)
  const onChange = vi.fn()
  const pager = usePagination({
    page: pageRef,
    pageSize: ref(pageSize),
    total: ref(total),
    onChange,
  })
  return { pager, pageRef, onChange }
}

describe('usePagination', () => {
  it('derives pages from total/pageSize, minimum 1', () => {
    expect(setup(0).pager.pages.value).toBe(1)
    expect(setup(50).pager.pages.value).toBe(1)
    expect(setup(51).pager.pages.value).toBe(2)
    expect(setup(250, 1, 50).pager.pages.value).toBe(5)
  })

  it('computes canPrev/canNext from current page', () => {
    const { pager } = setup(250, 1)
    expect(pager.canPrev.value).toBe(false)
    expect(pager.canNext.value).toBe(true)
  })

  it('next() advances the page and triggers onChange', () => {
    const { pager, pageRef, onChange } = setup(250, 1)
    pager.next()
    expect(pageRef.value).toBe(2)
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it('prev() at page 1 is ignored without triggering onChange', () => {
    const { pager, pageRef, onChange } = setup(250, 1)
    pager.prev()
    expect(pageRef.value).toBe(1)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('go() clamps out-of-range targets silently', () => {
    const { pager, onChange } = setup(100, 1) // pages=2
    pager.go(99)
    expect(onChange).not.toHaveBeenCalled()
    pager.go(0)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('reset() always reloads even when already on page 1 (pageSize-change semantics)', () => {
    const { pager, onChange } = setup(250, 1)
    pager.reset()
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it('reset() returns to page 1 before reloading', () => {
    const page = ref(3)
    const onChange = vi.fn()
    usePagination({ page, pageSize: ref(50), total: ref(500), onChange }).reset()
    expect(page.value).toBe(1)
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it('reacts to external pageSize bumps (trace mode forcing >=200)', () => {
    const pageSize = ref(50)
    const pager = usePagination({ page: ref(1), pageSize, total: ref(500), onChange: () => {} })
    expect(pager.pages.value).toBe(10)
    pageSize.value = 200
    expect(pager.pages.value).toBe(3)
  })
})
