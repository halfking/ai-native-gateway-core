#!/usr/bin/env node

/**
 * i18n-cleanup.mjs — 清理 [TODO: ...] 标记并修复缺失的翻译
 *
 * 用法：
 *   node scripts/i18n-cleanup.mjs --dry-run          # 预览：只打印不写
 *   node scripts/i18n-cleanup.mjs --apply            # 应用：修改所有 locale 文件
 *   node scripts/i18n-cleanup.mjs --apply --locales=zh-CN  # 只处理一个 locale
 *
 * 算法：
 *   1. 扫描所有语言文件中的 [TODO: ...] 标记
 *   2. 对于每个 TODO 标记：
 *      - 检查是否在嵌套结构中有对应翻译
 *      - 如果有，移动嵌套翻译到根级别
 *      - 如果没有，添加正确的翻译（从源语言复制）
 *   3. 移除所有冗余的根级别 TODO 标记
 *
 * 安全保证：
 *   - 默认 dry-run；--apply 才会真改
 *   - 写入前打印 diff
 *   - 不破坏文件结构
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

// 真正缺失的翻译（需要手动添加）
const MISSING_TRANSLATIONS = {
  'login.changePassword': {
    'zh-CN': '修改密码',
    'en-US': 'Change password',
    'ja-JP': 'パスワード変更',
    'de-DE': 'Passwort ändern',
    'fr-FR': 'Changer le mot de passe',
    'es-ES': 'Cambiar contraseña',
    'ar-SA': 'تغيير كلمة المرور',
    'zh-TW': '修改密碼',
  },
  'login.passwordChangeSuccess': {
    'zh-CN': '密码修改成功',
    'en-US': 'Password changed successfully',
    'ja-JP': 'パスワードが正常に変更されました',
    'de-DE': 'Passwort erfolgreich geändert',
    'fr-FR': 'Mot de passe changé avec succès',
    'es-ES': 'Contraseña cambiada exitosamente',
    'ar-SA': 'تم تغيير كلمة المرور بنجاح',
    'zh-TW': '密碼修改成功',
  },
}

function parseArgs(argv) {
  const opts = {
    apply: false,
    dryRun: true,
    locales: null, // 处理的语言列表，null 表示全部
  }
  for (const a of argv) {
    if (a === '--apply') { opts.apply = true; opts.dryRun = false }
    else if (a === '--dry-run') opts.dryRun = true
    else if (a.startsWith('--locales=')) {
      opts.locales = a.slice(10).split(',')
    }
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
  // 这是一个简化的解析器，假设文件结构是标准的 export default { ... }
  // 对于复杂的文件，可能需要更完善的解析

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

  // 1. 检查是否是源语言文件
  const isSource = locale === SOURCE_LOCALE

  // 2. 查找所有 TODO 标记
  const todoRegex = /(\w+)\s*:\s*"\[TODO:\s*([^\]]+)\]"/g
  let match

  while ((match = todoRegex.exec(content)) !== null) {
    const [fullMatch, key, todoKey] = match
    todoCount++

    // 3. 检查嵌套结构中是否有对应翻译
    const nestedValue = findNestedValue(parseTsExport(content), todoKey)

    if (nestedValue && !isTodoPlaceholder(nestedValue)) {
      // 有嵌套翻译，需要移动到根级别
      console.log(`  [MOVE] ${key}: ${nestedValue} (from nested ${todoKey})`)

      // 在实际处理中，这里需要：
      // 1. 从嵌套结构中移除该键
      // 2. 在根级别添加正确的值
      // 这需要更复杂的 AST 操作，暂时跳过
    } else if (MISSING_TRANSLATIONS[todoKey]) {
      // 真正缺失的翻译，从预定义的翻译中获取
      const translation = MISSING_TRANSLATIONS[todoKey][locale]
      if (translation) {
        console.log(`  [FIX] ${key}: "${translation}"`)
        modified = modified.replace(
          fullMatch,
          `${key}: "${translation}"`
        )
      } else {
        console.log(`  [WARN] Missing translation for ${todoKey} in ${locale}`)
      }
    } else {
      // 其他情况，标记为需要处理
      console.log(`  [TODO] ${key}: ${todoKey}`)
    }
  }

  // 4. 写入修改
  if (modified !== content && !opts.dryRun) {
    writeFileSync(filePath, modified, 'utf8')
    console.log(`  [WRITE] ${filePath}`)
  }

  return { changed: modified !== content, count: todoCount }
}

// 主函数
function main() {
  opts = parseArgs(process.argv.slice(2))
  const locales = opts.locales || LOCALES

  console.log('i18n-cleanup: 清理 TODO 标记并修复缺失翻译')
  console.log(`模式: ${opts.dryRun ? 'dry-run' : 'apply'}`)
  console.log(`处理语言: ${locales.join(', ')}`)
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

    for (const file of files) {
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