#!/usr/bin/env node

/**
 * fix-provider-detail-all.mjs — 修复所有语言的 providerDetail.ts 文件
 *
 * 这个脚本会移除所有冗余的 TODO 标记
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

// 主处理函数
function processFile(filePath, locale) {
  if (!existsSync(filePath)) {
    console.log(`  [SKIP] File not found: ${filePath}`)
    return { changed: false }
  }

  const content = readFileSync(filePath, 'utf8')
  let modified = content

  // 移除所有根级别的 TODO 标记
  const todoRegex = /^\s*(\w+)\s*:\s*"\[TODO:\s*([^\]]+)\]",?\s*$/gm
  let match

  while ((match = todoRegex.exec(content)) !== null) {
    const [fullMatch] = match
    modified = modified.replace(fullMatch + '\n', '')
  }

  // 移除空的 [TODO] add nested keys 对象
  const emptyObjectRegex = /^\s*(\w+)\s*:\s*\{\s*\/\/\s*\[TODO\]\s*add\s*nested\s*keys\s*\},?\s*$/gm
  while ((match = emptyObjectRegex.exec(modified)) !== null) {
    const [fullMatch] = match
    modified = modified.replace(fullMatch + '\n', '')
  }

  // 写入修改
  if (modified !== content) {
    writeFileSync(filePath, modified, 'utf8')
    console.log(`  [WRITE] ${filePath}`)
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

  console.log('fix-provider-detail-all: 修复所有语言的 providerDetail.ts 文件')
  console.log(`模式: ${dryRun ? 'dry-run' : 'apply'}`)
  console.log(`处理语言: ${locales.join(', ')}`)
  console.log('')

  let totalChanged = 0

  for (const locale of locales) {
    const filePath = join(LOCALES_DIR, locale, 'providerDetail.ts')
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