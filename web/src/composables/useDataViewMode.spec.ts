// useDataViewMode.spec.ts — 表格/卡片视图模式门禁（docs/UI规范/00 §5.4 · H3）。
//
// 最要紧的一条是 **compact 下 setMode 既不生效也不落盘**。
// 如果只把切换钮 `v-if` 掉而放开 setMode，调用方仍能在别处把横滚表格
// 留在 320px 屏上 —— 那是规范明令禁止的降级，而且没有任何 UI 会暴露它。
import { beforeEach, describe, expect, it } from 'vitest'
import { nextTick, ref } from 'vue'
import { _resetForTests } from './useWindowClass'
import {
  DATA_VIEW_MODE_KEY,
  _resetDataViewModeForTests,
  useDataViewMode,
} from './useDataViewMode'

/** 把 useWindowClass 单例钉在指定档位（与 AppBottomNav.test.ts 同一套替身）。 */
function mockWindowClass(cls: 'compact' | 'medium' | 'expanded' | 'large'): void {
  _resetForTests()
  const bounds: Record<string, [number, number]> = {
    compact: [0, 767.98],
    medium: [768, 1023.98],
    expanded: [1024, 1439.98],
    large: [1440, 4000],
  }
  const [lo] = bounds[cls]
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => {
      const min = query.match(/min-width:\s*([\d.]+)px/)
      const max = query.match(/max-width:\s*([\d.]+)px/)
      const matches = min ? lo >= parseFloat(min[1]) : max ? lo <= parseFloat(max[1]) : false
      return {
        matches,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }
    },
  })
}

beforeEach(() => {
  _resetDataViewModeForTests()
  mockWindowClass('expanded')
})

describe('useDataViewMode：非 compact 档', () => {
  it('默认表格，且允许切换', () => {
    const { preference, effective, canSwitch, isCompact } = useDataViewMode()
    expect(isCompact.value).toBe(false)
    expect(canSwitch.value).toBe(true)
    expect(preference.value).toBe('table')
    expect(effective.value).toBe('table')
  })

  it('setMode 切换并落盘，effective 跟随 preference', () => {
    const { preference, effective, setMode } = useDataViewMode()
    expect(setMode('cards')).toBe(true)
    expect(preference.value).toBe('cards')
    expect(effective.value).toBe('cards')
    expect(localStorage.getItem(DATA_VIEW_MODE_KEY)).toBe('cards')

    expect(setMode('table')).toBe(true)
    expect(effective.value).toBe('table')
    expect(localStorage.getItem(DATA_VIEW_MODE_KEY)).toBe('table')
  })

  it('设置成与当前相同的值不算变更，但返回 true', () => {
    const { setMode } = useDataViewMode()
    expect(setMode('table')).toBe(true)
    // 没有重复写盘的必要，但也不该报错
    expect(localStorage.getItem(DATA_VIEW_MODE_KEY)).toBeNull()
  })

  it('读存储恢复上次偏好', () => {
    localStorage.setItem(DATA_VIEW_MODE_KEY, 'cards')
    const { preference, effective } = useDataViewMode()
    expect(preference.value).toBe('cards')
    expect(effective.value).toBe('cards')
  })

  it('存储里的非法值退回默认，而不是当成合法模式', () => {
    localStorage.setItem(DATA_VIEW_MODE_KEY, 'table-with-unicorns')
    const { preference } = useDataViewMode()
    expect(preference.value).toBe('table')
  })

  it('defaultMode 可覆盖', () => {
    localStorage.setItem(DATA_VIEW_MODE_KEY, 'table')
    const { preference } = useDataViewMode({ defaultMode: 'cards' })
    // 存储优先于默认值
    expect(preference.value).toBe('table')
  })

  it('initial 外部值优先于存储与默认，并在其变化时跟随', async () => {
    localStorage.setItem(DATA_VIEW_MODE_KEY, 'table')
    const initial = ref<'table' | 'cards' | null>('cards')
    const { preference } = useDataViewMode({ initial })
    // immediate: true —— 首次值在 watch 注册时就已生效，无需等一拍
    expect(preference.value).toBe('cards')

    initial.value = 'table'
    // watch 默认异步 flush（pre），必须等一拍，否则读到的是上一拍的值
    await nextTick()
    expect(preference.value).toBe('table')
  })

  it('initial 为 null 时不动存储', () => {
    const initial = ref<'table' | 'cards' | null>(null)
    const { preference } = useDataViewMode({ initial })
    expect(preference.value).toBe('table')
    expect(localStorage.getItem(DATA_VIEW_MODE_KEY)).toBeNull()
  })
})

describe('useDataViewMode：compact 档强制卡片', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('effective 恒为 cards，canSwitch 为 false', () => {
    const { effective, canSwitch, isCompact } = useDataViewMode()
    expect(isCompact.value).toBe(true)
    expect(canSwitch.value).toBe(false)
    expect(effective.value).toBe('cards')
  })

  it('setMode 直接返回 false，既不生效也不写存储', () => {
    const { preference, effective, setMode } = useDataViewMode()
    expect(setMode('table')).toBe(false)
    // 偏好没被改
    expect(preference.value).toBe('table')
    // 实际渲染仍是卡片
    expect(effective.value).toBe('cards')
    // ★ 关键：不落盘。否则回到桌面后会带着用户在 compact 下没能设成的模式
    expect(localStorage.getItem(DATA_VIEW_MODE_KEY)).toBeNull()
  })

  it('存储里已有 table 时，compact 仍渲染卡片', () => {
    localStorage.setItem(DATA_VIEW_MODE_KEY, 'table')
    const { preference, effective, setMode } = useDataViewMode()
    expect(preference.value).toBe('table') // 偏好被读到了
    expect(effective.value).toBe('cards') // 但实际渲染不是表格
    expect(setMode('table')).toBe(false)
  })

  it('initial 在 compact 下不生效（不把外部值塞进偏好）', () => {
    const initial = ref<'table' | 'cards' | null>('cards')
    const { preference } = useDataViewMode({ initial })
    expect(preference.value).toBe('table')
  })
})

describe('useDataViewMode：存储不可用时的降级', () => {
  it('localStorage.getItem 抛错时退回默认，不让组合式函数炸掉', () => {
    const original = Storage.prototype.getItem
    Storage.prototype.getItem = () => {
      throw new Error('SecurityError: storage disabled')
    }
    try {
      const { preference, effective } = useDataViewMode()
      expect(preference.value).toBe('table')
      expect(effective.value).toBe('table')
    } finally {
      Storage.prototype.getItem = original
    }
  })

  it('localStorage.setItem 抛错（配额满）时内存偏好仍生效', () => {
    const original = Storage.prototype.setItem
    Storage.prototype.setItem = () => {
      throw new Error('QuotaExceededError')
    }
    try {
      const { setMode, effective } = useDataViewMode()
      expect(setMode('cards')).toBe(true)
      // 没落盘但本次会话内照样生效 —— 这正是内存偏好存在的理由
      expect(effective.value).toBe('cards')
    } finally {
      Storage.prototype.setItem = original
    }
  })
})
