// primaryFocus.ts — 「主色只留给主动作」的可执行判据（2026-10-27 · 第 20 增量）
//
// ★ 要治的病（仓内明文规范 vs 真仓现状的偏离）：
//   `DESIGN.md` §色彩段原话：
//     「状态用最小的颜色量表达、**每个元素都承担信息职责**」
//     「颜色 → 只用语义色」
//   impeccable `skill/reference/colorize.md`：
//     「**Keep the primary action easy to find; do not spend its color on decoration.**」
//     「**Let the strongest color own a deliberate region or role instead of scattering
//       tiny accents.**」
//   而真仓实测（截图取证）：`views/ModelsView.vue` 的**模型清单表**里，
//     **每一行**都渲染一个实心主色按钮「查看详情」（53 行 ⇒ 53 个蓝底按钮），
//   ⇒ 页面级真正的主操作（右上角唯一的「新增模型」实心按钮）被**稀释到看不见**。
//   ★ 这是 **AI 生成代码的典型痕迹**：每个 row 都给一个最显眼的按钮。
//
// ★★★ 本门只判**一条**判据（判据越少越可证伪）：
//
//   **实心主色按钮不得落在「行 / 列表项」这类重复容器内**
//   —— 否则主色被重复消费，焦点从「唯一的主动作」稀释成「每行都一样」。
//
// ★ 为什么要按【形状】判、且必须穷举形态（LESSONS.md §451 形态一 / §450 形态一）：
//   实心主色在本仓有**两种实现写法**，同一形状两套语法：
//     ① Element Plus：`<el-button type="primary">`（**不带 `link`** —— 带 `link` 是文字链，不是实心）
//     ② 自有类：`<button class="btn btn-primary">`
//   ⇒ 只认一种写法 ⇒ 另一族整族假绿。
//
// ★ 「重复容器」的形状同样要穷举：
//     ① `<td>` / `<th>`（原生表格行内）
//     ② `<el-table-column>`（Element Plus 表格的行内 slot）
//     ③ 带 `v-for` 的元素（网格卡片 / 列表项）
//     ④ `<template #actions>`（compact 卡片的行内动作区）
//   ⇒ 漏一种 ⇒ 该形态整族漏网。
//
// ★ 豁免（**对话框 / 抽屉本身就是一个「刻意的焦点区域」**）：
//   colorize.md 说的是「把主色留给一个刻意区域」⇒ 对话框内**那一个**确认按钮
//   恰恰是「刻意区域」的正确用法 ⇒ 不判。
//
// ★ 本文件是**判据**（纯函数 + 文件扫描），门在 `web/scripts/design-primary-focus-guard.mjs`，
//   运行时尺在 `web/scripts/design-primary-focus-runtime-check.mjs`。
//   放 `src/` 是为了能被 vitest 覆盖（与 `aiSmells.ts` / `elevation.ts` 同一范式）。

import { readdirSync, readFileSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
// ★ 用 `.ts` 后缀 import：项目的 tsconfig 开了 allowImportingTsExtensions，
//   而 Node 的 --experimental-strip-types 对相对 import **要求**显式扩展名。
import { maskSource } from '../i18n/hardcodedCjk.ts'

// ───────────────────────── 形态表 ─────────────────────────

/** 实心主色的实现形态（穷举；带 `link` 的 Element Plus 按钮渲染成文字链，不算实心）。 */
export const SOLID_PRIMARY_FORMS = [
  { id: 'element-plus-primary', label: '`<el-button type="primary">`（不带 link）' },
  { id: 'own-btn-primary', label: '`<button class="… btn-primary …">`' },
] as const

/** 重复容器（行 / 列表项）的形态（穷举）。 */
export const ROW_CONTAINER_FORMS = [
  { id: 'table-cell', label: '原生表格单元格 `<td>` / `<th>`' },
  { id: 'element-table-column', label: 'Element Plus 表格行 `<el-table-column>`' },
  { id: 'row-slot', label: '数据视图的行 slot（`<template #actions="{ row }">` 一类）' },
  { id: 'list-item', label: '列表项 `<li>` / 表格行 `<tr>`' },
  { id: 'v-for-block', label: '`v-for` 的块级项（**刻意区域卡**候选）' },
] as const

/**
 * 焦点区域豁免（对话框 / 抽屉）。
 *
 * ★★★ **标签名 ≠ 类名** —— 这是「把判据搬到另一个观测层」时最容易踩的坑
 *   （LESSONS.md §456 形态：上下文依赖要一起搬）：
 *   源码层看到的是 `<AppModal>`（**标签名** `appmodal`），
 *   运行时看到的是 `<div class="modal-overlay app-modal">`（**类名** `app-modal`）。
 *   ⇒ 只按标签名判 ⇒ 运行时**一个焦点区域都认不出**；只按类名判 ⇒ 源码层认不出。
 *   ⇒ 两层都按【标签名 + 类名 + 语义角色】三条一起判，名单保持一致。
 *
 * ★ 形状清单要跟着真仓走：仓内 **2026-09-13 起**把弹层壳收敛成了
 *   `ui/AppModal` / `ui/AppDrawer`，而 `el-dialog` / `el-drawer` 早已不是主路径
 *   ⇒ 只写老名单 ⇒ 豁免分支是**死的**（读数 `focusRegionSolidPrimary: 0` 暴露了它）。
 */
export const FOCUS_REGION_TAGS = ['el-dialog', 'el-drawer', 'appmodal', 'appdrawer'] as const

/** 焦点区域的**类名**形态（运行时层用；认 BEM 派生，不认前缀相同的其它词）。 */
export const FOCUS_REGION_CLASSES = ['app-modal', 'app-drawer', 'modal-panel'] as const

/** 类名是否命中「基名」？认 BEM 派生（`app-modal__panel`），不认 `app-modalish`。 */
export function matchesClassBase(token: string, base: string): boolean {
  if (token === base) return true
  if (!token.startsWith(base)) return false
  const rest = token.slice(base.length)
  return rest.startsWith('__') || rest.startsWith('--')
}

// ───────────────────────── 标签树 ─────────────────────────

export interface TagNode {
  /** 小写标签名（`EL-BUTTON` ⇒ `el-button`） */
  name: string
  /** 原始属性串（不含标签名与尖括号） */
  attrs: string
  /** 开标签起点 / 终点 */
  openStart: number
  openEnd: number
  /** 内容区间（自闭合标签为空区间） */
  innerStart: number
  innerEnd: number
  children: TagNode[]
  parent?: TagNode
  selfClosing: boolean
}

const VOID_TAGS = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'param', 'source', 'track', 'wbr'])

/**
 * 扫描一个区间里的标签，建树。
 *
 * ★ 属性值里可能有 `>`（如 `:label="a > b"`）⇒ 扫描开标签时**必须逐字符跟踪引号**，
 *   否则会在值里的 `>` 处截断，后面的标签全部解析错位。
 */
export function parseTagTree(masked: string, from: number, to: number): TagNode[] {
  const roots: TagNode[] = []
  const stack: TagNode[] = []
  let i = from

  const push = (node: TagNode) => {
    const parent = stack[stack.length - 1]
    if (parent) {
      node.parent = parent
      parent.children.push(node)
    } else {
      roots.push(node)
    }
  }

  while (i < to) {
    const lt = masked.indexOf('<', i)
    if (lt === -1 || lt >= to) break
    // 注释 / 指令 / DOCTYPE 一律跳过（maskSource 已剥注释，这里是兜底）
    if (masked.startsWith('<!--', lt)) {
      const end = masked.indexOf('-->', lt)
      i = end === -1 ? to : end + 3
      continue
    }
    if (masked.startsWith('<!', lt) || masked.startsWith('<?', lt)) {
      const end = masked.indexOf('>', lt)
      i = end === -1 ? to : end + 1
      continue
    }
    // 闭合标签
    if (masked[lt + 1] === '/') {
      const end = masked.indexOf('>', lt)
      if (end === -1) break
      const name = masked.slice(lt + 2, end).trim().toLowerCase()
      // 弹出到匹配的开标签（容错：找不到就忽略这个闭合标签）
      for (let k = stack.length - 1; k >= 0; k--) {
        if (stack[k].name === name) {
          const node = stack[k]
          node.innerEnd = lt
          stack.length = k
          break
        }
      }
      i = end + 1
      continue
    }
    // 开标签：逐字符扫描，注意引号内的 `>`
    const nameMatch = /^<([A-Za-z][A-Za-z0-9._:-]*)/.exec(masked.slice(lt, lt + 200))
    if (!nameMatch) { i = lt + 1; continue }
    const name = nameMatch[1].toLowerCase()
    let j = lt + nameMatch[0].length
    let quote = ''
    while (j < to) {
      const c = masked[j]
      if (quote) {
        if (c === quote) quote = ''
      } else if (c === '"' || c === "'") {
        quote = c
      } else if (c === '>') {
        break
      }
      j++
    }
    if (j >= to) break
    const attrs = masked.slice(lt + nameMatch[0].length, j)
    const selfClosing = attrs.trimEnd().endsWith('/')
    const node: TagNode = {
      name,
      attrs,
      openStart: lt,
      openEnd: j + 1,
      innerStart: j + 1,
      innerEnd: selfClosing ? j + 1 : j + 1,
      children: [],
      selfClosing,
    }
    push(node)
    if (!selfClosing && !VOID_TAGS.has(name)) stack.push(node)
    i = j + 1
  }

  // 未闭合的开标签：内容区间到区间尾
  for (const node of stack) node.innerEnd = to
  return roots
}

/** 祖先链（由近到远）。 */
export function ancestorsOf(node: TagNode): TagNode[] {
  const out: TagNode[] = []
  let p = node.parent
  while (p) {
    out.push(p)
    p = p.parent
  }
  return out
}

// ───────────────────────── 判定 ─────────────────────────

export interface SolidPrimaryButton {
  file: string
  line: number
  col: number
  /** 元素形态 id（见 SOLID_PRIMARY_FORMS） */
  form: string
  /** 是否落在重复容器（行 / 列表项）内 */
  rowLevel: boolean
  /** 命中的容器形态 id（rowLevel 为 true 时有值） */
  containerForm?: string
  /** 是否在焦点区域（对话框 / 抽屉）内 ⇒ 豁免 */
  inFocusRegion: boolean
  /** 元素原文（截断） */
  snippet: string
}

/**
 * 正规的属性分词（引号感知）。
 *
 * ★ 为什么要重写而不是用正则 `/(\w+)="([^"]*)"/`：
 *   Element Plus 的 `link` 是**布尔属性**，写法是 `<el-button type="primary" link>`
 *   —— **没有 `=`、没有引号** ⇒ 正则版完全看不见它
 *   ⇒ 文字链（link 变体）会被误判成实心主色 ⇒ 整族假红。
 *   ⇒ 布尔属性的存在性本身就是判据 ⇒ 分词必须能区分「有值」与「裸属性」。
 */
export interface ParsedAttr {
  /** 原始属性名（小写，可能带指令前缀，如 `:link` / `v-bind:type` / `@click`） */
  name: string
  /** 归一化后的基础名（去掉 `:` / `v-bind:` / `.` 前缀） */
  base: string
  /** 有值属性的值；裸属性为 null */
  value: string | null
}

/** 去掉指令前缀，得到「基础属性名」。 */
function baseAttrName(raw: string): string {
  let n = raw.toLowerCase()
  if (n.startsWith('v-bind:')) n = n.slice('v-bind:'.length)
  else if (n.startsWith(':')) n = n.slice(1)
  else if (n.startsWith('.')) n = n.slice(1)
  return n
}

export function parseAttrs(attrs: string): ParsedAttr[] {
  const out: ParsedAttr[] = []
  let i = 0
  const n = attrs.length
  while (i < n) {
    // 跳过空白
    while (i < n && /\s/.test(attrs[i])) i++
    if (i >= n) break
    // 属性名
    const nameStart = i
    while (i < n && /[A-Za-z0-9_:@.-]/.test(attrs[i])) i++
    if (i === nameStart) {
      i++ // 不认识的字符，跳过
      continue
    }
    const raw = attrs.slice(nameStart, i)
    // 跳过 name 与 `=` 之间的空白
    let j = i
    while (j < n && /\s/.test(attrs[j])) j++
    if (attrs[j] === '=') {
      j++
      while (j < n && /\s/.test(attrs[j])) j++
      let value = ''
      if (attrs[j] === '"' || attrs[j] === "'") {
        const q = attrs[j]
        j++
        const vs = j
        while (j < n && attrs[j] !== q) j++
        value = attrs.slice(vs, j)
        j++
      } else {
        const vs = j
        while (j < n && !/\s/.test(attrs[j])) j++
        value = attrs.slice(vs, j)
      }
      out.push({ name: raw.toLowerCase(), base: baseAttrName(raw), value })
      i = j
    } else {
      // 裸属性（布尔）
      out.push({ name: raw.toLowerCase(), base: baseAttrName(raw), value: null })
      i = nameStart + raw.length
    }
  }
  return out
}

/** 属性表（**基础名** ⇒ 值）；裸属性的值为 `null`。 */
export function attrsOf(node: TagNode): Record<string, string | null> {
  const out: Record<string, string | null> = {}
  for (const a of parseAttrs(node.attrs)) out[a.base] = a.value
  return out
}

/**
 * 该元素是否处于「link 文字链」形态？
 *
 * ★ Element Plus 的 `link` 是布尔 prop，三种写法都要认：
 *   ① `link`（裸属性）② `link="true"` ③ `:link="true"` / `v-bind:link="true"`
 *   ⇒ 判据是**存在性**，不是值。
 *   ★ 但 `:link="false"`（动态假值）⇒ 仍然是实心 ⇒ 值恰为 `false` 时不算 link。
 */
function isLinkVariant(a: Record<string, string | null>): boolean {
  if (!('link' in a)) return false
  const v = a.link
  if (v === null) return true
  return v.trim().toLowerCase() !== 'false'
}

/**
 * 该元素是不是「实心主色」？（两种实现形态都认）
 *
 * ★ Element Plus 的 `type="primary"` 有四个变体，只有**第一个**是实心：
 *   - `type="primary"`               ⇒ **实心**（蓝底白字）
 *   - `type="primary" link`          ⇒ 文字链（无底色）
 *   - `type="primary" text`          ⇒ 文字按钮（无底色）
 *   - `type="primary" plain`         ⇒ 描边按钮（白底蓝边）
 *   ⇒ 后三个都**不是**实心主色 ⇒ 把它们算进来 = 假红。
 */
export function isSolidPrimary(node: TagNode): string | null {
  if (node.name === 'el-button') {
    const a = attrsOf(node)
    const type = (a.type ?? '').toLowerCase()
    if (type !== 'primary') return null
    // ★ `link` / `text` / `plain` 三个变体都不是实心 ⇒ 逐个排除。
    if (isLinkVariant(a)) return null
    for (const v of ['text', 'plain']) {
      const val = a[v]
      // 属性不存在 ⇒ 变体未开启 ⇒ 仍是实心
      if (val === undefined) continue
      // 裸属性（`text` / `plain`）⇒ 变体开启 ⇒ 不是实心
      if (val === null) return null
      // 显式假值 ⇒ 变体未开启 ⇒ 仍是实心
      if (val.trim().toLowerCase() === 'false') continue
      return null
    }
    return 'element-plus-primary'
  }
  if (node.name === 'button') {
    const a = attrsOf(node)
    const cls = a.class ?? ''
    if (/(^|\s)btn-primary(\s|$)/.test(cls)) return 'own-btn-primary'
    return null
  }
  return null
}

/** 该节点是否带 `v-for`？ */
function hasVFor(node: TagNode): boolean {
  return /(^|\s)v-for\s*=/.test(node.attrs)
}

/** 行 slot 的作用域变量名（拿到「一行」的 slot）。 */
const ROW_SCOPE_NAMES = new Set(['row', 'item', 'record', 'entry'])

/**
 * 该 `<template>` 是不是「数据视图的行 slot」？
 *
 * ★ 形态：`<template #actions="{ row }">` / `<template #cards="{ row }">`
 *   —— 紧凑数据视图（`ui/ResponsiveDataView`）把「一行」交给父组件渲染，
 *   于是父组件的 slot 内容**在源码里没有 `v-for`**，也没有 `<td>` 祖先
 *   ⇒ 只认 `td` / `el-table-column` / `v-for` 的话，这一族**整族漏网**
 *   （实测 `VibeCodingView.vue` 的 compact 卡片动作区就是这样漏掉的）。
 *
 * ★ 判据用**作用域变量名**（`row` / `item` / `record` / `entry`）而不是 slot 名
 *   —— slot 名是自由的（`#actions` / `#cards` / `#default` 都可能），
 *   而「这个 slot 拿到的是一行」是稳定的语义。
 */
export function isRowSlot(node: TagNode): boolean {
  if (node.name !== 'template') return false
  const m = /(?:#|v-slot:)([A-Za-z0-9_$-]+)\s*=\s*"([^"]*)"/.exec(node.attrs)
  if (!m) return false
  const ids = m[2]
    .replace(/[{}]/g, ' ')
    .split(/[\s:,]+/)
    .map((s) => s.trim().toLowerCase())
    .filter(Boolean)
  return ids.some((id) => ROW_SCOPE_NAMES.has(id))
}

/**
 * 该元素落在哪种「重复容器」里？（没有则返回 null）
 * ★ 穷举形态（见 ROW_CONTAINER_FORMS）——漏一种 ⇒ 该形态整族漏网。
 *
 * ★★ 为什么把 `v-for` 拆成两种形态（`list-item` / `v-for-block`）：
 *   impeccable `colorize.md`：「Let the strongest color own a **deliberate region**
 *   or role instead of scattering tiny accents.」
 *   ⇒ 「一行数据」与「一张刻意区域卡」是**两件事**：
 *     - `<li>` / `<tr>` / `<td>` = **数据行**（同质、密集、每行都一样）⇒ 主色不得落在其中；
 *     - `v-for` 的**块级**项（卡片网格）= **刻意区域**（它自己就是版面主角）⇒ 可豁免。
 *   ⇒ 两者在源码上的差别就是**元素语义**（`<li>` 是列表项，`<div>` 是块），
 *     这不是「按写法判」——HTML 语义本身就是形状。
 */
export function rowContainerForm(node: TagNode): string | null {
  const chain = [node, ...ancestorsOf(node)]
  // ① 原生表格单元格
  for (const n of chain) {
    if (n.name === 'td' || n.name === 'th') return 'table-cell'
  }
  // ② Element Plus 表格行
  for (const n of chain) {
    if (n.name === 'el-table-column') return 'element-table-column'
  }
  // ③ 数据视图的行 slot
  for (const n of chain) {
    if (isRowSlot(n)) return 'row-slot'
  }
  // ④ 列表项 / 表格行
  for (const n of chain) {
    if (n.name === 'li' || n.name === 'tr') return 'list-item'
  }
  // ⑤ 带 v-for 的元素（块级项 ⇒ 刻意区域候选）
  for (const n of chain) {
    if (hasVFor(n)) return 'v-for-block'
  }
  return null
}

/**
 * 该元素是否在焦点区域（对话框 / 抽屉 / 模态面板）内？
 *
 * ★ 焦点区域有两种实现形态，都要认：
 *   ① **组件壳**：`<el-dialog>` / `<el-drawer>` / `<AppModal>` / `<AppDrawer>`
 *   ② **语义角色**：`<div role="dialog" aria-modal="true">`（仓内自制模态面板，如
 *      `ApiKeySelectModal.vue` 的 `modal-panel`）
 *   ⇒ 只认组件壳 ⇒ 自制模态整族漏网（只认壳、不认角色），
 *     只认角色 ⇒ 组件壳漏网。两种都认才完整。
 */
export function inFocusRegion(node: TagNode): boolean {
  for (const a of ancestorsOf(node)) {
    if ((FOCUS_REGION_TAGS as readonly string[]).includes(a.name)) return true
    const attrs = attrsOf(a)
    const role = (attrs.role ?? '').toLowerCase()
    if (role === 'dialog' || role === 'alertdialog') return true
    if ((attrs['aria-modal'] ?? '').toLowerCase() === 'true') return true
    // ★ 类名形态（与运行时层同名单）
    const cls = attrs.class ?? ''
    const tokens = cls.split(/\s+/).filter(Boolean)
    if ((FOCUS_REGION_CLASSES as readonly string[]).some((c) => tokens.some((t) => matchesClassBase(t, c)))) {
      return true
    }
  }
  return false
}

/** 扫描一个已遮罩的源文件，找出全部实心主色按钮并判定其上下文。 */
export function findSolidPrimaryButtons(masked: string, file: string): SolidPrimaryButton[] {
  const out: SolidPrimaryButton[] = []
  // ★ 只在 `<template>` 块里找（脚本里的字符串不该被当成按钮）
  for (const tpl of templateBlocks(masked)) {
    const roots = parseTagTree(masked, tpl[0], tpl[1])
    const walk = (nodes: TagNode[]) => {
      for (const node of nodes) {
        const form = isSolidPrimary(node)
        if (form) {
          const container = rowContainerForm(node)
          const before = masked.slice(0, node.openStart)
          const line = before.split('\n').length
          const col = node.openStart - (before.lastIndexOf('\n') + 1) + 1
          const b: SolidPrimaryButton = {
            file,
            line,
            col,
            form,
            rowLevel: container !== null,
            containerForm: container ?? undefined,
            inFocusRegion: inFocusRegion(node),
            snippet: masked.slice(node.openStart, node.openEnd).replace(/\s+/g, ' ').slice(0, 160),
          }
          out.push(b)
        }
        walk(node.children)
      }
    }
    walk(roots)
  }
  return out
}

/**
 * 取 `<template>` 块的区间（**深度计数配对**，不是「第一个 `</template>`」）。
 *
 * ★ LESSONS.md §419 形态一：Vue 允许**嵌套 `<template v-if>`**，
 *   用「第一个闭合标签」配对会把文件后半段变成盲区。
 *
 * ★ 引号感知：开标签的属性值里可能有 `>`（如 `<template v-if="a > b">`），
 *   ⇒ 必须逐字符跟踪引号，否则 `>` 处的截断会让**区间起点**落在属性值中间。
 *
 * ★ 注释感知：`<!-- <template> -->` 这类注释里的标签不得计入深度，
 *   否则深度永远回不到 0 ⇒ 整个文件后半段变成盲区。
 */
export function templateBlocks(masked: string): Array<[number, number]> {
  const out: Array<[number, number]> = []
  let depth = 0
  let start = -1
  let i = 0
  while (i < masked.length) {
    const lt = masked.indexOf('<', i)
    if (lt === -1) break
    // 注释：整段跳过
    if (masked.startsWith('<!--', lt)) {
      const end = masked.indexOf('-->', lt)
      i = end === -1 ? masked.length : end + 3
      continue
    }
    // 闭合标签
    if (masked.startsWith('</template>', lt)) {
      depth--
      if (depth === 0 && start >= 0) {
        out.push([start, lt])
        start = -1
      }
      i = lt + '</template>'.length
      continue
    }
    // 开标签：`<template` 之后必须是非标识符字符
    if (masked.startsWith('<template', lt)) {
      const after = masked[lt + '<template'.length]
      if (after && /[A-Za-z0-9._:-]/.test(after)) {
        i = lt + '<template'.length
        continue
      }
      let j = lt + '<template'.length
      let quote = ''
      while (j < masked.length) {
        const c = masked[j]
        if (quote) {
          if (c === quote) quote = ''
        } else if (c === '"' || c === "'") {
          quote = c
        } else if (c === '>') {
          break
        }
        j++
      }
      if (j >= masked.length) break
      if (depth === 0) start = j + 1
      depth++
      i = j + 1
      continue
    }
    i = lt + 1
  }
  if (depth > 0 && start >= 0) out.push([start, masked.length])
  return out
}

// ───────────────────────── 文件扫描 ─────────────────────────

export interface PrimaryFocusFinding extends SolidPrimaryButton {
  reason: string
}

export interface PrimaryFocusFacts {
  /** 行内 / 列表项内的实心主色按钮数（= 违规数） */
  rowLevelSolidPrimary: number
  /** 全仓实心主色按钮总数（读数用） */
  totalSolidPrimary: number
  /** 落在焦点区域（对话框 / 抽屉）内的数量（豁免，读数用） */
  focusRegionSolidPrimary: number
  /** 命中登记豁免的数量（豁免，读数用） */
  registeredSolidPrimary: number
  /** 落在「刻意区域卡」内且该视图无竞争主色的数量（豁免，读数用） */
  deliberateRegionSolidPrimary: number
}

/**
 * 登记豁免（人工判定的「刻意区域」）。
 *
 * ★ 为什么需要它：impeccable `colorize.md` 说的是
 *   「Let the strongest color own a **deliberate region or role**」
 *   —— 「刻意区域」是**视觉判断**（例如「定价卡本身就是版面主角」），
 *   **无法从源码形状可靠地推出来**（实测：仓内 `card` 命名并不统一：
 *   `pricing-card card` / `tool-card` / `probe-card` / `stat-card` ……
 *   ⇒ 按类名判「是不是卡」会得到假绿假红两头的错）。
 *   ⇒ 只能**人工判定 + 显式登记 + 附 witness**（与第 19 增量「内部标识豁免簿」同一范式）。
 */
export interface PrimaryFocusExemption {
  id: string
  /** 相对仓库根的文件路径（正斜杠） */
  file: string
  /** 元素的开始标签文本（空白归一后） */
  snippet: string
  /** 为什么豁免（人话） */
  reason: string
  /** witness：凭什么说它是「刻意区域」——必须指向事实源（版面上谁是主角） */
  witness: string
}

/** 片段归一（空白折叠），用于登记簿与扫描结果对拍。 */
export function normalizeSnippet(s: string): string {
  return s.replace(/\s+/g, ' ').trim()
}

/** 该发现是否命中登记豁免？ */
export function isExemptFinding(
  finding: Pick<SolidPrimaryButton, 'file' | 'snippet'>,
  exemptions: PrimaryFocusExemption[],
): PrimaryFocusExemption | undefined {
  const sn = normalizeSnippet(finding.snippet)
  return exemptions.find((e) => e.file === finding.file && normalizeSnippet(e.snippet) === sn)
}

/**
 * 单文件的裁决（**纯函数**）—— 自证与真扫描走**同一个函数**
 * （★ LESSONS.md §411：自证里再抄一遍判据 ⇒ 自证证明的是抄件，不是真判据）。
 */
export interface RowLevelDecision {
  /** 判红的元素 */
  violations: SolidPrimaryButton[]
  /** 该视图的竞争主色数（既不在重复容器内、又不在焦点区域内的实心主色） */
  competing: number
  /** 落在刻意区域卡内且无竞争的元素数（豁免） */
  deliberateRegion: number
}

export function decideRowLevel(
  buttons: SolidPrimaryButton[],
  exemptions: PrimaryFocusExemption[] = [],
): RowLevelDecision {
  const competing = buttons.filter((b) => !b.rowLevel && !b.inFocusRegion).length
  const violations: SolidPrimaryButton[] = []
  let deliberateRegion = 0
  for (const b of buttons) {
    if (b.inFocusRegion) continue
    if (!b.rowLevel) continue
    if (isExemptFinding(b, exemptions)) continue
    // 豁免 ②：刻意区域卡（块级 v-for 项）+ 该视图无竞争主色
    if (b.containerForm === 'v-for-block' && competing === 0) {
      deliberateRegion++
      continue
    }
    violations.push(b)
  }
  return { violations, competing, deliberateRegion }
}

/**
 * 扫描目录，返回违规明细与读数。
 *
 * ★★★ 判据（唯一一条，完整形态）：
 *
 *   **实心主色不得落在「数据行」内。**
 *
 *   - **数据行**（4 种形态，穷举）：`<td>/<th>` · `<el-table-column>` ·
 *     数据视图的行 slot · `<li>`/`<tr>`
 *   - **豁免 ①（焦点区域）**：模态 / 抽屉内 —— 那里主色属于「这个浮层的主动作」
 *     （`colorize.md`：「the strongest color own a **deliberate region**」）
 *   - **豁免 ②（刻意区域卡）**：`v-for` 的**块级**项，且**该视图内没有别的实心主色**
 *     与之竞争 —— 卡片自己就是版面主角，它的按钮是「这个区域的角色」，
 *     不是「主色被重复消费」。
 *
 * ★ 为什么豁免 ② 要带「无竞争主色」这个前置条件：
 *   实测 `FreePoolView.vue` 同屏有 **9 个**非焦点区的实心主色（工具栏 / 表单提交），
 *   此时卡片里的实心主色**不再**是「这个区域的角色」⇒ 它是在和工具栏抢主色 ⇒ 判红。
 *   ⇒ 「刻意区域」不是「块级」的同义词，**还得没人跟它抢**。
 */
export function scanPrimaryFocus(
  files: string[],
  rootDir: string,
  exemptions: PrimaryFocusExemption[] = [],
): { findings: PrimaryFocusFinding[]; facts: PrimaryFocusFacts } {
  const findings: PrimaryFocusFinding[] = []
  let total = 0
  let focus = 0
  let registered = 0
  let deliberateRegion = 0
  for (const abs of files) {
    const src = readFileSync(abs, 'utf8')
    const masked = maskSource(abs, src)
    const rel = relative(rootDir, abs).replace(/\\/g, '/')
    const buttons = findSolidPrimaryButtons(masked, rel)
    const decision = decideRowLevel(buttons, exemptions)
    deliberateRegion += decision.deliberateRegion
    for (const b of buttons) {
      total++
      if (b.inFocusRegion) focus++
      if (b.rowLevel && isExemptFinding(b, exemptions)) registered++
    }
    for (const b of decision.violations) {
      findings.push({
        ...b,
        reason:
          b.containerForm === 'v-for-block'
            ? `实心主色落在「刻意区域卡」内，但该视图另有 ${decision.competing} 处实心主色与之竞争 ⇒ 主色被重复消费`
            : `实心主色按钮落在「数据行」内（${b.containerForm}）⇒ 主色被重复消费，页面级主动作被稀释`,
      })
    }
  }
  return {
    findings,
    facts: {
      rowLevelSolidPrimary: findings.length,
      totalSolidPrimary: total,
      focusRegionSolidPrimary: focus,
      registeredSolidPrimary: registered,
      deliberateRegionSolidPrimary: deliberateRegion,
    },
  }
}

// ───────────────────────── 自证 ─────────────────────────

export interface SelftestCase {
  name: string
  ok: boolean
  msg?: string
}

/**
 * 自证用例。
 * ★ 纪律（LESSONS.md §411）：**断言必须恒真/恒假地被抓出来** ⇒
 *   每个用例都要有一组「必须判绿」的对照，否则判据可能是恒真。
 */
export const PRIMARY_FOCUS_SELFTEST: SelftestCase[] = [
  {
    name: 'Element Plus 行内 primary ⇒ 判红（表格列内）',
    ok: (() => {
      const src = `<template><el-table><el-table-column label="操作"><template #default="scope"><el-button type="primary" size="small">看</el-button></template></el-table-column></el-table></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel && f[0].containerForm === 'element-table-column'
    })(),
  },
  {
    name: '★ 对照：Element Plus 的 `link` 变体 ⇒ 不算实心（判绿）',
    ok: (() => {
      const src = `<template><el-table-column><template #default="scope"><el-button type="primary" link>看</el-button></template></el-table-column></template>`
      return findSolidPrimaryButtons(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ `link` 的动态绑定形态（`:link="true"`）⇒ 也不算实心（判绿）',
    ok: (() => {
      const src = `<template><el-table-column><template #default="scope"><el-button type="primary" :link="true">看</el-button></template></el-table-column></template>`
      return findSolidPrimaryButtons(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 属性**值里**出现 "link" 字样（`title="see link"`）⇒ 不得误判成 link 变体（判红）',
    ok: (() => {
      const src = `<template><el-table-column><template #default="scope"><el-button type="primary" title="see link">看</el-button></template></el-table-column></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel === true
    })(),
  },
  {
    name: '★ 别的布尔属性（`round`）不得影响实心判定（判红）',
    ok: (() => {
      const src = `<template><el-table-column><template #default="scope"><el-button type="primary" round>看</el-button></template></el-table-column></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel === true
    })(),
  },
  {
    name: '★ `<template v-if="a > b">` 的属性值里有 `>` ⇒ 区间不得错位（按钮仍能扫到）',
    ok: (() => {
      const src = `<template><template v-if="a > b"><el-button type="primary">看</el-button></template></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel === false
    })(),
  },
  {
    name: '★ 注释里的 `<template>` 不得影响深度配对（注释后的按钮仍能扫到）',
    ok: (() => {
      const src = `<template><!-- <template> --><el-button type="primary">看</el-button></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel === false
    })(),
  },
  {
    name: '★ `plain` 变体（描边按钮）⇒ 不算实心（判绿）',
    ok: (() => {
      const src = `<template><el-table-column><template #default="scope"><el-button type="primary" plain>看</el-button></template></el-table-column></template>`
      return findSolidPrimaryButtons(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ `text` 变体（文字按钮）⇒ 不算实心（判绿）',
    ok: (() => {
      const src = `<template><el-table-column><template #default="scope"><el-button type="primary" text>看</el-button></template></el-table-column></template>`
      return findSolidPrimaryButtons(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 数据视图的行 slot（`<template #actions="{ row }">`）⇒ 判红（row-slot 形态）',
    ok: (() => {
      const src = `<template><ResponsiveDataView><template #actions="{ row }"><el-button type="primary">新建</el-button></template></ResponsiveDataView></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel && f[0].containerForm === 'row-slot'
    })(),
  },
  {
    name: '★ 自制模态面板（`role="dialog"`）⇒ 豁免（焦点区域的第二种形态）',
    ok: (() => {
      const src = `<template><div role="dialog" aria-modal="true"><ul><li v-for="k in keys"><button class="btn btn-primary">用</button></li></ul></div></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel === true && f[0].inFocusRegion === true
    })(),
  },
  {
    name: '★ `AppModal` 组件壳 ⇒ 豁免（焦点区域的第三种形态）',
    ok: (() => {
      const src = `<template><AppModal><div v-for="x in list"><button class="btn btn-primary">用</button></div></AppModal></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel === true && f[0].inFocusRegion === true
    })(),
  },
  {
    name: '★ 焦点区域的**类名**形态（`class="app-modal"` / `modal-panel`）⇒ 豁免（运行时层的口径）',
    ok: (() => {
      const a = `<template><div class="modal-overlay app-modal"><div v-for="x in list"><button class="btn btn-primary">用</button></div></div></template>`
      const b = `<template><div class="modal-panel card" role="dialog"><div v-for="x in list"><button class="btn btn-primary">用</button></div></div></template>`
      const c = `<template><div class="app-drawer__panel"><div v-for="x in list"><button class="btn btn-primary">用</button></div></div></template>`
      const d = `<template><div class="app-modalish"><div v-for="x in list"><button class="btn btn-primary">用</button></div></div></template>`
      return (
        findSolidPrimaryButtons(a, 'x.vue')[0].inFocusRegion === true &&
        findSolidPrimaryButtons(b, 'x.vue')[0].inFocusRegion === true &&
        findSolidPrimaryButtons(c, 'x.vue')[0].inFocusRegion === true &&
        findSolidPrimaryButtons(d, 'x.vue')[0].inFocusRegion === false
      )
    })(),
  },
  {
    name: '★ 登记豁免只对「文件 + 片段」精确生效（改一处片段即失效）',
    ok: (() => {
      const src = `<template><div v-for="t in tools"><button class="btn btn-primary btn-sm">配置</button></div></template>`
      const f = findSolidPrimaryButtons(src, 'components/X.vue')
      const ex = [{ id: 'e1', file: 'components/X.vue', snippet: '<button class="btn btn-primary btn-sm">', reason: 'r', witness: 'w' }]
      const hit = isExemptFinding(f[0], ex)
      const miss = isExemptFinding({ file: 'components/X.vue', snippet: '<button class="btn btn-primary">' }, ex)
      return !!hit && !miss
    })(),
  },
  {
    name: '★ 刻意区域卡 + 该视图无竞争主色 ⇒ 豁免',
    ok: (() => {
      const src = `<template><div v-for="t in tools"><button class="btn btn-primary btn-sm">配置</button></div></template>`
      const d = decideRowLevel(findSolidPrimaryButtons(src, 'x.vue'))
      return d.violations.length === 0 && d.competing === 0 && d.deliberateRegion === 1
    })(),
  },
  {
    name: '★ 刻意区域卡 + 该视图**另有**竞争主色 ⇒ 判红（同一处代码，只因多了一个工具栏按钮就改判）',
    ok: (() => {
      const src = `<template><div class="toolbar"><button class="btn btn-primary">新增</button></div><div v-for="t in tools"><button class="btn btn-primary btn-sm">配置</button></div></template>`
      const d = decideRowLevel(findSolidPrimaryButtons(src, 'x.vue'))
      return d.violations.length === 1 && d.competing === 1 && d.deliberateRegion === 0
    })(),
  },
  {
    name: '★ 数据行内的实心主色 ⇒ 无论有没有竞争主色都判红',
    ok: (() => {
      const withCompeting = `<template><div class="toolbar"><button class="btn btn-primary">新增</button></div><table><tr><td><button class="btn btn-primary">查看详情</button></td></tr></table></template>`
      const without = `<template><table><tr><td><button class="btn btn-primary">查看详情</button></td></tr></table></template>`
      return decideRowLevel(findSolidPrimaryButtons(withCompeting, 'x.vue')).violations.length === 1
        && decideRowLevel(findSolidPrimaryButtons(without, 'x.vue')).violations.length === 1
    })(),
  },
  {
    name: '★ 焦点区域内的实心主色 ⇒ 豁免（且不计入「竞争主色」）',
    ok: (() => {
      const src = `<template><AppModal><el-button type="primary">保存</el-button></AppModal></template>`
      const d = decideRowLevel(findSolidPrimaryButtons(src, 'x.vue'))
      return d.violations.length === 0 && d.competing === 0
    })(),
  },
  {
    name: '自有 `btn-primary` 行内 ⇒ 判红（v-for 块级项 ⇒ 刻意区域候选）',
    ok: (() => {
      const src = `<template><div v-for="t in tools"><button class="btn btn-primary btn-sm">配置</button></div></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel && f[0].containerForm === 'v-for-block' && f[0].form === 'own-btn-primary'
    })(),
  },
  {
    name: '★ `<li v-for>` ⇒ 列表项形态（数据行，不是刻意区域）',
    ok: (() => {
      const src = `<template><ul><li v-for="k in keys"><button class="btn btn-primary btn-sm">用</button></li></ul></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel && f[0].containerForm === 'list-item'
    })(),
  },
  {
    name: '★ 对照：页面级唯一主动作（不在任何重复容器内）⇒ 判绿',
    ok: (() => {
      const src = `<template><div class="toolbar"><button class="btn btn-primary">新增</button></div></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel === false
    })(),
  },
  {
    name: '★ 对照：对话框内的确认按钮 ⇒ 豁免（焦点区域）',
    ok: (() => {
      const src = `<template><el-dialog><template #footer><el-button type="primary">保存</el-button></template></el-dialog></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel === false && f[0].inFocusRegion === true
    })(),
  },
  {
    name: '原生 `<td>` 内的 primary ⇒ 判红',
    ok: (() => {
      const src = `<template><table><tr><td><button class="btn btn-primary">查看详情</button></td></tr></table></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel && f[0].containerForm === 'table-cell'
    })(),
  },
  {
    name: '★ 属性值里的 `>` 不得让解析错位（`:label="a > b"` 之后的按钮仍能识别）',
    ok: (() => {
      const src = `<template><el-table-column :label="a > b"><template #default="scope"><el-button type="primary">看</el-button></template></el-table-column></template>`
      const f = findSolidPrimaryButtons(src, 'x.vue')
      return f.length === 1 && f[0].rowLevel
    })(),
  },
  {
    name: '★ 脚本区里的字符串不算按钮（只看 template 块）',
    ok: (() => {
      const src = `<script setup>const x = '<button class="btn btn-primary">假</button>'</script><template><div>ok</div></template>`
      return findSolidPrimaryButtons(src, 'x.vue').length === 0
    })(),
  },
  {
    name: '★ 嵌套 `<template v-if>` 之后的按钮仍能被扫到（区间深度配对）',
    ok: (() => {
      const src = `<template><template v-if="a"><el-button type="primary">甲</el-button></template><div><button class="btn btn-primary">乙</button></div></template>`
      return findSolidPrimaryButtons(src, 'x.vue').length === 2
    })(),
  },
  {
    name: '★ 判据不含全局状态（同一输入两次结果一致）',
    ok: (() => {
      const src = `<template><el-table-column><template #default="s"><el-button type="primary">看</el-button></template></el-table-column></template>`
      const a = findSolidPrimaryButtons(src, 'x.vue')
      const b = findSolidPrimaryButtons(src, 'x.vue')
      return JSON.stringify(a) === JSON.stringify(b)
    })(),
  },
  {
    name: '★ 判据对「行内 vs 非行内」敏感（改一处必须改结论）',
    ok: (() => {
      const inRow = `<template><el-table-column><template #default="s"><el-button type="primary">看</el-button></template></el-table-column></template>`
      const notRow = `<template><div class="toolbar"><el-button type="primary">看</el-button></div></template>`
      return findSolidPrimaryButtons(inRow, 'x.vue')[0].rowLevel === true
        && findSolidPrimaryButtons(notRow, 'x.vue')[0].rowLevel === false
    })(),
  },
]

export function runPrimaryFocusSelftest(): { passed: number; total: number; failed: string[] } {
  const failed: string[] = []
  let passed = 0
  for (const c of PRIMARY_FOCUS_SELFTEST) {
    if (c.ok) passed++
    else failed.push(c.name)
  }
  return { passed, total: PRIMARY_FOCUS_SELFTEST.length, failed }
}

// ───────────────────────── 目录扫描便捷函数 ─────────────────────────

const SCAN_EXT = new Set(['.vue'])
const SKIP_DIRS = new Set(['node_modules', 'dist', '.git', 'coverage', 'reports'])

export function listVueFiles(rootDir: string): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    let entries
    try {
      entries = readdirSync(dir, { withFileTypes: true }) as unknown as Array<{ name: string; isDirectory(): boolean }>
    } catch {
      return
    }
    for (const e of entries) {
      if (SKIP_DIRS.has(e.name)) continue
      const p = join(dir, e.name)
      if (e.isDirectory()) walk(p)
      else if (SCAN_EXT.has(extname(e.name))) out.push(p)
    }
  }
  walk(rootDir)
  return out.sort()
}
