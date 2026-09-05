#!/usr/bin/env node

/**
 * remove-redundant-todos.mjs — 移除冗余的根级别 TODO 标记
 *
 * 这个脚本专门处理 providerDetail.ts 等文件中的冗余 TODO 标记
 * 这些 TODO 标记是重复的，因为正确的翻译已经存在于嵌套结构中
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

// 需要处理的文件（包含冗余 TODO 标记的文件）
const TARGET_FILES = [
  'providerDetail.ts',
  'models.ts',
  'keys.ts',
  'requests.ts',
  'pricingManagement.ts',
  'standardModelPricing.ts',
  'agentRegistryView.ts',
  'freePool.ts',
  'auditLog.ts',
  'workTypes.ts',
  'decisions.ts',
  'compression.ts',
  'dataLifecycle.ts',
  'tenants.ts',
]

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
  const todoRegex = /^\s*(\w+)\s*:\s*"\[TODO:\s*([^\]]+)\]",?\s*$/gm
  let match

  while ((match = todoRegex.exec(content)) !== null) {
    const [fullMatch, key, todoKey] = match
    todoCount++

    // 检查这个 TODO 标记是否是冗余的
    // 冗余的条件：嵌套结构中已经有对应的翻译
    const parts = todoKey.split('.')
    if (parts.length >= 2) {
      // 检查嵌套结构中是否有对应的翻译
      const nestedKeyPath = parts.slice(1).join('.')
      const nestedRegex = new RegExp(`^\\s*${nestedKeyPath.replace(/\./g, '\\s*:\\s*')}\\s*:\\s*['"].+['"]`, 'm')
      if (nestedRegex.test(content)) {
        console.log(`  [REMOVE] ${key}: ${todoKey} (redundant)`)
        // 移除这一行
        modified = modified.replace(fullMatch + '\n', '')
      } else {
        console.log(`  [KEEP] ${key}: ${todoKey} (not redundant)`)
      }
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
  const files = process.argv.includes('--file=')
    ? [process.argv.find(a => a.startsWith('--file=')).slice(7)]
    : TARGET_FILES

  console.log('remove-redundant-todos: 移除冗余的根级别 TODO 标记')
  console.log(`模式: ${dryRun ? 'dry-run' : 'apply'}`)
  console.log(`处理语言: ${locales.join(', ')}`)
  console.log(`处理文件: ${files.join(', ')}`)
  console.log('')

  let totalTodos = 0
  let totalRemoved = 0

  for (const locale of locales) {
    const localeDir = join(LOCALES_DIR, locale)
    if (!existsSync(localeDir)) {
      console.log(`[SKIP] Locale directory not found: ${localeDir}`)
      continue
    }

    console.log(`\n处理语言: ${locale}`)

    for (const file of files) {
      const filePath = join(localeDir, file)
      const { changed, count } = processFile(filePath, locale)
      totalTodos += count
      if (changed) totalRemoved++
    }
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！发现 ${totalTodos} 个 TODO 标记`)
  console.log(`已处理 ${totalRemoved} 个文件`)

  if (dryRun) {
    console.log('\n移除 --dry-run 参数来实际应用修改')
  }
}

main()