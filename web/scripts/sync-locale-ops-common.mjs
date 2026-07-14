#!/usr/bin/env node
import { readFileSync, writeFileSync } from 'fs'
import { join, dirname } from 'path'
import { fileURLToPath } from 'url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..', 'src', 'locales')

const COMMON_FLAT = {
  'zh-TW': {
    status: '狀態',
    view: '查看',
    updateSuccess: '更新成功',
    operationFailed: '操作失敗',
    error: '錯誤',
  },
  'ja-JP': {
    status: 'ステータス',
    view: '表示',
    updateSuccess: '更新しました',
    operationFailed: '操作に失敗しました',
    error: 'エラー',
  },
  'de-DE': {
    status: 'Status',
    view: 'Anzeigen',
    updateSuccess: 'Erfolgreich aktualisiert',
    operationFailed: 'Vorgang fehlgeschlagen',
    error: 'Fehler',
  },
  'fr-FR': {
    status: 'Statut',
    view: 'Voir',
    updateSuccess: 'Mis à jour',
    operationFailed: 'Échec de l’opération',
    error: 'Erreur',
  },
  'es-ES': {
    status: 'Estado',
    view: 'Ver',
    updateSuccess: 'Actualizado',
    operationFailed: 'Operación fallida',
    error: 'Error',
  },
  'ar-SA': {
    status: 'الحالة',
    view: 'عرض',
    updateSuccess: 'تم التحديث',
    operationFailed: 'فشلت العملية',
    error: 'خطأ',
  },
}

const UNKNOWN = {
  'zh-TW': '未知',
  'ja-JP': '不明',
  'de-DE': 'Unbekannt',
  'fr-FR': 'Inconnu',
  'es-ES': 'Desconocido',
  'ar-SA': 'غير معروف',
}

const AUTH_ACTIONS = {
  'de-DE': {
    'authentication.login': 'Anmeldung',
    'authentication.login_failed': 'Anmeldung fehlgeschlagen',
    'authentication.logout': 'Abmeldung',
  },
  'fr-FR': {
    'authentication.login': 'Connexion',
    'authentication.login_failed': 'Échec de connexion',
    'authentication.logout': 'Déconnexion',
  },
  'es-ES': {
    'authentication.login': 'Inicio de sesión',
    'authentication.login_failed': 'Error de inicio de sesión',
    'authentication.logout': 'Cierre de sesión',
  },
  'ja-JP': {
    'authentication.login': 'ログイン',
    'authentication.login_failed': 'ログイン失敗',
    'authentication.logout': 'ログアウト',
  },
  'zh-TW': {
    'authentication.login': '登入',
    'authentication.login_failed': '登入失敗',
    'authentication.logout': '登出',
  },
  'ar-SA': {
    'authentication.login': 'تسجيل الدخول',
    'authentication.login_failed': 'فشل تسجيل الدخول',
    'authentication.logout': 'تسجيل الخروج',
  },
}

function patchCommon(loc) {
  const path = join(ROOT, loc, 'common.ts')
  let c = readFileSync(path, 'utf8')
  if (/\n  view: '/.test(c)) return

  const flat = COMMON_FLAT[loc]
  const block = Object.entries(flat)
    .map(([k, v]) => `  ${k}: '${v.replace(/'/g, "\\'")}',`)
    .join('\n')

  c = c.replace(/\n  yes: '[^']+',\n\}/, (m) => m.replace(/\n\}/, `\n${block}\n}`))
  writeFileSync(path, c, 'utf8')
  console.log('common', loc)
}

function patchOps(loc) {
  const path = join(ROOT, loc, 'ops.ts')
  let c = readFileSync(path, 'utf8')
  const u = UNKNOWN[loc]

  if (!c.includes('channel:') || c.includes('_unknown:')) {
    // may already have _unknown in channel
  }
  if (!c.match(/channel:[\s\S]*?_unknown/)) {
    c = c.replace(/(canary: '[^']+',)\n(\s+\},)/, `$1\n      _unknown: '${u}',\n$2`)
  }
  if (!c.match(/logStatus:[\s\S]*?_unknown/)) {
    c = c.replace(/(rolled_back: '[^']+',)\n(\s+\},)/, `$1\n      _unknown: '${u}',\n$2`)
  }
  if (!c.match(/vibecoding:[\s\S]*status:[\s\S]*?_unknown/)) {
    c = c.replace(
      /(vibecoding:[\s\S]*?completed: '[^']+',)\n(\s+\},)\n(\s+\},)/,
      `$1\n      _unknown: '${u}',\n$2\n$3`,
    )
  }

  writeFileSync(path, c, 'utf8')
  console.log('ops', loc)
}

function patchAuditLog(loc) {
  const path = join(ROOT, loc, 'auditLog.ts')
  let c = readFileSync(path, 'utf8')
  if (c.includes("'authentication.login'")) return

  const actions = AUTH_ACTIONS[loc]
  const lines = Object.entries(actions)
    .map(([k, v]) => `    '${k}': '${v.replace(/'/g, "\\'")}',`)
    .join('\n')

  c = c.replace(/('auth\.rate_limited': '[^']+',)\n(\s+\},)/, `$1\n${lines}\n$2`)
  c = c.replace(/refreshing: '刷新中…'/, "refreshing: 'Refreshing…'")
  c = c.replace(/refresh: '刷新'/, "refresh: 'Refresh'")

  writeFileSync(path, c, 'utf8')
  console.log('auditLog', loc)
}

// en-US flat key fix
{
  const path = join(ROOT, 'en-US', 'auditLog.ts')
  let c = readFileSync(path, 'utf8')
  c = c.replace(/refreshing: '刷新中…'/, "refreshing: 'Refreshing…'")
  c = c.replace(/refresh: '刷新'/, "refresh: 'Refresh'")
  writeFileSync(path, c, 'utf8')
  console.log('auditLog en-US flat keys')
}

for (const loc of Object.keys(COMMON_FLAT)) {
  patchCommon(loc)
  patchOps(loc)
  patchAuditLog(loc)
}
