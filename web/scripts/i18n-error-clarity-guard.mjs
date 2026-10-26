#!/usr/bin/env node
// i18n-error-clarity-guard.mjs — 「错误陈述必须给出恢复动作」门（跨 `t()` 边界）
// （2026-10-27 第 15 增量 · L1 错误三问轮）
//
// 用法：
//   node scripts/i18n-error-clarity-guard.mjs            # 检查；违规 ⇒ exit 1
//   node scripts/i18n-error-clarity-guard.mjs --json     # 输出 JSON 报告
//   node scripts/i18n-error-clarity-guard.mjs --selftest # 判据自证
//   node scripts/i18n-error-clarity-guard.mjs --list     # 逐条列出登记槽位与当前值
//
// ─────────────────────────────────────────────────────────────────────────────
// ★ 为什么要有这道门（**这道门测的是 impeccable 要的东西**）
//
//   impeccable `skill/reference/clarify.md` 原文：
//     "An actionable error answers:
//        1. what failed;
//        2. why, when known and useful;
//        3. how to recover or what alternative remains."
//
// ★ 本仓**已经有一道**源码侧的 R1 门（`src/i18n/copyClarity.ts`）：
//   它判「错误位置的字面量必须给出恢复动作」。
//   ★★ 但它**只判字面量** ⇒ `error.value = t('keys.loadFailed')` 这种
//   **值来自语言包**的写法**它一个字都看不到**
//   ⇒ 实测：语言包侧 145 条错误文案是「加载失败」「保存失败」这种**裸失败短语**
//      —— 既没点名主体，也没有恢复动作 ⇒ R1 门**全绿**，而界面在混同。
//   ⇒ ★★ 这就是「**门测的东西必须逐字对照要的东西**」：
//      门测的是「字面量」，要的是「用户看到的文案」。
//
// ★ 本门把 R1 的判据**跨过 `t()` 边界**：
//   ① 位置信号 = **源码侧**的错误位置（赋值目标 / 报错调用 / 兜底形状 / throw）
//      ⇒ 复用 `src/i18n/errorPositions.ts`（与 R1 门**同一份实现**，不另写一份）
//   ② 判据 = 对**语言包的值**用同一把尺子（恢复动作词表 + 失败信号词表）
//
// ★ 为什么位置信号**不用键路径**（第 12 增量的死路）：
//   键路径分不出「错误陈述」与「计数摘要」
//   （`providerDetail.models.refreshSummaryFailed` = "失败 {n}" 是计数，不是错误陈述）
//   ⇒ 源码上下文分得出 ⇒ 位置信号搬到源码侧。
//
// ─────────────────────────────────────────────────────────────────────────────
// ★ 判据（6 条，全部可证伪）
//
//   ① L1a 裸失败短语必须登记：
//      源码侧错误位置上的 `t('key')`，若其**语言包值**是裸失败短语
//      （含失败信号 ∧ 无原因槽 ∧ 无「不含失败信号的分句」里的恢复动作）
//      ⇒ 该键必须在登记簿里（slots 或 exemptions）⇒ **漏网即红**。
//
//   ② L1b 已登记的错误陈述位不许退回裸失败短语（8 语种逐语种判）。
//
//   ③ L1c 豁免项必须**仍然**落在它的豁免类别里
//      （`runtime-reason` / `count-summary` / `info-missing`）
//      ⇒ 豁免不能被用来藏违规（反向自证）。
//
//   ④ L1d 登记键 8 语种齐备。
//
//   ⑤ L1e 同语种内登记键的**值互不相同**（同一句话不许两份）
//      ⇒ 这条直接治「24 个页面共用一句『加载失败』」。
//
//   ⑥ L1f 登记键的值必须含**恢复动作**（分句口径）—— 无条件。
//
// ★ 「恢复动作必须在不含失败信号的分句里」：
//   「加载**选择**策略失败」里的「选择」是**主体的一部分**（选择策略），不是恢复动作
//   ⇒ 用整句词表匹配会**漏判整族**（第 15 增量实测：修正后多抓到 3 条）。
//   ⇒ 与 R1 的同款陷阱同源：「恢复词不能是失败的那个动作」。

import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(fileURLToPath(import.meta.url), '../..')
const REGISTRY_FILE = join(ROOT, 'scripts', 'i18n-error-clarity-registry.json')
const LOCALES = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW']

function readRegistry(file = REGISTRY_FILE) {
  return JSON.parse(readFileSync(file, 'utf8'))
}

// ───────────────────────── 词表（8 语种） ─────────────────────────

/** 失败信号：值在陈述「一次失败」。 */
export const FAIL_SIGNAL = {
  'zh-CN': /(失败|错误|异常|不可用|无法|未能|没能|中断|被拒|未找到|不支持|失效|不允许)/,
  'zh-TW': /(失敗|錯誤|異常|不可用|無法|未能|沒能|中斷|被拒|找不到|未找到|不支援|失效|不允許)/,
  'ja-JP': /(失敗|エラー|例外|利用できません|できません|出来ませんでした|中断|拒否|見つかりません|未対応|サポートされていません|無効|不許可)/,
  'en-US': /(failed|failure|error|exception|unavailable|could not|couldn't|cannot|can't|unable to|denied|rejected|not found|unsupported|invalid|interrupted|broken)/i,
  'de-DE': /(fehlgeschlagen|fehlschlag|fehler|exception|nicht verfügbar|konnte nicht|können nicht|unmöglich|verweigert|nicht gefunden|nicht unterstützt|ungültig|abgebrochen)/i,
  'es-ES': /(falló|fallido|fallo|error|excepción|no disponible|no se pudo|no se ha podido|imposible|denegado|no encontrado|no compatible|no válido|interrumpido)/i,
  'fr-FR': /(échoué|échec|erreur|exception|indisponible|n'a pas pu|ne peut pas|impossible|refusé|introuvable|non pris en charge|non valide|interrompu)/i,
  'ar-SA': /(فشل|فشلت|خطأ|استثناء|غير متاح|تعذّر|تعذر|تعذرت|مرفوض|غير موجود|غير مدعوم|غير صالح|مقاطع)/,
}

/** 恢复动作：用户「下一步怎么办」。 */
export const RECOVER_WORD = {
  'zh-CN': /(重试|重新|检查|请|前往|联系|刷新|调整|确认|稍后|启用|更新|查看|恢复|补充|指定|设置|改用|登录|填|解决|修复|排查|降级|清理|打开|选择|切换|返回|申请|等待|重置|断开|重连|升级|回滚|重载|复制|粘贴|输入|填写|放宽|再次|允许|支持|改为|换成|另用)/,
  'zh-TW': /(重試|重新|檢查|請|前往|聯絡|聯繫|更新|檢視|恢復|補充|指定|設定|改用|登入|解決|修復|排查|降級|清理|開啟|選擇|切換|返回|申請|等待|重設|斷開|重連|升級|回滾|重載|複製|貼上|輸入|填寫|放寬|再次|允許|支援|改為|確認|調整|啟用|填|換成)/,
  'ja-JP': /(再試行|やり直|確認|チェック|してください|再度|再実行|再読み込み|再保存|再削除|変更|修正|対処|問い合わせ|連絡|調整|絞り込|解除|参照|設定|有効|改善|復旧|復元|再接続|再起動|再インストール)/,
  'en-US': /(retry|try again|check|please|adjust|change|select|choose|reload|refresh|contact|verify|sign in|log in|again|reconnect|restart|switch|enable|disable|fix|update|restore|reopen|redownload|request|instead)/i,
  'de-DE': /(erneut|bitte|prüfen|prüfe|anpassen|ändern|auswählen|kontaktieren|neu laden|neu starten|reparieren|korrigieren|beheben|herstellen|wiederherstellen|aktivieren|deaktivieren|stattdessen)/i,
  'es-ES': /(reintent|vuelva a intentarlo|inténtelo|compruebe|comprueba|por favor|ajuste|cambie|seleccione|elija|contacte|vuelva a cargar|recargue|reinicie|repare|corrija|habilite|deshabilite|restablezca|restablecer|en su lugar|usa)/i,
  'fr-FR': /(réessayez|réessayer|veuillez|vérifiez|vérifier|ajuste|modifiez|sélectionnez|choisissez|contactez|rechargez|redémarrez|réparez|corrigez|activez|désactivez|rétablissez|demandez|à la place|utilisez)/i,
  'ar-SA': /(أعد المحاولة|إعادة المحاولة|أعد|إعادة|يرجى|تحقق|تحقّق|اضبط|عدّل|اختر|اختار|تواصل|أعد التحميل|أعد التشغيل|أصلح|صحّح|فعّل|عطّل|استعد|راجع)/,
}

/** 原因槽：具体原因在运行时由代码填进来。 */
export const REASON_SLOT = /\{(reason|msg|message|error|err|detail|details|kind|code)\}/i

/** 分句：中文/西/法/阿用逗号句号；日文用「、。」。 */
// ★ 必须含 ASCII 句点：英文/德文/西文/法文的句子边界是 '.'
//   （漏了它 ⇒ "The list could not be loaded. Please retry." 整句被当一个分句
//     ⇒ 「could not」把整句标成失败句 ⇒ 后半句的 "Please retry" 看不见 ⇒ **整族漏判**）
const CLAUSE_SPLIT = /[，,。;；：:！!？?、.]/

export function splitClauses(value) {
  return String(value).split(CLAUSE_SPLIT).map((c) => c.trim()).filter(Boolean)
}

/**
 * 这个值是不是「裸失败短语」。
 *
 * 裸失败短语 = 含失败信号 ∧ 无原因槽 ∧ **没有**「不含失败信号的分句」里的恢复动作。
 */
export function isBareFailurePhrase(value, locale) {
  const fail = FAIL_SIGNAL[locale]
  const rec = RECOVER_WORD[locale]
  if (!fail || !rec) return false
  const v = String(value)
  if (!fail.test(v)) return false
  if (REASON_SLOT.test(v)) return false
  const clauses = splitClauses(v)
  const hasRecovery = clauses.some((c) => !fail.test(c) && rec.test(c))
  return !hasRecovery
}

/** 值里有没有恢复动作（无条件口径，不要求它同时含失败信号）。 */
export function hasRecoveryAction(value, locale) {
  const fail = FAIL_SIGNAL[locale]
  const rec = RECOVER_WORD[locale]
  if (!fail || !rec) return false
  const clauses = splitClauses(String(value))
  return clauses.some((c) => !fail.test(c) && rec.test(c))
}

/** 「信息缺失」形态（逐语种）—— 豁免 `info-missing` 的自洽判据。 */
export const INFO_MISSING = {
  'zh-CN': [/没拿到/, /没有详情/, /无法获取详情/],
  'zh-TW': [/沒取得/, /沒有詳情/, /無法取得詳情/],
  'ja-JP': [/取得できません/, /取得できなかった/, /得られませ/],
  'en-US': [/no .{0,20}(error )?details received/i, /could not (obtain|get)/i, /unable to (obtain|get)/i],
  'de-DE': [/keine .{0,20}fehlerdetails/i, /konnte .{0,20}nicht (erhalten|abrufen)/i],
  'es-ES': [/no se obtuvo/i, /no se pudo obtener/i],
  'fr-FR': [/impossible d.?obtenir/i, /aucun d.tail? d.erreur/i],
  'ar-SA': [/لم يتم الحصول/, /تعذّر الحصول/],
}

/** 豁免类别的自洽判据（`runtime-reason` 需要源码事实）。 */
export function exemptionStillHolds(category, value, composed, locale = 'zh-CN') {
  const v = String(value)
  switch (category) {
    case 'runtime-reason':
      // 值本身以冒号结尾（原因跟在后面），**或**调用点在运行时拼了别的东西。
      return /[:：]\s*$/.test(v) || (composed && composed.length > 0)
    case 'count-summary':
      return /成功.*失败|\{ok\}|\{failed\}|\{skipped\}/.test(v)
    case 'info-missing':
      // ★ 必须**逐语种**：第一版只写中文形态 ⇒ de-DE/en-US/es-ES/fr-FR/ja-JP/ar-SA
      //   的同一条豁免被误判成「豁免不成立」（实测 7 条假阳性）。
      return (INFO_MISSING[locale] ?? INFO_MISSING['zh-CN']).some((re) => re.test(v))
    default:
      return false
  }
}

// ───────────────────────── 判据（纯函数） ─────────────────────────

/**
 * @param {object} facts
 * @param {Map<string,string[]>} facts.candidateKeys  源码侧错误位置上的 t() 键 → 调用点
 * @param {Map<string,Map<string,string>>} facts.perLocaleValues 各语种值表
 * @param {Map<string,Set<string>>} facts.perLocaleKeys    各语种键集合
 * @param {Map<string,string[]>} facts.composedKeys      运行时拼了别的内容的 t() 键 → 调用点
 * @param {object} facts.registry                        登记簿
 */
export function checkErrorClarity(facts) {
  const { candidateKeys, perLocaleValues, perLocaleKeys, composedKeys, registry } = facts
  const violations = []
  const slots = registry.slots || {}
  const exemptions = registry.exemptions || {}
  const registered = new Set([...Object.keys(slots), ...Object.keys(exemptions)])

  // ① L1a：裸失败短语必须登记
  //
  // ★★ 触发只用**源语种（zh-CN）** —— 这是本轮修掉的一个「量具指错对象」：
  //   第一版在**8 个语种**上各判一遍 ⇒ 1938 条假阳性：
  //     · 成功语被当成错误陈述（`approval.list.success.rejected` = "Request rejected"）
  //     · 确认提示被当成错误陈述（"…This cannot be undone." 里含 "cannot"）
  //     · 取消通知被当成错误陈述（"Abgebrochen (…)"）
  //   ⇒ 根因：**词表判据在「不是错误陈述」的值上照样会命中**。
  //   ⇒ 改法：只用源语种判「这是不是裸失败短语」（单一判据、单一语种 ⇒ 无跨语种噪声），
  //     跨语种的措辞一致性交给 ②/⑥（只跑**已登记**的槽位 ⇒ 精确）。
  const srcTable = perLocaleValues.get('zh-CN')
  for (const [key, sites] of candidateKeys) {
    if (registered.has(key)) continue
    if (!srcTable) continue
    const v = srcTable[key]
    if (v === undefined) continue
    if (!isBareFailurePhrase(v, 'zh-CN')) continue
    violations.push({
      rule: 'L1a-unregistered-bare',
      key,
      locale: 'zh-CN',
      value: v,
      reason: '错误位置上的语言包值是裸失败短语，但该键没有登记',
      sites: sites.slice(0, 3),
    })
  }

  // ② L1b：已登记的错误陈述位不许退回裸失败短语
  for (const [key] of Object.entries(slots)) {
    for (const loc of LOCALES) {
      const table = perLocaleValues.get(loc)
      if (!table) continue
      const v = table[key]
      if (v === undefined) continue
      if (isBareFailurePhrase(v, loc)) {
        violations.push({
          rule: 'L1b-regressed',
          key,
          locale: loc,
          value: v,
          reason: '登记的错误陈述位退回成了裸失败短语（缺恢复动作）',
        })
      }
    }
  }

  // ③ L1c：豁免项必须仍然落在它的豁免类别里
  for (const [key, ex] of Object.entries(exemptions)) {
    const composed = composedKeys ? composedKeys.get(key) : undefined
    for (const loc of LOCALES) {
      const table = perLocaleValues.get(loc)
      if (!table) continue
      const v = table[key]
      if (v === undefined) continue
      if (!exemptionStillHolds(ex.category, v, composed, loc)) {
        violations.push({
          rule: 'L1c-exemption-misused',
          key,
          locale: loc,
          value: v,
          reason: `豁免类别「${ex.category}」不再成立 ⇒ 豁免被用来藏违规`,
        })
      }
    }
  }

  // ④ L1d：登记键 8 语种齐备
  for (const [key] of Object.entries(slots)) {
    for (const loc of LOCALES) {
      const keys = perLocaleKeys.get(loc)
      if (!keys) continue
      if (!keys.has(key)) {
        violations.push({
          rule: 'L1d-missing-locale',
          key,
          locale: loc,
          reason: '登记的错误陈述位在该语种不存在',
        })
      }
    }
  }

  // ⑤ L1e：同语种内登记键的值互不相同（同一句话不许两份）
  for (const loc of LOCALES) {
    const table = perLocaleValues.get(loc)
    if (!table) continue
    const byValue = new Map()
    for (const key of Object.keys(slots)) {
      const v = table[key]
      if (v === undefined) continue
      if (!byValue.has(v)) byValue.set(v, [])
      byValue.get(v).push(key)
    }
    for (const [value, keys] of byValue) {
      if (keys.length > 1) {
        violations.push({
          rule: 'L1e-duplicate-value',
          key: keys[0],
          locale: loc,
          value,
          reason: `同一语种内 ${keys.length} 个错误陈述位共用同一句文案`,
          related: keys,
        })
      }
    }
  }

  // ⑥ L1f：登记键的值必须含恢复动作（无条件）
  for (const [key] of Object.entries(slots)) {
    for (const loc of LOCALES) {
      const table = perLocaleValues.get(loc)
      if (!table) continue
      const v = table[key]
      if (v === undefined) continue
      if (!hasRecoveryAction(v, loc)) {
        violations.push({
          rule: 'L1f-no-recovery',
          key,
          locale: loc,
          value: v,
          reason: '登记的错误陈述位没有给出恢复动作',
        })
      }
    }
  }

  return violations
}

// ───────────────────────── 事实源 ─────────────────────────

export async function loadFacts() {
  const quality = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'localeValueQuality.ts')).href)
  const positions = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'errorPositions.ts')).href)
  const clarity = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'copyClarity.ts')).href)
  const registry = readRegistry()
  const perLocaleKeys = quality.allLocaleKeySets(join(ROOT, 'src', 'locales'))
  const perLocaleValues = quality.allLocaleValues(join(ROOT, 'src', 'locales'))
  const files = clarity.uiFiles()
  const candidateKeys = positions.errorSlotLocaleKeys(files)
  const composedKeys = positions.composedLocaleKeyCalls(files)
  return { candidateKeys, perLocaleKeys, perLocaleValues, composedKeys, registry }
}

// ───────────────────────── 自证 ─────────────────────────

async function selftest() {
  const cases = []
  const check = (id, desc, expected, actual) => {
    cases.push({ id, desc, expected, actual, ok: JSON.stringify(expected) === JSON.stringify(actual) })
  }

  /**
   * 造一份 **8 语种齐全**的干净事实源。
   *
   * ★ 教训（第 11 增量 §418 / 第 15 增量复现）：
   *   夹具本身若有缺口，期望值就会写成「一堆不相干的违规」⇒ 断言变成「恒非空」。
   *   ★ 本轮的具体坑：**用同一个中文值喂 8 个语种** ⇒
   *     en-US/de-DE 的词表不认识中文 ⇒ 那些语种「不判红」⇒ 期望 8 条实得 2 条。
   *   ⇒ 夹具必须是**逐语种**的。
   */
  const OK_VALUES = {
    'zh-CN': 'Alpha 列表没能加载出来，请重试',
    'zh-TW': 'Alpha 列表沒能載入出來，請重試',
    'ja-JP': 'Alpha リストを読み込めませんでした。再試行してください。',
    'en-US': 'The Alpha list could not be loaded. Please retry.',
    'de-DE': 'Die Alpha-Liste konnte nicht geladen werden. Bitte erneut versuchen.',
    'es-ES': 'No se pudo cargar la lista Alpha. Vuelva a intentarlo.',
    'fr-FR': 'Le chargement de la liste Alpha a échoué. Veuillez réessayer.',
    'ar-SA': 'تعذّر تحميل قائمة Alpha. أعد المحاولة.',
  }
  const BARE_VALUES = {
    'zh-CN': '加载失败',
    'zh-TW': '載入失敗',
    'ja-JP': '読み込みに失敗しました。',
    'en-US': 'Load failed.',
    'de-DE': 'Laden fehlgeschlagen.',
    'es-ES': 'Error de carga.',
    'fr-FR': 'Échec du chargement.',
    'ar-SA': 'فشل التحميل.',
  }
  /** 有失败信号、但**没有**恢复动作（⇒ 触发 L1f，不触发 L1b）。 */
  const NO_RECOVERY_VALUES = {
    'zh-CN': '数据为空',
    'zh-TW': '資料為空',
    'ja-JP': 'データがありません。',
    'en-US': 'The data is empty.',
    'de-DE': 'Die Daten sind leer.',
    'es-ES': 'Los datos están vacíos.',
    'fr-FR': 'Les données sont vides.',
    'ar-SA': 'البيانات فارغة.',
  }
  const COUNT_SUMMARY = {
    'zh-CN': '批量探活完成：成功 {ok} / 失败 {failed}',
    'zh-TW': '批次探活完成：成功 {ok} / 失敗 {failed}',
    'ja-JP': '一括プローブ完了：成功 {ok} / 失敗 {failed}',
    'en-US': 'Batch probe done: {ok} ok / {failed} failed.',
    'de-DE': 'Sammelprüfung fertig: {ok} ok / {failed} fehlgeschlagen.',
    'es-ES': 'Sonda por lotes completada: {ok} correctos / {failed} fallidos.',
    'fr-FR': 'Sondegroupé terminée : {ok} ok / {failed} échecs.',
    'ar-SA': 'اكتمل الفحص الجماعي: {ok} ناجح / {failed} فاشل.',
  }
  const mkFacts = (over = {}) => {
    const perLocaleKeys = new Map()
    const perLocaleValues = new Map()
    for (const loc of LOCALES) {
      perLocaleKeys.set(loc, new Set(['ns.a.loadFailed', 'ns.b.saveFailed', 'ns.c.countSummary']))
      perLocaleValues.set(loc, {
        'ns.a.loadFailed': OK_VALUES[loc],
        'ns.b.saveFailed': OK_VALUES[loc] + ' (B)',
        'ns.c.countSummary': COUNT_SUMMARY[loc],
      })
    }
    return {
      registry: {
        kindVocabulary: ['load', 'save'],
        slots: {
          'ns.a.loadFailed': { kind: 'load', subject: 'Alpha', new: 'x' },
          'ns.b.saveFailed': { kind: 'save', subject: 'Beta', new: 'y' },
        },
        exemptions: {
          'ns.c.countSummary': { category: 'count-summary', reason: '计数项名，不是错误陈述' },
        },
      },
      candidateKeys: new Map([
        ['ns.a.loadFailed', ['a.vue:1']],
        ['ns.b.saveFailed', ['b.vue:1']],
        ['ns.c.countSummary', ['c.vue:1']],
      ]),
      composedKeys: new Map(),
      perLocaleKeys,
      perLocaleValues,
      ...over,
    }
  }
  const rulesOf = (v) => v.map((x) => x.rule).sort()
  const countOf = (v, rule) => v.filter((x) => x.rule === rule).length

  // 纯函数层：isBareFailurePhrase
  check('S1', '裸失败短语（加载失败）⇒ 是', true, isBareFailurePhrase('加载失败', 'zh-CN'))
  check('S2', '有恢复动作 ⇒ 不是', false, isBareFailurePhrase('加载失败，请重试', 'zh-CN'))
  check('S3', '有原因槽 ⇒ 不是', false, isBareFailurePhrase('保存失败：{reason}。请重试', 'zh-CN'))
  check('S4', '★ 恢复词在含失败信号的分句里（主体的一部分）⇒ 仍是裸失败短语', true,
    isBareFailurePhrase('加载选择策略失败', 'zh-CN'))
  check('S5', '没有失败信号的文案 ⇒ 不是', false, isBareFailurePhrase('暂无数据', 'zh-CN'))
  check('S6', 'en-US：bare ⇒ 是', true, isBareFailurePhrase('Load failed', 'en-US'))
  check('S7', 'en-US：有恢复动作 ⇒ 不是', false, isBareFailurePhrase('The list could not be loaded. Please retry.', 'en-US'))
  check('S8', 'de-DE：有恢复动作 ⇒ 不是', false,
    isBareFailurePhrase('Die Anbieterliste konnte nicht geladen werden. Bitte erneut versuchen.', 'de-DE'))

  // 纯函数层：hasRecoveryAction
  check('S9', '恢复动作检测（分句口径）', true, hasRecoveryAction('加载失败，请重试', 'zh-CN'))
  check('S10', '恢复动作检测：主体里的动词不算', false, hasRecoveryAction('加载选择策略失败', 'zh-CN'))

  // 判据层：干净事实源 ⇒ 全绿
  const clean = checkErrorClarity(mkFacts())
  check('S11', '干净事实源 ⇒ 零违规', [], rulesOf(clean))

  // 判据层：L1a 未登记的裸失败短语 ⇒ 红
  {
    const facts = mkFacts()
    facts.candidateKeys.set('ns.d.rogue', ['d.vue:1'])
    for (const [loc, table] of facts.perLocaleValues) table['ns.d.rogue'] = BARE_VALUES[loc]
    const v = checkErrorClarity(facts)
    check('S12', 'L1a：未登记的裸失败短语 ⇒ 红 1 条', 1, countOf(v, 'L1a-unregistered-bare'))
  }

  // 判据层：L1b 已登记位退回裸失败短语 ⇒ 红
  {
    const facts = mkFacts()
    for (const [loc, table] of facts.perLocaleValues) table['ns.a.loadFailed'] = BARE_VALUES[loc]
    const v = checkErrorClarity(facts)
    check('S13', 'L1b：登记位退回裸失败短语 ⇒ 红 8 条（8 语种）', 8, countOf(v, 'L1b-regressed'))
  }

  // 判据层：L1c 豁免被用来藏违规 ⇒ 红
  {
    const facts = mkFacts()
    for (const [loc, table] of facts.perLocaleValues) table['ns.c.countSummary'] = BARE_VALUES[loc]
    const v = checkErrorClarity(facts)
    check('S14', 'L1c：豁免项的值不再落在类别里 ⇒ 红 8 条', 8, countOf(v, 'L1c-exemption-misused'))
  }

  // 判据层：L1d 某语种缺登记键 ⇒ 红
  {
    const facts = mkFacts()
    facts.perLocaleKeys.get('ja-JP').delete('ns.a.loadFailed')
    const v = checkErrorClarity(facts)
    check('S15', 'L1d：某语种缺登记键 ⇒ 红 1 条', 1, countOf(v, 'L1d-missing-locale'))
  }

  // 判据层：L1e 同语种内两键同值 ⇒ 红
  {
    const facts = mkFacts()
    for (const [, table] of facts.perLocaleValues) table['ns.b.saveFailed'] = table['ns.a.loadFailed']
    const v = checkErrorClarity(facts)
    check('S16', 'L1e：同语种内两键同值 ⇒ 红 8 条', 8, countOf(v, 'L1e-duplicate-value'))
  }

  // 判据层：L1f 登记值无恢复动作 ⇒ 红
  {
    const facts = mkFacts()
    for (const [loc, table] of facts.perLocaleValues) table['ns.a.loadFailed'] = NO_RECOVERY_VALUES[loc]
    const v = checkErrorClarity(facts)
    check('S17', 'L1f：登记值没有恢复动作 ⇒ 红 8 条', 8, countOf(v, 'L1f-no-recovery'))
  }

  // 判据层：豁免项在「运行时拼原因」下成立（源码事实）
  {
    const facts = mkFacts()
    facts.registry.exemptions['ns.c.countSummary'] = { category: 'runtime-reason', reason: '运行时拼原因' }
    for (const [loc, table] of facts.perLocaleValues) table['ns.c.countSummary'] = BARE_VALUES[loc] + ': '
    facts.composedKeys.set('ns.c.countSummary', ['c.vue:9'])
    const v = checkErrorClarity(facts)
    check('S18', 'L1c：值以冒号结尾 ⇒ 豁免成立 ⇒ 0 条', 0, countOf(v, 'L1c-exemption-misused'))
  }
  {
    const facts = mkFacts()
    facts.registry.exemptions['ns.c.countSummary'] = { category: 'runtime-reason', reason: '运行时拼原因' }
    for (const [loc, table] of facts.perLocaleValues) table['ns.c.countSummary'] = BARE_VALUES[loc]
    const v = checkErrorClarity(facts)
    check('S19', 'L1c：既不以冒号结尾、也没有运行时拼 ⇒ 豁免不成立 ⇒ 红 8 条', 8,
      countOf(v, 'L1c-exemption-misused'))
  }

  // 判据层：真实事实源 ⇒ 全绿（★ 门必须在自己的仓里是绿的）
  const real = checkErrorClarity(await loadFacts())
  check('S20', '真实事实源 ⇒ 零违规', [], rulesOf(real))

  const failed = cases.filter((c) => !c.ok)
  for (const c of failed) {
    console.log(`❌ ${c.id} ${c.desc}\n   期望 ${JSON.stringify(c.expected)}\n   实得 ${JSON.stringify(c.actual)}`)
  }
  console.log(`\n自证 ${cases.length - failed.length}/${cases.length}` + (failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(failed.length ? 2 : 0)
}

// ───────────────────────── CLI ─────────────────────────

const isSelftest = process.argv.includes('--selftest')
const isJson = process.argv.includes('--json')
const isList = process.argv.includes('--list')

// ★ main-guard（2026-10-27 第 16 增量 · 错误出口轮）
//
//   本文件**导出**判据函数（FAIL_SIGNAL / RECOVER_WORD / splitClauses /
//   isBareFailurePhrase / hasRecoveryAction / exemptionStillHolds / checkErrorClarity）
//   ⇒ 第 16 增量的「错误出口门」要**复用同一把尺子**（同一口径只能有一份实现）。
//   ★ 但本文件原先在 import 时就会跑 CLI 并 `process.exit()`
//     ⇒ 被别的门 import 时，import 方会被一起带走（退出码非 0 ⇒ 看起来像「门红了」）。
//   ⇒ 只在「本文件就是入口」时才跑 CLI。
const isMain = (() => {
  const entry = process.argv[1]
  if (!entry) return false
  try {
    return pathToFileURL(resolve(entry)).href === import.meta.url
  } catch {
    return false
  }
})()

if (!isMain) {
  // 被 import：只提供判据，不跑 CLI（上面的分支体保持为空即可）。
} else if (isSelftest) {
  await selftest()
} else {
  const facts = await loadFacts()
  const violations = checkErrorClarity(facts)
  if (isList) {
    const slots = facts.registry.slots || {}
    for (const [key, meta] of Object.entries(slots)) {
      const zh = facts.perLocaleValues.get('zh-CN')?.[key] ?? '（缺）'
      console.log(`${key}\t[${meta.kind}]\t${zh}`)
    }
    for (const [key, meta] of Object.entries(facts.registry.exemptions || {})) {
      const zh = facts.perLocaleValues.get('zh-CN')?.[key] ?? '（缺）'
      console.log(`[豁免] ${key}\t[${meta.category}]\t${zh}`)
    }
    process.exit(0)
  }
  if (isJson) {
    console.log(JSON.stringify({ violations, count: violations.length }, null, 2))
  } else {
    console.log('口径：位置信号 = 源码侧错误位置（errorPositions.ts，与 R1 门同一份实现）；'
      + '裸失败短语 = 含失败信号 ∧ 无原因槽 ∧ 无「不含失败信号的分句」里的恢复动作')
    console.log(`错误位置上的语言包键：${facts.candidateKeys.size}；登记槽位：${Object.keys(facts.registry.slots || {}).length}；`
      + `豁免：${Object.keys(facts.registry.exemptions || {}).length}`)
    if (violations.length === 0) {
      console.log('\n✅ 错误陈述都说得清「怎么办」—— PASS')
    } else {
      console.log(`\n❌ ${violations.length} 处违规：`)
      for (const v of violations) {
        console.log(`  [${v.rule}] ${v.key}${v.locale ? ` @${v.locale}` : ''} — ${v.reason}`)
        if (v.value) console.log(`      「${v.value}」`)
        if (v.related) console.log(`      同值键：${v.related.join(', ')}`)
        if (v.sites) console.log(`      调用点：${v.sites.join(', ')}`)
      }
    }
  }
  process.exit(violations.length ? 1 : 0)
}
