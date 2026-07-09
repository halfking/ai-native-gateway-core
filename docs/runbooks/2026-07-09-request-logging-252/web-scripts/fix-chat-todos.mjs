#!/usr/bin/env node

/**
 * fix-chat-todos.mjs — 修复 chat.ts 中的 TODO 标记
 *
 * 这个脚本专门处理 chat.ts 文件中的 TODO 标记
 * 将嵌套结构中的翻译移动到根级别
 */

import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { join, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const ROOT = resolve(__dirname, '..')
const LOCALES_DIR = join(ROOT, 'src', 'locales')

// 支持的语言
const LOCALES = ['zh-CN', 'en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA', 'zh-TW']

// chat.ts 中的键映射关系（根级别键 -> 嵌套路径）
const KEY_MAPPING = {
  'auto': 'page.auto',
  'copied': 'session.copied',
  'copy': 'session.copy',
  'copySummary': 'modal.copySummary',
  'errorPrefix': 'page.errorPrefix',
  'fetchKeyFailed': 'page.fetchKeyFailed',
  'keyNotSelected': 'page.keyNotSelected',
  'loading': 'page.loading',
  'noAvailableKeys': 'page.noAvailableKeys',
  'roleAssistant': 'session.roleAssistant',
  'roleUser': 'session.roleUser',
  'selectKey': 'page.selectKey',
  'selectKeyRequired': 'page.selectKeyRequired',
  'send': 'input.send',
  'sendFailed': 'page.sendFailed',
  'sending': 'input.sending',
  'sessionForbidden': 'page.sessionForbidden',
  'summarize': 'session.summarize',
  'summarizeFailed': 'page.summarizeFailed',
  'summarizing': 'session.summarizing',
  'unrevealable': 'page.unrevealable',
}

// 从嵌套路径中提取值
function extractNestedValue(content, nestedPath) {
  const parts = nestedPath.split('.')
  let current = content

  for (const part of parts) {
    // 查找 part: { 或 part: '...' 或 part: "..."
    const regex = new RegExp(`^\\s*${part}\\s*:\\s*`, 'm')
    const match = content.match(regex)
    if (!match) return null

    // 找到这一行
    const lines = content.split('\n')
    const lineIndex = lines.findIndex(l => l.match(regex))
    if (lineIndex === -1) return null

    const line = lines[lineIndex]

    // 检查是否是对象
    if (line.includes('{')) {
      // 需要找到对应的闭合括号
      let depth = 0
      let startLine = lineIndex
      for (let i = lineIndex; i < lines.length; i++) {
        const opens = (lines[i].match(/\{/g) || []).length
        const closes = (lines[i].match(/\}/g) || []).length
        depth += opens - closes
        if (depth === 0) {
          // 提取这个对象的内容
          const objectContent = lines.slice(startLine, i + 1).join('\n')
          // 递归查找
          const remainingPath = parts.slice(parts.indexOf(part) + 1).join('.')
          if (remainingPath) {
            return extractNestedValue(objectContent, remainingPath)
          }
          return objectContent
        }
      }
    } else {
      // 是一个值
      const valueMatch = line.match(/:\s*['"](.+?)['"]/)
      if (valueMatch) {
        return valueMatch[1]
      }
    }
  }

  return null
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

  // 查找所有 TODO 标记
  const todoRegex = /(\w+)\s*:\s*"\[TODO:\s*([^\]]+)\]"/g
  let match

  while ((match = todoRegex.exec(content)) !== null) {
    const [fullMatch, key, todoKey] = match
    todoCount++

    // 检查这个键是否有映射关系
    if (KEY_MAPPING[key]) {
      const nestedPath = KEY_MAPPING[key]
      console.log(`  [MAP] ${key} -> ${nestedPath}`)

      // 从嵌套结构中提取值
      const value = extractNestedValue(content, nestedPath)
      if (value) {
        console.log(`  [FIX] ${key}: "${value}"`)
        modified = modified.replace(
          fullMatch,
          `${key}: "${value}"`
        )
      } else {
        console.log(`  [WARN] Could not extract value for ${nestedPath}`)
      }
    } else {
      console.log(`  [TODO] ${key}: ${todoKey}`)
    }
  }

  // 写入修改
  if (modified !== content) {
    writeFileSync(filePath, modified, 'utf8')
    console.log(`  [WRITE] ${filePath}`)
  }

  return { changed: modified !== content, count: todoCount }
}

// 主函数
function main() {
  const dryRun = process.argv.includes('--dry-run')
  const locales = process.argv.includes('--locale=')
    ? [process.argv.find(a => a.startsWith('--locale=')).slice(9)]
    : LOCALES

  console.log('fix-chat-todos: 修复 chat.ts 中的 TODO 标记')
  console.log(`模式: ${dryRun ? 'dry-run' : 'apply'}`)
  console.log(`处理语言: ${locales.join(', ')}`)
  console.log('')

  let totalTodos = 0
  let totalFixed = 0

  for (const locale of locales) {
    const filePath = join(LOCALES_DIR, locale, 'chat.ts')
    const { changed, count } = processFile(filePath, locale)
    totalTodos += count
    if (changed) totalFixed++
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！发现 ${totalTodos} 个 TODO 标记`)
  console.log(`已处理 ${totalFixed} 个文件`)

  if (dryRun) {
    console.log('\n移除 --dry-run 参数来实际应用修改')
  }
}

main()