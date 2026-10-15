/**
 * i18n 词典扫描器 —— 从 `.ts` 词典源码里抽出「键路径 → 字面值」。
 *
 * ## 为什么要有这个文件（而不是把逻辑塞在门里）
 *
 * `verify-i18n-parity.mjs` 要跑在 **build 前置**里（`npm run build` 的第一步），
 * 那时没有 tsx、没有编译链，import 一个 `.ts` 会直接炸 ⇒ 只能手写扫描。
 * 但手写的扫描逻辑必须有地方**被单测**，否则它就是又一道没人验证的门。
 * ⇒ 抽成独立模块，由 `src/i18n/dictParserParity.spec.ts` 与真实对象逐键对账。
 *
 * ## ★★ 为什么是**逐字符状态机**，而不是「按行 + 几个正则」
 *
 * 前一版（按行 + 三四个正则）栽在两处：
 *   ① 键名带引号：`'unexpected-shape':` / `{ '5m': … }` ⇒ 静默丢 **7** 个键；
 *   ② 一行多键：`jan: '1 月', feb: '2 月', …` ⇒ 静默丢 **12** 个键。
 * 两处的后果都不是「误报」，而是
 *   **判据 1/2/3（键集一致 / 无未翻译 / 无重复）对这些键根本不生效** ——
 *   zh/en 在那儿写得不一致、值等于键名，门全程绿。
 *
 * 更要紧的是：**用正则切「键: 值」时，值内部的逗号与冒号会被误当分隔符。**
 * 例：`note: 'a, b: c'` 会被切成 `note='a'` 与 `b='c'` 两个键 ——
 * 词典里一旦出现这种值，门就会**凭空造出键**（假红）或**吞掉键**（假绿）。
 *
 * ⇒ 唯一正确的做法：**先把字符串与注释吃掉，再看结构。**
 * 下面的五态文法保证 `{` `}` `,` `:` 只在字符串与注释**之外**才有结构含义。
 *
 * ## 文法（词典文件实际只用这一种形态）
 *
 *   dict    := "export const" 名字 "=" "{" [pair ("," pair)*] "}" ("as" "const")?
 *   pair    := name ":" ( string | "{" [pair ...] "}" )
 *   name    := 标识符 | "'字符串'" | "\"字符串\""
 *
 *   状态机：NEED_NAME → NEED_COLON → NEED_VALUE → NEED_SEP →（`,`）NEED_NAME
 *                     └───────────────（`{`）────────────→ NEED_NAME（压栈）
 *                                                  └────（`}`）────→ 弹栈，NEED_SEP
 */

/** @typedef {{ out: Map<string, string>, duplicates: string[] }} DictScan */

/**
 * @param {string} src 词典源码
 * @returns {DictScan}
 */
export function parseDictSource(src) {
  /** @type {Map<string, string>} */
  const out = new Map()
  /** @type {Map<string, number>} */ // 键路径 → 首次出现的行号（1-based）
  const firstSeen = new Map()
  /** @type {string[]} */
  const duplicates = []

  /** @type {(string|null)[]} */ // 打开中的对象名；顶层是 null（不产生路径段）
  const stack = [null]
  /** @type {'NAME'|'COLON'|'VALUE'|'SEP'} */
  let state = 'NAME'
  let name = null // 刚读到的键名
  let currentKey = null // 已读到冒号的键名
  let line = 1

  const n = src.length
  let i = 0

  /** 跳过空白与注释；返回后 i 指向下一个有结构的字符（或末尾）。 */
  const skipTrivia = () => {
    while (i < n) {
      const c = src[i]
      if (c === '\n') {
        line++
        i++
      } else if (c === ' ' || c === '\t' || c === '\r') {
        i++
      } else if (c === '/' && src[i + 1] === '/') {
        while (i < n && src[i] !== '\n') i++
      } else if (c === '/' && src[i + 1] === '*') {
        i += 2
        while (i < n && !(src[i] === '*' && src[i + 1] === '/')) {
          if (src[i] === '\n') line++
          i++
        }
        i += 2
      } else {
        return
      }
    }
  }

  /** 读一个字符串字面量（含转义），返回 {value, end}。i 指向起始引号。 */
  const readString = () => {
    const quote = src[i]
    let j = i + 1
    let value = ''
    while (j < n && src[j] !== quote) {
      if (src[j] === '\\' && quote !== '`') {
        value += src[j + 1] ?? ''
        j += 2
        continue
      }
      if (src[j] === '\n') line++
      value += src[j]
      j++
    }
    return { value, end: j + 1 }
  }

  /** 记录一个叶子键。 */
  const record = (key, value, atLine) => {
    const keyPath = [...stack.filter((x) => x !== null), key].join('.')
    if (firstSeen.has(keyPath)) {
      duplicates.push(`${keyPath}（第 ${firstSeen.get(keyPath)} 行与第 ${atLine} 行）`)
    } else {
      firstSeen.set(keyPath, atLine)
      out.set(keyPath, value)
    }
  }

  while (i < n) {
    skipTrivia()
    if (i >= n) break
    const c = src[i]

    if (c === '{') {
      // `name: {` ⇒ 这个 name 就是被打开的对象键；
      // 顶层的 `export const zhCN = {` 没有冒号 ⇒ 压入 null（不产生路径段）。
      stack.push(state === 'VALUE' ? currentKey : null)
      currentKey = null
      state = 'NAME'
      i++
      continue
    }
    if (c === '}') {
      if (stack.length > 1) stack.pop()
      currentKey = null
      state = 'SEP'
      i++
      continue
    }
    if (c === ',') {
      currentKey = null
      state = 'NAME'
      i++
      continue
    }
    if (c === ':') {
      if (state === 'COLON') currentKey = name
      state = 'VALUE'
      i++
      continue
    }
    if (c === "'" || c === '"' || c === '`') {
      const at = line
      const { value, end } = readString()
      i = end
      if (state === 'VALUE' && currentKey !== null) {
        record(currentKey, value, at)
        currentKey = null
        state = 'SEP'
      } else {
        // NEED_NAME 位置上的字符串 ⇒ 带引号的键名
        name = value
        state = 'COLON'
      }
      continue
    }
    if (/[A-Za-z_$]/.test(c)) {
      let j = i
      while (j < n && /[\w$-]/.test(src[j])) j++
      const word = src.slice(i, j)
      i = j
      if (state === 'NAME') {
        name = word
        state = 'COLON'
      }
      // NEED_COLON / NEED_VALUE / NEED_SEP 位置的标识符是噪声（`export` `const` `as`），
      // 直接忽略 —— 它们后面没有冒号，不会被记录。
      continue
    }
    // 其余（数字等非键值结构）：不构成键值
    if (state === 'VALUE') state = 'SEP'
    i++
  }
  return { out, duplicates }
}
