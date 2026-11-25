// ★ 2026-11-26：PI 分析详情模板的「翻译调用泄漏」钉测。
//
// 缘由是 211 上真实页面读回的文本：17 处标签把 `t('…')` 当成普通文本渲染了出来
// （例：`t('common.piAnalysis.forensics.verifyExpect'):` 原样显示），另有 5 处键前缀
// 写成了 `%s.`。vue-tsc 不报——文本节点里的 `t('…')` 是合法文本；纯函数库的单测也
// 看不见——缺陷在模板里。只有「把页面真的渲染出来读文本」或「直接扫模板源码」才抓得住。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const viewSrc = readFileSync(join(__dirname, 'PIAnalysisView.vue'), 'utf8')
// 模板里有嵌套的 <template v-if>（slot）⇒ 取到**最后一个** </template>。
const template = viewSrc.slice(viewSrc.indexOf('<template>'), viewSrc.lastIndexOf('</template>'))

// 顺序扫描模板，找出落在**普通文本节点**里的翻译调用：页面会把 t('…') 这几个字符原样显示。
//
//	· 标签内（<…>）按引号规则跟踪——属性值里的 > 不算标签结束（`v-if="a > b"` 之后
//	  的 `:title="t('…')"` 仍是属性绑定）；
//	· 插值 {{ … }} 整体跳过；注释不算。
//	★ 不用「看前面最近的 < 和 > 谁更近」的位置判断：属性值里的 > 会把它带偏 ⇒ 误报。
export function leakedTextTranslationCalls(tpl: string): string[] {
  const out: string[] = []
  let i = 0
  let inTag = false
  let quote: string | null = null
  while (i < tpl.length) {
    const c = tpl[i]
    if (inTag) {
      if (quote) {
        if (c === quote) quote = null
      } else if (c === '"' || c === "'") {
        quote = c
      } else if (c === '>') {
        inTag = false
      }
      i++
      continue
    }
    if (c === '<') {
      inTag = true
      i++
      continue
    }
    if (c === '{' && tpl[i + 1] === '{') {
      const end = tpl.indexOf('}}', i)
      i = end < 0 ? tpl.length : end + 2
      continue
    }
    const m = /^t\(\s*['"`]([^'"`]+)['"`]/.exec(tpl.slice(i, i + 200))
    if (m) {
      out.push(m[1])
      i += m[0].length
      continue
    }
    i++
  }
  return out
}

function localeForensicsKeys(locale: string): Set<string> {
  const src = readFileSync(join(__dirname, `../../locales/${locale}/common.ts`), 'utf8')
  const start = src.indexOf('forensics: {')
  expect(start).toBeGreaterThanOrEqual(0)
  const lineStart = src.lastIndexOf('\n', start) + 1
  const indent = start - lineStart
  let k = src.indexOf('\n', start) + 1
  for (;;) {
    const end = src.indexOf('\n', k)
    const line = src.slice(k, end)
    if (/^\s*}/.test(line) && line.length - line.trimStart().length <= indent) break
    k = end + 1
  }
  const body = src.slice(src.indexOf('\n', start) + 1, k)
  return new Set(Array.from(body.matchAll(/^\s*([A-Za-z0-9_]+):/gm), (m) => m[1]))
}

describe('PIAnalysisView template: translation calls', () => {
  it('文本节点里没有裸露的翻译调用（会被原样显示成 t(\'…\')）', () => {
    expect(leakedTextTranslationCalls(template), '这些调用没放进 {{ }} 里').toEqual([])
  })

  it('模板里的翻译键都以合法命名空间开头', () => {
    // 任何非空白字符都收进来再判（含 % $ 之类）——只收「合法字符」等于把写错的键跳过。
    const keys = Array.from(template.matchAll(/\bt\(\s*['"`]([^'"`\s]+)['"`]/g), (m) => m[1])
    expect(keys.length).toBeGreaterThan(20)
    const bad = keys.filter((k) => !/^common(\.[A-Za-z0-9_]+)+$/.test(k))
    expect(bad, '键不以 common. 开头或含非法字符（例：%s.xxx）').toEqual([])
  })

  it('模板用到的 common.piAnalysis.forensics.* 静态键在 zh-CN 与 en-US 里都存在', () => {
    const used = new Set(
      Array.from(template.matchAll(/common\.piAnalysis\.forensics\.([A-Za-z0-9_]+)(?![$\{])/g), (m) => m[1]),
    )
    expect(used.size).toBeGreaterThan(20)
    for (const locale of ['zh-CN', 'en-US']) {
      const defined = localeForensicsKeys(locale)
      const missing = Array.from(used).filter((k) => !defined.has(k)).sort()
      expect(missing, `${locale} 缺这些键`).toEqual([])
    }
  })

  it('动态拼接的键（步骤类别 / 风险）取值闭集都有对应文案', () => {
    const defined = localeForensicsKeys('zh-CN')
    for (const kind of ['config', 'code', 'ops', 'upstream', 'client']) {
      expect(defined.has(`kind${kind[0].toUpperCase()}${kind.slice(1)}`), `kind${kind}`).toBe(true)
    }
    for (const risk of ['low', 'medium', 'high']) {
      expect(defined.has(`risk${risk[0].toUpperCase()}${risk.slice(1)}`), `risk${risk}`).toBe(true)
    }
  })
})

describe('leakedTextTranslationCalls 判定自检', () => {
  it('属性值里的 > 不会把属性绑定误判成文本', () => {
    const tpl = `<button v-if="list.length > 0" :title="t('common.button.clear')">{{ t('common.ok') }}</button>`
    expect(leakedTextTranslationCalls(tpl)).toEqual([])
  })
  it('普通文本里的调用（211 上真实出现过的形态）会被抓到', () => {
    const tpl = `<label>t('common.piAnalysis.forensics.verifyExpect'):</label><span>{{ x }}</span>`
    expect(leakedTextTranslationCalls(tpl)).toEqual(['common.piAnalysis.forensics.verifyExpect'])
  })
})
