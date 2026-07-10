#!/usr/bin/env node

/**
 * fix-chat-flat-keys.mjs — 为所有语言的 chat.ts 文件添加扁平键
 *
 * 这个脚本会将嵌套结构中的翻译移动到根级别
 * 以支持 Vue 组件中的扁平路径引用
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

// 扁平键到嵌套路径的映射
const FLAT_KEY_MAPPING = {
  'loading': 'page.loading',
  'keyNotSelected': 'page.keyNotSelected',
  'errorPrefix': 'page.errorPrefix',
  'fetchKeyFailed': 'page.fetchKeyFailed',
  'selectKeyRequired': 'page.selectKeyRequired',
  'sessionForbidden': 'page.sessionForbidden',
  'sendFailed': 'page.sendFailed',
  'summarizeFailed': 'page.summarizeFailed',
  'noAvailableKeys': 'page.noAvailableKeys',
  'selectKey': 'page.selectKey',
  'unrevealable': 'page.unrevealable',
  'auto': 'page.auto',
  'summarizing': 'session.summarizing',
  'summarize': 'session.summarize',
  'roleUser': 'session.roleUser',
  'roleAssistant': 'session.roleAssistant',
  'copied': 'session.copied',
  'copy': 'session.copy',
  'sending': 'input.sending',
  'send': 'input.send',
  'copySummary': 'modal.copySummary',
}

// 从嵌套路径中提取值
function extractNestedValue(content, nestedPath) {
  const parts = nestedPath.split('.')
  let current = content

  for (const part of parts) {
    const regex = new RegExp(`^\\s*${part}\\s*:\\s*`, 'm')
    if (regex.test(current)) {
      const lines = current.split('\n')
      const lineIndex = lines.findIndex(l => l.match(regex))
      if (lineIndex >= 0) {
        const line = lines[lineIndex]
        if (line.includes('{')) {
          // 是对象，继续下钻
          let depth = 0
          for (let i = lineIndex; i < lines.length; i++) {
            const opens = (lines[i].match(/\{/g) || []).length
            const closes = (lines[i].match(/\}/g) || []).length
            depth += opens - closes
            if (depth === 0) {
              current = lines.slice(lineIndex + 1, i).join('\n')
              break
            }
          }
        } else {
          // 是叶子节点
          const valueMatch = line.match(/:\s*['"](.+?)['"]/)
          if (valueMatch) {
            return valueMatch[1]
          }
        }
      }
    }
  }

  return null
}

// 主处理函数
function processFile(filePath, locale) {
  if (!existsSync(filePath)) {
    console.log(`  [SKIP] File not found: ${filePath}`)
    return { changed: false }
  }

  const content = readFileSync(filePath, 'utf8')

  // 检查是否已经有扁平键
  const hasFlatKeys = content.includes('// 扁平键（供 Vue 组件直接使用）')
  if (hasFlatKeys) {
    console.log(`  [SKIP] ${filePath} already has flat keys`)
    return { changed: false }
  }

  let modified = content

  // 添加扁平键
  const flatKeys = []
  for (const [flatKey, nestedPath] of Object.entries(FLAT_KEY_MAPPING)) {
    const value = extractNestedValue(content, nestedPath)
    if (value) {
      flatKeys.push(`  ${flatKey}: '${value}',`)
    }
  }

  if (flatKeys.length > 0) {
    // 在文件末尾添加扁平键
    const flatKeysBlock = `\n  // 扁平键（供 Vue 组件直接使用）\n${flatKeys.join('\n')}\n`
    modified = modified.replace(/\}(\s*)$/, `${flatKeysBlock}}$1`)

    // 写入修改
    writeFileSync(filePath, modified, 'utf8')
    console.log(`  [WRITE] ${filePath} (added ${flatKeys.length} flat keys)`)
    return { changed: true }
  }

  return { changed: false }
}

// 主函数
function main() {
  const dryRun = process.argv.includes('--dry-run')
  const locales = process.argv.includes('--locale=')
    ? [process.argv.find(a => a.startsWith('--locale=')).slice(9)]
    : LOCALES

  console.log('fix-chat-flat-keys: 为所有语言的 chat.ts 文件添加扁平键')
  console.log(`模式: ${dryRun ? 'dry-run' : 'apply'}`)
  console.log(`处理语言: ${locales.join(', ')}`)
  console.log('')

  let totalChanged = 0

  for (const locale of locales) {
    const filePath = join(LOCALES_DIR, locale, 'chat.ts')
    const { changed } = processFile(filePath, locale)
    if (changed) totalChanged++
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！已处理 ${totalChanged} 个文件`)

  if (dryRun) {
    console.log('\n移除 --dry-run 参数来实际应用修改')
  }
}

main()