#!/usr/bin/env node
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const localesDir = path.join(__dirname, '../src/locales')

const CORRUPT_TAIL =
  /(\s+executeNotImpl: "[^"]+"\s*)\n\s*pages\.filesystemMaintenance:[\s\S]*$/m

const pagesByLocale = {
  'de-DE': {
    filesystemMaintenance: 'Dateisystem-Wartung',
    storageConfig: 'Speicherkonfiguration',
    logManagement: 'Protokollverwaltung',
    usageCost: 'Nutzungskosten',
  },
  'fr-FR': {
    filesystemMaintenance: 'Maintenance du système de fichiers',
    storageConfig: 'Configuration du stockage',
    logManagement: 'Gestion des journaux',
    usageCost: 'Coût d’utilisation',
  },
  'es-ES': {
    filesystemMaintenance: 'Mantenimiento del sistema de archivos',
    storageConfig: 'Configuración de almacenamiento',
    logManagement: 'Gestión de registros',
    usageCost: 'Coste de uso',
  },
  'ar-SA': {
    filesystemMaintenance: 'صيانة نظام الملفات',
    storageConfig: 'إعداد التخزين',
    logManagement: 'إدارة السجلات',
    usageCost: 'تكلفة الاستخدام',
  },
  'ja-JP': {
    filesystemMaintenance: 'ファイルシステムメンテナンス',
    storageConfig: 'ストレージ設定',
    logManagement: 'ログ管理',
    usageCost: '利用コスト',
  },
  'zh-TW': {
    filesystemMaintenance: '檔案系統維護',
    storageConfig: '儲存設定',
    logManagement: '日誌管理',
    usageCost: '用量成本',
  },
}

for (const [locale, pages] of Object.entries(pagesByLocale)) {
  const filePath = path.join(localesDir, locale, 'dataLifecycle.ts')
  let content = fs.readFileSync(filePath, 'utf8')
  if (!CORRUPT_TAIL.test(content)) {
    console.log(`skip ${locale}/dataLifecycle.ts`)
    continue
  }
  const lines = Object.entries(pages)
    .map(([k, v]) => `    ${k}: '${v}',`)
    .join('\n')
  content = content.replace(
    CORRUPT_TAIL,
    `$1,\n  pages: {\n${lines}\n  },\n}`,
  )
  fs.writeFileSync(filePath, content, 'utf8')
  console.log(`fixed ${locale}/dataLifecycle.ts`)
}
