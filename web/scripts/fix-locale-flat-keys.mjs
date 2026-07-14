#!/usr/bin/env node
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const localesDir = path.join(__dirname, '../src/locales')

function formatValue(v, indent) {
  if (typeof v === 'string') {
    const q = v.includes("'") ? '"' : "'"
    return `${q}${v}${q}`
  }
  if (typeof v === 'object' && v !== null) {
    const inner = Object.entries(v)
      .map(([k, val]) => `${' '.repeat(indent + 2)}${k}: ${formatValue(val, indent + 2)},`)
      .join('\n')
    return `{\n${inner}\n${' '.repeat(indent)}}`
  }
  return String(v)
}

function formatSection(name, obj) {
  const lines = Object.entries(obj).map(
    ([k, v]) => `    ${k}: ${formatValue(v, 4)},`,
  )
  return `  ${name}: {\n${lines.join('\n')}\n  },`
}

const overviewByLocale = {
  'de-DE': {
    title: 'Betriebsübersicht',
    loadFailed: 'Übersichtsdaten konnten nicht geladen werden',
    onlineInstances: 'Online-Instanzen',
    totalLicenses: 'Lizenzen gesamt',
    pendingApprovals: 'Ausstehende Aktivierungen',
    todayUpgrades: 'Upgrades heute',
    openFaults: 'Offene Störungen',
    recentUpgrades: 'Letzte Upgrades',
    recentFaults: 'Neueste Warnungen',
    pendingOffline: 'Ausstehende Offline-Aktivierungen',
    viewAll: 'Alle anzeigen',
  },
  'fr-FR': {
    title: 'Vue d’ensemble des opérations',
    loadFailed: 'Échec du chargement de la vue d’ensemble',
    onlineInstances: 'Instances en ligne',
    totalLicenses: 'Licences totales',
    pendingApprovals: 'Activations en attente',
    todayUpgrades: 'Mises à niveau du jour',
    openFaults: 'Pannes ouvertes',
    recentUpgrades: 'Mises à niveau récentes',
    recentFaults: 'Alertes récentes',
    pendingOffline: 'Activations hors ligne en attente',
    viewAll: 'Tout voir',
  },
  'es-ES': {
    title: 'Resumen de operaciones',
    loadFailed: 'Error al cargar datos del resumen',
    onlineInstances: 'Instancias en línea',
    totalLicenses: 'Licencias totales',
    pendingApprovals: 'Activaciones pendientes',
    todayUpgrades: 'Actualizaciones hoy',
    openFaults: 'Fallos abiertos',
    recentUpgrades: 'Actualizaciones recientes',
    recentFaults: 'Alertas recientes',
    pendingOffline: 'Activaciones offline pendientes',
    viewAll: 'Ver todo',
  },
  'ar-SA': {
    title: 'نظرة عامة على العمليات',
    loadFailed: 'فشل تحميل بيانات النظرة العامة',
    onlineInstances: 'النسخ المتصلة',
    totalLicenses: 'إجمالي التراخيص',
    pendingApprovals: 'تفعيلات قيد الموافقة',
    todayUpgrades: 'ترقيات اليوم',
    openFaults: 'أعطال مفتوحة',
    recentUpgrades: 'آخر الترقيات',
    recentFaults: 'أحدث التنبيهات',
    pendingOffline: 'تفعيلات offline قيد الموافقة',
    viewAll: 'عرض الكل',
  },
  'ja-JP': {
    title: '運用概要',
    loadFailed: '概要データの読み込みに失敗しました',
    onlineInstances: 'オンラインインスタンス',
    totalLicenses: 'ライセンス総数',
    pendingApprovals: '承認待ちアクティベーション',
    todayUpgrades: '本日のアップグレード',
    openFaults: '未処理の障害',
    recentUpgrades: '最近のアップグレード',
    recentFaults: '最新アラート',
    pendingOffline: '承認待ちオフラインアクティベーション',
    viewAll: 'すべて表示',
  },
  'zh-TW': {
    title: '維運概覽',
    loadFailed: '載入概覽資料失敗',
    onlineInstances: '線上實例',
    totalLicenses: 'License 總數',
    pendingApprovals: '待審批啟用',
    todayUpgrades: '今日升級',
    openFaults: '未處理故障',
    recentUpgrades: '最近升級',
    recentFaults: '最新告警',
    pendingOffline: '待審批離線啟用',
    viewAll: '查看全部',
  },
}

const expandByLocale = {
  'de-DE': {
    oldExpires: 'expires_at vorher',
    newExpires: 'expires_at nachher',
    noDiff: 'Keine Diff-Felder für diese Aktion',
  },
  'fr-FR': {
    oldExpires: 'expires_at avant',
    newExpires: 'expires_at après',
    noDiff: 'Aucun champ de diff pour cette action',
  },
  'es-ES': {
    oldExpires: 'expires_at anterior',
    newExpires: 'expires_at nuevo',
    noDiff: 'Sin campos de diferencia para esta acción',
  },
  'ar-SA': {
    oldExpires: 'expires_at قبل التغيير',
    newExpires: 'expires_at بعد التغيير',
    noDiff: 'لا حقول فرق لهذه العملية',
  },
  'ja-JP': {
    oldExpires: '変更前 expires_at',
    newExpires: '変更後 expires_at',
    noDiff: 'この操作に差分フィールドはありません',
  },
  'zh-TW': {
    oldExpires: '變更前 expires_at',
    newExpires: '變更後 expires_at',
    noDiff: '此操作無差異欄位',
  },
}

const OPS_CORRUPT =
  /(\s+\},\s*\n)\s*overview\.totalLicenses:[\s\S]*$/m

const ROUTING_CORRUPT =
  /(\s+\},\s*\n)\s*expand\.oldExpires:[\s\S]*$/m

for (const [locale, overview] of Object.entries(overviewByLocale)) {
  const filePath = path.join(localesDir, locale, 'ops.ts')
  let content = fs.readFileSync(filePath, 'utf8')
  if (!OPS_CORRUPT.test(content)) {
    console.log(`skip ${locale}/ops.ts`)
    continue
  }
  const section = formatSection('overview', overview).replace(/,$/, '')
  content = content.replace(OPS_CORRUPT, `$1\n${section}\n}`)
  fs.writeFileSync(filePath, content, 'utf8')
  console.log(`fixed ${locale}/ops.ts`)
}

for (const [locale, expand] of Object.entries(expandByLocale)) {
  const filePath = path.join(localesDir, locale, 'routingAudit.ts')
  let content = fs.readFileSync(filePath, 'utf8')
  if (!ROUTING_CORRUPT.test(content)) {
    console.log(`skip ${locale}/routingAudit.ts`)
    continue
  }
  const section = formatSection('expand', expand).replace(/,$/, '')
  content = content.replace(ROUTING_CORRUPT, `$1${section}\n}`)
  fs.writeFileSync(filePath, content, 'utf8')
  console.log(`fixed ${locale}/routingAudit.ts`)
}
