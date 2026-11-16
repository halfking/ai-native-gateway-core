#!/usr/bin/env node
// patch-current-model-i18n.mjs — L2 密钥行「当前模型」列文案（2026-11-17）
import { readFileSync, writeFileSync } from 'fs'
import { join, dirname } from 'path'
import { fileURLToPath } from 'url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const localesDir = join(__dirname, '../src/locales')

const translations = {
  'zh-TW': '當前模型',
  'en-US': 'Current Model',
  'ja-JP': '現在のモデル',
  'de-DE': 'Aktuelles Modell',
  'fr-FR': 'Modèle actuel',
  'es-ES': 'Modelo actual',
  'ar-SA': 'النموذج الحالي',
}

for (const [locale, value] of Object.entries(translations)) {
  const filePath = join(localesDir, locale, 'simpleMode.ts')
  let content = readFileSync(filePath, 'utf8')
  
  // 在 accessList.client 之后插入 currentModel
  const needle = `    client: `
  const idx = content.indexOf(needle)
  if (idx === -1) {
    console.error(`[${locale}] 找不到 client 行，跳过`)
    continue
  }
  
  const lineEnd = content.indexOf('\n', idx)
  const insertion = lineEnd + 1
  
  const patch = `    currentModel: '${value}',\n`
  content = content.slice(0, insertion) + patch + content.slice(insertion)
  
  writeFileSync(filePath, content, 'utf8')
  console.log(`[${locale}] ✓ currentModel 已添加`)
}

console.log('\n所有语种已更新')
