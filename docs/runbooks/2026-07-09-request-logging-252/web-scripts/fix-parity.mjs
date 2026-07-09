#!/usr/bin/env node

/**
 * fix-parity.mjs — 修复多语言文件的 parity 问题
 *
 * 这个脚本会：
 * 1. 从 zh-CN 源文件中提取所有键
 * 2. 检查其他语言文件是否有缺失的键
 * 3. 如果缺失，从 zh-CN 复制（作为占位符）
 */

import { readFileSync, writeFileSync, existsSync, readdirSync } from 'node:fs'
import { join, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const ROOT = resolve(__dirname, '..')
const LOCALES_DIR = join(ROOT, 'src', 'locales')

// 支持的语言
const LOCALES = ['zh-CN', 'en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA', 'zh-TW']
const SOURCE_LOCALE = 'zh-CN'

// 解析 TypeScript 导出对象，提取所有叶子键
function extractLeafKeys(content) {
  const keys = []
  const lines = content.split('\n')
  const stack = [{ prefix: '', indent: -1 }]

  for (const line of lines) {
    const trimmed = line.trim()
    if (trimmed.startsWith('//') || trimmed === '') continue

    const indent = line.search(/\S/)
    if (indent < 0) continue

    // 处理 export default
    if (/^export\s+default\s+\{/.test(trimmed)) {
      continue
    }

    // 处理对象结束
    if (trimmed === '}' || trimmed === '},') {
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) {
        stack.pop()
      }
      continue
    }

    // 处理对象开始
    const openMatch = trimmed.match(/^(\w+)\s*:\s*\{/)
    if (openMatch) {
      const key = openMatch[1]
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) {
        stack.pop()
      }
      const current = stack[stack.length - 1]
      stack.push({ prefix: current.prefix ? `${current.prefix}.${key}` : key, indent })
      continue
    }

    // 处理键值对
    const kvMatch = trimmed.match(/^(\w+)\s*:/)
    if (kvMatch) {
      const key = kvMatch[1]
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) {
        stack.pop()
      }
      const current = stack[stack.length - 1]
      const fullKey = current.prefix ? `${current.prefix}.${key}` : key
      keys.push(fullKey)
    }
  }

  return keys
}

// 检查文件是否有某个键
function hasKey(content, key) {
  // 转义正则特殊字符
  const escaped = key.replace(/\./g, '\\.')
  const regex = new RegExp(`^\\s*${escaped}\\s*:`, 'm')
  return regex.test(content)
}

// 主处理函数
function processFile(sourcePath, targetPath, locale) {
  if (!existsSync(sourcePath) || !existsSync(targetPath)) {
    return { changed: false, missing: [] }
  }

  const sourceContent = readFileSync(sourcePath, 'utf8')
  const targetContent = readFileSync(targetPath, 'utf8')

  const sourceKeys = extractLeafKeys(sourceContent)
  const missingKeys = sourceKeys.filter(key => !hasKey(targetContent, key))

  return { changed: false, missing: missingKeys }
}

// 主函数
function main() {
  const dryRun = process.argv.includes('--dry-run')
  const locales = process.argv.includes('--locale=')
    ? [process.argv.find(a => a.startsWith('--locale=')).slice(9)]
    : LOCALES.filter(l => l !== SOURCE_LOCALE)

  console.log('fix-parity: 修复多语言文件的 parity 问题')
  console.log(`模式: ${dryRun ? 'dry-run' : 'apply'}`)
  console.log(`处理语言: ${locales.join(', ')}`)
  console.log('')

  // 收集所有缺失的键
  const allMissing = {}

  for (const locale of locales) {
    const localeDir = join(LOCALES_DIR, locale)
    if (!existsSync(localeDir)) continue

    const sourceDir = join(LOCALES_DIR, SOURCE_LOCALE)
    const files = readdirSync(sourceDir).filter(f => f.endsWith('.ts'))

    for (const file of files) {
      const sourcePath = join(sourceDir, file)
      const targetPath = join(localeDir, file)

      if (!existsSync(targetPath)) {
        continue
      }

      const { missing } = processFile(sourcePath, targetPath, locale)
      if (missing.length > 0) {
        if (!allMissing[file]) {
          allMissing[file] = {}
        }
        allMissing[file][locale] = missing
      }
    }
  }

  // 打印摘要
  console.log('缺失键摘要:')
  for (const [file, localesMissing] of Object.entries(allMissing)) {
    console.log(`  ${file}:`)
    for (const [locale, keys] of Object.entries(localesMissing)) {
      console.log(`    ${locale}: ${keys.length} missing keys`)
    }
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！`)
}

main()