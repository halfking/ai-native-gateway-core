// add-missing-baseline-keys.mjs — 一次性脚本：补齐 **基线缺陷** 键（2026-11-23 审计轮）。
//
// ★ 这两个键不是本轮引入的，是已推 main 的并行会话提交（ba7b54c4a「会话分析页面加载状态优化」
//   与 RequestLogsView 的 scanningFailed 用法）留下的 i18n 缺口 —— 它们让
//   localeKeyParity 门禁在 origin/main 上就红，挡住任何部署。
//   ⇒ 本轮补齐，让门禁恢复绿。
//
// 键：
//   sessions.stats.loading              — SessionStatsPanel 加载覆盖层
//   requests.list.error.scanningFailed  — RequestLogsView 的「查到 N 条但显示不出来」
//     占位符：{count}
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const LOCALES_DIR = 'src/locales'

const STATS_LOADING = {
  'zh-CN': '正在加载会话统计…',
  'zh-TW': '正在載入會話統計…',
  'en-US': 'Loading session statistics…',
  'ja-JP': 'セッション統計を読み込み中…',
  'de-DE': 'Sitzungsstatistik wird geladen…',
  'es-ES': 'Cargando estadísticas de sesión…',
  'fr-FR': 'Chargement des statistiques de session…',
  'ar-SA': 'جارٍ تحميل إحصاءات الجلسة…',
}

const SCANNING_FAILED = {
  'zh-CN': '数据加载异常：查询到 {count} 条记录，但无法正常显示。这通常是服务端数据格式问题，请联系管理员检查后端日志。',
  'zh-TW': '資料載入異常：查詢到 {count} 筆記錄，但無法正常顯示。這通常是伺服器端資料格式問題，請聯絡管理員檢查後端記錄。',
  'en-US': 'Data load anomaly: {count} records were returned but cannot be displayed. This is usually a server-side data format issue; ask an administrator to check the backend logs.',
  'ja-JP': 'データ読み込み異常：{count} 件のレコードが取得されましたが表示できません。サーバ側のデータ形式の問題が疑われるため、管理者にバックエンドログの確認を依頼してください。',
  'de-DE': 'Ladefehler: {count} Datensätze wurden abgerufen, können aber nicht angezeigt werden. Ursache ist meist ein serverseitiges Datenformatproblem; bitte einen Administrator, die Backend-Logs zu prüfen.',
  'es-ES': 'Anomalía al cargar datos: se consultaron {count} registros pero no pueden mostrarse. Suele ser un problema de formato de datos en el servidor; pide a un administrador que revise los registros del backend.',
  'fr-FR': 'Anomalie de chargement : {count} enregistrements ont été récupérés mais ne peuvent pas être affichés. Il s’agit généralement d’un problème de format de données côté serveur ; demandez à un administrateur de vérifier les journaux du backend.',
  'ar-SA': 'خلل في تحميل البيانات: تم استرجاع {count} سجل لكن تعذّر عرضها. غالبًا مشكلة في صيغة البيانات على الخادم؛ راجع سجلات الخادم مع مسؤول.',
}

for (const locale of Object.keys(STATS_LOADING)) {
  // ── 1) sessions.stats.loading ──
  // ★ 检测必须限定在 stats 块内：文件里别的块（如 loadingList）也有 `loading:`，
  //   宽松的 includes() 会误判成「已存在」⇒ 该补的语种被跳过。
  const sFile = join(LOCALES_DIR, locale, 'sessions.ts')
  let sSrc = readFileSync(sFile, 'utf8')
  const statsBlock = sSrc.match(/^  stats: \{([\s\S]*?)^  \},/m)
  if (!statsBlock) throw new Error(`${locale}: sessions.stats 锚点未找到`)
  if (!/(^|\n)    loading:/.test(statsBlock[1])) {
    const anchor = /(^  stats: \{\n)/m
    sSrc = sSrc.replace(
      anchor,
      (full) => `${full}    loading: ${JSON.stringify(STATS_LOADING[locale])},\n`,
    )
    writeFileSync(sFile, sSrc, 'utf8')
    console.log(`sessions.stats.loading  -> ${locale}`)
  } else {
    console.log(`skip sessions.stats.loading (exists): ${locale}`)
  }

  // ── 2) requests.list.error.scanningFailed ──
  const rFile = join(LOCALES_DIR, locale, 'requests.ts')
  let rSrc = readFileSync(rFile, 'utf8')
  if (!rSrc.includes('scanningFailed')) {
    // 在 list 块内插入 error 子块（list: { ... } 的直接子级）。
    // 锚点：list 块内第一个顶层子键（缩进 4 空格）。
    const listRe = /^  list: \{\n([\s\S]*?)^  \},\n/m
    const m = rSrc.match(listRe)
    if (!m) throw new Error(`${locale}: requests.list 锚点未找到`)
    const body = m[1]
    const subAnchor = body.match(/^ {4}\w+: \{/m)
    if (!subAnchor) throw new Error(`${locale}: requests.list 内无子键锚点`)
    const insertAt = subAnchor.index
    const errBlock =
      `    error: {\n` +
      `      scanningFailed: ${JSON.stringify(SCANNING_FAILED[locale])},\n` +
      `    },\n`
    const newBody = body.slice(0, insertAt) + errBlock + body.slice(insertAt)
    rSrc = rSrc.replace(listRe, `  list: {\n${newBody}  },\n`)
    writeFileSync(rFile, rSrc, 'utf8')
    console.log(`requests.list.error.scanningFailed -> ${locale}`)
  } else {
    console.log(`skip requests.list.error.scanningFailed (exists): ${locale}`)
  }
}
