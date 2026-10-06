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
 * 扫描根目录。默认是 `web/`；`--root` 可指向别的应用，例如移动端 Hyper：
 *   node scripts/compact-table-census.mjs --root ../web-mobile/src
 * ⚠️ `--root` 必须在**任何扫描发生前**求值（分桶循环之前），
 *    否则会拿着上一次的根目录去跑 —— 那是「同一个值被两套口径消费」的同款坑。
 */
const args = process.argv.slice(2)
const rootIdx = args.indexOf('--root')
const ROOT = rootIdx >= 0 && args[rootIdx + 1] ? resolve(process.cwd(), args[rootIdx + 1]) : SRC
/**
 * 归一：统一成正斜杠。**保留 `src/` 前缀**。
 *
 * ⚠️ 这里曾经剥掉 `^src/`，而 `SRC` 本身就是 web 根 ⇒ 剥完再 `join(SRC, rel)`
 * 会指错一层（`web/components/...`，真实路径是 `web/src/components/...`）。
 * 当时之所以没炸，是主循环读文件用的是未归一的 `relRaw`、只有记桶用 `rel`，
 * 于是**同一个 rel 在同一段代码里被两套口径消费**，读和记指向不同文件。
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
  for (const f of walkAll(dir)) if (f.endsWith('.vue')) yield f
}

/**
 * 遍历**所有**文件（不只 .vue）。
 * ⚠️ 别拿 `walk()` 干这活：它只 yield `.vue`，
 *    早先 `globalCssIndex()` 用了它 ⇒ 一条 .css 都没进索引，
 *    「全局样式表横滚容器」桶恒为 0，而症状是「看起来正常的 0」——
 *    量具失明长得和「确实没有」一模一样。
 */
function* walkAll(dir) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name === '.git') continue
    const full = join(dir, name)
    if (statSync(full).isDirectory()) yield* walkAll(full)
    else yield full
  }
}

/**
 * 本文件是否**由作者声明**了横向滚动容器。
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
 *
 * ★ **本函数只认「作者声明过」的容器**。组件库**原生自带**的横滚不在这里判，
 *   归 `hasNativeTableScroll()` 单独一桶 —— 把两者混算，
 *   「作者做了适配」和「框架白送的」就分不开了。
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

/**
 * 组件库**原生自带**的横向滚动（本仓目前只认 Element Plus 的 `el-table`）。
 *
 * ★ 上一轮把这层漏掉，报出 4 个「10 列 / 9 列无横滚容器」的**假阳性**。
 *   查 element-plus 2.14.3 实现，证据链三环（源码，非记忆、非文档转述）：
 *   ① `table.vue_..._lang.mjs` 把 `bodyWrapper` 用 `ElScrollbar` 包住：
 *      `createVNode(ElScrollbar, { ref: scrollBarRef, "wrap-style": scrollbarStyle, ... })`
 *   ② `theme-chalk/el-scrollbar.css`：`.el-scrollbar__wrap{height:100%;overflow:auto}`
 *   ③ `style-helper.mjs`：`tableBodyStyles = { width: layout.bodyWidth + 'px' }`
 *      —— 内层 `<table>` 宽度 = 列宽总和，列宽超过容器即触发 wrap 横向滚动。
 *   ⇒ `el-table` 天然能横滚，**页面不必自己声明滚动容器**。
 *
 * ⚠️ 这只证明「表格体可横滚」。作者若额外写了 `max-height` + 固定列，
 *   或用 `flexible` 关掉了自适应，行为可能不同 —— 属运行时细节，
 *   要证伪仍需 device-fit 在登录态实测。
 */
function hasNativeTableScroll(text) {
  return /<el-table\b/.test(text)
}

/**
 * ⑥ **全局样式表**里定义的滚动容器（此前完全看不见的一层）。
 *
 * ★ 本轮自查又翻出一个假阳性：`NodeDetailRequestsPanel.vue` 用
 *   `<div class="nd-table-wrap">` 包着 `<table>`，而 `.nd-table-wrap` 的
 *   `overflow: auto` 定义在**另一个文件** `src/styles/node-detail-drawer.css:134`。
 *   早先的判据只扫 .vue 自己的 `<style>` 块 ⇒ 把它误报进 unwrapped。
 *   同一批自查里，**移动端两个表格页（NodesView / UsageView）也都靠
 *   `web-mobile/src/styles/shared.css:227` 的 `.table-scroll` 才合规** ——
 *   若普查扫到 web-mobile，会一次报出两个假阳性。
 *
 * ⚠️ **只认真正全局生效的来源**：独立 `.css` 文件 + 各 .vue 里**未加 scoped**
 *   的 `<style>`。`<style scoped>` 编译后带 data 属性、**不跨组件生效**，
 *   拿别的组件的 scoped 类名来判是**同名巧合**（第一版探针就栽在这：
 *   `.card` 在别的组件里有 overflow，被误判成「已覆盖」）。
 *
 * 只查**表格外层最近 3 层**的祖先类名 —— 更外层的祖先通常不是滚动容器，
 * 认得越宽，假阳性越多。
 */
function hasGlobalCssScrollContainer(text) {
  const i = text.indexOf('<table')
  if (i < 0) return false
  const tags = [...text.slice(0, i).matchAll(/<([A-Za-z][\w-]*)([^<>]*?)\/?>/g)]
  const idx = globalCssIndex()
  for (let k = tags.length - 1; k >= 0 && k >= tags.length - 3; k--) {
    const cm = tags[k][2].match(/class="([^"]+)"/)
    if (!cm) continue
    for (const c of cm[1].split(/\s+/)) {
      const hit = idx.get(c)
      if (hit && /overflow(?:-x|-y)?\s*:\s*(auto|scroll)/.test(hit)) return true
    }
  }
  return false
}

/** 全局生效的 CSS：类名 → 规则体。懒建；scoped 的不收。 */
let GLOBAL_CSS = null
function globalCssIndex() {
  if (GLOBAL_CSS) return GLOBAL_CSS
  const map = new Map()
  const add = (text) => {
    const clean = text.replace(/\/\*[\s\S]*?\*\//g, '')
    for (const m of clean.matchAll(/\.([A-Za-z0-9_-]+)\s*\{([^}]*)\}/g)) map.set(m[1], m[2])
  }
  for (const abs of walkAll(ROOT)) {
    if (abs.endsWith('.css')) { add(readFileSync(abs, 'utf8')); continue }
    if (!abs.endsWith('.vue')) continue
    for (const m of readFileSync(abs, 'utf8').matchAll(/<style([^>]*)>([\s\S]*?)<\/style>/g)) {
      if (/\bscoped\b/.test(m[1])) continue
      add(m[2])
    }
  }
  GLOBAL_CSS = map
  return map
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

/** `<el-table>` 的实测列数：一个 `<el-table-column>` 就是一列。 */
function elTableColumnCount(text) {
  const el = text.match(/<el-table\b[\s\S]*?<\/el-table>/)
  return el ? (el[0].match(/<el-table-column\b/g) || []).length : 0
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
 *  ② Element Plus `<el-table>`：**一个 `<el-table-column>` 就是一列**。
 *  ③ 其它封装组件（`<DataTable :columns=…>` / slots 传列）：列数在 props 与运行时，
 *     源码测不到 ⇒ `unknown`，不并入窄表。
 *
 *   · `narrow`  —— 静态测到 1..4 列。
 *   · `unknown` —— 测不到。
 *   · `wide`    —— 静态测到 ≥5 列。
 *
 * ⚠️ 注意 ② 在**主分桶路径上已不可达**：`<el-table>` 页会被
 *   `hasNativeTableScroll()` 先截进「组件原生横滚」桶。它在这里仍有用，
 *   是因为 `--debug` 与下面的「组件原生横滚实测列数」清单共用它 ——
 *   别把列数信息丢掉，否则那 7 个页面的宽度风险就成了没量过的东西。
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
  const colCount = elTableColumnCount(text)
  if (colCount > 0) return colCount <= 4 ? 'narrow' : 'wide'
  return 'unknown'
}

const jsonIdx = args.indexOf('--json')
const OUT = jsonIdx >= 0 ? args[jsonIdx + 1] : null

const buckets = {
  'cards（compact 出卡片）': [],
  'degradation（作者声明的横滚容器）': [],
  '组件原生横滚（el-table）': [],
  '全局样式表横滚容器': [],
  'min-width 声明（待核容器）': [],
  'narrow（实测 ≤4 列窄表）': [],
  '列数不可静态测（组件式表格）': [],
  'unwrapped（待人判：既无横滚也非窄表）': [],
}

const files = [...walk(ROOT)].map(f => relative(ROOT, f)).sort()
for (const relRaw of files) {
  const rel = norm(relRaw)
  const text = readFileSync(join(ROOT, relRaw), 'utf8')
  if (!/<table\b|<el-table\b|<AppDataTable\b|<DataTable\b/.test(text)) continue

  const verdict = narrowTableVerdict(text)
  if (isCardified(text)) buckets['cards（compact 出卡片）'].push(rel)
  else if (hasScrollContainer(text)) buckets['degradation（作者声明的横滚容器）'].push(rel)
  else if (hasNativeTableScroll(text)) buckets['组件原生横滚（el-table）'].push(rel)
  else if (hasGlobalCssScrollContainer(text)) buckets['全局样式表横滚容器'].push(rel)
  else if (declaresMinWidth(text)) buckets['min-width 声明（待核容器）'].push(rel)
  else if (verdict === 'narrow') buckets['narrow（实测 ≤4 列窄表）'].push(rel)
  else if (verdict === 'unknown') buckets['列数不可静态测（组件式表格）'].push(rel)
  else buckets['unwrapped（待人判：既无横滚也非窄表）'].push(rel)
}

const total = Object.values(buckets).reduce((a, b) => a + b.length, 0)
console.log('=== compact 数据表覆盖普查（不判红，只出报表）===')
console.log(`扫描根：${ROOT}`)
console.log(`扫描 .vue ${files.length} 个；含表格 ${total} 个\n`)
for (const [k, v] of Object.entries(buckets)) {
  const full = k.startsWith('unwrapped') || k.startsWith('min-width') || k.startsWith('列数不可静态测') || k.startsWith('组件原生横滚')
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

// 组件原生横滚的那几页**能滚**不等于**窄**：它们照样是宽度风险最高的一批
// （10 列 / 9 列在小屏上照样挤成一团），所以把实测列数打出来。
const nativeList = buckets['组件原生横滚（el-table）']
if (nativeList.length) {
  console.log('\n—— 组件原生横滚页的实测列数（能滚 ≠ 不挤）——')
  const rows = nativeList.map((r) => ({ r, n: elTableColumnCount(readFileSync(join(ROOT, r), 'utf8')) }))
  rows.sort((a, b) => b.n - a.n)
  for (const { r, n } of rows) console.log(`    ${String(n).padStart(2)} 列  ${r}`)
}

if (OUT) {
  writeFileSync(OUT, JSON.stringify({ total, buckets }, null, 2))
  console.log(`\n已写出：${OUT}`)
}

// ── 自检模式：把某文件的分桶判据逐条打出来（用于「判据无牙」时定位）──
const dbgIdx = args.indexOf('--debug')
if (dbgIdx >= 0) {
  const target = args[dbgIdx + 1]
  const p = join(ROOT, target)
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
