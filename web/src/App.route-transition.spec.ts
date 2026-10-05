// App.route-transition.spec.ts — compact 路由转场门禁（docs/UI规范/12 §7，2026-10-05）。
//
// 断言的是契约，不是动画本身（jsdom 无真实渲染管线）：
// 1. 已登录 RouterView 用 v-slot + Transition，name 由 isCompact 三元裁决 ——
//    compact = page-fade（160ms 纯 opacity），medium+ = page-none（无 CSS ⇒ 瞬时）。
// 2. :key 只在 compact 绑定 route.path；桌面 key=undefined，维持「同组件不重挂」
//    的既有行为 —— 桌面零回归红线（00 F11）在 DOM 与行为两层都不得破。
// 3. page-fade 规则只允许 opacity（GPU 合成）；出现 layout 属性即红。
// 4. page-none 刻意无规则 —— 若有人「补全」它，桌面就被悄悄改了，必须红。
// 5. page-fade 不得进 styles/hyper.css：死选择器门禁要求类名在 src/ 有实义引用，
//    Transition 运行时生成的类名做不到，进 hyper.css 必打空（10 §4.6.2 D6 同族）。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(process.cwd(), 'src/App.vue'), 'utf8')
/** 剥掉全部注释后再做源码断言（本仓既有教训：不剥会把注释里的词误判成违规）。 */
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

/** 抽出 .page-fade-* 规则块内的声明文本（从名字出现处到下一个右花括号）。 */
function fadeRuleDeclarations(): string {
  const out: string[] = []
  const re = /\.page-fade-[a-z-]+\s*\{([^}]*)\}/g
  let m: RegExpExecArray | null
  while ((m = re.exec(source))) out.push(m[1])
  return out.join('\n')
}

describe('App.vue：compact 路由转场（12 §7）', () => {
  it('已登录 RouterView 用 v-slot + Transition + out-in，name 由 isCompact 裁决', () => {
    expect(codeOnly).toMatch(/<RouterView v-slot="\{ Component \}">/)
    expect(codeOnly).toMatch(/<Transition :name="isCompact \? 'page-fade' : 'page-none'" mode="out-in">/)
    expect(codeOnly).toMatch(/<component :is="Component" :key="isCompact \? route\.path : undefined" \/>/)
  })

  it('未登录 RouterView 不加转场 —— guest 体系保持原貌（登记于 12 §7，不是遗漏）', () => {
    const guestArea = codeOnly.slice(codeOnly.lastIndexOf('<main class="guest-main">'))
    expect(guestArea).toMatch(/<RouterView \/>/)
    expect(guestArea).not.toMatch(/<Transition/)
  })

  it('page-fade 只允许动 opacity —— 出现任何 layout/transform 声明即违规', () => {
    const decls = fadeRuleDeclarations()
    expect(decls).toContain('opacity')
    const banned = /\b(transform|translate|margin|padding|top|left|right|bottom|width|height|filter|backdrop|box-shadow|background|color)\s*:/
    const offending = decls.split('\n').filter((l) => banned.test(l))
    expect(offending, `page-fade 规则里出现禁动属性：${offending.join(' | ')}`).toEqual([])
  })

  it('时长 160ms 且 easing 固定 ease（与 web-mobile 对齐，超过 250ms 即手感拖沓）', () => {
    expect(source).toMatch(/transition:\s*opacity 160ms ease/)
  })

  it('prefers-reduced-motion 下转场取消（12 §4 契约）', () => {
    const reduced = source.match(/@media \(prefers-reduced-motion: reduce\)\s*\{[\s\S]*?\}\s*\}/)
    expect(reduced).toBeTruthy()
    expect(reduced![0]).toMatch(/transition:\s*none/)
  })

  it('page-none 刻意无任何规则 —— 被「补全」即桌面被改', () => {
    expect(source).not.toMatch(/\.page-none/)
  })

  it('page-fade 不进 styles/hyper.css（死选择器门禁约束：运行时类名无法在 src/ 立实义引用）', () => {
    const hyperCss = readFileSync(resolve(process.cwd(), 'src/styles/hyper.css'), 'utf8')
    expect(hyperCss).not.toContain('page-fade')
  })
})
