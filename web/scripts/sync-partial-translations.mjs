#!/usr/bin/env node
// sync-partial-translations.mjs — 一次性补齐 7 个非 zh-CN locale 的部分缺失 key
//
// 用法：
//   node scripts/sync-partial-translations.mjs [--dry-run]
//
// 策略：
//   - zh-TW：从 zh-CN 复制（同属 CJK 表意文字族，符合现有惯例）
//   - de-DE / fr-FR / es-ES / ar-SA：从 en-US 复制（英文 placeholder，便于人工翻译）
//   - 已经存在的 key 跳过，不覆盖
//
// 完成后请人工用更准确的翻译替换 [DE/FR/ES/AR] 占位（前缀标识）
// 但 zh-TW 与 ja-JP 接受复用中文作为过渡期翻译。

import { readFileSync, writeFileSync, readdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const ROOT = resolve(__dirname, '..')
const LOCALES_DIR = join(ROOT, 'src', 'locales')

const DRY_RUN = process.argv.includes('--dry-run')

const TARGET_LOCALES = ['zh-TW', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA']

const SOURCE_BY_LOCALE = {
  'zh-TW': 'zh-CN',
  'de-DE': 'en-US',
  'fr-FR': 'en-US',
  'es-ES': 'en-US',
  'ar-SA': 'en-US',
}

function evalAsCjs(code, absPath) {
  const mod = { exports: {} }
  const sandbox = { module: mod }
  vm.createContext(sandbox)
  vm.runInContext(code, sandbox, { filename: absPath })
  return mod.exports
}
import vm from 'node:vm'
function loadModuleFile(locale, moduleName) {
  const absPath = join(LOCALES_DIR, locale, `${moduleName}.ts`)
  const src = readFileSync(absPath, 'utf8')
  const code = src.replace(/^export default /m, 'module.exports = ')
  return { data: evalAsCjs(code, absPath), absPath }
}

function loadLocale(locale) {
  const idxPath = join(LOCALES_DIR, locale, 'index.ts')
  const src = readFileSync(idxPath, 'utf8')
  const importRe = /^\s*import\s+(\w+)\s+from\s+['"]\.\/(\w+)['"]\s*$/gm
  const modules = new Map()
  let m
  while ((m = importRe.exec(src)) !== null) {
    modules.set(m[2], loadModuleFile(locale, m[2]).data)
  }
  const exportBlock = src.match(/export\s+default\s*\{([\s\S]*?)\n\}/)
  const merged = {}
  for (const [name, data] of modules) merged[name] = data
  if (exportBlock) {
    for (const line of exportBlock[1].split('\n')) {
      const am = line.match(/^\s*(\w+)\s*:\s*(\w+)\s*,?\s*$/)
      if (am && modules.has(am[2]) && am[1] !== am[2]) {
        merged[am[1]] = modules.get(am[2])
        delete merged[am[2]]
      }
    }
  }
  return merged
}

function collectLeafKeys(obj, prefix = '') {
  const keys = new Set()
  if (typeof obj !== 'object' || obj === null) return keys
  if (Array.isArray(obj)) return keys
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k
    if (Array.isArray(v)) keys.add(path)
    else if (typeof v === 'object' && v !== null) {
      for (const sub of collectLeafKeys(v, path)) keys.add(sub)
    } else if (v !== null && v !== undefined) {
      keys.add(path)
    }
  }
  return keys
}

function loadLocaleKeys(locale) {
  const data = loadLocale(locale)
  return collectLeafKeys(data)
}

function propertyName(property) {
  return property.name && ts.isIdentifier(property.name) || ts.isStringLiteral(property.name)
    ? property.name.text
    : null
}

function findExportObject(sourceFile) {
  for (const statement of sourceFile.statements) {
    if (!ts.isExportAssignment(statement) || !ts.isObjectLiteralExpression(statement.expression)) continue
    return statement.expression
  }
  return null
}

function findProperty(object, name) {
  return object.properties.find((property) => propertyName(property) === name) ?? null
}

function findSourceProperty(object, path) {
  let current = object
  for (let i = 0; i < path.length; i++) {
    const property = findProperty(current, path[i])
    if (!property) return null
    if (i === path.length - 1) return property
    if (!property.initializer || !ts.isObjectLiteralExpression(property.initializer)) return null
    current = property.initializer
  }
  return null
}

function insertKey(filePath, keyPath, sourceFilePath, dryRun = false) {
  let content = readFileSync(filePath, 'utf8')
  const target = ts.createSourceFile(filePath, content, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const sourceContent = readFileSync(sourceFilePath, 'utf8')
  const source = ts.createSourceFile(sourceFilePath, sourceContent, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const targetRoot = findExportObject(target)
  const sourceRoot = findExportObject(source)
  if (!targetRoot || !sourceRoot) return { changed: false, reason: 'no export default object' }

  let object = targetRoot
  let missingAt = -1
  for (let i = 0; i < keyPath.length; i++) {
    const property = findProperty(object, keyPath[i])
    if (!property) { missingAt = i; break }
    if (i < keyPath.length - 1) {
      if (!property.initializer || !ts.isObjectLiteralExpression(property.initializer)) {
        return { changed: false, reason: 'path collides with leaf' }
      }
      object = property.initializer
    } else {
      return { changed: false, reason: 'leaf exists' }
    }
  }
  if (missingAt < 0) return { changed: false, reason: 'path exists' }

  const sourceProperty = findSourceProperty(sourceRoot, keyPath.slice(0, missingAt + 1))
  if (!sourceProperty) return { changed: false, reason: 'source property not found' }
  const propertyText = sourceProperty.getText(source).replace(/[ \t]+$/gm, '').trimEnd()
  const lastProperty = object.properties[object.properties.length - 1]
  const closeBrace = object.getEnd() - 1
  const betweenLastAndClose = lastProperty
    ? content.slice(lastProperty.getEnd(), closeBrace)
    : ''
  const comma = lastProperty && !betweenLastAndClose.includes(',') ? ',' : ''
  const lineStart = content.lastIndexOf('\n', closeBrace - 1) + 1
  const indent = content.slice(lineStart, closeBrace).match(/^\s*/)?.[0] ?? ''
  const insertion = `\n${indent}  ${propertyText},\n${indent}`
  const prefix = content.slice(0, closeBrace).replace(/[ \t]+$/g, '')
  content = prefix + comma + insertion + content.slice(closeBrace)
  if (!dryRun) writeFileSync(filePath, content, 'utf8')
  return { changed: true, line: content.slice(0, closeBrace).split('\n').length + 1 }
}

const sourceKeys = loadLocaleKeys('zh-CN')
console.log(`Source (zh-CN) leaf keys: ${sourceKeys.size}`)

const totalReport = []
for (const locale of TARGET_LOCALES) {
  const sourceLocale = SOURCE_BY_LOCALE[locale]
  const localeKeys = loadLocaleKeys(locale)
    let inserted = 0
  let skipped = 0
  for (const key of sourceKeys) {
    if (localeKeys.has(key)) { skipped++; continue }
     const modName = key.split('.')[0]
     const modPath = join(LOCALES_DIR, locale, `${modName}.ts`)
     const sourcePath = join(LOCALES_DIR, sourceLocale, `${modName}.ts`)
     try {
       const r = insertKey(modPath, key.split('.').slice(1), sourcePath, DRY_RUN)
      if (r.changed) inserted++
      else if (DRY_RUN) console.log(`  [skip] ${locale}/${modName} :: ${key} (${r.reason})`)
    } catch (e) {
      console.error(`FAIL ${locale}/${modName} :: ${key}: ${e.message}`)
    }
  }
  totalReport.push({ locale, source: sourceLocale, inserted, skipped })
}

console.log('\n=== summary ===')
console.table(totalReport)
console.log(DRY_RUN ? '\n(dry-run, no changes written)' : '\n✓ done')
