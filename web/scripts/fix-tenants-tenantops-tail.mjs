#!/usr/bin/env node
/**
 * Fix corrupted flat tenantOps keys appended outside export object in tenants.ts
 */
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const localesDir = path.join(__dirname, '../src/locales')

const CORRUPT_TAIL = /(\s+typeTopup: "[^"]+"\s*\})\s*\n\s*status\.active:[\s\S]*$/m

const tenantOpsByLocale = {
  'de-DE': {
    autoUpdate: {
      title: 'Meine Updates',
      subtitle: 'Schreibgeschützte Update-Infos für Mandant {tenant}',
      loadFailed:
        'Updates konnten nicht geladen werden. Später erneut versuchen oder Plattform-Admin kontaktieren.',
      infoAlert:
        'Die Plattform liefert Updates gemäß Release-Richtlinie. Diese Seite bietet kein Veröffentlichen oder Rollback.',
      currentVersion: 'Aktuelle Version',
      emptyReleases: 'Keine Updates verfügbar',
      table: {
        version: 'Version',
        title: 'Titel',
        channel: 'Kanal',
        mandatory: 'Pflicht',
        publishedAt: 'Veröffentlicht',
        yes: 'Ja',
        no: 'Nein',
      },
      unknownVersion: 'Unbekannt',
      loadError: 'Verfügbare Updates konnten nicht geladen werden',
    },
    license: {
      title: 'Meine Lizenz',
      subtitle: 'Schreibgeschützte Lizenzinfos für Mandant {tenant}',
      loadFailed:
        'Lizenzinformationen konnten nicht geladen werden. Später erneut versuchen oder Plattform-Admin kontaktieren.',
      infoAlert: 'Diese Seite ist schreibgeschützt. Lizenzänderungen über Plattform-Admin.',
      empty: 'Keine Lizenzeinträge für diesen Mandanten',
      table: {
        tier: 'Tarif',
        maxDevices: 'Geräte-Limit',
        expiresAt: 'Läuft ab',
        status: 'Status',
        features: 'Funktionen',
      },
      status: {
        active: 'Aktiv',
        expired: 'Abgelaufen',
        revoked: 'Widerrufen',
      },
    },
  },
  'fr-FR': {
    autoUpdate: {
      title: 'Mes mises à jour',
      subtitle: 'Infos de mise à jour en lecture seule pour le locataire {tenant}',
      loadFailed:
        'Échec du chargement des mises à jour. Réessayez plus tard ou contactez l’administrateur.',
      infoAlert:
        'La plateforme fournit les mises à jour selon la politique de publication. Cette page ne permet ni publication ni retour arrière.',
      currentVersion: 'Version actuelle',
      emptyReleases: 'Aucune mise à jour disponible',
      table: {
        version: 'Version',
        title: 'Titre',
        channel: 'Canal',
        mandatory: 'Obligatoire',
        publishedAt: 'Publié le',
        yes: 'Oui',
        no: 'Non',
      },
      unknownVersion: 'Inconnu',
      loadError: 'Impossible de charger les mises à jour disponibles',
    },
    license: {
      title: 'Ma licence',
      subtitle: 'Infos de licence en lecture seule pour le locataire {tenant}',
      loadFailed:
        'Échec du chargement de la licence. Réessayez plus tard ou contactez l’administrateur.',
      infoAlert:
        'Cette page est en lecture seule. Contactez l’administrateur pour modifier la licence.',
      empty: 'Aucun enregistrement de licence pour ce locataire',
      table: {
        tier: 'Offre',
        maxDevices: 'Limite d’appareils',
        expiresAt: 'Expiration',
        status: 'Statut',
        features: 'Fonctionnalités',
      },
      status: {
        active: 'Active',
        expired: 'Expirée',
        revoked: 'Révoquée',
      },
    },
  },
  'es-ES': {
    autoUpdate: {
      title: 'Mis actualizaciones',
      subtitle: 'Información de actualización de solo lectura para el inquilino {tenant}',
      loadFailed:
        'No se pudieron cargar las actualizaciones. Reintente más tarde o contacte al administrador.',
      infoAlert:
        'La plataforma entrega actualizaciones según la política de publicación. Esta página no publica ni revierte.',
      currentVersion: 'Versión actual',
      emptyReleases: 'No hay actualizaciones disponibles',
      table: {
        version: 'Versión',
        title: 'Título',
        channel: 'Canal',
        mandatory: 'Obligatoria',
        publishedAt: 'Publicado',
        yes: 'Sí',
        no: 'No',
      },
      unknownVersion: 'Desconocido',
      loadError: 'No se pudieron cargar las actualizaciones disponibles',
    },
    license: {
      title: 'Mi licencia',
      subtitle: 'Información de licencia de solo lectura para el inquilino {tenant}',
      loadFailed:
        'No se pudo cargar la licencia. Reintente más tarde o contacte al administrador.',
      infoAlert:
        'Esta página es de solo lectura. Contacte al administrador para cambios de licencia.',
      empty: 'No hay registros de licencia para este inquilino',
      table: {
        tier: 'Plan',
        maxDevices: 'Límite de dispositivos',
        expiresAt: 'Vence',
        status: 'Estado',
        features: 'Funciones',
      },
      status: {
        active: 'Activa',
        expired: 'Expirada',
        revoked: 'Revocada',
      },
    },
  },
  'ar-SA': {
    autoUpdate: {
      title: 'تحديثاتي',
      subtitle: 'معلومات التحديث للقراءة فقط للمستأجر {tenant}',
      loadFailed: 'فشل تحميل التحديثات. أعد المحاولة لاحقًا أو اتصل بمسؤول المنصة.',
      infoAlert:
        'توفر المنصة التحديثات وفق سياسة الإصدار. هذه الصفحة لا تنشر ولا تتراجع عن التحديثات.',
      currentVersion: 'الإصدار الحالي',
      emptyReleases: 'لا توجد تحديثات متاحة',
      table: {
        version: 'الإصدار',
        title: 'العنوان',
        channel: 'القناة',
        mandatory: 'إلزامي',
        publishedAt: 'تاريخ النشر',
        yes: 'نعم',
        no: 'لا',
      },
      unknownVersion: 'غير معروف',
      loadError: 'تعذر تحميل التحديثات المتاحة',
    },
    license: {
      title: 'ترخيصي',
      subtitle: 'معلومات الترخيص للقراءة فقط للمستأجر {tenant}',
      loadFailed: 'فشل تحميل معلومات الترخيص. أعد المحاولة لاحقًا أو اتصل بمسؤول المنصة.',
      infoAlert: 'هذه الصفحة للقراءة فقط. اتصل بمسؤول المنصة لتغيير الترخيص.',
      empty: 'لا توجد سجلات ترخيص لهذا المستأجر',
      table: {
        tier: 'الباقة',
        maxDevices: 'حد الأجهزة',
        expiresAt: 'تاريخ الانتهاء',
        status: 'الحالة',
        features: 'الميزات',
      },
      status: {
        active: 'نشط',
        expired: 'منتهي',
        revoked: 'ملغى',
      },
    },
  },
  'ja-JP': {
    autoUpdate: {
      title: 'マイアップデート',
      subtitle: 'テナント {tenant} の読み取り専用更新情報',
      loadFailed: '更新情報の読み込みに失敗しました。後でもう一度お試しいただくか、管理者にお問い合わせください。',
      infoAlert:
        'プラットフォームはリリースポリシーに従って更新を提供します。このページでは公開やロールバックはできません。',
      currentVersion: '現在のバージョン',
      emptyReleases: '利用可能な更新はありません',
      table: {
        version: 'バージョン',
        title: 'タイトル',
        channel: 'チャネル',
        mandatory: '必須',
        publishedAt: '公開日',
        yes: 'はい',
        no: 'いいえ',
      },
      unknownVersion: '不明',
      loadError: '利用可能な更新を読み込めませんでした',
    },
    license: {
      title: 'マイライセンス',
      subtitle: 'テナント {tenant} の読み取り専用ライセンス情報',
      loadFailed: 'ライセンス情報の読み込みに失敗しました。後でもう一度お試しいただくか、管理者にお問い合わせください。',
      infoAlert: 'このページは読み取り専用です。ライセンス変更はプラットフォーム管理者にお問い合わせください。',
      empty: 'このテナントのライセンス記録はありません',
      table: {
        tier: 'プラン',
        maxDevices: 'デバイス上限',
        expiresAt: '有効期限',
        status: 'ステータス',
        features: '機能',
      },
      status: {
        active: '有効',
        expired: '期限切れ',
        revoked: '失効',
      },
    },
  },
  'zh-TW': {
    autoUpdate: {
      title: '我的更新',
      subtitle: '租戶 {tenant} 的唯讀更新資訊',
      loadFailed: '更新資訊載入失敗，請稍後重試或聯絡平台管理員。',
      infoAlert: '平台會根據發布策略為你的實例提供更新，此頁面不提供發布或回滾操作。',
      currentVersion: '目前版本',
      emptyReleases: '目前沒有可用更新',
      table: {
        version: '版本',
        title: '標題',
        channel: '渠道',
        mandatory: '必須更新',
        publishedAt: '發布時間',
        yes: '是',
        no: '否',
      },
      unknownVersion: '未知',
      loadError: '無法載入可用更新',
    },
    license: {
      title: '我的授權',
      subtitle: '租戶 {tenant} 的授權資訊（唯讀）',
      loadFailed: '授權資訊載入失敗，請稍後重試或聯絡平台管理員。',
      infoAlert: '此頁面僅顯示目前租戶資訊，授權變更請聯絡平台管理員。',
      empty: '目前租戶暫無授權記錄',
      table: {
        tier: '套餐',
        maxDevices: '裝置上限',
        expiresAt: '到期時間',
        status: '狀態',
        features: '功能',
      },
      status: {
        active: '有效',
        expired: '已過期',
        revoked: '已撤銷',
      },
    },
  },
}

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

function formatTenantOps(ops) {
  const lines = Object.entries(ops).map(
    ([k, v]) => `    ${k}: ${formatValue(v, 4)},`,
  )
  return `  tenantOps: {\n${lines.join('\n')}\n  },\n}`
}

for (const [locale, tenantOps] of Object.entries(tenantOpsByLocale)) {
  const filePath = path.join(localesDir, locale, 'tenants.ts')
  let content = fs.readFileSync(filePath, 'utf8')
  if (!CORRUPT_TAIL.test(content)) {
    console.log(`skip ${locale}: no corrupt tail`)
    continue
  }
  content = content.replace(CORRUPT_TAIL, `$1,\n\n${formatTenantOps(tenantOps)}`)
  fs.writeFileSync(filePath, content, 'utf8')
  console.log(`fixed ${locale}/tenants.ts`)
}
