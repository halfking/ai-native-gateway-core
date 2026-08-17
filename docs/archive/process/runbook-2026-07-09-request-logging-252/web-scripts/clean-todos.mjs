#!/usr/bin/env node

/**
 * clean-todos.mjs — 清理所有 [TODO: ...] 标记
 *
 * 这个脚本会：
 * 1. 找到所有 [TODO: ...] 标记
 * 2. 检查是否在嵌套结构中有对应的翻译
 * 3. 如果有，移除冗余的 TODO 标记
 * 4. 如果没有，保留标记以便后续处理
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

// 需要处理的文件
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
  let removedCount = 0

  // 查找所有 TODO 标记
  const todoRegex = /^\s*(\w+)\s*:\s*"\[TODO:\s*([^\]]+)\]",?\s*$/gm
  let match

  while ((match = todoRegex.exec(content)) !== null) {
    const [fullMatch, key, todoKey] = match
    todoCount++

    // 提取嵌套路径
    const parts = todoKey.split('.')
    if (parts.length >= 2) {
      // 构建嵌套路径（去掉第一个部分，因为它是命名空间）
      const nestedPath = parts.slice(1).join('.')
      const nestedParts = nestedPath.split('.')

      // 检查嵌套结构中是否有对应的翻译
      let found = false
      let searchContent = content

      for (const part of nestedParts) {
        const regex = new RegExp(`^\\s*${part}\\s*:\\s*`, 'm')
        if (regex.test(searchContent)) {
          // 找到这个键，检查它是否是对象
          const lines = searchContent.split('\n')
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
                  // 提取对象内容
                  searchContent = lines.slice(lineIndex + 1, i).join('\n')
                  break
                }
              }
            } else {
              // 是叶子节点，找到了
              found = true
              break
            }
          }
        }
      }

      if (found) {
        console.log(`  [REMOVE] ${key}: ${todoKey} (redundant)`)
        // 移除这一行
        modified = modified.replace(fullMatch + '\n', '')
        removedCount++
      } else {
        console.log(`  [KEEP] ${key}: ${todoKey} (not redundant)`)
      }
    }
  }

  // 写入修改
  if (modified !== content) {
    writeFileSync(filePath, modified, 'utf8')
    console.log(`  [WRITE] ${filePath} (removed ${removedCount} TODOs)`)
  }

  return { changed: modified !== content, count: todoCount, removed: removedCount }
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

  console.log('clean-todos: 清理所有 [TODO: ...] 标记')
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
      const { changed, count, removed } = processFile(filePath, locale)
      totalTodos += count
      totalRemoved += removed
    }
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！发现 ${totalTodos} 个 TODO 标记`)
  console.log(`已移除 ${totalRemoved} 个冗余标记`)

  if (dryRun) {
    console.log('\n移除 --dry-run 参数来实际应用修改')
  }
}

main()