#!/usr/bin/env node

/**
 * sync-from-zhcn.mjs — 从 zh-CN 同步缺失的键到其他语言文件
 *
 * 这个脚本会：
 * 1. 解析 zh-CN 文件中的所有键
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

// 翻译映射（中文 -> 其他语言）
const TRANSLATIONS = {
  // 通用翻译
  '全部': 'All',
  '健康': 'Healthy',
  '降级': 'Degraded',
  '不可用': 'Unavailable',
  '未知': 'Unknown',
  '加载失败': 'Load failed',
  '加载详情失败': 'Load detail failed',
  '加载统计失败': 'Load stats failed',
  '加载拓扑失败': 'Load topology failed',
  '创建关联失败': 'Create relation failed',
  'Agent': 'Agent',
  'depends_on': 'depends_on',
  'calls': 'calls',
  'similar_to': 'similar_to',
  '30天': '30 days',
  '7天': '7 days',
  '24小时': '24 hours',
  '增量拼接': 'Delta append',
  '机械裁剪': 'Mechanical trim',
  'Memora注入': 'Memora inject',
  'LLM总结': 'LLM summary',
  '空操作': 'No-op',
  '未压缩': 'Not compressed',
  '滑动窗口(Token)': 'Sliding window (tokens)',
  '滑动窗口(条数)': 'Sliding window (count)',
  '滑动窗口(空闲)': 'Sliding window (idle)',
  '每6小时': 'Every 6 hours',
  '按天': 'Daily',
  '按小时': 'Hourly',
  '—': '—',
  '刷新中…': 'Refreshing…',
  '刷新': 'Refresh',
  '加载中…': 'Loading…',
  '加载失败:': 'Load failed:',
  '请输入有效的目标 Agent ID': 'Please enter a valid target Agent ID',
  'depends_on（依赖）': 'depends_on (dependency)',
  'calls（调用）': 'calls (call)',
  'similar_to（替代）': 'similar_to (substitute)',
  'LLM 端点': 'LLM endpoint',
  'MCP 服务': 'MCP server',
}

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
    const kvMatch = trimmed.match(/^(\w+)\s*:\s*['"](.+?)['"],?\s*$/)
    if (kvMatch) {
      const key = kvMatch[1]
      const value = kvMatch[2]
      while (stack.length > 1 && stack[stack.length - 1].indent >= indent) {
        stack.pop()
      }
      const current = stack[stack.length - 1]
      const fullKey = current.prefix ? `${current.prefix}.${key}` : key
      result.push({ key: fullKey, value })
    }
  }

  return result
}

// 检查文件是否有某个键（带值匹配）
function hasKey(content, key) {
  const escaped = key.replace(/\./g, '\\.')
  // 使用简单的缩进匹配
  const parts = key.split('.')
  const lastKey = parts[parts.length - 1]
  const regex = new RegExp(`^\\s*${lastKey}\\s*:`, 'm')
  return regex.test(content)
}

// 翻译值
function translateValue(value, targetLocale) {
  if (targetLocale === SOURCE_LOCALE) return value
  if (TRANSLATIONS[value]) {
    return TRANSLATIONS[value]
  }
  return value
}

// 主处理函数
function processFile(sourcePath, targetPath, locale, missingKeys) {
  if (!existsSync(sourcePath) || !existsSync(targetPath)) {
    return false
  }

  const sourceContent = readFileSync(sourcePath, 'utf8')
  const targetContent = readFileSync(targetPath, 'utf8')

  const sourceKeys = extractLeafKeysWithValues(sourceContent)
  const missing = sourceKeys.filter(k => !hasKey(targetContent, k.key))

  if (missing.length === 0) return false

  // 构建缺失的键
  const newKeys = missing.map(k => {
    const translatedValue = translateValue(k.value, locale)
    return { key: k.key, value: translatedValue }
  })

  // 按路径分组，正确构建嵌套结构
  const newContent = buildNewContent(targetContent, newKeys)

  // 写入修改
  writeFileSync(targetPath, newContent, 'utf8')
  return true
}

// 构建新内容，正确处理嵌套结构
function buildNewContent(content, newKeys) {
  let modified = content

  // 按顶级键分组
  const groupedByTopLevel = {}
  for (const { key, value } of newKeys) {
    const topLevel = key.split('.')[0]
    if (!groupedByTopLevel[topLevel]) {
      groupedByTopLevel[topLevel] = []
    }
    groupedByTopLevel[topLevel].push({ key, value })
  }

  // 对于每个顶级键，在文件末尾添加
  for (const [topLevel, keys] of Object.entries(groupedByTopLevel)) {
    // 检查是否已经有这个顶级键
    const topLevelRegex = new RegExp(`^\\s*${topLevel}\\s*:`, 'm')
    if (!topLevelRegex.test(modified)) {
      // 添加为新的扁平键
      let newKeysBlock = ''
      for (const { key, value } of keys) {
        newKeysBlock += `  ${key}: '${value.replace(/'/g, "\\'")}',\n`
      }
      modified = modified.replace(/\}(\s*)$/, `\n${newKeysBlock}}$1`)
    } else {
      // 已存在顶级键，需要在正确的嵌套位置添加
      for (const { key, value } of keys) {
        const parts = key.split('.')
        const escapedValue = value.replace(/'/g, "\\'")
        if (parts.length === 1) {
          // 扁平键，直接添加
          if (!hasKey(modified, key)) {
            modified = modified.replace(/\}(\s*)$/, `  ${key}: '${escapedValue}',\n}$1`)
          }
        }
      }
    }
  }

  return modified
}

// 主函数
function main() {
  const locales = LOCALES.filter(l => l !== SOURCE_LOCALE)

  console.log('sync-from-zhcn: 从 zh-CN 同步缺失的键到其他语言文件')
  console.log(`处理语言: ${locales.join(', ')}`)
  console.log('')

  let totalFixed = 0

  for (const locale of locales) {
    const sourceDir = join(LOCALES_DIR, SOURCE_LOCALE)
    const localeDir = join(LOCALES_DIR, locale)
    const files = readdirSync(sourceDir).filter(f => f.endsWith('.ts'))

    for (const file of files) {
      const sourcePath = join(sourceDir, file)
      const targetPath = join(localeDir, file)

      if (!existsSync(targetPath)) continue

      const fixed = processFile(sourcePath, targetPath, locale, [])
      if (fixed) totalFixed++
    }
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！修复了 ${totalFixed} 个文件`)
}

main()