#!/usr/bin/env node

/**
 * fix-parity-nested.mjs — 修复 parity 测试中的嵌套结构问题
 *
 * 这个脚本会：
 * 1. 读取 zh-CN 源文件，提取所有嵌套键路径
 * 2. 检查其他语言文件是否有这些嵌套键
 * 3. 如果缺失，从 zh-CN 复制值并正确添加到嵌套结构中
 */

import { readFileSync, writeFileSync, existsSync, readdirSync } from 'node:fs'
import { join, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const ROOT = resolve(__dirname, '..')
const LOCALES_DIR = join(ROOT, 'src', 'locales')

const LOCALES = ['en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA', 'zh-TW']
const SOURCE_LOCALE = 'zh-CN'

// 解析 TypeScript 导出对象，提取所有叶子键及其值
function extractLeafKeysWithValues(content) {
  const result = []
  const lines = content.split('\n')
  const stack = [{ prefix: '', indent: -1 }]

  for (const line of lines) {
    const trimmed = line.trim()
    if (trimmed.startsWith('//') || trimmed === '') continue

    const indent = line.search(/\S/)
    if (indent < 0) continue

    if (/^export\s+default\s+\{/.test(trimmed)) continue

    if (trimmed === '}' || trimmed === '},') {
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) stack.pop()
      continue
    }

    const openMatch = trimmed.match(/^(\w+)\s*:\s*\{/)
    if (openMatch) {
      const key = openMatch[1]
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) stack.pop()
      const current = stack[stack.length - 1]
      stack.push({ prefix: current.prefix ? `${current.prefix}.${key}` : key, indent })
      continue
    }

    const kvMatch = trimmed.match(/^(\w+)\s*:\s*['"](.+?)['"],?\s*$/)
    if (kvMatch) {
      const key = kvMatch[1]
      const value = kvMatch[2]
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) stack.pop()
      const current = stack[stack.length - 1]
      const fullKey = current.prefix ? `${current.prefix}.${key}` : key
      result.push({ key: fullKey, value })
    }
  }

  return result
}

// 检查文件是否有某个键（嵌套路径）
function hasNestedKey(content, key) {
  const parts = key.split('.')
  let currentContent = content
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i]
    if (i === parts.length - 1) {
      // Leaf key
      const regex = new RegExp(`^\\s*${part}\\s*:\\s*['"]`, 'm')
      return regex.test(currentContent)
    } else {
      // Object key
      const regex = new RegExp(`^\\s*${part}\\s*:\\s*\\{`, 'm')
      if (!regex.test(currentContent)) return false
      // Extract the object content
      const lines = currentContent.split('\n')
      const lineIndex = lines.findIndex(l => l.match(regex))
      if (lineIndex === -1) return false
      let depth = 0
      for (let j = lineIndex; j < lines.length; j++) {
        const opens = (lines[j].match(/\{/g) || []).length
        const closes = (lines[j].match(/\}/g) || []).length
        depth += opens - closes
        if (depth === 0) {
          currentContent = lines.slice(lineIndex + 1, j).join('\n')
          break
        }
      }
    }
  }
  return false
}

// 向文件中添加嵌套键
function addNestedKey(content, key, value) {
  const parts = key.split('.')
  const lastKey = parts[parts.length - 1]
  const escapedValue = value.replace(/'/g, "\\'")

  // 如果只有一层，直接在根级别添加
  if (parts.length === 1) {
    return content.replace(/\}(\s*)$/, `  ${lastKey}: '${escapedValue}',\n}$1`)
  }

  // 多层嵌套：需要找到父对象并在其中添加
  // 简化：先尝试找到所有父对象，如果都存在，在最深的父对象中添加
  const parentPath = parts.slice(0, -1).join('.')

  // 检查父对象是否存在
  if (!hasNestedKey(content, parentPath)) {
    // 父对象不存在，扁平化为根级别（用下划线连接）
    const flatKey = parts.join('_')
    return content.replace(/\}(\s*)$/, `  ${flatKey}: '${escapedValue}',\n}$1`)
  }

  // 父对象存在，找到最后一个父对象并在其中添加
  // 这是一个简化的实现：找到最后一个父对象的结束位置
  const lines = content.split('\n')
  const stack = [{ prefix: '', indent: -1 }]
  let targetEnd = -1

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    const trimmed = line.trim()
    if (trimmed.startsWith('//') || trimmed === '') continue
    const indent = line.search(/\S/)
    if (indent < 0) continue
    if (/^export\s+default\s+\{/.test(trimmed)) continue

    if (trimmed === '}' || trimmed === '},') {
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) stack.pop()
      // Check if this is the closing of our target parent
      if (stack.length > 0 && stack[stack.length - 1].prefix === parentPath) {
        targetEnd = i
      }
      continue
    }

    const openMatch = trimmed.match(/^(\w+)\s*:\s*\{/)
    if (openMatch) {
      const key = openMatch[1]
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) stack.pop()
      const current = stack[stack.length - 1]
      stack.push({ prefix: current.prefix ? `${current.prefix}.${key}` : key, indent })
      continue
    }
  }

  if (targetEnd === -1) {
    // Fallback: flat key
    const flatKey = parts.join('_')
    return content.replace(/\}(\s*)$/, `  ${flatKey}: '${escapedValue}',\n}$1`)
  }

  // Insert the key before the closing brace
  const parentIndent = '  '.repeat(parentPath.split('.').length)
  const newLine = `${parentIndent}${lastKey}: '${escapedValue}',`
  lines.splice(targetEnd, 0, newLine)
  return lines.join('\n')
}

// 主处理函数
function processFile(sourcePath, targetPath, locale) {
  if (!existsSync(sourcePath) || !existsSync(targetPath)) {
    return { fixed: 0 }
  }

  const sourceContent = readFileSync(sourcePath, 'utf8')
  let targetContent = readFileSync(targetPath, 'utf8')

  const sourceKeys = extractLeafKeysWithValues(sourceContent)
  const missing = sourceKeys.filter(k => !hasNestedKey(targetContent, k.key))

  if (missing.length === 0) return { fixed: 0 }

  // 逐个添加缺失的键
  for (const { key, value } of missing) {
    targetContent = addNestedKey(targetContent, key, value)
  }

  writeFileSync(targetPath, targetContent, 'utf8')
  return { fixed: missing.length }
}

// 主函数
function main() {
  console.log('fix-parity-nested: 修复 parity 测试中的嵌套结构问题')
  console.log(`处理语言: ${LOCALES.join(', ')}`)
  console.log('')

  let totalFixed = 0

  for (const locale of LOCALES) {
    const sourceDir = join(LOCALES_DIR, SOURCE_LOCALE)
    const localeDir = join(LOCALES_DIR, locale)
    const files = readdirSync(sourceDir).filter(f => f.endsWith('.ts'))

    for (const file of files) {
      const sourcePath = join(sourceDir, file)
      const targetPath = join(localeDir, file)

      const { fixed } = processFile(sourcePath, targetPath, locale)
      if (fixed > 0) {
        console.log(`  ${locale}/${file}: added ${fixed} nested keys`)
        totalFixed += fixed
      }
    }
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！总共添加了 ${totalFixed} 个缺失的键`)
}

main()