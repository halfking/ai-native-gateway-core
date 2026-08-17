#!/usr/bin/env node

/**
 * i18n-fix-flat.mjs — 将嵌套翻译移动到根级别，修复 TODO 标记
 *
 * 用法：
 *   node scripts/i18n-fix-flat.mjs --dry-run          # 预览：只打印不写
 *   node scripts/i18n-fix-flat.mjs --apply            # 应用：修改所有 locale 文件
 *   node scripts/i18n-fix-flat.mjs --apply --locale=zh-CN  # 只处理一个 locale
 *   node scripts/i18n-fix-flat.mjs --apply --file=chat.ts  # 只处理一个文件
 *
 * 算法：
 *   1. 读取语言文件
 *   2. 查找所有 [TODO: ...] 标记
 *   3. 对于每个 TODO 标记：
 *      - 检查嵌套结构中是否有对应翻译
 *      - 如果有，移动翻译到根级别
 *      - 如果没有，从源语言复制翻译
 *   4. 移除冗余的 TODO 标记
 *   5. 写入修改后的文件
 */

import { readFileSync, writeFileSync, readdirSync, existsSync } from 'node:fs'
import { join, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const ROOT = resolve(__dirname, '..')
const LOCALES_DIR = join(ROOT, 'src', 'locales')

// 支持的语言
const LOCALES = ['zh-CN', 'en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA', 'zh-TW']

// 源语言（用于获取正确翻译）
const SOURCE_LOCALE = 'zh-CN'

function parseArgs(argv) {
  const opts = {
    apply: false,
    dryRun: true,
    locale: null, // 处理的语言
    file: null,   // 处理的文件
  }
  for (const a of argv) {
    if (a === '--apply') { opts.apply = true; opts.dryRun = false }
    else if (a === '--dry-run') opts.dryRun = true
    else if (a.startsWith('--locale=')) opts.locale = a.slice(9)
    else if (a.startsWith('--file=')) opts.file = a.slice(7)
  }
  return opts
}

// 检查字符串是否是 TODO 占位符
function isTodoPlaceholder(value) {
  return typeof value === 'string' && value.startsWith('[TODO:') && value.endsWith(']')
}

// 从 TODO 占位符中提取键路径
function extractKeyFromTodo(todoStr) {
  const match = todoStr.match(/^\[TODO:\s*(.+)\]$/)
  return match ? match[1] : null
}

// 递归查找对象中的嵌套键值
function findNestedValue(obj, keyPath) {
  const parts = keyPath.split('.')
  let current = obj
  for (const part of parts) {
    if (current && typeof current === 'object' && part in current) {
      current = current[part]
    } else {
      return undefined
    }
  }
  return current
}

// 递归移除对象中的嵌套键
function removeNestedKey(obj, keyPath) {
  const parts = keyPath.split('.')
  const lastKey = parts.pop()
  let current = obj
  for (const part of parts) {
    if (current && typeof current === 'object' && part in current) {
      current = current[part]
    } else {
      return
    }
  }
  if (current && typeof current === 'object') {
    delete current[lastKey]
  }
}

// 解析 TypeScript 导出对象（简化版，处理嵌套结构）
function parseTsExport(content) {
  const lines = content.split('\n')
  const result = {}
  const stack = [{ obj: result, indent: -1 }]

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    const trimmed = line.trim()

    // 跳过注释和空行
    if (trimmed.startsWith('//') || trimmed === '') continue

    // 检查缩进
    const indent = line.search(/\S/)
    if (indent < 0) continue

    // 处理 export default
    if (/^export\s+default\s+\{/.test(trimmed)) {
      continue
    }

    // 处理对象开始
    const openMatch = trimmed.match(/^(\w+)\s*:\s*\{/)
    if (openMatch) {
      const key = openMatch[1]
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) {
        stack.pop()
      }
      const parent = stack[stack.length - 1].obj
      if (typeof parent === 'object') {
        parent[key] = {}
        stack.push({ obj: parent[key], indent })
      }
      continue
    }

    // 处理对象结束
    if (trimmed === '}' || trimmed === '},') {
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) {
        stack.pop()
      }
      continue
    }

    // 处理键值对
    const kvMatch = trimmed.match(/^(\w+)\s*:\s*(.+),?\s*$/)
    if (kvMatch) {
      const key = kvMatch[1]
      let value = kvMatch[2]

      // 解析值
      if (value.startsWith('"') && value.endsWith('"')) {
        value = value.slice(1, -1)
      } else if (value.startsWith("'") && value.endsWith("'")) {
        value = value.slice(1, -1)
      }

      // 确定当前对象
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) {
        stack.pop()
      }
      const currentObj = stack[stack.length - 1].obj
      if (typeof currentObj === 'object') {
        currentObj[key] = value
      }
    }
  }

  return result
}

// 主处理函数
function processFile(filePath, locale) {
  if (!existsSync(filePath)) {
    console.log(`  [SKIP] File not found: ${filePath}`)
    return { changed: false, count: 0 }
  }

  const content = readFileSync(filePath, 'utf8')
  let modified = content
  let todoCount = 0

  // 1. 解析文件内容
  const parsed = parseTsExport(content)

  // 2. 查找所有 TODO 标记
  const todoRegex = /(\w+)\s*:\s*"\[TODO:\s*([^\]]+)\]"/g
  let match

  while ((match = todoRegex.exec(content)) !== null) {
    const [fullMatch, key, todoKey] = match
    todoCount++

    // 3. 检查嵌套结构中是否有对应翻译
    const nestedValue = findNestedValue(parsed, todoKey)

    if (nestedValue && !isTodoPlaceholder(nestedValue)) {
      // 有嵌套翻译，移动到根级别
      console.log(`  [MOVE] ${key}: "${nestedValue}" (from ${todoKey})`)

      // 替换 TODO 标记为正确的翻译
      const escapedValue = nestedValue.replace(/"/g, '\\"')
      modified = modified.replace(
        fullMatch,
        `${key}: "${escapedValue}"`
      )
    } else {
      // 没有嵌套翻译，从源语言复制
      const sourceFilePath = join(LOCALES_DIR, SOURCE_LOCALE, basename(filePath))
      if (existsSync(sourceFilePath)) {
        const sourceContent = readFileSync(sourceFilePath, 'utf8')
        const sourceParsed = parseTsExport(sourceContent)
        const sourceValue = findNestedValue(sourceParsed, todoKey)

        if (sourceValue && !isTodoPlaceholder(sourceValue)) {
          console.log(`  [COPY] ${key}: "${sourceValue}" (from ${SOURCE_LOCALE})`)

          // 替换 TODO 标记为源语言的翻译
          const escapedValue = sourceValue.replace(/"/g, '\\"')
          modified = modified.replace(
            fullMatch,
            `${key}: "${escapedValue}"`
          )
        } else {
          console.log(`  [WARN] No translation found for ${todoKey} in ${SOURCE_LOCALE}`)
        }
      }
    }
  }

  // 4. 写入修改
  if (modified !== content && !opts.dryRun) {
    writeFileSync(filePath, modified, 'utf8')
    console.log(`  [WRITE] ${filePath}`)
  }

  return { changed: modified !== content, count: todoCount }
}

// 获取文件名
function basename(filePath) {
  return filePath.split('/').pop()
}

// 主函数
function main() {
  opts = parseArgs(process.argv.slice(2))
  const locales = opts.locale ? [opts.locale] : LOCALES

  console.log('i18n-fix-flat: 将嵌套翻译移动到根级别，修复 TODO 标记')
  console.log(`模式: ${opts.dryRun ? 'dry-run' : 'apply'}`)
  console.log(`处理语言: ${locales.join(', ')}`)
  if (opts.file) console.log(`处理文件: ${opts.file}`)
  console.log('')

  let totalTodos = 0
  let totalFixed = 0

  for (const locale of locales) {
    const localeDir = join(LOCALES_DIR, locale)
    if (!existsSync(localeDir)) {
      console.log(`[SKIP] Locale directory not found: ${localeDir}`)
      continue
    }

    console.log(`\n处理语言: ${locale}`)

    // 读取所有 .ts 文件
    const files = readdirSync(localeDir).filter(f => f.endsWith('.ts'))
    const targetFiles = opts.file ? files.filter(f => f === opts.file) : files

    for (const file of targetFiles) {
      const filePath = join(localeDir, file)
      const { changed, count } = processFile(filePath, locale)
      totalTodos += count
      if (changed) totalFixed++
    }
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！发现 ${totalTodos} 个 TODO 标记`)
  console.log(`已处理 ${totalFixed} 个文件`)

  if (opts.dryRun) {
    console.log('\n使用 --apply 参数来实际应用修改')
  }
}

let opts
main()