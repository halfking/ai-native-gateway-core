import { describe, it, expect } from 'vitest'
import { zhCN } from './zh-CN'
import { enUS } from './en-US'
import { sourceBlob, literalKeys, concatPrefixes, isRealPrefix, flattenDict, prefixHits, NAV_PROOF_KEYS, PREFIX_RES, PREFIX_FORM_NAMES } from './keyScan'

/**
 * ★★★★★ i18n **孤儿键**普查 —— 与 `orphanLedger.spec.ts`（孤儿 API 模块）同族，方向相反：
 *   那个量「模块有人 import 吗」，这个量「**词典里的键有人用吗**」。
 *
 * ## 为什么要有这道门
 *
 * `scripts/verify-i18n-parity.mjs` 已经守了四件事，但**没有一件是反向的**：
 *   1. zh/en 键集一致 · 2. 无「值 === 键名」 · 3. 无重复键 · 4. **源码引用的键必须存在**。
 * 第 4 条是「**用到的必须在**」，于是整整一类缺陷结构上就漏掉：
 *   **词典里有、但没有任何代码用到的键**。
 *
 * ## ★ 为什么这里 `import { zhCN }`，而 parity 脚本要手写正则解析
 *
 * parity 脚本刻意不 import（它要跑在 build 前置里、不依赖 tsx），代价是手写解析器。
 * 本文件在 vitest 里，**没有那个约束** ⇒ 直接 import 真实对象。
 *
 * ★★ 这不是偷懒，是本批**实测出来的教训**：那份手写解析器在同一个词典上连续栽了五次
 *   （内联对象的带引号键名 `'5m'` 漏读 / 正则加了捕获组后组号没移 /
 *    独立成行的带引号键名 `'unexpected-shape'` 漏读 / 由此产生的栈失衡 /
 *    最后干脆给出 3814 / 3744 两个都不是 3833 的「键总数」）。
 *   五次的表现完全一样：**「词典里明明有，我的量具说没有」**。
 *   ⇒ 与其把解析器调对，不如**不用解析器**。阴性读数的可信度取决于量具，
 *   而「少写一个解析器」是提高可信度最快的办法。
 *
 * ## 「被用到」的三种形态（缺一即成批假孤儿）
 *
 *   ① **字面量**：`t('a.b.c')` / `titleKey: 'nav.home'` 等任何带引号的点号路径
 *   ② **拼接**：`t('maas.dim_' + d)`、`` t(`dh.state.${state}`) ``
 *   ③ **藏在 helper 里**：`api/probeTimelineCache.ts` 的
 *      `? 'cache.state.' + v : 'cache.state.unknown'` —— 只扫前两种会把它整族误判
 *
 * ⚠️ 覆盖面本身也是判据的一部分，所以 ①-3 / ⑥-2 专门盯「三种形态各还抓不抓得到」。
 */

const ZH_KEYS: string[] = flattenDict(zhCN)
const EN_KEYS: string[] = flattenDict(enUS)
const { files: ALL_FILES, blob: SRC_BLOB } = sourceBlob()
const LITERAL_KEYS = literalKeys(SRC_BLOB)
const CONCAT = concatPrefixes(SRC_BLOB)
const KEY_PREFIXES: string[] = [...CONCAT].filter((p) => isRealPrefix(p, ZH_KEYS)).sort()

/** ★ 孤儿判定抽成**纯函数** —— 否则自证只能写成恒假的断言。 */
function isOrphan(
  key: string,
  literals: Set<string> = LITERAL_KEYS,
  prefixes: string[] = KEY_PREFIXES,
): boolean {
  return !literals.has(key) && !prefixes.some((p) => key.startsWith(p))
}

const ORPHANS: string[] = ZH_KEYS.filter((k) => isOrphan(k)).sort()

/**
 * ★★★ 三条棘轮判定各自抽成**具名纯函数** ——
 *   原因：批 140 变异台首轮 K03/K04/K06 把 `unknown` / `used` / `blank` **整个掏空**，
 *   用例**照样全绿**（`[] === []`）。而我第一版加的「内置负控」也没咬住 ——
 *   因为负控走的是**另一段表达式**，把被测的那段掏空，负控照样绿。
 *   ⇒ 负控必须**调用同一个函数**，只是传一个「被打破」的输入。
 */
const unregisteredOrphans = (orphans: string[], registered: string[]): string[] =>
  orphans.filter((k) => !registered.includes(k))

const noLongerOrphans = (registered: string[], orphanSet: Set<string>): string[] =>
  registered.filter((k) => !orphanSet.has(k))

const blankNotes = (rows: { key: string; note: string }[]): string[] =>
  rows.filter((o) => !o.note || o.note.trim().length < 4).map((o) => o.key)

/**
 * ★★★ 已知孤儿键（**债**，不是豁免）—— 逐条注明「为什么确定它没人用」。
 *
 * ⚠️ **本批不删它们**：删键的前提是量具可信，而本批的量具恰恰是边建边证伪出来的
 *   （见文件头那段「栽了五次」）。在一个**已知有洞**的量具上做删除是不可逆的。
 *   ⇒ 本批只做三件事：把它们**量出来**、**钉住不许再涨**、**逐条注明**。
 *   删除是一个口径决策，登记待拍板。
 *
 * 分两类：
 *   A. **整块死**（67 条 / 多数命名空间）：命名空间在源码里零出现
 *   B. **块内死**（5 条）：命名空间在用，只有这几条没有
 */
const KNOWN_ORPHAN_I18N: { key: string; note: string }[] = [
  // ── `account.role` 整块：命名空间在源码里零出现
  { key: 'account.role', note: '整块无人引用' },
  // ── `account.versionInfo` 整块：命名空间在源码里零出现
  { key: 'account.versionInfo', note: '整块无人引用' },
  // ── `acfgRules.channelsTitle` 整块：命名空间在源码里零出现
  { key: 'acfgRules.channelsTitle', note: '整块无人引用' },
  // ── `acfgRules.noChannelConfig` 整块：命名空间在源码里零出现
  { key: 'acfgRules.noChannelConfig', note: '整块无人引用' },
  // ── `acfgRules.noChannels` 整块：命名空间在源码里零出现
  { key: 'acfgRules.noChannels', note: '整块无人引用' },
  // ── `alerts.occurredAt` 整块：命名空间在源码里零出现
  { key: 'alerts.occurredAt', note: '整块无人引用' },
  // ── `alerts.reason` 整块：命名空间在源码里零出现
  { key: 'alerts.reason', note: '整块无人引用' },
  // ── `audit.open` 整块：命名空间在源码里零出现
  { key: 'audit.open', note: '整块无人引用' },
  // ── `audit.openFor` 整块：命名空间在源码里零出现
  { key: 'audit.openFor', note: '整块无人引用' },
  // ── `common.confirmDirtyTitle` 整块：命名空间在源码里零出现
  { key: 'common.confirmDirtyTitle', note: '整块无人引用' },
  // ── `common.discard` 整块：命名空间在源码里零出现
  { key: 'common.discard', note: '整块无人引用' },
  // ── `common.keepEditing` 整块：命名空间在源码里零出现
  { key: 'common.keepEditing', note: '整块无人引用' },
  // ── `common.loadMore` 整块：命名空间在源码里零出现
  { key: 'common.loadMore', note: '整块无人引用' },
  // ── `common.loadingCancelable` 整块：命名空间在源码里零出现
  { key: 'common.loadingCancelable', note: '整块无人引用' },
  // ── `common.moreActions` 整块：命名空间在源码里零出现
  { key: 'common.moreActions', note: '整块无人引用' },
  // ── `common.searchPlaceholder` 整块：命名空间在源码里零出现
  { key: 'common.searchPlaceholder', note: '整块无人引用' },
  // ── `dashboardOps.operational` 块内：命名空间在用，只有这几条没有
  { key: 'dashboardOps.operational.probeLoop', note: '同块的 statusWord* / neverRan / checks10m 等 17 条都在用，只有它没有 —— 探针循环那一栏重写后残留' },
  // ── `dh.check` 块内：命名空间在用，只有这几条没有
  { key: 'dh.check.envelope', note: '同块的 flagBad / shapeBad / sourceBad 三条都在用（DataHorizonView.vue:168-174）' },
  // ── `dh.load` 块内：命名空间在用，只有这几条没有
  { key: 'dh.load.title', note: '同块只有 dh.load.btn 在用（DataHorizonView.vue:113）；title 与 dh.title 重复' },
  // ── `dj.snap` 块内：命名空间在用，只有这几条没有
  { key: 'dj.snap.truncatedCount', note: '同块的 truncatedBad / truncatedYes / truncatedNo 都在用；这一条被 truncatedCountBad 取代' },
  { key: 'dj.snap.version', note: '同块的 versionBad 都在用；这一条被 versionBad 取代' },
  // ── `heatmap.granularity` 整块：命名空间在源码里零出现
  { key: 'heatmap.granularity', note: '整块无人引用' },
  // ── `heatmap.nextRetry` 整块：命名空间在源码里零出现
  { key: 'heatmap.nextRetry', note: '整块无人引用' },
  // ── `home.backgroundTasks` 整块：命名空间在源码里零出现
  { key: 'home.backgroundTasks', note: '整块无人引用' },
  // ── `home.build` 整块：命名空间在源码里零出现
  { key: 'home.build', note: '整块无人引用' },
  // ── `home.providers` 整块：命名空间在源码里零出现
  { key: 'home.providers', note: '整块无人引用' },
  // ── `keys.appName` 整块：命名空间在源码里零出现
  { key: 'keys.appName', note: '整块无人引用' },
  // ── `keys.createSuccess` 整块：命名空间在源码里零出现
  { key: 'keys.createSuccess', note: '整块无人引用' },
  // ── `keys.masked` 整块：命名空间在源码里零出现
  { key: 'keys.masked', note: '整块无人引用' },
  // ── `keys.name` 整块：命名空间在源码里零出现
  { key: 'keys.name', note: '整块无人引用' },
  // ── `keys.prefix` 整块：命名空间在源码里零出现
  { key: 'keys.prefix', note: '整块无人引用' },
  // ── `keys.status` 整块：命名空间在源码里零出现
  { key: 'keys.status', note: '整块无人引用' },
  // ── `maas.navMaas` 整块：命名空间在源码里零出现
  { key: 'maas.navMaas', note: '整块无人引用' },
  // ── `models.contextWindow` 整块：命名空间在源码里零出现
  { key: 'models.contextWindow', note: '整块无人引用' },
  // ── `models.families` 整块：命名空间在源码里零出现
  { key: 'models.families', note: '整块无人引用' },
  // ── `models.parameters` 整块：命名空间在源码里零出现
  { key: 'models.parameters', note: '整块无人引用' },
  // ── `models.providerCount` 整块：命名空间在源码里零出现
  { key: 'models.providerCount', note: '整块无人引用' },
  // ── `models.unmappedHint` 整块：命名空间在源码里零出现
  { key: 'models.unmappedHint', note: '整块无人引用' },
  // ── `models.vendor` 整块：命名空间在源码里零出现
  { key: 'models.vendor', note: '整块无人引用' },
  // ── `models.versions` 整块：命名空间在源码里零出现
  { key: 'models.versions', note: '整块无人引用' },
  // ── `nav.nodeHealth` 整块：命名空间在源码里零出现
  { key: 'nav.nodeHealth', note: '整块无人引用' },
  // ── `nodes.detail` 整块：命名空间在源码里零出现
  { key: 'nodes.detail', note: '整块无人引用' },
  // ── `nodes.manualDisabled` 整块：命名空间在源码里零出现
  { key: 'nodes.manualDisabled', note: '整块无人引用' },
  // ── `nodes.state` 整块：命名空间在源码里零出现
  { key: 'nodes.state', note: '整块无人引用' },
  // ── `routingAudit.actor` 整块：命名空间在源码里零出现
  { key: 'routingAudit.actor', note: '整块无人引用' },
  // ── `routingLog.actor` 整块：命名空间在源码里零出现
  { key: 'routingLog.actor', note: '整块无人引用' },
  // ── `routingLog.detail` 整块：命名空间在源码里零出现
  { key: 'routingLog.detail', note: '整块无人引用' },
  // ── `routingLog.errWindow` 整块：命名空间在源码里零出现
  { key: 'routingLog.errWindow', note: '整块无人引用' },
  // ── `routingLog.errorCode` 整块：命名空间在源码里零出现
  { key: 'routingLog.errorCode', note: '整块无人引用' },
  // ── `routingLog.errorMessage` 整块：命名空间在源码里零出现
  { key: 'routingLog.errorMessage', note: '整块无人引用' },
  // ── `routingLog.kindAll` 整块：命名空间在源码里零出现
  { key: 'routingLog.kindAll', note: '整块无人引用' },
  // ── `routingLog.kindProbe` 整块：命名空间在源码里零出现
  { key: 'routingLog.kindProbe', note: '整块无人引用' },
  // ── `routingLog.kindRouting` 整块：命名空间在源码里零出现
  { key: 'routingLog.kindRouting', note: '整块无人引用' },
  // ── `routingLog.loadMore` 整块：命名空间在源码里零出现
  { key: 'routingLog.loadMore', note: '整块无人引用' },
  // ── `routingLog.noMore` 整块：命名空间在源码里零出现
  { key: 'routingLog.noMore', note: '整块无人引用' },
  // ── `routingLog.outboundModel` 整块：命名空间在源码里零出现
  { key: 'routingLog.outboundModel', note: '整块无人引用' },
  // ── `routingLog.requestId` 整块：命名空间在源码里零出现
  { key: 'routingLog.requestId', note: '整块无人引用' },
  // ── `routingLog.resultAll` 整块：命名空间在源码里零出现
  { key: 'routingLog.resultAll', note: '整块无人引用' },
  // ── `routingLog.resultFailed` 整块：命名空间在源码里零出现
  { key: 'routingLog.resultFailed', note: '整块无人引用' },
  // ── `routingLog.resultSuccess` 整块：命名空间在源码里零出现
  { key: 'routingLog.resultSuccess', note: '整块无人引用' },
  // ── `routingLog.source` 整块：命名空间在源码里零出现
  { key: 'routingLog.source', note: '整块无人引用' },
  // ── `routingLog.sticky` 整块：命名空间在源码里零出现
  { key: 'routingLog.sticky', note: '整块无人引用' },
  // ── `routingLog.tier` 整块：命名空间在源码里零出现
  { key: 'routingLog.tier', note: '整块无人引用' },
  // ── `sa.navTitle` 整块：命名空间在源码里零出现
  { key: 'sa.navTitle', note: '整块无人引用' },
  // ── `sctx.noStatusFieldNote` 整块：命名空间在源码里零出现
  { key: 'sctx.noStatusFieldNote', note: '整块无人引用' },
  // ── `turns.injectionBlocked` 整块：命名空间在源码里零出现
  { key: 'turns.injectionBlocked', note: '整块无人引用' },
  // ── `turns.modelsUsed` 整块：命名空间在源码里零出现
  { key: 'turns.modelsUsed', note: '整块无人引用' },
  // ── `turns.outputVerdict` 整块：命名空间在源码里零出现
  { key: 'turns.outputVerdict', note: '整块无人引用' },
  // ── `turns.turnsTitle` 整块：命名空间在源码里零出现
  { key: 'turns.turnsTitle', note: '整块无人引用' },
  // ── `waterfall.credLane` 整块：命名空间在源码里零出现
  { key: 'waterfall.credLane', note: '整块无人引用' },
  // ── `waterfall.expand` 整块：命名空间在源码里零出现
  { key: 'waterfall.expand', note: '整块无人引用' },
  // ── `waterfall.modelLane` 整块：命名空间在源码里零出现
  { key: 'waterfall.modelLane', note: '整块无人引用' },
]

const REGISTERED: string[] = KNOWN_ORPHAN_I18N.map((o) => o.key)

describe('① 读数：分母必须写在这里，不许只报「孤儿有几条」', () => {
  it('①-1 ★★★★★ 词典键总数 / 字面量引用 / 动态前缀 / 孤儿，各是多少', () => {
    console.log(
      `[①-1] 词典 zh ${ZH_KEYS.length} 键 · en ${EN_KEYS.length} 键 · 一致 ${ZH_KEYS.length === EN_KEYS.length}\n` +
        `       源码文件 ${ALL_FILES.length} 个 · 抽到的字面量键 ${LITERAL_KEYS.size}\n` +
        `       拼接点 ${CONCAT.size} 个，其中真是词典键前缀的 ${KEY_PREFIXES.length} 个\n` +
        `       ⇒ 孤儿键 **${ORPHANS.length}** 条（已登记 ${REGISTERED.length}）`,
    )
    // ★ 分母对账：孤儿 ⊆ 词典键（防止分类器自己漏项）
    expect(ORPHANS.every((k) => ZH_KEYS.includes(k))).toBe(true)
    expect(ZH_KEYS.length).toBeGreaterThan(3000)
  })

  it('①-2 ★★★★★ 召回自证：同一个键，被引用⇒不算孤儿；移出引用集⇒立刻算孤儿', () => {
    // ★ 双向各验一次。只验「移出后变孤儿」，则「永远算孤儿」也能过；
    //   只验「引用时不孤儿」，则「永远不算孤儿」也能过。
    const sample = [...LITERAL_KEYS].find((k) => ZH_KEYS.includes(k))
    expect(sample, '一个字面量引用都没抽到？扫描器坏了').toBeTypeOf('string')
    expect(isOrphan(sample!)).toBe(false)
    const stripped = new Set(LITERAL_KEYS)
    stripped.delete(sample!)
    expect(isOrphan(sample!, stripped)).toBe(true)
  })

  it('①-2b ★★★★ 前缀覆盖也必须双向：前缀在⇒不算孤儿；前缀撤掉⇒立刻算孤儿', () => {
    const p = KEY_PREFIXES.find((q) => ZH_KEYS.some((k) => k.startsWith(q)))
    expect(p, '一个词典键前缀都没抽到？覆盖面退化了').toBeTypeOf('string')
    const key = ZH_KEYS.find((k) => k.startsWith(p!))!
    expect(isOrphan(key)).toBe(false)
    expect(isOrphan(key, LITERAL_KEYS, KEY_PREFIXES.filter((q) => q !== p))).toBe(true)
  })

  it('①-3 ★★★★★ 精确率自证：三种拼接形态各至少抓到 1 个前缀', () => {
    // ★ 覆盖面本身也要被判。若有人删掉 P_PREFIX_RES 里的第三条正则，
    //   `cache.state.*` / `probeModel.health.*` / `autoRoute.outcome.*` 会成批变成假孤儿。
    const hits = prefixHits(SRC_BLOB)
    console.log(`[①-3] 拼接形态命中：${JSON.stringify(PREFIX_FORM_NAMES.map((n, i) => [n, hits[i]]))}`)
    // ★★ 先钉**形状**再验命中 —— 批 140 变异 K07 实测出的洞：
    //   只验「每种形态命中数 > 0」的话，**删掉 P_PREFIX_RES 里的一项**之后，
    //   `hits` 数组也跟着变短，循环不再检查被删的那一项 ⇒ ①-3 变成空洞。
    expect(PREFIX_RES).toHaveLength(3)
    expect(PREFIX_FORM_NAMES).toHaveLength(3)
    for (let i = 0; i < hits.length; i++) {
      expect(hits[i], `${PREFIX_FORM_NAMES[i]} 一个都没抓到 ⇒ 覆盖面退化了`).toBeGreaterThan(0)
    }
  })
  it('①-4 ★★★★★ 负控：strip 之后，**代码里**（不是注释里）引用的键必须仍然抽得到', () => {
    // ★ `keyScan.ts` 的文件头记着本批踩过的坑：`appNav.ts:231` 的一行**注释**里
    //   含有 `/api/admin/maas/tenants/**`，其中的 `/**` 被块注释正则当成了起点，
    //   一路吃掉 18319 个字符 —— `titleKey: 'nav.annotations'` 等真实引用随之消失，
    //   孤儿判据会把它们**全报成孤儿**。
    //   ⇒ 「strip 注释」这件事本身必须被负控盯着，否则它是一个静默的漏检源。
    const lost = NAV_PROOF_KEYS.filter((k) => !LITERAL_KEYS.has(k))
    expect(lost, 'stripComments 误伤了真实代码 ⇒ 孤儿读数会虚高').toEqual([])
  })
})

// ── ② 棘轮：不许新欠债 ───────────────────────────────────────────────────────

describe('② ★★★ 未登记的孤儿键一律红', () => {
  it('②-1 ★★★★★ 判为孤儿的键必须全部已登记', () => {
    // ★ 负控**先跑**：证明这个函数还能报出失败（否则下面那句 `toEqual([])` 是恒真的）
    const probe = ORPHANS[0]
    expect(
      probe === undefined ? [] : unregisteredOrphans([probe], []),
      '★ 负控失效：未登记的孤儿必须能被报出来',
    ).toEqual([probe])
    const unknown = unregisteredOrphans(ORPHANS, REGISTERED)
    expect(
      unknown,
      '这些键词典里有、但没有任何代码用得到，又没登记 ⇒\n' +
        '  · 真有人用 ⇒ 是量具漏了某种写法，回去补 keyScan 的正则，别登记；\n' +
        '  · 确实没人用 ⇒ 在 KNOWN_ORPHAN_I18N 里登记并注明原因。\n' +
        unknown.join('\n'),
    ).toEqual([])
  })
})

// ── ③ 棘轮反向：登记项不许悄悄变好 ──────────────────────────────────────────

describe('③ ★★★ 登记项必须仍然是孤儿（有人用了就要删登记）', () => {
  it('③-1 ★★★★★ KNOWN_ORPHAN_I18N 里每一条都仍然判为孤儿', () => {
    const present = new Set(ORPHANS)
    // ★ 负控**先跑**（同 ②-1：负控必须走同一个函数）
    const probeKey = REGISTERED[0]
    expect(
      probeKey === undefined ? [] : noLongerOrphans([probeKey], new Set<string>()),
      '★ 负控失效：不再是孤儿的登记项必须能被报出来',
    ).toEqual([probeKey])
    const used = noLongerOrphans(REGISTERED, present)
    expect(
      used,
      '这些登记项已经不判为孤儿了 ⇒ 有代码开始用它们（或量具变了），\n' +
        '把登记删掉，别让名单变成免死金牌：\n' +
        used.join('\n'),
    ).toEqual([])
  })

  it('③-2 ★★★ 名单里不许有重名项', () => {
    expect(REGISTERED.filter((k, i) => REGISTERED.indexOf(k) !== i)).toEqual([])
  })
})

// ── ④ 名单规模与质量 ────────────────────────────────────────────────────────

describe('④ 名单规模与质量', () => {
  it('④-1 ★★★★ KNOWN_ORPHAN_I18N 恰好 72 条（逐条定性过）', () => {
    expect(KNOWN_ORPHAN_I18N).toHaveLength(72)
  })

  it('④-2 ★★★★ 每一条都必须有非空的「为什么确定没人用」', () => {
    // ★ 负控**先跑**（同 ②-1）
    expect(blankNotes([{ key: 'probe', note: '' }]),
      '★ 负控失效：空 note 必须被判为「没写原因」').toEqual(['probe'])
    const blank = blankNotes(KNOWN_ORPHAN_I18N)
    expect(blank, '这些登记项没写原因 ⇒ 不许登记').toEqual([])
  })

  it('④-3 ★★★ 每个登记键都必须真实存在于**两侧**词典（否则是笔误不是债）', () => {
    const ghost = REGISTERED.filter((k) => !ZH_KEYS.includes(k) || !EN_KEYS.includes(k))
    expect(ghost, `名单里有不存在的键：${ghost.join(', ')}`).toEqual([])
  })

  it('④-4 ★★ 判据自身不得依赖未纳入仓库的路径', () => {
    expect(ZH_KEYS.length).toBeGreaterThan(3000)
    expect(ALL_FILES.length).toBeGreaterThan(100)
  })
})
