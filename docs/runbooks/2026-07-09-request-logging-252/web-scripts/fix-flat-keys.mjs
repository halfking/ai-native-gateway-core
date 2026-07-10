#!/usr/bin/env node

/**
 * fix-flat-keys.mjs — 修复扁平键引用问题
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

// 需要修复的文件和键映射
const FIXES = {
  'agentRegistryView.ts': {
    'all': 'kind.all',
    'llm_endpoint': 'kind.llm_endpoint',
    'mcp_server': 'kind.mcp_server',
    'agent': 'kind.agent',
    'depends_on': 'relationType.depends_on',
    'calls': 'relationType.calls',
    'similar_to': 'relationType.similar_to',
    'loadFailed': 'error.loadFailed',
    'healthy': 'health.healthy',
    'degraded': 'health.degraded',
    'down': 'health.down',
    'unknown': 'health.unknown',
    'detailFailed': 'error.detailFailed',
    'linkFailed': 'error.linkFailed',
    'statsFailed': 'error.statsFailed',
    'topologyFailed': 'error.topologyFailed',
    'invalidTargetId': 'error.invalidTargetId',
  },
  'auditLog.ts': {
    'loadFailed': 'error.loadFailed',
    'dash': 'dash',
    'refreshing': 'refreshing',
    'refresh': 'refresh',
  },
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
function processFile(filePath, locale, fixes) {
  if (!existsSync(filePath)) {
    console.log(`  [SKIP] File not found: ${filePath}`)
    return { changed: false }
  }

  const content = readFileSync(filePath, 'utf8')
  let modified = content

  // 对于每个需要修复的键
  for (const [flatKey, nestedPath] of Object.entries(fixes)) {
    // 检查是否已经有这个键
    const flatKeyRegex = new RegExp(`^\\s*${flatKey}\\s*:`, 'm')
    if (flatKeyRegex.test(content)) {
      continue // 已经存在，跳过
    }

    // 从嵌套结构中提取值
    const value = extractNestedValue(content, nestedPath)
    if (value) {
      console.log(`  [ADD] ${flatKey}: "${value}" (from ${nestedPath})`)
      // 在文件末尾添加
      modified = modified.replace(/\}(\s*)$/, `  ${flatKey}: '${value}',\n}$1`)
    }
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

  console.log('fix-flat-keys: 修复扁平键引用问题')
  console.log(`模式: ${dryRun ? 'dry-run' : 'apply'}`)
  console.log(`处理语言: ${locales.join(', ')}`)
  console.log('')

  let totalChanged = 0

  for (const locale of locales) {
    console.log(`\n处理语言: ${locale}`)

    for (const [file, fixes] of Object.entries(FIXES)) {
      const filePath = join(LOCALES_DIR, locale, file)
      const { changed } = processFile(filePath, locale, fixes)
      if (changed) totalChanged++
    }
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！已处理 ${totalChanged} 个文件`)

  if (dryRun) {
    console.log('\n移除 --dry-run 参数来实际应用修改')
  }
}

main()