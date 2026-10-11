import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// 表单控件字体门禁（2026-10-12）。
//
// 现象：供应商列表页搜索框的 placeholder「搜索名称 / code / 目录码」
// 在模拟器上逐字渲染成豆腐块「🔍 ? ? ? ? / code」，而同一页面的正文
// 「共 52 家」完全正常 —— **同页两套字体**，只有控件那套没有 CJK 字形。
//
// 根因是两条，缺一不可地共同作用：
//   ① shared.css 只给 button 写了 `font: inherit`，<input>/<select>/
//      <textarea> 仍在用 UA 默认字体；
//   ② iOS 26.3 运行时里 UA 默认字体解析成 San Francisco（无 CJK），
//      而 body 的 fallback 链必须**显式**含一个真带中文字形的家族。
//
// 只修 ① 时，若将来有人把 body 的 font-family 改成只有 -apple-system/
// sans-serif，本组会红 —— 那正是同一个豆腐块问题的另一条腿。
// 反过来只修 ② 而漏掉 ①，也一样红。两条都在，控件与正文才真的同源。

const CSS = readFileSync(resolve(process.cwd(), 'src/styles/shared.css'), 'utf8')

/** 同一选择器在本文件里可能出现多次（body 既有 min-width 重置、也有 font-family），
 *  所以收集**全部**规则块再判断；只取第一块会把后面的字体规则漏掉，
 *  判据就成了空断言（曾经真踩过：body 在 :16 与 :56 各有一条）。 */
function blocksFor(selector: string): string[] {
  const re = new RegExp(`(?:^|\\})\\s*${selector}\\s*(?:,[^{]*)?\\{([^}]*)\\}`, 'gm')
  const out: string[] = []
  let m: RegExpExecArray | null
  while ((m = re.exec(CSS)) !== null) {
    // `noUncheckedIndexedAccess` 下 m[1] 的类型是 string | undefined，
    // 而正则保证第 1 个捕获组存在（`\{([^}]*)\}`）—— 直接断言成 string，
    // 别用 `?? ''`：那样会把「正则写错了」静默变成「规则块为空」，
    // 判据就退化成恒真。
    out.push(m[1] as string)
  }
  if (out.length === 0) throw new Error(`shared.css 里找不到选择器 ${selector}`)
  return out
}

describe('form controls share the body font (CJK must not fall back to a tofu font)', () => {
  it.each(['input', 'select', 'textarea'])('%s inherits the document font', (sel) => {
    // 变异点：删掉 shared.css 里那条 input/select/textarea 规则，这里就红。
    expect(blocksFor(sel).join("\n")).toMatch(/(^|;)\s*font\s*:\s*inherit\s*;/)
  })

  it('body font stack names a CJK-capable family, not only Latin system fonts', () => {
    const body = blocksFor("body").join("\n")
    const stack = /font-family:([^;]+);/.exec(body)?.[1] ?? ''
    // 变异点：把 'Hiragino Sans GB' 从 body 字体栈里删掉，这里就红。
    expect(stack).toMatch(/['"]Hiragino Sans GB['"]/)
    // -apple-system / BlinkMacSystemFont 单独存在时没有 CJK 字形，
    // 且末尾的通用 sans-serif 在 iOS 上同样解析成 San Francisco。
    expect(stack).not.toMatch(/^\s*-apple-system\s*,\s*BlinkMacSystemFont\s*,\s*sans-serif\s*$/s)
  })
})