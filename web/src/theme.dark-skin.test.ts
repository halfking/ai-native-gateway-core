import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { applyTheme } from './theme'

// 暗色皮肤适配回归（2026-09-13 用户报告：暗色下 EP 组件大块亮色）。
// 根因：EP 组件消费 --el-*（theme-chalk/dark/css-vars.css 以 html.dark 门控），
// 但 applyTheme 只切 data-theme，EP 组件始终亮色。
const mainTs = readFileSync(resolve(process.cwd(), 'src/main.ts'), 'utf8')
const elementDarkCss = readFileSync(
  resolve(process.cwd(), 'src/styles/element-dark.css'),
  'utf8',
)
// 启动路径：index.html <head> 内同步执行的 theme-init.js（public/，不参与打包），
// 是「刷新/直链（无 ?theme= 参数）」场景下唯一设置主题的代码——漏掉 dark 类
// 会让 EP 组件在每次刷新后回退亮色（2026-09-13 审计发现的真因）。
const themeInitJs = readFileSync(resolve(process.cwd(), 'public/theme-init.js'), 'utf8')

describe('dark skin bridging (EP components)', () => {
  it('applyTheme toggles html.dark class together with data-theme', () => {
    const root = document.documentElement
    applyTheme('dark')
    expect(root.getAttribute('data-theme')).toBe('dark')
    expect(root.classList.contains('dark')).toBe(true)

    applyTheme('light')
    expect(root.getAttribute('data-theme')).toBe('light')
    expect(root.classList.contains('dark')).toBe(false)
  })

  it('main.ts imports EP dark css-vars and the element-dark bridge', () => {
    expect(mainTs).toContain("'element-plus/theme-chalk/dark/css-vars.css'")
    expect(mainTs).toContain("'./styles/element-dark.css'")
    // 桥接文件必须晚于 EP dark 文件引入（同特异性时后者胜出的兜底，
    // 桥接文件本身另用更高特异性 html.dark[data-theme='dark'] 双保险）。
    expect(mainTs.indexOf("'element-plus/theme-chalk/dark/css-vars.css'")).toBeLessThan(
      mainTs.indexOf("'./styles/element-dark.css'"),
    )
  })

  it('theme-init.js boot script toggles the dark class (refresh persistence)', () => {
    expect(themeInitJs).toContain("classList.toggle('dark'")
    // 与 theme.ts 同一存储键与同一双轨约定，防两处漂移
    expect(themeInitJs).toContain("llmgw_theme")
    expect(themeInitJs).toContain("setAttribute('data-theme'")
  })

  it('element-dark.css bridges EP surfaces to app tokens at higher specificity', () => {
    // 选择器只出现在规则处（行首），亮色不得被波及：桥接只存在于 dark 门控内
    expect(elementDarkCss.match(/(^|\n)html\.dark\[data-theme/g)).toHaveLength(1)
    for (const epVar of [
      '--el-bg-color:',
      '--el-bg-color-overlay:',
      '--el-bg-color-page:',
      '--el-fill-color-blank:',
      '--el-text-color-primary:',
      '--el-border-color:',
    ]) {
      expect(elementDarkCss).toContain(epVar)
      expect(elementDarkCss).toContain('var(--kx-')
    }
  })
})
