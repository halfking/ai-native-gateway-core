#!/usr/bin/env node

/**
 * fix-flat-keys-all.mjs — 为所有语言文件添加扁平键
 *
 * 这个脚本会：
 * 1. 检查每个文件是否有"扁平键"注释
 * 2. 如果没有，从 zh-CN 复制这些键到其他语言文件
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

// 从 zh-CN 提取扁平键
function extractFlatKeys(content) {
  const flatKeysMatch = content.match(/\/\/ 扁平键（供 Vue 组件直接使用）\n([\s\S]*?)\n\}\s*$/m)
  if (!flatKeysMatch) return null

  const lines = flatKeysMatch[1].split('\n')
  const keys = {}
  for (const line of lines) {
    const match = line.match(/^\s*(\w+)\s*:\s*['"](.+?)['"],?\s*$/)
    if (match) {
      keys[match[1]] = match[2]
    }
  }
  return keys
}

// 翻译映射（基于 zh-CN 翻译为各语言）
const TRANSLATIONS = {
  // 通用翻译
  '全部': { 'en-US': 'All', 'ja-JP': 'すべて', 'de-DE': 'Alle', 'fr-FR': 'Tous', 'es-ES': 'Todos', 'ar-SA': 'الكل', 'zh-TW': '全部' },
  'LLM 端点': { 'en-US': 'LLM endpoint', 'ja-JP': 'LLM エンドポイント', 'de-DE': 'LLM-Endpunkt', 'fr-FR': 'Point d\'accès LLM', 'es-ES': 'Endpoint LLM', 'ar-SA': 'نقطة نهاية LLM', 'zh-TW': 'LLM 端點' },
  'MCP 服务': { 'en-US': 'MCP server', 'ja-JP': 'MCP サーバー', 'de-DE': 'MCP-Server', 'fr-FR': 'Serveur MCP', 'es-ES': 'Servidor MCP', 'ar-SA': 'خادم MCP', 'zh-TW': 'MCP 服務' },
  'Agent': { 'en-US': 'Agent', 'ja-JP': 'エージェント', 'de-DE': 'Agent', 'fr-FR': 'Agent', 'es-ES': 'Agente', 'ar-SA': 'وكيل', 'zh-TW': 'Agent' },
  '加载失败': { 'en-US': 'Load failed', 'ja-JP': '読み込み失敗', 'de-DE': 'Laden fehlgeschlagen', 'fr-FR': 'Échec du chargement', 'es-ES': 'Error al cargar', 'ar-SA': 'فشل التحميل', 'zh-TW': '載入失敗' },
  '健康': { 'en-US': 'Healthy', 'ja-JP': '健康', 'de-DE': 'Gesund', 'fr-FR': 'Sain', 'es-ES': 'Saludable', 'ar-SA': 'صحي', 'zh-TW': '健康' },
  '降级': { 'en-US': 'Degraded', 'ja-JP': '低下', 'de-DE': 'Beeinträchtigt', 'fr-FR': 'Dégradé', 'es-ES': 'Degradado', 'ar-SA': 'متضرر', 'zh-TW': '降級' },
  '不可用': { 'en-US': 'Down', 'ja-JP': 'ダウン', 'de-DE': 'Nicht verfügbar', 'fr-FR': 'Indisponible', 'es-ES': 'No disponible', 'ar-SA': 'غير متاح', 'zh-TW': '不可用' },
  '未知': { 'en-US': 'Unknown', 'ja-JP': '不明', 'de-DE': 'Unbekannt', 'fr-FR': 'Inconnu', 'es-ES': 'Desconocido', 'ar-SA': 'غير معروف', 'zh-TW': '未知' },
  '加载详情失败': { 'en-US': 'Load detail failed', 'ja-JP': '詳細の読み込み失敗', 'de-DE': 'Detail laden fehlgeschlagen', 'fr-FR': 'Échec du chargement des détails', 'es-ES': 'Error al cargar detalles', 'ar-SA': 'فشل تحميل التفاصيل', 'zh-TW': '載入詳情失敗' },
  '创建关联失败': { 'en-US': 'Create relation failed', 'ja-JP': '関連付けの作成失敗', 'de-DE': 'Beziehung erstellen fehlgeschlagen', 'fr-FR': 'Échec de la création de relation', 'es-ES': 'Error al crear relación', 'ar-SA': 'فشل إنشاء العلاقة', 'zh-TW': '建立關聯失敗' },
  '加载统计失败': { 'en-US': 'Load stats failed', 'ja-JP': '統計の読み込み失敗', 'de-DE': 'Statistik laden fehlgeschlagen', 'fr-FR': 'Échec du chargement des statistiques', 'es-ES': 'Error al cargar estadísticas', 'ar-SA': 'فشل تحميل الإحصائيات', 'zh-TW': '載入統計失敗' },
  '加载拓扑失败': { 'en-US': 'Load topology failed', 'ja-JP': 'トポロジーの読み込み失敗', 'de-DE': 'Topologie laden fehlgeschlagen', 'fr-FR': 'Échec du chargement de la topologie', 'es-ES': 'Error al cargar topología', 'ar-SA': 'فشل تحميل الهيكل', 'zh-TW': '載入拓撲失敗' },
  '请输入有效的目标 Agent ID': { 'en-US': 'Please enter a valid target Agent ID', 'ja-JP': '有効なターゲットエージェント ID を入力してください', 'de-DE': 'Bitte geben Sie eine gültige Ziel-Agent-ID ein', 'fr-FR': 'Veuillez saisir un ID d\'agent cible valide', 'es-ES': 'Por favor ingrese un ID de Agent objetivo válido', 'ar-SA': 'يرجى إدخال معرف Agent هدف صالح', 'zh-TW': '請輸入有效的目標 Agent ID' },
  'depends_on（依赖）': { 'en-US': 'depends_on', 'ja-JP': 'depends_on（依存）', 'de-DE': 'depends_on (Abhängigkeit)', 'fr-FR': 'depends_on (dépendance)', 'es-ES': 'depends_on (dependencia)', 'ar-SA': 'depends_on (تبعية)', 'zh-TW': 'depends_on（依賴）' },
  'calls（调用）': { 'en-US': 'calls', 'ja-JP': 'calls（呼び出し）', 'de-DE': 'calls (Aufruf)', 'fr-FR': 'calls (appel)', 'es-ES': 'calls (llamada)', 'ar-SA': 'calls (استدعاء)', 'zh-TW': 'calls（呼叫）' },
  'similar_to（替代）': { 'en-US': 'similar_to', 'ja-JP': 'similar_to（代替）', 'de-DE': 'similar_to (Ersatz)', 'fr-FR': 'similar_to (substitut)', 'es-ES': 'similar_to (sustituto)', 'ar-SA': 'similar_to (بديل)', 'zh-TW': 'similar_to（替代）' },
}

// 翻译键
function translateKey(value, targetLocale) {
  if (targetLocale === SOURCE_LOCALE) return value
  if (TRANSLATIONS[value] && TRANSLATIONS[value][targetLocale]) {
    return TRANSLATIONS[value][targetLocale]
  }
  return value
}

// 主处理函数
function processFile(file, locale) {
  const sourcePath = join(LOCALES_DIR, SOURCE_LOCALE, file)
  const targetPath = join(LOCALES_DIR, locale, file)

  if (!existsSync(sourcePath) || !existsSync(targetPath)) {
    return { changed: false }
  }

  const sourceContent = readFileSync(sourcePath, 'utf8')
  const targetContent = readFileSync(targetPath, 'utf8')

  // 检查目标文件是否已经有扁平键
  if (targetContent.includes('// 扁平键（供 Vue 组件直接使用）')) {
    return { changed: false }
  }

  // 从源文件提取扁平键
  const flatKeys = extractFlatKeys(sourceContent)
  if (!flatKeys || Object.keys(flatKeys).length === 0) {
    return { changed: false }
  }

  // 构建扁平键块
  let flatKeysBlock = '\n  // 扁平键（供 Vue 组件直接使用）\n'
  for (const [key, value] of Object.entries(flatKeys)) {
    const translatedValue = translateKey(value, locale)
    flatKeysBlock += `  ${key}: '${translatedValue}',\n`
  }

  // 在文件末尾添加扁平键
  let modified = targetContent.replace(/\}(\s*)$/, `${flatKeysBlock}}$1`)

  // 写入修改
  writeFileSync(targetPath, modified, 'utf8')
  return { changed: true, count: Object.keys(flatKeys).length }
}

// 主函数
function main() {
  const locales = LOCALES.filter(l => l !== SOURCE_LOCALE)

  console.log('fix-flat-keys-all: 为所有语言文件添加扁平键')
  console.log(`处理语言: ${locales.join(', ')}`)
  console.log('')

  let totalChanged = 0
  let totalKeys = 0

  for (const locale of locales) {
    const sourceDir = join(LOCALES_DIR, SOURCE_LOCALE)
    const files = readdirSync(sourceDir).filter(f => f.endsWith('.ts'))

    for (const file of files) {
      const { changed, count } = processFile(file, locale)
      if (changed) {
        totalChanged++
        totalKeys += count || 0
        console.log(`  ${locale}/${file}: added ${count} flat keys`)
      }
    }
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！处理了 ${totalChanged} 个文件，添加了 ${totalKeys} 个扁平键`)
}

main()