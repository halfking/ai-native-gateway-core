// add-top5-keys.mjs — 一次性脚本：为 8 语种 sessionAnalytics locale 补齐
// 2026-11-23 审计轮新增的 Top5 四件套文案键。
//
// 用法：node scripts/add-top5-keys.mjs   （跑完即删）
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const LOCALES_DIR = 'src/locales'

/** top5 区块文案（含 scopeNote 口径披露） */
const TOP5 = {
  'zh-CN': {
    title: 'Top5 排行（模型 / 供应商 / 用户 / 任务）',
    scopeNote: '口径与本页上方统计一致：模型与供应商为请求级（按请求聚合），用户与任务为会话级（按会话聚合），两组不可相加。',
    models: 'Top5 模型',
    providers: 'Top5 供应商',
    users: 'Top5 用户',
    tasks: 'Top5 任务',
  },
  'zh-TW': {
    title: 'Top5 排行（模型 / 供應商 / 用戶 / 任務）',
    scopeNote: '口徑與本頁上方統計一致：模型與供應商為請求級（按請求聚合），用戶與任務為會話級（按會話聚合），兩組不可相加。',
    models: 'Top5 模型',
    providers: 'Top5 供應商',
    users: 'Top5 用戶',
    tasks: 'Top5 任務',
  },
  'en-US': {
    title: 'Top 5 rankings (models / providers / users / tasks)',
    scopeNote: 'Same basis as the statistics above: models and providers are request-level (aggregated by request), users and tasks are session-level (aggregated by session); the two groups must not be added together.',
    models: 'Top 5 models',
    providers: 'Top 5 providers',
    users: 'Top 5 users',
    tasks: 'Top 5 tasks',
  },
  'ja-JP': {
    title: 'Top5 ランキング（モデル / プロバイダー / ユーザー / タスク）',
    scopeNote: '上部の統計と同じ基準です：モデルとプロバイダーはリクエスト単位（リクエスト集計）、ユーザーとタスクはセッション単位（セッション集計）。2 つのグループは合算できません。',
    models: 'Top5 モデル',
    providers: 'Top5 プロバイダー',
    users: 'Top5 ユーザー',
    tasks: 'Top5 タスク',
  },
  'de-DE': {
    title: 'Top-5-Ranglisten (Modelle / Anbieter / Benutzer / Aufgaben)',
    scopeNote: 'Gleiche Basis wie die Statistik oben: Modelle und Anbieter sind auf Anfrage-Ebene (nach Anfragen aggregiert), Benutzer und Aufgaben auf Sitzungs-Ebene (nach Sitzungen aggregiert); die beiden Gruppen dürfen nicht addiert werden.',
    models: 'Top 5 Modelle',
    providers: 'Top 5 Anbieter',
    users: 'Top 5 Benutzer',
    tasks: 'Top 5 Aufgaben',
  },
  'es-ES': {
    title: 'Clasificaciones Top 5 (modelos / proveedores / usuarios / tareas)',
    scopeNote: 'Misma base que las estadísticas anteriores: modelos y proveedores son a nivel de solicitud (agregados por solicitud), usuarios y tareas a nivel de sesión (agregados por sesión); ambos grupos no deben sumarse.',
    models: 'Top 5 modelos',
    providers: 'Top 5 proveedores',
    users: 'Top 5 usuarios',
    tasks: 'Top 5 tareas',
  },
  'fr-FR': {
    title: 'Classements Top 5 (modèles / fournisseurs / utilisateurs / tâches)',
    scopeNote: "Même base que les statistiques ci-dessus : les modèles et les fournisseurs sont au niveau requête (agrégés par requête), les utilisateurs et les tâches au niveau session (agrégés par session) ; les deux groupes ne doivent pas être additionnés.",
    models: 'Top 5 modèles',
    providers: 'Top 5 fournisseurs',
    users: 'Top 5 utilisateurs',
    tasks: 'Top 5 tâches',
  },
  'ar-SA': {
    title: 'أفضل 5 (النماذج / المزوّدون / المستخدمون / المهام)',
    scopeNote: 'الأساس نفسه كما في الإحصاءات أعلاه: النماذج والمزوّدون على مستوى الطلب (مجمّعة حسب الطلب)، والمستخدمون والمهام على مستوى الجلسة (مجمّعة حسب الجلسة)؛ لا يجوز جمع المجموعتين معًا.',
    models: 'أفضل 5 نماذج',
    providers: 'أفضل 5 مزوّدين',
    users: 'أفضل 5 مستخدمين',
    tasks: 'أفضل 5 مهام',
  },
}

/** action.viewAllModels */
const VIEW_ALL_MODELS = {
  'zh-CN': '查看所有模型',
  'zh-TW': '查看所有模型',
  'en-US': 'View all models',
  'ja-JP': 'すべてのモデルを表示',
  'de-DE': 'Alle Modelle anzeigen',
  'es-ES': 'Ver todos los modelos',
  'fr-FR': 'Voir tous les modèles',
  'ar-SA': 'عرض جميع النماذج',
}

/** col.models（列名，与已有的 col.models「模型用量」语义不同 ⇒ 用 col.modelsCount 避免撞义） */
const COL_MODELS_COUNT = {
  'zh-CN': '模型数',
  'zh-TW': '模型數',
  'en-US': 'Models',
  'ja-JP': 'モデル数',
  'de-DE': 'Modelle',
  'es-ES': 'Modelos',
  'fr-FR': 'Modèles',
  'ar-SA': 'عدد النماذج',
}

for (const [locale, texts] of Object.entries(TOP5)) {
  const file = join(LOCALES_DIR, locale, 'sessionAnalytics.ts')
  let src = readFileSync(file, 'utf8')

  if (src.includes('top5: {')) {
    console.log(`skip (already patched): ${locale}`)
    continue
  }

  // 1) 插入 top5 区块 —— 放在 hint 区块之后（hint: {...}, 后面紧跟 \n\n）
  const hintAnchor = /\n(\s*)hint: \{([\s\S]*?)\n\s*\},\n/
  const m = src.match(hintAnchor)
  if (!m) throw new Error(`${locale}: hint 区块锚点未找到`)
  const indent = m[1]
  const block =
    `\n${indent}top5: {\n` +
    `  ${indent}title: ${JSON.stringify(texts.title)},\n` +
    `  ${indent}scopeNote: ${JSON.stringify(texts.scopeNote)},\n` +
    `  ${indent}models: ${JSON.stringify(texts.models)},\n` +
    `  ${indent}providers: ${JSON.stringify(texts.providers)},\n` +
    `  ${indent}users: ${JSON.stringify(texts.users)},\n` +
    `  ${indent}tasks: ${JSON.stringify(texts.tasks)},\n` +
    `${indent}},\n`
  src = src.replace(hintAnchor, (full, ind) => `${full}${block}`)

  // 2) 插入 action.viewAllModels —— 紧跟 viewAllUsers 行
  const vauRe = /(\n(\s*)viewAllUsers: '[^']*',\n)/
  if (!vauRe.test(src)) throw new Error(`${locale}: viewAllUsers 锚点未找到`)
  src = src.replace(
    vauRe,
    (full, _g1, ind) => `${full}${ind}viewAllModels: ${JSON.stringify(VIEW_ALL_MODELS[locale])},\n`,
  )

  // 3) 插入 col.modelsCount —— 紧跟 col.requests 行
  const reqRe = /(\n(\s*)requests: '[^']*',\n)/
  if (!reqRe.test(src)) throw new Error(`${locale}: col.requests 锚点未找到`)
  src = src.replace(
    reqRe,
    (full, _g1, ind) => `${full}${ind}modelsCount: ${JSON.stringify(COL_MODELS_COUNT[locale])},\n`,
  )

  writeFileSync(file, src, 'utf8')
  console.log(`patched: ${locale}`)
}
