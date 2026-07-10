#!/usr/bin/env node

/**
 * fix-all-parity.mjs — 完整修复所有 parity 问题
 *
 * 这个脚本会：
 * 1. 读取 zh-CN 源文件
 * 2. 检查所有其他语言文件
 * 3. 同步所有缺失的键（包括嵌套结构中的键）
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

// 翻译映射
const TRANSLATIONS = {
  // requests 模块
  'apiKeyIdPrefix': { 'en-US': 'API Key #{id}', 'ja-JP': 'APIキー #{id}', 'de-DE': 'API-Schlüssel #{id}', 'fr-FR': 'Clé API #{id}', 'es-ES': 'Clave API #{id}', 'ar-SA': 'مفتاح API #{id}', 'zh-TW': 'API 金鑰 #{id}' },
  'collapse': { 'en-US': 'Collapse', 'ja-JP': '折りたたむ', 'de-DE': 'Einklappen', 'fr-FR': 'Réduire', 'es-ES': 'Contraer', 'ar-SA': 'طي', 'zh-TW': '折疊' },
  'expand': { 'en-US': 'Expand', 'ja-JP': '展開', 'de-DE': 'Ausklappen', 'fr-FR': 'Développer', 'es-ES': 'Expandir', 'ar-SA': 'توسيع', 'zh-TW': '展開' },
  'title': { 'en-US': 'Title', 'ja-JP': 'タイトル', 'de-DE': 'Titel', 'fr-FR': 'Titre', 'es-ES': 'Título', 'ar-SA': 'العنوان', 'zh-TW': '標題' },
  'noData': { 'en-US': 'No data', 'ja-JP': 'データなし', 'de-DE': 'Keine Daten', 'fr-FR': 'Aucune donnée', 'es-ES': 'Sin datos', 'ar-SA': 'لا توجد بيانات', 'zh-TW': '無資料' },
  'stop': { 'en-US': 'Stop', 'ja-JP': '停止', 'de-DE': 'Stoppen', 'fr-FR': 'Arrêter', 'es-ES': 'Detener', 'ar-SA': 'إيقاف', 'zh-TW': '停止' },
  'generate': { 'en-US': 'Generate', 'ja-JP': '生成', 'de-DE': 'Generieren', 'fr-FR': 'Générer', 'es-ES': 'Generar', 'ar-SA': 'إنشاء', 'zh-TW': '產生' },
  'generating': { 'en-US': 'Generating…', 'ja-JP': '生成中…', 'de-DE': 'Wird generiert…', 'fr-FR': 'Génération…', 'es-ES': 'Generando…', 'ar-SA': 'جاري الإنشاء…', 'zh-TW': '產生中…' },
  'skipped': { 'en-US': 'Skipped', 'ja-JP': 'スキップ', 'de-DE': 'Übersprungen', 'fr-FR': 'Ignoré', 'es-ES': 'Omitido', 'ar-SA': 'تم التخطي', 'zh-TW': '已跳過' },
  'none': { 'en-US': 'None', 'ja-JP': 'なし', 'de-DE': 'Keine', 'fr-FR': 'Aucun', 'es-ES': 'Ninguno', 'ar-SA': 'لا شيء', 'zh-TW': '無' },
  'noop': { 'en-US': 'No-op', 'ja-JP': '何もしない', 'de-DE': 'Keine Aktion', 'fr-FR': 'Aucune action', 'es-ES': 'Sin acción', 'ar-SA': 'لا إجراء', 'zh-TW': '無操作' },
  'length': { 'en-US': 'Length', 'ja-JP': '長さ', 'de-DE': 'Länge', 'fr-FR': 'Longueur', 'es-ES': 'Longitud', 'ar-SA': 'الطول', 'zh-TW': '長度' },
  'end_turn': { 'en-US': 'end_turn', 'ja-JP': 'end_turn', 'de-DE': 'end_turn', 'fr-FR': 'end_turn', 'es-ES': 'end_turn', 'ar-SA': 'end_turn', 'zh-TW': 'end_turn' },
  'function_call': { 'en-US': 'function_call', 'ja-JP': 'function_call', 'de-DE': 'function_call', 'fr-FR': 'function_call', 'es-ES': 'function_call', 'ar-SA': 'function_call', 'zh-TW': 'function_call' },
  'tool_calls': { 'en-US': 'tool_calls', 'ja-JP': 'tool_calls', 'de-DE': 'tool_calls', 'fr-FR': 'tool_calls', 'es-ES': 'tool_calls', 'ar-SA': 'tool_calls', 'zh-TW': 'tool_calls' },
  'max_tokens': { 'en-US': 'max_tokens', 'ja-JP': 'max_tokens', 'de-DE': 'max_tokens', 'fr-FR': 'max_tokens', 'es-ES': 'max_tokens', 'ar-SA': 'max_tokens', 'zh-TW': 'max_tokens' },
  'delta_append': { 'en-US': 'delta_append', 'ja-JP': 'delta_append', 'de-DE': 'delta_append', 'fr-FR': 'delta_append', 'es-ES': 'delta_append', 'ar-SA': 'delta_append', 'zh-TW': 'delta_append' },
  'mechanical_trim': { 'en-US': 'mechanical_trim', 'ja-JP': 'mechanical_trim', 'de-DE': 'mechanical_trim', 'fr-FR': 'mechanical_trim', 'es-ES': 'mechanical_trim', 'ar-SA': 'mechanical_trim', 'zh-TW': 'mechanical_trim' },
  'mechanical_4xx': { 'en-US': 'mechanical_4xx', 'ja-JP': 'mechanical_4xx', 'de-DE': 'mechanical_4xx', 'fr-FR': 'mechanical_4xx', 'es-ES': 'mechanical_4xx', 'ar-SA': 'mechanical_4xx', 'zh-TW': 'mechanical_4xx' },
  'mechanical_fallback': { 'en-US': 'mechanical_fallback', 'ja-JP': 'mechanical_fallback', 'de-DE': 'mechanical_fallback', 'fr-FR': 'mechanical_fallback', 'es-ES': 'mechanical_fallback', 'ar-SA': 'mechanical_fallback', 'zh-TW': 'mechanical_fallback' },
  'memora_l1_inject': { 'en-US': 'memora_l1_inject', 'ja-JP': 'memora_l1_inject', 'de-DE': 'memora_l1_inject', 'fr-FR': 'memora_l1_inject', 'es-ES': 'memora_l1_inject', 'ar-SA': 'memora_l1_inject', 'zh-TW': 'memora_l1_inject' },
  'llm_summary': { 'en-US': 'llm_summary', 'ja-JP': 'llm_summary', 'de-DE': 'llm_summary', 'fr-FR': 'llm_summary', 'es-ES': 'llm_summary', 'ar-SA': 'llm_summary', 'zh-TW': 'llm_summary' },
  'llm_summary_done': { 'en-US': 'llm_summary_done', 'ja-JP': 'llm_summary_done', 'de-DE': 'llm_summary_done', 'fr-FR': 'llm_summary_done', 'es-ES': 'llm_summary_done', 'ar-SA': 'llm_summary_done', 'zh-TW': 'llm_summary_done' },
  'sliding_window_token': { 'en-US': 'sliding_window_token', 'ja-JP': 'sliding_window_token', 'de-DE': 'sliding_window_token', 'fr-FR': 'sliding_window_token', 'es-ES': 'sliding_window_token', 'ar-SA': 'sliding_window_token', 'zh-TW': 'sliding_window_token' },
  'sliding_window_count': { 'en-US': 'sliding_window_count', 'ja-JP': 'sliding_window_count', 'de-DE': 'sliding_window_count', 'fr-FR': 'sliding_window_count', 'es-ES': 'sliding_window_count', 'ar-SA': 'sliding_window_count', 'zh-TW': 'sliding_window_count' },
  'sliding_window_idle': { 'en-US': 'sliding_window_idle', 'ja-JP': 'sliding_window_idle', 'de-DE': 'sliding_window_idle', 'fr-FR': 'sliding_window_idle', 'es-ES': 'sliding_window_idle', 'ar-SA': 'sliding_window_idle', 'zh-TW': 'sliding_window_idle' },
  'sliding_window_mechanical_trim': { 'en-US': 'sliding_window_mechanical_trim', 'ja-JP': 'sliding_window_mechanical_trim', 'de-DE': 'sliding_window_mechanical_trim', 'fr-FR': 'sliding_window_mechanical_trim', 'es-ES': 'sliding_window_mechanical_trim', 'ar-SA': 'sliding_window_mechanical_trim', 'zh-TW': 'sliding_window_mechanical_trim' },
  'mode_1_auto_threshold': { 'en-US': 'mode_1_auto_threshold', 'ja-JP': 'mode_1_auto_threshold', 'de-DE': 'mode_1_auto_threshold', 'fr-FR': 'mode_1_auto_threshold', 'es-ES': 'mode_1_auto_threshold', 'ar-SA': 'mode_1_auto_threshold', 'zh-TW': 'mode_1_auto_threshold' },
  'mode_2_on_4xx': { 'en-US': 'mode_2_on_4xx', 'ja-JP': 'mode_2_on_4xx', 'de-DE': 'mode_2_on_4xx', 'fr-FR': 'mode_2_on_4xx', 'es-ES': 'mode_2_on_4xx', 'ar-SA': 'mode_2_on_4xx', 'zh-TW': 'mode_2_on_4xx' },
  'strategy_count': { 'en-US': 'strategy_count', 'ja-JP': 'strategy_count', 'de-DE': 'strategy_count', 'fr-FR': 'strategy_count', 'es-ES': 'strategy_count', 'ar-SA': 'strategy_count', 'zh-TW': 'strategy_count' },
  'strategy_idle': { 'en-US': 'strategy_idle', 'ja-JP': 'strategy_idle', 'de-DE': 'strategy_idle', 'fr-FR': 'strategy_idle', 'es-ES': 'strategy_idle', 'ar-SA': 'strategy_idle', 'zh-TW': 'strategy_idle' },
  'strategy_token': { 'en-US': 'strategy_token', 'ja-JP': 'strategy_token', 'de-DE': 'strategy_token', 'fr-FR': 'strategy_token', 'es-ES': 'strategy_token', 'ar-SA': 'strategy_token', 'zh-TW': 'strategy_token' },
  'with_llm_summary': { 'en-US': 'with_llm_summary', 'ja-JP': 'with_llm_summary', 'de-DE': 'with_llm_summary', 'fr-FR': 'with_llm_summary', 'es-ES': 'with_llm_summary', 'ar-SA': 'with_llm_summary', 'zh-TW': 'with_llm_summary' },
  'with_sliding_compress': { 'en-US': 'with_sliding_compress', 'ja-JP': 'with_sliding_compress', 'de-DE': 'with_sliding_compress', 'fr-FR': 'with_sliding_compress', 'es-ES': 'with_sliding_compress', 'ar-SA': 'with_sliding_compress', 'zh-TW': 'with_sliding_compress' },
  'same_session_delta': { 'en-US': 'same_session_delta', 'ja-JP': 'same_session_delta', 'de-DE': 'same_session_delta', 'fr-FR': 'same_session_delta', 'es-ES': 'same_session_delta', 'ar-SA': 'same_session_delta', 'zh-TW': 'same_session_delta' },
  'same_session_no_retransmit': { 'en-US': 'same_session_no_retransmit', 'ja-JP': 'same_session_no_retransmit', 'de-DE': 'same_session_no_retransmit', 'fr-FR': 'same_session_no_retransmit', 'es-ES': 'same_session_no_retransmit', 'ar-SA': 'same_session_no_retransmit', 'zh-TW': 'same_session_no_retransmit' },
  'estimatedNote': { 'en-US': 'Estimated', 'ja-JP': '推定', 'de-DE': 'Geschätzt', 'fr-FR': 'Estimé', 'es-ES': 'Estimado', 'ar-SA': 'مُقدَّر', 'zh-TW': '預估' },
  'equalsRequest': { 'en-US': 'Equals request', 'ja-JP': '同等リクエスト', 'de-DE': 'Gleiche Anfrage', 'fr-FR': 'Requête égale', 'es-ES': 'Solicitud igual', 'ar-SA': 'طلب متساوي', 'zh-TW': '相同請求' },
  'failureDetail': { 'en-US': 'Failure detail', 'ja-JP': '障害詳細', 'de-DE': 'Fehlerdetails', 'fr-FR': 'Détail de l\'échec', 'es-ES': 'Detalle del fallo', 'ar-SA': 'تفاصيل الفشل', 'zh-TW': '失敗詳情' },
  'finishReasonTitle': { 'en-US': 'Finish reason', 'ja-JP': '終了理由', 'de-DE': 'Abschlussgrund', 'fr-FR': 'Raison de fin', 'es-ES': 'Razón de finalización', 'ar-SA': 'سبب الانتهاء', 'zh-TW': '結束原因' },
  'keyPointsHeading': { 'en-US': 'Key points', 'ja-JP': '要点', 'de-DE': 'Kernpunkte', 'fr-FR': 'Points clés', 'es-ES': 'Puntos clave', 'ar-SA': 'النقاط الرئيسية', 'zh-TW': '重點' },
  'llmReported': { 'en-US': 'LLM reported', 'ja-JP': 'LLM 報告', 'de-DE': 'LLM gemeldet', 'fr-FR': 'LLM rapporté', 'es-ES': 'LLM reportado', 'ar-SA': 'تقرير LLM', 'zh-TW': 'LLM 回報' },
  'noKey': { 'en-US': 'No key', 'ja-JP': 'キーなし', 'de-DE': 'Kein Schlüssel', 'fr-FR': 'Aucune clé', 'es-ES': 'Sin clave', 'ar-SA': 'لا مفتاح', 'zh-TW': '無金鑰' },
  'noKeyDetail': { 'en-US': 'No key detail', 'ja-JP': 'キー詳細なし', 'de-DE': 'Keine Schlüsseldetails', 'fr-FR': 'Aucun détail de clé', 'es-ES': 'Sin detalle de clave', 'ar-SA': 'لا تفاصيل المفتاح', 'zh-TW': '無金鑰詳情' },
  'pendingPricingNoCurrency': { 'en-US': 'Pending pricing (no currency)', 'ja-JP': '価格保留（通貨なし）', 'de-DE': 'Ausstehende Preisgestaltung (keine Währung)', 'fr-FR': 'Tarification en attente (pas de devise)', 'es-ES': 'Precio pendiente (sin moneda)', 'ar-SA': 'تسعير معلق (بدون عملة)', 'zh-TW': '價格待定（無貨幣）' },
  'resultFailure': { 'en-US': 'Failure', 'ja-JP': '失敗', 'de-DE': 'Fehler', 'fr-FR': 'Échec', 'es-ES': 'Fallo', 'ar-SA': 'فشل', 'zh-TW': '失敗' },
  'resultInProgress': { 'en-US': 'In progress', 'ja-JP': '進行中', 'de-DE': 'In Bearbeitung', 'fr-FR': 'En cours', 'es-ES': 'En progreso', 'ar-SA': 'قيد التقدم', 'zh-TW': '進行中' },
  'resultSuccess': { 'en-US': 'Success', 'ja-JP': '成功', 'de-DE': 'Erfolg', 'fr-FR': 'Succès', 'es-ES': 'Éxito', 'ar-SA': 'نجاح', 'zh-TW': '成功' },
  'summaryHeading': { 'en-US': 'Summary', 'ja-JP': '要約', 'de-DE': 'Zusammenfassung', 'fr-FR': 'Résumé', 'es-ES': 'Resumen', 'ar-SA': 'ملخص', 'zh-TW': '摘要' },
  'writeMemora': { 'en-US': 'Write Memora', 'ja-JP': 'Memoraに書き込み', 'de-DE': 'Memora schreiben', 'fr-FR': 'Écrire Memora', 'es-ES': 'Escribir Memora', 'ar-SA': 'كتابة Memora', 'zh-TW': '寫入 Memora' },
  'writingMemora': { 'en-US': 'Writing Memora…', 'ja-JP': 'Memora書き込み中…', 'de-DE': 'Memora wird geschrieben…', 'fr-FR': 'Écriture Memora…', 'es-ES': 'Escribiendo Memora…', 'ar-SA': 'جاري كتابة Memora…', 'zh-TW': '寫入 Memora 中…' },
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

// 检查文件是否有某个键
function hasKey(content, key) {
  const parts = key.split('.')
  const lastKey = parts[parts.length - 1]
  const regex = new RegExp(`^\\s*${lastKey}\\s*:`, 'm')
  return regex.test(content)
}

// 翻译值
function translateValue(value, key, targetLocale) {
  // 首先检查键级别的翻译
  if (TRANSLATIONS[key] && TRANSLATIONS[key][targetLocale]) {
    return TRANSLATIONS[key][targetLocale]
  }
  // 然后检查值级别的翻译
  if (TRANSLATIONS[value] && TRANSLATIONS[value][targetLocale]) {
    return TRANSLATIONS[value][targetLocale]
  }
  return value
}

// 主处理函数
function processFile(sourcePath, targetPath, locale) {
  if (!existsSync(sourcePath) || !existsSync(targetPath)) {
    return { fixed: 0 }
  }

  const sourceContent = readFileSync(sourcePath, 'utf8')
  const targetContent = readFileSync(targetPath, 'utf8')

  const sourceKeys = extractLeafKeysWithValues(sourceContent)
  const missing = sourceKeys.filter(k => !hasKey(targetContent, k.key))

  if (missing.length === 0) return { fixed: 0 }

  // 在文件末尾添加缺失的键
  let newKeysBlock = '\n  // 同步的扁平键\n'
  for (const { key, value } of missing) {
    const translatedValue = translateValue(value, key, locale)
    const escapedValue = translatedValue.replace(/'/g, "\\'")
    newKeysBlock += `  ${key}: '${escapedValue}',\n`
  }

  const newContent = targetContent.replace(/\}(\s*)$/, `${newKeysBlock}}$1`)

  writeFileSync(targetPath, newContent, 'utf8')
  return { fixed: missing.length }
}

// 主函数
function main() {
  console.log('fix-all-parity: 完整修复所有 parity 问题')
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
        console.log(`  ${locale}/${file}: added ${fixed} keys`)
        totalFixed += fixed
      }
    }
  }

  console.log('\n' + '='.repeat(50))
  console.log(`完成！总共添加了 ${totalFixed} 个缺失的键`)
}

main()