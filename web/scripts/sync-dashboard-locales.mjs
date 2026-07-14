#!/usr/bin/env node
import { readFileSync, writeFileSync } from 'fs'
import { join, dirname } from 'path'
import { fileURLToPath } from 'url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..', 'src', 'locales')
const en = readFileSync(join(ROOT, 'en-US', 'dashboard.ts'), 'utf8')

function extractBlock(src, name) {
  const re = new RegExp(`${name}: \\{([\\s\\S]*?)\\n  \\},`)
  const m = src.match(re)
  return m ? m[0] : null
}

const tabsBlock = extractBlock(en, 'tabs')
const v2Block = extractBlock(en, 'v2')

const localeTabs = {
  'zh-TW': tabsBlock
    .replace('Live Request Stream', '即時請求流')
    .replace('Sessions & Statistics', '會話與統計')
    .replace('System Monitoring', '系統監測'),
  'ja-JP': tabsBlock
    .replace('Live Request Stream', 'リアルタイムリクエスト')
    .replace('Sessions & Statistics', 'セッションと統計')
    .replace('System Monitoring', 'システム監視'),
  'de-DE': tabsBlock
    .replace('Live Request Stream', 'Live-Anfragestream')
    .replace('Sessions & Statistics', 'Sitzungen & Statistik')
    .replace('System Monitoring', 'Systemüberwachung'),
  'fr-FR': tabsBlock
    .replace('Live Request Stream', 'Flux de requêtes en direct')
    .replace('Sessions & Statistics', 'Sessions et statistiques')
    .replace('System Monitoring', 'Surveillance système'),
  'es-ES': tabsBlock
    .replace('Live Request Stream', 'Flujo de solicitudes en vivo')
    .replace('Sessions & Statistics', 'Sesiones y estadísticas')
    .replace('System Monitoring', 'Monitoreo del sistema'),
  'ar-SA': tabsBlock
    .replace('Live Request Stream', 'تدفق الطلبات المباشر')
    .replace('Sessions & Statistics', 'الجلسات والإحصائيات')
    .replace('System Monitoring', 'مراقبة النظام'),
}

const localeV2 = {
  'zh-TW': `  v2: {
    quickApiKey: 'API Key', quickModels: '模型', quickApiKeyTitle: '查看 API Key 排行',
    quickModelsTitle: '查看模型統計', refreshData: '重新整理資料', retry: '重試',
    reloadAria: '重新載入資料', degradedViewLabel: '視圖',
    degradedHint: '資料視圖 {view} 尚未初始化，請先執行資料聚合遷移',
    emptyTitle: '暫無請求資料',
    emptyHint: '設定供應商後，透過 /v1/chat/completions 發起呼叫即可查看即時流。',
    modelsPage: '模型頁', totalTokensShort: '總 Token', totalCredits: '總積分消耗',
    creditsSub: '按定價 × token 計算', modelCount: '模型數', sessionCompression: '會話壓縮',
  },`,
  'ja-JP': `  v2: {
    quickApiKey: 'API Key', quickModels: 'モデル', quickApiKeyTitle: 'API Key ランキング',
    quickModelsTitle: 'モデル統計', refreshData: 'データ更新', retry: '再試行',
    reloadAria: 'データ再読み込み', degradedViewLabel: 'ビュー',
    degradedHint: 'データビュー {view} は未初期化です。集計マイグレーションを実行してください。',
    emptyTitle: 'リクエストデータなし',
    emptyHint: 'プロバイダー設定後、/v1/chat/completions で呼び出すと表示されます。',
    modelsPage: 'モデル', totalTokensShort: '総トークン', totalCredits: '総クレジット',
    creditsSub: '価格 × トークン', modelCount: 'モデル数', sessionCompression: 'セッション圧縮',
  },`,
}

const liveExtraEn = `
    emptyWaiting: 'Waiting for live request stream data…',
    groupByVendor: 'By vendor', groupByProvider: 'By provider', groupByModel: 'By model',
    probeAll: 'All', probeOnly: 'Probes only',
    probeAllTitle: 'Show all requests (default)', probeOnlyTitle: 'Show probe requests only',
    cacheWindow: 'Cache / window', connectionDetailTitle: 'Click for connection details',
    dimensionVendor: 'Vendor', dimensionProvider: 'Provider', dimensionModel: 'Model',
    statusOpen: 'Connected', statusConnecting: 'Connecting', statusReconnecting: 'Reconnecting',
    statusUnsupported: 'Unsupported', statusClosed: 'Disconnected',
    sseDetailTitle: 'SSE connection details', sseStatusLabel: 'Status', sseUrlLabel: 'SSE URL',
    editUrl: 'Edit', editUrlPlaceholder: 'Enter SSE URL', save: 'Save', resetDefault: 'Default',
    cancel: 'Cancel', testConnection: 'Test connection', close: 'Close',
    sseTestOk: 'SSE connection OK!\\nStatus: connected\\nURL: {url}',
    sseTestFail: 'SSE not connected\\nStatus: {status}\\nURL: {url}',
    redisWarning: 'Redis unavailable: {error}. Live data falls back to DB queries.',
    redisFallbackError: 'Cache service connection failed',`

for (const loc of ['zh-TW', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA']) {
  const path = join(ROOT, loc, 'dashboard.ts')
  let c = readFileSync(path, 'utf8')

  // strip duplicate Chinese tail from line "  tabs:" near end through closing
  c = c.replace(/\n  tabs: \{\n    liveStream: "实时请求流"[\s\S]*$/, '\n}')

  if (!c.includes('selfcheck:')) {
    const tabs = localeTabs[loc] || tabsBlock
    const v2 = localeV2[loc] || v2Block
    c = c.replace(/(refresh: [^\n]+\n)/, `$1${tabs}\n${v2}\n`)
  }

  if (!c.includes('emptyWaiting:')) {
    const extra = loc === 'zh-TW' ? liveExtraEn.replace(/Waiting/g, '等待').replace(/By vendor/g, '按原廠') : 
                  loc === 'ja-JP' ? liveExtraEn.replace(/Waiting for/g, '待機中').replace(/By vendor/g, 'ベンダー別') :
                  liveExtraEn
    c = c.replace(/(empty: [^\n]+)(\n)(  \},)/, (_, emptyLine, nl, close) => {
      const line = emptyLine.endsWith(',') ? emptyLine : `${emptyLine},`
      return `${line}${nl}${extra}${nl}${close}`
    })
  }

  writeFileSync(path, c, 'utf8')
  console.log('ok', loc)
}
