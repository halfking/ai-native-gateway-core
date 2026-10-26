// plainAttrBinding.ts — 「普通属性里放 i18n 调用」的判据（2026-10-25 新增）
//
// ── 为什么需要这道门 ────────────────────────────────────────────
// Vue 3 的**普通属性不做插值**：
//
//   <span title="$t('common.close')">   →  title 的值就是字符串 "$t('common.close')"
//   <span :title="$t('common.close')">  →  绑定形式才会求值
//
// 实测（@vue/compiler-dom 3.5.43）：
//   compile('<div title="$t(\'common.close\')">hi</div>', { mode: 'module' })
//   ⇒ { title: "$t('common.close')" }      ← 静态字符串，不是表达式
//
// 后果：编译**不报错**、vue-tsc **不报错**、运行时**不报错**，
//       但用户看到的 tooltip / placeholder 是字面量 `$t('common.close')`。
//       ★ 而且它比原文长得多 ⇒ 还会把版面撑宽（这正是本轮 A/B 要量的那件事）。
//
// 同族：Vue 3 也**移除了属性里的 mustache 插值** ⇒ `text="{{ x }}"` 同样渲染成字面量。
//
// ── 与既有门的分工 ──────────────────────────────────────────────
//   · keys_referenced / localeKeyParity  只管「键是否存在」
//   · callSiteParams.bindingKind          只管「静默吞错的变量有没有消费者」
//   · ★ 本门                             管「i18n 调用有没有落在会被静默渲染的位置」
//
// ── 判据纪律 ────────────────────────────────────────────────────
//   ① 只看 `<template>` 块（`<script>` 里的字符串不是模板）。
//   ② **先剥 HTML 注释** —— 仓内的修复说明注释里会原样写出被修的那段
//      （`text="{{ tabErrors.pricing }}"`），不剥就会把注释当缺陷。
//   ③ **绑定/指令形式一律放行**（`:x` / `@x` / `#x` / `v-x`）—— 它们会求值。
//   ④ 非空下限 —— 「一个属性都没扫到」也是绿的（假绿），必须挡住。

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { splitSfcBlocks } from './callSiteParams'

/** i18n 调用形态：`t(` / `$t(` / `tc(` / `i18n.t(` / `i18n.global.t(`。 */
const I18N_CALL_RE =
  /(?<![\w$.])(?:\$t|t|tc)\s*\(|i18n(?:\s*\.\s*\w+)*\s*\.\s*t\s*\(/

/** 一处「本该求值却被静默渲染」的位置。 */
export interface PlainAttrFinding {
  /** 1-based 行号（相对整个 .vue 文件）。 */
  line: number
  /** 属性名（不含绑定前缀）。 */
  attr: string
  /** 属性值的截断片段。 */
  value: string
  /** 命中原因：`call` = 放了 i18n 调用；`mustache` = 放了 mustache 插值。 */
  reason: 'call' | 'mustache'
}

/** 属性语法：`name="value"` / `name='value'`。名字里允许 `:`（绑定）与 `.`（修饰）。 */
const ATTR_RE = /([:@#]?[A-Za-z_][\w.\-:[\]]*)\s*=\s*(["'])([\s\S]*?)\2/g

/**
 * 剥 HTML 注释，**保持长度与行数不变**（非换行字符换成空格）
 * ⇒ 后面用同一份偏移算行号才不会错位。
 */
export function stripHtmlComments(src: string): string {
  return src.replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' '))
}

/** 绑定/指令前缀 ⇒ 属性值会被求值 ⇒ 不属于本门的缺陷。 */
function isEvaluated(attrName: string): boolean {
  return /^[:@#]/.test(attrName) || /^v-/.test(attrName)
}

/**
 * 扫一个 .vue 源文件，返回「普通属性里放 i18n 调用 / mustache」的位置。
 *
 * @param src  .vue 文件内容
 * @param file 文件名（仅用于报错信息）
 */
export function scanPlainAttrBindings(src: string, file = ''): PlainAttrFinding[] {
  return scanPlainAttrBindingsWithStats(src, file).findings
}

/**
 * 同上，但**额外回报扫过的属性数** ——
 * 「一个属性都没扫到」也是绿的（假绿），所以非空下限要用这个数来卡。
 */
export function scanPlainAttrBindingsWithStats(
  src: string,
  file = '',
): { findings: PlainAttrFinding[]; attrsScanned: number } {
  const findings: PlainAttrFinding[] = []
  let attrsScanned = 0
  for (const block of splitSfcBlocks(src)) {
    if (block.kind !== 'template') continue
    const raw = src.slice(block.start, block.end)
    const tpl = stripHtmlComments(raw)
    ATTR_RE.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = ATTR_RE.exec(tpl)) !== null) {
      const attr = m[1]
      const value = m[3]
      attrsScanned++
      if (isEvaluated(attr)) continue
      const hasCall = I18N_CALL_RE.test(value)
      const hasMustache = value.includes('{{')
      if (!hasCall && !hasMustache) continue
      findings.push({
        line: src.slice(0, block.start + m.index).split('\n').length,
        attr,
        value: value.length > 80 ? value.slice(0, 80) + '…' : value,
        reason: hasMustache ? 'mustache' : 'call',
      })
    }
  }
  return { findings, attrsScanned }
}

/** 扫一个目录下的全部 .vue 文件。 */
export function scanPlainAttrBindingsInDir(dir: string): Array<PlainAttrFinding & { file: string }> {
  const out: Array<PlainAttrFinding & { file: string }> = []
  for (const f of listVueFiles(dir)) {
    for (const hit of scanPlainAttrBindings(readFileSync(f, 'utf8'), f)) {
      out.push({ ...hit, file: f })
    }
  }
  return out
}

/** 递归列出 .vue 文件（跳过 node_modules / dist 等）。 */
export function listVueFiles(dir: string): string[] {
  const out: string[] = []
  const SKIP = new Set(['node_modules', 'dist', '.git'])
  const rec = (d: string) => {
    let entries: import('node:fs').Dirent[]
    try {
      entries = readdirSync(d, { withFileTypes: true })
    } catch {
      return
    }
    for (const e of entries) {
      const p = join(d, e.name)
      let st: import('node:fs').Stats
      try {
        st = statSync(p)
      } catch {
        continue
      }
      if (st.isDirectory()) {
        if (SKIP.has(e.name)) continue
        rec(p)
      } else if (e.name.endsWith('.vue')) {
        out.push(p)
      }
    }
  }
  rec(dir)
  return out
}

/**
 * 判定层自证：**双向**（会触发 + 不误触发各半）。
 * 恒绿门与恒红门同样没用。
 */
export function selfCheckPlainAttrBinding(): Array<{ id: string; desc: string; ok: boolean; actual: string }> {
  const cases: Array<{ id: string; desc: string; src: string; expectHit: boolean }> = [
    {
      id: 'plain-dollar-t',
      desc: '普通属性里的 $t(...) → 必须报（Vue 3 普通属性不插值）',
      src: '<template>\n  <span title="$t(\'common.close\')">x</span>\n</template>\n',
      expectHit: true,
    },
    {
      id: 'plain-t',
      desc: '普通属性里的 t(...) → 必须报',
      src: "<template>\n  <span title=\"t('common.close')\">x</span>\n</template>\n",
      expectHit: true,
    },
    {
      id: 'plain-mustache',
      desc: '普通属性里的 mustache（Vue 3 已移除属性插值）→ 必须报',
      src: '<template>\n  <span title="{{ label }}">x</span>\n</template>\n',
      expectHit: true,
    },
    {
      id: 'bound-attr',
      desc: '绑定形式 :title → 必须放行',
      src: '<template>\n  <span :title="$t(\'common.close\')">x</span>\n</template>\n',
      expectHit: false,
    },
    {
      id: 'v-bind-attr',
      desc: 'v-bind:title → 必须放行',
      src: '<template>\n  <span v-bind:title="t(\'common.close\')">x</span>\n</template>\n',
      expectHit: false,
    },
    {
      id: 'event-handler',
      desc: '@click="t(...)" → 事件处理器会求值 → 必须放行',
      src: '<template>\n  <button @click="t(\'common.close\')">x</button>\n</template>\n',
      expectHit: false,
    },
    {
      id: 'plain-text',
      desc: '普通属性里的普通文案 → 必须放行',
      src: '<template>\n  <span title="关闭">x</span>\n</template>\n',
      expectHit: false,
    },
    {
      id: 'comment',
      desc: 'HTML 注释里写的那段（修复说明）→ 必须放行',
      src:
        '<template>\n  <!-- 原来写成 text="{{ tabErrors.pricing }}" —— 花括号在引号里是字面量 -->\n  <span :text="tabErrors.pricing">x</span>\n</template>\n',
      expectHit: false,
    },
    {
      id: 'script-block',
      desc: '<script> 里的字符串不是模板 → 必须放行',
      src: '<script setup>\nconst s = "title=\\\"$t(\'x\')\\\""\n</script>\n',
      expectHit: false,
    },
    {
      id: 'method-not-t',
      desc: '属性值里的 format(...) 之类不是 i18n 调用 → 必须放行',
      src: '<template>\n  <span :title="format(x)">y</span>\n</template>\n',
      expectHit: false,
    },
  ]
  return cases.map((c) => {
    const hits = scanPlainAttrBindings(c.src, c.id)
    const ok = c.expectHit ? hits.length > 0 : hits.length === 0
    return {
      id: c.id,
      desc: c.desc,
      ok,
      actual: `命中 ${hits.length} 条（期望 ${c.expectHit ? '>0' : '0'}）${
        hits.length ? '：' + hits.map((h) => `${h.attr}=${h.value}`).join(' / ') : ''
      }`,
    }
  })
}
