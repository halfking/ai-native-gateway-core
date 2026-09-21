/**
 * Vitest 全局 setup（2026-09-13，方案 §4.3 P0）。
 *
 * jsdom 未实现 window.matchMedia，凡触到 matchMedia 的组件/组合式函数
 * （如 useBreakpoint、theme.ts 的 prefers-color-scheme 检测）在测试下会直接
 * TypeError。这里补一个最小 stub：默认 matches=false、监听器 no-op。
 * 需要控制 matches 的用例（如 useBreakpoint.test.ts）可自行覆盖 window.matchMedia。
 */
if (typeof window !== 'undefined' && typeof window.matchMedia !== 'function') {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  })
}

export {}
