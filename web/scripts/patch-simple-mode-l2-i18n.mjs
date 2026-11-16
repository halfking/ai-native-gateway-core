#!/usr/bin/env node
// patch-simple-mode-l2-i18n.mjs — L2 模型管理弹层文案补丁（2026-11-17）
// 在 providers 节下补 11 个新键，8 语种同步
import { readFileSync, writeFileSync } from 'fs'
import { join, dirname } from 'path'
import { fileURLToPath } from 'url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const localesDir = join(__dirname, '../src/locales')

const translations = {
  'zh-CN': {
    models: '模型',
    modelsProvided: '该供应商当前提供的',
    modelsWithRequests: '本区间有请求的',
    modelsCatalog: '完整目录',
    modelsUnit: '个',
    modelsEmpty: '无',
    modelsCatalogHint: '完整目录包含所有可路由模型，供参考',
    last30Days: '最近 30 天',
    requests: '次请求',
    enable: '启用',
    disable: '停用',
  },
  'zh-TW': {
    models: '模型',
    modelsProvided: '該供應商當前提供的',
    modelsWithRequests: '本區間有請求的',
    modelsCatalog: '完整目錄',
    modelsUnit: '個',
    modelsEmpty: '無',
    modelsCatalogHint: '完整目錄包含所有可路由模型，供參考',
    last30Days: '最近 30 天',
    requests: '次請求',
    enable: '啟用',
    disable: '停用',
  },
  'en-US': {
    models: 'Models',
    modelsProvided: 'Currently provided by this provider',
    modelsWithRequests: 'Models with requests in this period',
    modelsCatalog: 'Full catalog',
    modelsUnit: 'models',
    modelsEmpty: 'None',
    modelsCatalogHint: 'The full catalog contains all routable models for reference',
    last30Days: 'Last 30 days',
    requests: 'requests',
    enable: 'Enable',
    disable: 'Disable',
  },
  'ja-JP': {
    models: 'モデル',
    modelsProvided: 'このプロバイダーが現在提供しているもの',
    modelsWithRequests: 'この期間にリクエストがあったモデル',
    modelsCatalog: '完全なカタログ',
    modelsUnit: '個',
    modelsEmpty: 'なし',
    modelsCatalogHint: '完全なカタログには、参考のためにルーティング可能なすべてのモデルが含まれています',
    last30Days: '過去30日間',
    requests: 'リクエスト',
    enable: '有効化',
    disable: '無効化',
  },
  'de-DE': {
    models: 'Modelle',
    modelsProvided: 'Derzeit von diesem Anbieter bereitgestellt',
    modelsWithRequests: 'Modelle mit Anfragen in diesem Zeitraum',
    modelsCatalog: 'Vollständiger Katalog',
    modelsUnit: 'Modelle',
    modelsEmpty: 'Keine',
    modelsCatalogHint: 'Der vollständige Katalog enthält alle routbaren Modelle als Referenz',
    last30Days: 'Letzte 30 Tage',
    requests: 'Anfragen',
    enable: 'Aktivieren',
    disable: 'Deaktivieren',
  },
  'fr-FR': {
    models: 'Modèles',
    modelsProvided: 'Actuellement fourni par ce fournisseur',
    modelsWithRequests: 'Modèles avec des demandes dans cette période',
    modelsCatalog: 'Catalogue complet',
    modelsUnit: 'modèles',
    modelsEmpty: 'Aucun',
    modelsCatalogHint: 'Le catalogue complet contient tous les modèles routables pour référence',
    last30Days: 'Derniers 30 jours',
    requests: 'demandes',
    enable: 'Activer',
    disable: 'Désactiver',
  },
  'es-ES': {
    models: 'Modelos',
    modelsProvided: 'Actualmente proporcionado por este proveedor',
    modelsWithRequests: 'Modelos con solicitudes en este período',
    modelsCatalog: 'Catálogo completo',
    modelsUnit: 'modelos',
    modelsEmpty: 'Ninguno',
    modelsCatalogHint: 'El catálogo completo contiene todos los modelos enrutables como referencia',
    last30Days: 'Últimos 30 días',
    requests: 'solicitudes',
    enable: 'Habilitar',
    disable: 'Deshabilitar',
  },
  'ar-SA': {
    models: 'النماذج',
    modelsProvided: 'يوفرها هذا المزود حاليًا',
    modelsWithRequests: 'النماذج التي لها طلبات في هذه الفترة',
    modelsCatalog: 'الفهرس الكامل',
    modelsUnit: 'نماذج',
    modelsEmpty: 'لا يوجد',
    modelsCatalogHint: 'يحتوي الفهرس الكامل على جميع النماذج القابلة للتوجيه للرجوع إليها',
    last30Days: 'آخر 30 يومًا',
    requests: 'طلبات',
    enable: 'تمكين',
    disable: 'تعطيل',
  },
}

for (const [locale, keys] of Object.entries(translations)) {
  const filePath = join(localesDir, locale, 'simpleMode.ts')
  let content = readFileSync(filePath, 'utf8')
  
  // 在 providers 节的 added 后、闭合花括号前插入
  const needle = `    added: `
  const idx = content.lastIndexOf(needle)
  if (idx === -1) {
    console.error(`[${locale}] 找不到 added 行，跳过`)
    continue
  }
  
  // 找到 added 行的行尾
  const lineEnd = content.indexOf('\n', idx)
  const nextLine = content.indexOf('\n', lineEnd + 1)
  const insertion = nextLine === -1 ? content.length : nextLine
  
  // 构造补丁
  const patch = Object.entries(keys).map(([k, v]) => 
    `    ${k}: '${v}',`
  ).join('\n')
  
  // 插入
  content = content.slice(0, insertion) + '\n' + patch + content.slice(insertion)
  
  writeFileSync(filePath, content, 'utf8')
  console.log(`[${locale}] ✓ 补丁已应用`)
}

console.log('\n所有语种已更新')
