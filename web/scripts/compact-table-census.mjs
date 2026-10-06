/**
 * compact-table-census.mjs — 小屏数据展示覆盖普查（docs/UI规范 22 待立项 / 10 §4.6.24）。
 *
 * ## 为什么需要它
 *
 * 19 §5 全屏表格模式与 §4.6.21 的 device-fit 门都只解决「**能不能滚**」，
 * 但没有回答「**哪些页面根本没有滚动容器**」。
 * 参考仓 nbjl3 在 3d9d0267 把这件事从「已知留白」一句定性变成了可复测的报表：
 * 实测 60 个含表格视图，真缺口 1 个、假阳性 12 个。
 * 本仓含表格的 `.vue` 有 **90 个**（68 views + 22 components），量级更大。
 *
 * ## 本脚本**不判红**，只出报表
 *
 * 理由（同参考仓）：「有多少」必须可复测，但**分类判据本身会错**
 * ——只认字面类名会把按用途命名的等价滚动容器全报成缺口。
 * 所以本脚本输出一张「谁在哪个桶」的表，**待人判的桶由人决定**，
 * 不擅自把某一条升级成门禁。要升级，得先把该桶收敛到 0。
 *
 * ## 判据口径（与参考仓不同处已注明）
 *
 * · 滚动容器：**按语义**（CSS 规则里声明 `overflow-x/overflow: auto|scroll`，
 *   且该类名出现在模板里）——不认类名白名单，本仓声明 overflow 的类名有 107 个，
 *   字面 `.table-scroll` 只有 1 个；并下探**被引用组件内部自带的横滚**。
 * · 卡片化：接 `ResponsiveDataView`（compact 强制 cards）或 `useDataViewMode`。
 * · 列数：**三态**（narrow 实测 ≤4 / wide 实测 ≥5 / unknown 源码测不到），
 *   「测不到」单列一桶，不并入窄表——没量过的东西不替它签字。
 * · 路径口径：**扫描侧、登记侧、组件索引三处都必须是「相对 `web/` 的路径」**。
 *   本轮出过两次自相矛盾：`walk()` 给绝对路径而索引按相对路径读（双前缀 ENOENT
 *   被 catch 吞成空串 ⇒ 恒 false）；`norm()` 剥 `src/` 后 `join(SRC,rel)` 指错一层，
 *   而主循环读文件用未剥的、记桶用剥过的，同一个 rel 被两套口径消费。
 *
 * 用法：node scripts/compact-table-census.mjs [--json <path>]
 */
import { readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { join, resolve, dirname, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const SRC = resolve(__dirname, '..')

/**
 * 归一：统一成正斜杠。**保留 `src/` 前缀**。
 *
 * ⚠️ 这里曾经剥掉 `^src/`，而 `SRC` 本身就是 web 根 ⇒ 剥完再 `join(SRC, rel)`
 * 会指错一层（`web/components/...`，真实路径是 `web/src/components/...`）。
 * 当时之所以没炸，是主循环读文件用的是未归一的 `relRaw`、只有记桶用 `rel`，
 * 于��**同一个 rel 在同一段代码里被两套口径消费**，读和记指向不同文件。
 * 保留前缀后三处口径统一，报表里打出的 `src/views/X.vue` 也是能直接打开的路径。
 */
const norm = (p) => p.split('\\').join('/')

/** 组件源码缓存：同一个组件被几十个页面引用，逐个重读会慢。 */
const COMPONENT_CACHE = new Map()
/**
 * 组件名 → 相对 SRC 的文件路径列表（懒建索引）。
 *
 * ⚠️ **两条口径必须与主循环一致**，否则量具会自己骗自己：
 * `walk(SRC)` 产出的是**绝对路径**，而主循环用的是 `relative(SRC, f)`。
 * 之前索引直接存 `norm(绝对路径)`，而 `norm()` 只剥 `^src/`（对绝对路径永不匹配），
 * 于是 `join(SRC, 绝对路径)` 拼出 `web/Users/.../web/src/...` 这种双前缀路径，
 * readFileSync 抛错被 catch 吞成空串 ⇒ `componentHasHorizontalScroll` **恒 false**。
 * 表象是「页面里明明能滚，却被报进 unwrapped」—— 量具自相矛盾，不是页面缺容器。
 * ⇒ 两侧都过 `relative()` 再 `norm()`。
 *
 * 同名组件（实测有 `OperationAgreementDialog` 两份）**不做「后者胜出」**：
 * 静默挑一个等于让报表内容取决于 readdir 顺序。改为全部保留，
 * `componentHasHorizontalScroll` 取「任一份能横滚」——页面引用的是哪个名字，
 * 两个候选里只要有一个提供横滚就应判为已覆盖。
 */
let COMPONENT_INDEX = null
function componentIndex() {
  if (COMPONENT_INDEX) return COMPONENT_INDEX
  const map = new Map()
  for (const abs of walk(SRC)) {
    const rel = norm(relative(SRC, abs))
    const base = rel.split('/').pop().replace(/\.vue$/, '')
    if (!map.has(base)) map.set(base, [])
    map.get(base).push(rel)
  }
  COMPONENT_INDEX = map
  return map
}

/** 索引里同名冲突的组件名（报表自曝，不静默）。 */
function componentNameCollisions() {
  return [...componentIndex()].filter(([, rels]) => rels.length > 1).map(([n, rels]) => `${n}×${rels.length}`)
}

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name === '.git') continue
    const full = join(dir, name)
    if (statSync(full).isDirectory()) yield* walk(full)
    else if (name.endsWith('.vue')) yield full
  }
}

/**
 * 本文件是否**按语义**提供了横向滚动容器。
 *
 * ⚠️ **四种**写法都要认。漏一种就产生假阳性 —— 本轮实测前三次修判据，
 *    每次都翻出一批「其实已经合规」的页面：
 *   ① CSS 规则：`.foo { overflow-x: auto }` 且 `class="… foo …"`
 *   ② **内联 style**：`<div class="card" style="overflow-x:auto">`（仓内 7 个文件）
 *   ③ **组件内部自带**：`<DataTable>` 等封装组件的根节点已声明 `overflow-x: auto`
 *      （`components/ui/DataTable.vue:55` 实测）—— **这是最容易漏的一层**：
 *      页面里看不见任何 overflow 声明，但它就是能滚。
 *      ⇒ 必须去解析**被引用的组件**，不能只扫页面自身。
 *   ④ `min-width` 声明：只作**弱证据**（作者声明「它要滚」但容器未必存在），
 *      归独立桶，不与 ①②③ 混算。
 */
function hasScrollContainer(text) {
  // ② 内联 style
  if (/style="[^"]*overflow(?:-x|-y)?\s*:\s*(auto|scroll)/.test(text)) return true
  // ① CSS 规则
  const overflowClasses = new Set()
  for (const m of text.matchAll(/\.([A-Za-z0-9_-]+)\s*\{([^}]*)\}/g)) {
    if (/overflow(?:-x|-y)?\s*:\s*(auto|scroll)/.test(m[2])) overflowClasses.add(m[1])
  }
  if (overflowClasses.size > 0) {
    const tpl = text.match(/<template>([\s\S]*)<\/template>/)
    const markup = tpl ? tpl[1] : text
    if ([...overflowClasses].some(c => new RegExp(`class="[^"]*\\b${c.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\b`).test(markup))) return true
  }
  // ③ 引用的表格封装组件内部自带横滚
  for (const comp of referencedTableComponents(text)) {
    if (componentHasHorizontalScroll(comp)) return true
  }
  return false
}

/** 页面里引用了哪些表格类组件（PascalCase 标签 + 已知表组件名）。 */
function referencedTableComponents(text) {
  const names = new Set()
  for (const m of text.matchAll(/<([A-Z][A-Za-z0-9]*)\b/g)) {
    if (/table/i.test(m[1])) names.add(m[1])
  }
  return [...names]
}

/**
 * 该组件的**根容器**（或其子层）是否声明了横向滚动。
 *
 * ⚠️ 读文件失败**必须与「没有横滚」区分开**：两者都返回 false 时，
 *   量具会拿「我没读到」当「页面没做」，把已经合规的页面误报进 unwrapped。
 *   读不到就抛，让调用方看到，而不是静默吞掉。
 */
function componentHasHorizontalScroll(compName) {
  const rels = componentIndex().get(compName)
  if (!rels) return false
  return rels.some((rel) => {
    if (!COMPONENT_CACHE.has(rel)) {
      COMPONENT_CACHE.set(rel, readFileSync(join(SRC, rel), 'utf8'))
    }
    const text = COMPONENT_CACHE.get(rel)
    return /overflow(?:-x|-y)?\s*:\s*(auto|scroll)/.test(text)
  })
}

/** 弱证据：表格带 min-width 声明，但未找到配套滚动容器。 */
function declaresMinWidth(text) {
  // ⚠️ min-width 常挂在**组件标签**上（如 `<DataTable min-width="1080px">`），
  //   不在 `<table>` 块内部 —— 只搜 table 块会漏（本轮实测 12 个文件全被漏判）。
  //   判据：表格上下文（table 块 或 DataTable 类组件标签）里出现 min-width 数值。
  const tbl = text.match(/<table\b[\s\S]*?<\/table>/)
  if (tbl && /min-width\s*[:=]\s*["']?\d{3,}px/.test(tbl[0])) return true
  return /<(?:[A-Z][A-Za-z]*Table|DataTable|AppDataTable|el-table)\b[^>]*\bmin-width="[0-9]{3,}px"/.test(text)
}

/** 该视图在 compact 下是否根本不出表格。 */
function isCardified(text) {
  return /<ResponsiveDataView\b/.test(text)
    || /<CardList\b/.test(text)
    || /useDataViewMode\s*\(/.test(text)
    // 自带 v-if 分支出卡片（详情页内的子表常用）：compact 判据 + 卡片容器同文件
    || (/\bcompact\b/.test(text) && /class="[^"]*card/i.test(text) && /v-if="[^"]*compact/.test(text))
}

/**
 * 表格列数能否静态判定，返回三态。
 *
 * ⚠️ 早先这里是布尔：`if (!tbl) return true` —— 于是**所有组件式表格页
 *   （`<DataTable>`/`<el-table>`，没有字面 `<table>`）都被判成「≤4 列窄表」**。
 *   实测该桶 13 个里只有 6 个真测到了列数，另外 7 个是 `th=-1`（从未测过）。
 *   桶名写着「≤4 列」，却替 7 个没量过的东西背书 ⇒ 把**测不到**单列一桶，
 *   别混进「已确认窄表」。
 *
 * 两类**可静态测**的写法（实测本仓 90 个含表格页里，`<el-table>` 占相当比例）：
 *  ① 字面 `<table>` + `<thead>`：列数 = `<thead>` 里的 `<th>` 个数。
 *  ② Element Plus `<el-table>`：**一个 `<el-table-column>` 就是一列**，
 *     同样能从源码数出来 —— 早先把它当「测不到」是白扔证据。
 *  ③ 其它封装组件（`<DataTable :columns=…>` / slots 传列）：列数在 props 与运行时，
 *     源码测不到 ⇒ `unknown`，不并入窄表。
 *
 *   · `narrow`  —— 静态测到 1..4 列。
 *   · `unknown` —— 测不到。
 *   · `wide`    —— 静态测到 ≥5 列。
 */
function narrowTableVerdict(text) {
  const tbl = text.match(/<table\b[\s\S]*?<\/table>/)
  if (tbl) {
    const head = tbl[0].match(/<thead[\s\S]*?<\/thead>/)
    if (head) {
      const thCount = (head[0].match(/<th\b/g) || []).length
      if (thCount > 0) return thCount <= 4 ? 'narrow' : 'wide'
    }
  }
  const el = text.match(/<el-table\b[\s\S]*?<\/el-table>/)
  if (el) {
    const colCount = (el[0].match(/<el-table-column\b/g) || []).length
    if (colCount > 0) return colCount <= 4 ? 'narrow' : 'wide'
  }
  return 'unknown'
}

const args = process.argv.slice(2)
const jsonIdx = args.indexOf('--json')
const OUT = jsonIdx >= 0 ? args[jsonIdx + 1] : null

const buckets = {
  'cards（compact 出卡片）': [],
  'degradation（有横向滚动容器）': [],
  'min-width 声明（待核容器）': [],
  'narrow（实测 ≤4 列窄表）': [],
  '列数不可静态测（组件式表格）': [],
  'unwrapped（待人判：既无横滚也非窄表）': [],
}

const files = [...walk(SRC)].map(f => relative(SRC, f)).sort()
for (const relRaw of files) {
  const rel = norm(relRaw)
  const text = readFileSync(join(SRC, relRaw), 'utf8')
  if (!/<table\b|<el-table\b|<AppDataTable\b|<DataTable\b/.test(text)) continue

  const verdict = narrowTableVerdict(text)
  if (isCardified(text)) buckets['cards（compact 出卡片）'].push(rel)
  else if (hasScrollContainer(text)) buckets['degradation（有横向滚动容器）'].push(rel)
  else if (declaresMinWidth(text)) buckets['min-width 声明（待核容器）'].push(rel)
  else if (verdict === 'narrow') buckets['narrow（实测 ≤4 列窄表）'].push(rel)
  else if (verdict === 'unknown') buckets['列数不可静态测（组件式表格）'].push(rel)
  else buckets['unwrapped（待人判：既无横滚也非窄表）'].push(rel)
}

const total = Object.values(buckets).reduce((a, b) => a + b.length, 0)
console.log('=== compact 数据表覆盖普查（不判红，只出报表）===')
console.log(`扫描 .vue ${files.length} 个；含表格 ${total} 个\n`)
for (const [k, v] of Object.entries(buckets)) {
  const full = k.startsWith('unwrapped') || k.startsWith('min-width') || k.startsWith('列数不可静态测')
  console.log(`【${k}】${v.length}`)
  if (full) for (const r of v) console.log(`    ⚠️  ${r}`)
  else for (const r of v.slice(0, 8)) console.log(`    ${r}`)
  if (!full && v.length > 8) console.log(`    …… 其余 ${v.length - 8} 个`)
  console.log()
}
console.log('★ 待人判桶为 0 之前，本报表不得升级为门禁。')
console.log('★ 「narrow」只收**静态测到 ≤4 列**的页面（字面 <table> 数 <th>、');
console.log('  或 <el-table> 数 <el-table-column>）；封装组件传列的归「列数不可静态测」，')
console.log('  **不并入窄表** —— 没量过的东西不替它签字。')
console.log('★ 即便实测 ≤4 列也仍是启发式：4 列的宽表格（金额/日期/操作）仍可能撑破，')
console.log('  真结论要靠 device-fit 在登录态下跑一次实际渲染。')
if (componentNameCollisions().length) {
  console.log(`⚠️  同名组件（索引取「任一份能横滚」）：${componentNameCollisions().join(', ')}`)
}

if (OUT) {
  writeFileSync(OUT, JSON.stringify({ total, buckets }, null, 2))
  console.log(`\n已写出：${OUT}`)
}

// ── 自检模式：把某文件的分桶判据逐条打出来（用于「判据无牙」时定位）──
const dbgIdx = args.indexOf('--debug')
if (dbgIdx >= 0) {
  const target = args[dbgIdx + 1]
  const p = join(SRC, target)
  const t = readFileSync(p, 'utf8')
  console.log(`\n=== debug ${target} ===`)
  console.log('isCardified      :', isCardified(t))
  console.log('内联 style 横滚   :', /style="[^"]*overflow(?:-x|-y)?\s*:\s*(auto|scroll)/.test(t))
  const oc = new Set()
  for (const m of t.matchAll(/\.([A-Za-z0-9_-]+)\s*\{([^}]*)\}/g))
    if (/overflow(?:-x|-y)?\s*:\s*(auto|scroll)/.test(m[2])) oc.add(m[1])
  console.log('CSS 里声明 overflow 的类:', [...oc])
  console.log('引用的表格组件    :', referencedTableComponents(t))
  for (const c of referencedTableComponents(t)) {
    const rels = componentIndex().get(c) || []
    console.log(`  ${c} => ${rels.join(', ') || '未找到'}  内含横滚=${rels.length ? componentHasHorizontalScroll(c) : 'n/a'}`)
  }
  console.log('hasScrollContainer:', hasScrollContainer(t))
  console.log('declaresMinWidth  :', declaresMinWidth(t))
  console.log('narrowTableVerdict:', narrowTableVerdict(t), '（narrow=实测≤4列 / unknown=测不到 / wide=实测≥5列）')
}
process.exit(0)
