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

/**
 * jsdom 未实现 Element.scrollIntoView（实测 jsdom 24 仍是 undefined）。
 * 触到「滚入视野」路径的组件（如 LiveTopologyView 打开摘要浮层时的
 * scrollIntoView）在测试下会 TypeError。默认 no-op；需要断言「有没有滚、
 * 怎么滚」的用例可自行用 vi.spyOn 覆盖。
 */
if (typeof Element !== 'undefined' && typeof Element.prototype.scrollIntoView !== 'function') {
  Object.defineProperty(Element.prototype, 'scrollIntoView', {
    writable: true,
    configurable: true,
    value: () => {},
  })
}

export {}
