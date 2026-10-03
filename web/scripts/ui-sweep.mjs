/**
 * ui-sweep.mjs — 全路由运行时遍历扫描（2026-10-03）
 *
 * ui-audit.mjs 是静态判据：能证伪、不能证成。本脚本补上它证不了的那一半 ——
 * 在真浏览器里逐路由量一次：
 *
 *   1. console 错误 / 未捕获异常（老板要的「逐个页面检查前端 console 错误」）
 *   2. 失败请求（/api 4xx/5xx）
 *   3. **全屏**：页面根容器实际只占了主区宽度的百分之多少、是否左右等距居中
 *      —— 直接量「窄列居中」这个静态判据只能看声明值
 *   4. **逐字折行**：极窄却很高的文本元素（就是供应商表那个 bug 的签名）
 *   5. **内容裁切**：overflow:hidden 但内容比盒子高，且不可滚
 *
 * 用法：
 *   GATEWAY_API_TARGET=… GATEWAY_DEV_AUTH_TOKEN=… npm run dev   # 另开一个终端
 *   node scripts/ui-sweep.mjs                       # 扫 dev
 *   BASE_URL=http://127.0.0.1:5793 node scripts/ui-sweep.mjs
 *   node scripts/ui-sweep.mjs --json reports/ui-sweep.json
 *   node scripts/ui-sweep.mjs --only /providers,/tenants
 *   node scripts/ui-sweep.mjs --strict               # 有 P0/P1 时 exit 1
 *
 * 鉴权：走 extraHTTPHeaders 注入 Authorization，**不碰 cookie / localStorage**。
 * 路由清单从 src/router.ts 直接解析，不另维护一份（避免清单漂移）。
 *
 * ⚠ 跑之前先把工作树和进程都稳住，别在这期间干别的事。两次间歇性
 * `/api/auth/me` 404 都是在「扫描期间改了工作树」或「同时开了第二个浏览器」
 * 时出现的，受控复现 15/15 全绿：
 *   - 改工作树 → vite 触发 HMR 重载，页面正在 bootstrap 时模块被换掉，
 *     请求可能落到半路；
 *   - 两个 Playwright 打同一个 dev server + 同一个后端，互相挤。
 * 这类干扰出来的失败与产品缺陷在报告上长得一模一样（都是一条 console 错误），
 * 所以**发现孤立的单页 4xx 时，先在无干扰条件下单独复跑该页再下结论**。
 *
 * ⚠ 前置条件：**网关和 ai-native-maintain 都必须在跑**。
 * AppNavDrawer 是全局组件，挂在它上面的 `/maintain-api/healthz` 与
 * `/maintain-api/menu/ops` 一旦代理不到（maintain 没起 → ECONNREFUSED → 500），
 * **每一页**都会带上两条 console 错误，整个报告变成 4/4 全红 —— 看起来像
 * 回归，其实是被我停掉了一个依赖服务。2026-10-03 踩过：清理环境时顺手
 * pkill 了 maintain，下一轮弹层遍历四个用例齐红。
 * 本地起法（密钥必须与网关一致，否则 maintain 拒收网关 token → 401）：
 *   MAINTAIN_JWT_SECRET=<网关的 LLM_GATEWAY_JWT_SECRET 回退 LLM_GATEWAY_SECRET_KEY> \
 *   MAINTAIN_LISTEN_ADDR=127.0.0.1:8082 go run ./cmd/maintain
 */

import { readFileSync, writeFileSync, mkdirSync, readdirSync } from 'node:fs'
import { resolve, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(HERE, '..')

// ── 解析 playwright ────────────────────────────────────────────────
// web/ 没有装 playwright（不想为一次审计给产品加依赖），但本机 npx 缓存
// 与兄弟项目 maintain-web 里都有。
//
// 两个坑，都是「静默失效」型：
//  ① ESM 里写 require('node:fs') 必抛 ReferenceError，被外层 catch 吞掉
//     ⇒ npx 缓存目录一个都没扫到，只剩硬编码路径在兜底。
//  ② `require(p)` 成功 ≠ 这个 playwright 能跑：它的 chromium 修订号可能没装。
//     典型现场：三个 npx 缓存里分别是 1.61/1.63/1.64-alpha，本机只下了
//     chromium build 1223/1243，1.64-alpha 要 1224 ⇒ launch 时才炸。
//     所以候选的可用性判据是「**能不能真启动一个浏览器**」，不是「能不能 require 到」。
function playwrightCandidates() {
  const roots = []
  const npxRoot = join(process.env.HOME || '', '.npm', '_npx')
  let entries = []
  try {
    entries = readdirSync(npxRoot)
  } catch (e) {
    if (e.code !== 'ENOENT') throw e
  }
  for (const d of entries) roots.push(join(npxRoot, d, 'node_modules', 'playwright'))
  roots.push(
    '__DEV_HOME__/workspace/ai-native-tools/llm-gateway/ai-native-maintain/maintain-web/node_modules/playwright',
  )
  return roots
}

function installedBrowserRevisions() {
  const cache = join(process.env.HOME || '', 'Library', 'Caches', 'ms-playwright')
  try {
    return readdirSync(cache).filter((d) => d.startsWith('chromium'))
  } catch {
    return []
  }
}

// 逐个候选真启动一次，返回第一个能用的。
async function launchBrowser() {
  const tried = []
  for (const p of playwrightCandidates()) {
    let chromium
    try {
      chromium = createRequire(import.meta.url)(p).chromium
    } catch (e) {
      tried.push(`${p} → require 失败（${e.code || e.message.slice(0, 50)}）`)
      continue
    }
    try {
      const browser = await chromium.launch({ headless: true })
      return { browser, from: p }
    } catch (e) {
      tried.push(`${p} → launch 失败（${String(e.message).split('\n')[0].slice(0, 90)}）`)
    }
  }
  // 全部失败时报出**逐个路径 + 失败原因 + 本机已装的浏览器修订**，
  // 不要只说「找不到」——否则下一次同类问题又要从头猜。
  throw new Error(
    `没有可用的 playwright。试过：\n  - ${tried.join('\n  - ') || '（无候选路径）'}` +
      `\n本机已安装的浏览器：${installedBrowserRevisions().join(', ') || '（无）'}` +
      '\n若版本对不上，跑 `npx playwright install chromium` 补齐。',
  )
}

// ── 动态详情页：把 :param 换成**真实 ID** ──────────────────────────
//
// 2026-10-03 的复核缺口：原来这里 `if (path.includes(':')) continue` 一刀切，
// 于是 13 个详情页（/providers/:id、/tenants/:tenantId、/keys/:id …）**从来没被扫过**。
// 而详情页恰恰是抽屉和内嵌表格最密的地方 —— 本轮补的八处名称排序有六处就在详情页里。
// 「扫不到」在报告上和「扫过没问题」长得一样，所以必须补。
//
// ID 不写死：写死的 ID 换台环境就全失效。这里**调真实 API 取一个**，
// 取不到就明确报告「本地无该实体的真实 ID」——不静默丢弃。
// 每条都注明 ID 来自哪个端点，便于复核。
const DYNAMIC_ROUTES = [
  { path: '/providers/:id', comp: 'ProviderDetailView', from: '/api/providers', pick: (j) => j?.[0]?.id },
  { path: '/routing-v2/work-types/:key', comp: 'WorkTypesView', from: '/api/admin/work-types?include_disabled=true', pick: (j) => j?.[0]?.key },
  { path: '/tenants/:tenantId', comp: 'TenantDetailView', from: '/api/admin/tenants', pick: (j) => j?.[0]?.code },
  { path: '/keys/:id', comp: 'KeyDetailView', from: '/api/keys', pick: (j) => j?.[0]?.id },
  { path: '/request-detail/:requestId', comp: 'RequestDetailFullscreenView', from: '/api/logs?limit=1', pick: (j) => j?.items?.[0]?.request_id },
  { path: '/admin/request-registry/journey/:requestId', comp: 'RequestJourneyDetailView', from: '/api/logs?limit=1', pick: (j) => j?.items?.[0]?.request_id },

  // ⚠ 分析类的三个详情页，ID 必须取自**它们自己的列表端点**，不能从 request_logs 猜。
  // 我第一版用 `request_logs.virtual_client_id` 当 client_id，结果详情页稳定 404 ——
  // 那不是产品缺陷，是**我取的 ID 空间不对**（列表里叫 `claude-opus-4-8` 这种）。
  // 假阳性比漏报更坏：它会让下轮去查一个不存在的 bug。
  // 响应键是 `clients` / `tasks` / `users`，**不是** items —— 我第一版按 items 取，
  // 三个分析页全部「取不到 ID」被跳过。跳过是安全的（不静默丢），但覆盖率白丢。
  { path: '/admin/session-analytics/clients/:id', comp: 'ClientAnalyticsView', from: '/api/admin/session-analytics/clients?limit=1', pick: (j) => j?.clients?.[0]?.client_id || j?.items?.[0]?.client_id },
  { path: '/admin/session-analytics/tasks/:id', comp: 'TaskAnalyticsView', from: '/api/admin/session-analytics/tasks?limit=1', pick: (j) => j?.tasks?.[0]?.task_id || j?.items?.[0]?.task_id },
  { path: '/admin/session-analytics/users/:owner', comp: 'UserProfileView', from: '/api/admin/session-analytics/users?limit=1', pick: (j) => j?.users?.[0]?.owner_user || j?.items?.[0]?.owner_user },

  { path: '/admin/sessions/:id', comp: 'SessionDetailView', from: '/api/logs?limit=1', pick: (j) => j?.items?.map((r) => r.gw_session_id).find(Boolean) },
  // 凭据没有全局列表端点（/api/credentials/ 是详情路由，直接打会 400
  // "invalid credential path"），要经 provider 取：/api/providers → 取一个 id
  // → /api/providers/{id}/credentials。
  // 下面两条本地大概率取不到 ID（MaaS 订单本地为空、审批队列本地为空），
  // 但**照样登记**：有数据的环境就能自动扫到，本地取不到会在报告里点名。
  // 不登记 = 换台环境就静默少扫两个页面。
  // ⚠ 必须用**租户维度**的列表端点，不能用 /api/admin/maas/orders：
  // 后者跨租户，页面调的是 /api/maas/orders/{id}（租户内）——
  // 拿 admin 列表的 ID 去打租户端点，必然 404 **order not found**。
  // 那是租户隔离的正确行为，不是缺陷；是**我取的 ID 空间不对**。
  { path: '/tenant/orders/:id', comp: 'MaaSOrderView', from: '/api/maas/orders?limit=1', pick: (j) => j?.items?.[0]?.id },
  { path: '/admin/approvals/:id', comp: 'ApprovalDetailView', from: '/api/admin/approvals?limit=1', pick: (j) => j?.items?.[0]?.id },
  { path: '/admin/connection-registry/:credentialId/recovery', comp: 'NodeHealthTimelineView',
    chain: [
      { from: '/api/providers', pick: (j) => j?.[0]?.id },
      { from: (id) => `/api/providers/${id}/credentials`, pick: (j) => j?.[0]?.id },
    ] },
]

/** --only 里是否点名了这条动态路由（支持 /providers/:id 这种写法，也支持前缀）。 */
function wantPath(argv, dynPath) {
  if (!argv.includes('--only')) return true
  const want = (argv[argv.indexOf('--only') + 1] || '').split(',').map((s) => s.trim())
  return want.some((w) => w === dynPath || dynPath.startsWith(w) || w.startsWith(dynPath))
}

async function resolveDynamicRoutes(base, token) {
  const out = []
  const skipped = []
  const getJSON = async (url) => {
    const r = await fetch(url, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
    if (!r.ok) throw new Error(`HTTP ${r.status}`)
    return r.json()
  }
  for (const d of DYNAMIC_ROUTES) {
    let id = null
    let why = ''
    const sources = []
    try {
      if (d.chain) {
        // 链式取 ID：上一步的 ID 拼进下一步的端点（凭据要经 provider 才能拿到）
        let cur = null
        for (const step of d.chain) {
          const url = base + (typeof step.from === 'function' ? step.from(cur) : step.from)
          sources.push(url.replace(base, ''))
          const j = await getJSON(url)
          cur = step.pick(j)
          if (cur === null || cur === undefined || cur === '') {
            why = `链式解析在 ${url.replace(base, '')} 处取不到 ID`
            break
          }
        }
        id = cur
      } else {
        const url = base + d.from
        sources.push(d.from)
        const j = await getJSON(url)
        id = d.pick(j)
        if (id === null || id === undefined || id === '') why = `端点 ${d.from} 里取不到可用 ID（本地无该实体的数据）`
      }
    } catch (e) {
      if (!why) why = `取 ${sources[sources.length - 1] || d.from} 失败：${String(e.message || e).slice(0, 80)}`
    }
    if (id) {
      out.push({
        path: d.path.replace(/:\w+/, encodeURIComponent(String(id))),
        comp: d.comp,
        dynamic: true,
        sampleId: String(id),
        from: sources.join(' → '),
      })
    } else {
      skipped.push({ path: d.path, comp: d.comp, why })
    }
  }
  return { out, skipped }
}

// ── 路由清单：直接从 router.ts 解析 ────────────────────────────────
function parseRoutes() {
  const src = readFileSync(join(WEB, 'src', 'router.ts'), 'utf8')
  const out = []
  const dynamic = []
  const re = /\{\s*path:\s*'([^']+)'[^}]*?component:\s*([A-Za-z0-9_]+)/g
  let m
  while ((m = re.exec(src))) {
    let [, path, comp] = m
    // 动态参数页单独收集：它们要用真实 ID 拼出可访问的 URL（见 DYNAMIC_ROUTES），
    // 不能像从前那样直接 continue —— 那样 13 个详情页就永远不在报告里。
    if (path.includes(':') && path !== '/:pathMatch(.*)*') {
      dynamic.push({ path, comp })
      continue
    }
    if (path === '/:pathMatch(.*)*') continue
    if (/LoginView|ForbiddenView|CustomerUpdateActivateView|CustomerOfflineActivationView|BootstrapWizardView|MaintainUnavailableView|DispatchWaterfallPreview/.test(comp)) continue
    if (path === '/login' || path === '/forbidden' || path === '/bootstrap') continue
    if (path.startsWith('/customer/') || path.startsWith('/maintain') || path.startsWith('/dev/')) continue
    out.push({ path, comp })
  }
  // router.ts 里出现、但 DYNAMIC_ROUTES 没登记的动态页 → 报告里点名，
  // 免得下轮以为「表里没有 = 没有这种页」。
  const known = new Set(DYNAMIC_ROUTES.map((d) => d.path))
  // /maintain/:pathMatch(.*)* 刻意不登记：它渲染的是 MaintainUnavailableView
  // ——「maintain 不可用」的占位页，不是业务详情页。上面那个组件过滤也排除了它。
  const INTENTIONAL = new Set(['/maintain/:pathMatch(.*)*'])
  out.unresolved = dynamic.filter((d) => !known.has(d.path) && !INTENTIONAL.has(d.path))
  // 带重定向函数的行（如 /catalog → /models）由 re 抓不到，忽略：
  // 它们最终会落到已扫的目标页上。
  return out
}

// ── 页面内检查（跑在浏览器上下文里）───────────────────────────────
//
// ⚠ 作用域必须与 App.vue 的真实骨架一致：主区是
//   <section class="main-body"><RouterView/></section>   （App.vue:222）
//   **不是** <main> 标签。这条曾写成 querySelectorAll('main *')，
//   在真应用里匹配到 0 个元素 ⇒ 折行/裁切两个探针在全部页面上恒为空，
//   扫描报告却显示「0 违规」。空结果与「没有问题」在输出上完全一样。
//   ⇒ ① 作用域写死为 .main-body；② 探针回报 scanned 元素数，
//     判级时若为 0 直接判 P0（判据失效），绝不当作干净页。
const PAGE_PROBE = () => {
  const out = { fullWidth: null, verticalText: [], clipped: [], rootClass: '', rootWidth: 0, bodyWidth: 0, scanned: 0 }
  const body = document.querySelector('.main-body') || document.querySelector('main')
  if (!body) return out
  const bRect = body.getBoundingClientRect()
  out.bodyWidth = Math.round(bRect.width)

  // 1) 全屏：根容器实际占主区多少宽、是否左右等距
  const main = body.querySelector(':scope > *')
  if (main) {
    const r = main.getBoundingClientRect()
    out.rootClass = (typeof main.className === 'string' ? main.className : '').split(/\s+/)[0] || ''
    out.rootWidth = Math.round(r.width)
    const ratio = bRect.width > 0 ? r.width / bRect.width : 1
    const padLeft = r.left - bRect.left
    const padRight = bRect.right - r.right
    // 居中：左右留白接近相等，且明显窄于主区
    const centered = Math.abs(padLeft - padRight) < 24 && padLeft > 40
    out.fullWidth = { ratio: Math.round(ratio * 100) / 100, centered, padLeft: Math.round(padLeft), padRight: Math.round(padRight) }
  }

  const all = body.querySelectorAll('*')
  out.scanned = all.length
  // 可见文本量：「干净」与「空壳」在违规清单上长得一样（都是 0 条）。
  // 页面若只有骨架没有数据，scanned 可能不为 0 但 textLen 极小 ——
  // 把它一并带出来，报告才分得清「没违规」和「没内容」。
  out.textLen = (body.innerText || '').trim().length
  out.visibleBlocks = [...body.querySelectorAll('table,tbody,ul,ol,section,article,form')].filter((e) => e.clientHeight > 0).length

  // 2) 逐字折行签名：极窄 + 很高 + 含多行文本。
  //    供应商表那个 bug 的可测形态：列宽被压到 ~30px，文字按字符竖排。
  for (const el of all) {
    if (el.children.length > 2) continue
    const text = (el.textContent || '').trim()
    if (!text || text.length < 4) continue
    const w = el.clientWidth
    const h = el.clientHeight
    if (w > 0 && w < 44 && h > w * 2.2 && h > 90) {
      const cs = getComputedStyle(el)
      if (cs.overflowY === 'auto' || cs.overflowY === 'scroll') continue
      out.verticalText.push({
        tag: el.tagName.toLowerCase(),
        cls: (typeof el.className === 'string' ? el.className : '').slice(0, 60),
        w, h, text: text.slice(0, 30),
      })
      if (out.verticalText.length >= 8) break
    }
  }

  // 3) 内容裁切：overflow hidden + 内容比盒子高 + 自己不可滚
  for (const el of all) {
    const cs = getComputedStyle(el)
    const clips = cs.overflow === 'hidden' || cs.overflowY === 'hidden'
    if (!clips) continue
    if (el.scrollHeight > el.clientHeight + 8 && el.clientHeight > 0) {
      const scrollable = cs.overflowY === 'auto' || cs.overflowY === 'scroll'
      if (scrollable) continue
      out.clipped.push({
        tag: el.tagName.toLowerCase(),
        cls: (typeof el.className === 'string' ? el.className : '').slice(0, 60),
        clientH: el.clientHeight, scrollH: el.scrollHeight,
      })
      if (out.clipped.length >= 8) break
    }
  }
  return out
}

// 弹层探针。**按几何/定位找，不按 class 名找。**
//
// 2026-10-03 踩的坑，正是「判据只覆盖了它认识的名字」：
//   SessionSummaryDrawer 的根是 `.ssd-backdrop` / `.ssd-panel` ——
//   **两个 class 都不含 drawer / modal / dialog**。旧探针的选择器是
//   `[class*="drawer"],[class*="modal"],[class*="dialog"]`，于是这个抽屉
//   明明被点开了，探针报「未出现弹层」。在报告里它长得和「点击没反应」
//   一模一样 —— 而真相是判据瞎了。
//   教训与「页面探针扫 `main *` 而主区是 section」同族：**判据的元素集合
//   必须先证明覆盖了全集**，否则「没找到」会被写成「不存在」。
//
// 所以改成：position 为 fixed/absolute + 尺寸够大 + 可见 ⇒ 候选；
// 逐个记录 key（class + 矩形），调用方拿「点击前 / 点击后」两份快照做差，
// **只有新出现的才算这次触发打开的弹层**。这一层差分还顺手挡掉了
// 「页面上常驻的 fixed 元素（全局导航抽屉等）被误认成弹层」。
//
// 每个候选顺带算出 depthInCands（在其它候选里的祖先个数），
// 便于调用方取最外层 —— 旧探针两次选错对象（量到遮罩、量到 .drawer-body）
// 都是因为「用尺寸近似弹层本体」。
const OVERLAY_PROBE = () => {
  const vw = window.innerWidth
  const vh = window.innerHeight
  const sizeOk = (r) => r.width >= 120 && r.height >= 120
  const shown = (el) => {
    const cs = getComputedStyle(el)
    if (cs.display === 'none' || cs.visibility === 'hidden' || Number(cs.opacity) <= 0.05) return false
    return sizeOk(el.getBoundingClientRect())
  }
  const positioned = []
  for (const el of document.querySelectorAll('body *')) {
    const cs = getComputedStyle(el)
    if (cs.position !== 'fixed' && cs.position !== 'absolute') continue
    if (!shown(el)) continue
    positioned.push(el)
  }
  // 铺满视口的定位层 = 遮罩。**面板本体经常不是定位元素**：
  // ModelPicker / ChangePasswordDialog 这类居中弹窗是「fixed 遮罩 + flex 居中的
  // 静态盒子」，盒子本身 position:static。只收定位元素 ⇒ 候选里只剩遮罩，
  // 于是量出 1440px 的「面板宽度」，恒判绿（本轮第一次改探针就踩了这个）。
  // 所以再把**铺满层内部**尺寸够大的后代也收进来，真正的面板就在里面。
  const overlayRoots = positioned.filter((el) => {
    const r = el.getBoundingClientRect()
    return r.width >= vw * 0.95 && r.height >= vh * 0.95
  })
  const cands = [...positioned]
  for (const root of overlayRoots) {
    for (const el of root.querySelectorAll('*')) {
      if (positioned.includes(el)) continue
      if (shown(el)) cands.push(el)
    }
  }

  const cset = new Set(cands)
  const byEl = new Map(cands.map((el) => [el, `${(typeof el.className === 'string' ? el.className : '').slice(0, 60)}|${Math.round(el.getBoundingClientRect().left)}|${Math.round(el.getBoundingClientRect().top)}|${Math.round(el.getBoundingClientRect().width)}|${Math.round(el.getBoundingClientRect().height)}`]))
  return cands.map((el) => {
    const r = el.getBoundingClientRect()
    const cls = typeof el.className === 'string' ? el.className : ''
    // 只记**最近的候选祖先**的 key。深度不在这里算 —— 因为「遮罩」本身也是候选，
    // 若在这里按全集算深度，遮罩的每个后代深度都 ≥1，取最外层时会一个都选不出来
    // （本轮踩过：.drawer 和 .drawer-body 双双 depth≥1，结果选中了 .drawer-body）。
    let parentKey = null
    for (let p = el.parentElement; p; p = p.parentElement) {
      if (cset.has(p)) { parentKey = byEl.get(p); break }
    }
    const panelOverflows = el.scrollHeight > el.clientHeight + 8
    const scrollers = [el, ...el.querySelectorAll('*')].filter((c) => {
      const cs = getComputedStyle(c)
      return (cs.overflowY === 'auto' || cs.overflowY === 'scroll') && c.scrollHeight > c.clientHeight + 8
    })
    return {
      key: byEl.get(el),
      parentKey,
      cls,
      w: Math.round(r.width), h: Math.round(r.height),
      isBackdrop: r.width >= vw * 0.95 && r.height >= vh * 0.95,
      textLen: (el.innerText || '').trim().length,
      // 内容放不下时必须有可滚路径，否则底部字段看不到 —— 老板说的「显示内容必须完整」
      scrollable: scrollers.length > 0 || !panelOverflows,
    }
  })
}

// 从「点击前 / 点击后」两份快照里挑出这次新打开的弹层面板。
// 取**非遮罩候选里最外层**的那个（沿 parentKey 上溯，池内没有祖先者）——
// 旧探针两次选错对象（量到遮罩、量到 .drawer-body）都是因为用尺寸近似本体。
// 深度必须在**排除遮罩之后**才算，否则遮罩会把每个后代都压成 depth≥1。
export function pickOpenedPanel(before, after) {
  const seen = new Set(before.map((c) => c.key))
  const fresh = after.filter((c) => !seen.has(c.key))
  if (!fresh.length) return null
  const nonBackdrop = fresh.filter((c) => !c.isBackdrop)
  const pool = nonBackdrop.length ? nonBackdrop : fresh
  const poolKeys = new Set(pool.map((c) => c.key))
  const outermost = pool.filter((c) => {
    for (let k = c.parentKey; k; k = pool.find((p) => p.key === k)?.parentKey) {
      if (poolKeys.has(k)) return false
    }
    return true
  })
  const chosen = (outermost.length ? outermost : pool)
    .sort((a, b) => b.w * b.h - a.w * a.h)[0]
  return { ...chosen, pickedBackdrop: nonBackdrop.length === 0 }
}

// OVERLAY_PROBE 的判别力检查。与页面探针分开跑：两者的失败模式不一样——
// 页面探针曾经「扫不到元素」而恒绿，弹层探针曾经「选错对象」而恒绿。
// 夹具都按「点击前 / 点击后」两份快照组织，和真实调用方式一致。
const OVERLAY_FIXTURES = [
  {
    name: '右侧抽屉：必须量到抽屉本体（800px），不能量铺满视口的遮罩',
    before: '',
    after: `<div class="drawer-overlay"><div class="drawer"><div class="drawer-body">${'抽屉内容行<br>'.repeat(6)}</div></div></div>
      <style>
        .drawer-overlay{position:fixed;inset:0;background:rgba(0,0,0,.4)}
        .drawer{position:absolute;right:0;top:0;width:800px;height:100%;background:#fff}
        .drawer-body{padding:12px}
      </style>`,
    expect: { found: 'drawer', notBackdrop: true, wideEnough: true },
  },
  {
    name: '内容超出面板且面板自身不可滚 → 必须判「看不完整」',
    before: '',
    after: `<div class="drawer-overlay"><div class="drawer"><div class="drawer-body">${'很长的一行内容<br>'.repeat(80)}</div></div></div>
      <style>
        .drawer-overlay{position:fixed;inset:0;background:rgba(0,0,0,.4)}
        .drawer{position:absolute;right:0;top:0;width:800px;height:300px;background:#fff;overflow:hidden}
        .drawer-body{padding:12px}
      </style>`,
    expect: { found: 'drawer', notBackdrop: true, scrollable: false },
  },
  {
    name: '内容超出但 body 可滚 → 必须判「可滚」（与上一条成对）',
    before: '',
    after: `<div class="drawer-overlay"><div class="drawer"><div class="drawer-body">${'很长的一行内容<br>'.repeat(80)}</div></div></div>
      <style>
        .drawer-overlay{position:fixed;inset:0;background:rgba(0,0,0,.4)}
        .drawer{position:absolute;right:0;top:0;width:800px;height:300px;background:#fff;overflow:hidden}
        .drawer-body{padding:12px;height:100%;overflow-y:auto}
      </style>`,
    expect: { found: 'drawer', notBackdrop: true, scrollable: true },
  },
  {
    // ★ 这条是本轮真正的判据缺口：class 里没有 drawer/modal/dialog 三个词，
    // 旧的按名字选元素的探针对它完全瞎。抽屉**确实被点开了**，旧探针却报
    // 「未出现弹层」——在报告里和「点击没反应」长得一模一样。
    name: 'class 名里没有 drawer/modal/dialog 的抽屉（ssd-*）也必须量到',
    before: '',
    after: `<div class="ssd-backdrop" style="position:fixed;inset:0;background:rgba(0,0,0,.4)">
        <aside class="ssd-panel card" style="position:absolute;right:0;top:0;width:900px;height:100%;background:#fff">
          ${'会话总结正文<br>'.repeat(8)}
        </aside>
      </div>`,
    expect: { found: 'ssd-panel', notBackdrop: true, wideEnough: true },
  },
  {
    // ★ 居中弹窗的常见形态：fixed 遮罩 + **flex 居中的静态盒子**（盒子 position:static）。
    // 只收「定位元素」的话候选里只剩遮罩 ⇒ 量出 1440px 的面板宽度并恒判绿。
    // 这是把探针从「按 class 名」改成「按几何」之后**新引入**的盲点，
    // 由本轮真实 ModelPicker（.mp-overlay + .mp-dialog）撞出来。
    name: '居中弹窗：面板本体是静态盒子时也要量到盒子（420px）而不是遮罩',
    before: '',
    after: `<div class="user-info-overlay" style="position:fixed;inset:0;background:rgba(0,0,0,.5);display:flex;align-items:center;justify-content:center">
        <div class="pwd-card" style="width:420px;height:300px;background:#fff;padding:16px">
          <h3>修改密码</h3>${'表单字段<br>'.repeat(4)}
        </div>
      </div>`,
    expect: { found: 'pwd-card', notBackdrop: true, wideEnough: true },
  },
  {
    // 反向对照：常驻的 fixed 元素在「点击前」就存在，不该被当成本次新开的弹层。
    // 没有这条差分，任何页面上的全局导航抽屉都会让每个 case 假绿。
    name: '反向对照：点击前就存在的 fixed 元素不算「新打开的弹层」',
    before: `<div class="app-nav-drawer" style="position:fixed;left:0;top:0;width:300px;height:100%;background:#eee">常驻导航</div>`,
    after: `<div class="app-nav-drawer" style="position:fixed;left:0;top:0;width:300px;height:100%;background:#eee">常驻导航</div>`,
    expect: { found: null },
  },
  {
    name: '反向对照：页面没弹层时不得凭空报一个（差分为空）',
    before: '',
    after: `<div class="main-body"><p>普通内容</p></div>`,
    expect: { found: null },
  },
]

async function runOverlaySelftest() {
  const { browser } = await launchBrowser()
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  let pass = 0
  const fails = []
  for (const fx of OVERLAY_FIXTURES) {
    // 真实调用方式是「点击前 / 点击后」两份快照，夹具按同一契约组织 ——
    // 判据的输入形状必须和真实调用一致，否则自检通过也说明不了什么。
    const page = await ctx.newPage()
    await page.setContent(fx.before)
    await page.waitForTimeout(120)
    const before = await page.evaluate(OVERLAY_PROBE)
    await page.setContent(fx.after)
    await page.waitForTimeout(120)
    const after = await page.evaluate(OVERLAY_PROBE)
    const picked = pickOpenedPanel(before, after)
    await page.close()

    const actual = {
      found: picked ? picked.cls.split(/\s+/)[0] : null,
      notBackdrop: picked ? !picked.pickedBackdrop : null,
      // 面板宽度必须落在「窄于视口」区间：铺满视口就说明选到遮罩了
      wideEnough: picked ? picked.w > 120 && picked.w < 1440 * 0.95 : null,
      scrollable: picked ? picked.scrollable : null,
    }
    for (const k of Object.keys(fx.expect)) {
      if (actual[k] === fx.expect[k]) pass++
      else fails.push(`${fx.name}\n    判据 ${k}：期望 ${JSON.stringify(fx.expect[k])}，实测 ${JSON.stringify(actual[k])}`)
    }
  }
  await browser.close()
  const total = OVERLAY_FIXTURES.reduce((n, f) => n + Object.keys(f.expect).length, 0)
  console.log(`\n══ 弹层探针自检 ${pass}/${total} ══`)
  for (const f of fails) console.log(`  ✗ ${f}`)
  if (fails.length) {
    console.error(`\n✗ 弹层探针自检未通过：${fails.length}/${total}，先修判据再测弹层。`)
    process.exit(1)
  }
  console.log(`✓ ${OVERLAY_FIXTURES.length} 个夹具，正负双向都能区分`)
}

// 报告里回显步骤：对象步骤要显示成 `fill(sel)=值` 这种可读形态，
// 直接 JSON.stringify 会把一行撑得很长，反而看不清这条 case 到底点了什么。
function fmtSteps(steps) {
  return steps
    .map((s) => {
      if (typeof s === 'string') return s
      if (s.click) return `click ${s.click}`
      if (s.fill) return `fill ${s.fill[0]}=${s.fill[1]}`
      if (s.select) return `select ${s.select[0]}=${s.select[1]}`
      if (s.wait) return `wait ${s.wait}`
      if (typeof s.waitMs === 'number') return `wait ${s.waitMs}ms`
      return JSON.stringify(s)
    })
    .join(' → ')
}

// ── 步骤驱动器 ────────────────────────────────────────────────────
// 2026-10-03：原来只有「按选择器点一下」一种动作，于是 NodeDetailDrawer 被标成
// manual —— 真因不是「harness 走不到」，而是**驱动器的动作集里没有「选模型」和
// 「点主按钮」这两类动作**。这类 manual 是驱动器的能力缺口，不是页面的不可测性，
// 混进报告里会让「没写」看起来像「测不了」。
//
// 动作集（每种都在 STEP_FIXTURES 里有正反对照）：
//   'sel'                     → 等可见 + 点击（历史形态，保持不变）
//   {click: 'sel'}            → 同上
//   {fill: ['sel', 'text']}   → 填输入框
//   {select: ['sel', 'val']}  → 选原生 <select>
//   {wait: 'sel'}             → 只等可见，不点（等异步渲染出来的表格）
//   {waitMs: n}               → 纯等待
// 未知动作类型必须抛错，不能静默跳过 —— 静默跳过会让「清单里写了 4 步」与
// 「实际只做了 1 步」在报告上长得一样。
async function runSteps(page, steps, stepGapMs = 600) {
  for (const [i, step] of steps.entries()) {
    const at = `第 ${i + 1}/${steps.length} 步`
    const loc = (sel) => page.locator(sel).first()
    if (typeof step === 'string') {
      await loc(step).waitFor({ state: 'visible', timeout: 8000 })
      await loc(step).click({ timeout: 8000 })
    } else if (step && typeof step === 'object') {
      if (step.click) {
        await loc(step.click).waitFor({ state: 'visible', timeout: 8000 })
        await loc(step.click).click({ timeout: 8000 })
      } else if (step.fill) {
        await loc(step.fill[0]).waitFor({ state: 'visible', timeout: 8000 })
        await loc(step.fill[0]).fill(step.fill[1], { timeout: 8000 })
      } else if (step.select) {
        await loc(step.select[0]).waitFor({ state: 'visible', timeout: 8000 })
        await loc(step.select[0]).selectOption(step.select[1], { timeout: 8000 })
      } else if (step.wait) {
        await loc(step.wait).waitFor({ state: 'visible', timeout: 15000 })
      } else if (typeof step.waitMs === 'number') {
        await page.waitForTimeout(step.waitMs)
      } else {
        throw new Error(`${at} 动作类型无法识别：${JSON.stringify(step)}`)
      }
    } else {
      throw new Error(`${at} 步骤既不是字符串也不是对象：${JSON.stringify(step)}`)
    }
    // 多步之间给下拉/弹层展开留时间
    await page.waitForTimeout(stepGapMs)
  }
}

// 步骤驱动器的判别力检查。**必须有反向对照**：一个永远「成功」的驱动器会让
// 后面所有 case 的 P0（触发失败）变成永远不会触发的死代码。所以这里显式验证
// 「选择器写错时驱动器确实会抛」。
const STEP_FIXTURES = [
  {
    name: 'click + fill + select 真的改变了页面状态（不是空跑）',
    html: `<button class="t-open" onclick="document.getElementById('pnl').style.display='block'">open</button>
      <div class="pnl" id="pnl" style="display:none;width:300px;height:200px;background:#eee">panel</div>
      <input class="t-q" oninput="document.getElementById('e1').textContent=this.value" />
      <select class="t-s" onchange="document.getElementById('e2').textContent=this.value">
        <option value="">--</option><option value="alpha">alpha</option><option value="beta">beta</option>
      </select>
      <span id="e1"></span><span id="e2"></span>`,
    steps: [
      '.t-open',
      { fill: ['.t-q', 'glm-4.6'] },
      { select: ['.t-s', 'beta'] },
    ],
    read: () => ({
      panelOpen: getComputedStyle(document.getElementById('pnl')).display !== 'none',
      q: document.getElementById('e1').textContent,
      s: document.getElementById('e2').textContent,
    }),
    expect: { panelOpen: true, q: 'glm-4.6', s: 'beta' },
  },
  {
    name: '反向对照：选择器不存在时驱动器必须抛（否则 P0 是死代码）',
    html: `<div class="t-open">x</div>`,
    steps: [{ click: '.t-does-not-exist' }],
    expectThrow: true,
  },
  {
    name: '反向对照：动作类型无法识别时必须抛（不能静默跳过这步）',
    html: `<div class="t-open">x</div>`,
    steps: [{ hover: '.t-open' }],
    expectThrow: true,
  },
  {
    name: 'wait 只等可见、不点（等异步表格渲染出来）',
    html: `<div class="t-late" style="display:none;width:200px;height:200px">late</div>
      <script>setTimeout(function(){document.querySelector('.t-late').style.display='block'},400)</script>`,
    steps: [{ wait: '.t-late' }],
    read: () => ({ shown: getComputedStyle(document.querySelector('.t-late')).display === 'block' }),
    expect: { shown: true },
  },
]

async function runStepSelftest() {
  const { browser } = await launchBrowser()
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const fails = []
  let pass = 0
  for (const f of STEP_FIXTURES) {
    const page = await ctx.newPage()
    await page.setContent(f.html)
    let threw = false
    let got = null
    try {
      await runSteps(page, f.steps, 120)
      if (f.read) got = await page.evaluate(f.read)
    } catch {
      threw = true
    }
    await page.close()
    if (f.expectThrow) {
      if (threw) pass++
      else fails.push(`${f.name}\n    判据 expectThrow：期望抛错，实际没抛 —— 驱动器会把写错的选择器当成功`)
      continue
    }
    if (threw) {
      fails.push(`${f.name}\n    判据：步骤执行抛错，期望它跑通（驱动能力不足或选择器写错）`)
      continue
    }
    for (const k of Object.keys(f.expect)) {
      if (got?.[k] === f.expect[k]) pass++
      else fails.push(`${f.name}\n    判据 ${k}：期望 ${JSON.stringify(f.expect[k])}，实测 ${JSON.stringify(got?.[k])}`)
    }
  }
  await browser.close()
  const total = STEP_FIXTURES.reduce((n, f) => n + (f.expectThrow ? 1 : Object.keys(f.expect).length), 0)
  console.log(`\n══ 步骤驱动器自检 ${pass}/${total} ══`)
  for (const f of fails) console.log(`  ✗ ${f}`)
  if (fails.length) {
    console.error(`\n✗ 步骤驱动器自检未通过：${fails.length}/${total}，先修驱动器再测弹层。`)
    process.exit(1)
  }
  console.log(`✓ ${STEP_FIXTURES.length} 个夹具，动作类型与反向对照都能区分`)
}

// ── 弹层遍历（--overlays）─────────────────────────────────────────
// 弹层不开就不可能被静态门判出来：不点开就量不到「内容看不完整」。
// 触发点用**显式清单**而不是「找看起来像按钮的东西去点」——
// 启发式点击会点出一堆无关的浮层，报告里全是噪声，而且没有鉴别力。
// 每条 case 都写死 route + 已核实的触发选择器，测不到就是真测不到。
//
// steps 支持多步（很多入口藏在下拉菜单里：先开菜单再点菜单项）。
const OVERLAY_CASES = [
  { path: '/dashboard', steps: ['.quick-btn'], kind: 'drawer', note: 'StatsDrawer 快捷入口' },
  { path: '/users', steps: ['tbody tr.row-click'], kind: 'drawer', note: 'UserDetailDrawer 点行打开' },
  {
    path: '/dashboard',
    steps: ['.user-menu__trigger', '.user-menu__dropdown .user-menu__item:nth-child(2)'],
    kind: 'modal',
    note: 'ChangePasswordDialog 用户菜单 → 修改密码（居中弹窗）',
  },
  {
    // ModelPicker 被多个页面复用（路由看板、模型管理、创建密钥…），自己就是
    // 一个居中弹窗，且带 200+ 项的热门模型列表 —— 内容能不能看全最值得量。
    path: '/routing-v2?tab=resolve',
    steps: ['.resolve-picker .mp-trigger'],
    kind: 'modal',
    note: 'ModelPicker 模型选择弹窗（居中，含热门模型列表）',
  },
  {
    // 2026-10-03：原来是 manual，理由写的是「harness 只能点可见元素、不能操作
    // 下拉选择」。复核后这个理由不成立 —— 候选表要的不是原生下拉，而是
    // 「开 ModelPicker → 点热门模型 → 点解析」三步，驱动器的动作集里没有
    // 「点弹窗里的条目」而已。补上动作后这里能真跑。
    // ModelPicker 自己是个居中弹窗，NodeDetailDrawer 叠在它关闭之后才开，
    // 所以这条 case 顺带验证「弹窗里开弹窗」的层级。
    path: '/routing-v2?tab=resolve',
    steps: [
      '.resolve-picker .mp-trigger',
      { wait: '.mp-dialog .mp-grid .mp-model' },
      { click: '.mp-dialog .mp-grid .mp-model' },
      { wait: '.resolve-controls .btn-primary' },
      { click: '.resolve-controls .btn-primary' },
      { wait: '.row-actions .btn-ghost' },
      { click: '.row-actions .btn-ghost' },
    ],
    kind: 'drawer',
    note: 'NodeDetailDrawer 候选行「明细」（选模型 → 解析 → 点明细）',
  },
  {
    // 2026-10-03 新增：会话总结抽屉。
    // 入口按钮（`.session-summary-entry`）在**页面上**不在抽屉里，被
    // canSummarizeSession 门住（gwSessionFilter 非空才渲染）。而
    // gwSessionFilter 的入口是行内那个 `.trace-link`（点它走 filterByTrace，
    // 同时带上 session）。所以顺序是：点会话链接 → 等入口出现 → 点入口。
    //
    // ★ 这条 case 是本轮判据缺口的发现现场：抽屉**确实被点开了**，但旧探针按
    // class 名（drawer/modal/dialog）选元素，而 `.ssd-backdrop`/`.ssd-panel`
    // 两个名字一个都不沾，于是报「未出现弹层」。改成按几何差分后才量到。
    //
    // 选择器用 `.request-log-row .trace-link` 而不是「点第 N 行」：这个选择器
    // **只会匹配到确实带 gw_session_id 的行**。于是「本地没有带会话的行」这种
    // 情况会诚实地超时报 P0，而不是悄悄点开一个没有会话的空抽屉判绿。
    path: '/request-logs',
    steps: [
      { wait: 'tbody tr.request-log-row .trace-link' },
      { click: 'tbody tr.request-log-row .trace-link' },
      { wait: '.session-summary-entry .btn-primary' },
      { click: '.session-summary-entry .btn-primary' },
    ],
    kind: 'drawer',
    note: 'SessionSummaryDrawer 会话总结（页面上「打开会话总结」入口，需先按会话筛选）',
    // 本地这个会话只有 1 条日志，服务端会回
    // 400「日志条数不足，至少需要 2 条同会话记录」。那是**正确的产品行为**，
    // 但它同时给了本 case 一个额外的正向检查：错误必须被显示出来。
    // 声明了 expectErrorSurfaced，业务 4xx 就从「P1 噪声」变成
    // 「错误有没有被正确呈现」的断言 —— 静默吞掉才会判红。
    expectErrorSurfaced: '.ssd-error',
  },
  {
    // ⚠ 不要把「点请求日志行」写成这里的 case —— 我第一版就是这么写的，判据红了，
    // 一度以为撞上产品缺陷。回源码才看清：RequestLogsView 的
    // `showDetail()` 走的是 `openRequestDetailPage(...)`，即**跳全屏详情页**
    // `/request-detail/:requestId`，不是开抽屉。那个页面由页面遍历覆盖。
    // 顺带记一笔：该页模板里的 `<RequestLogDrawer :request-id="activeRequestId">`
    // 里 activeRequestId 全文件只在 closeDetail() 被置回 null，从未被赋成
    // 真实 id ⇒ 这处绑定恒不渲染。属遗留接线，不是本次审计要修的范围，
    // 但别让下轮再照着它写 case。
    path: '/dashboard',
    steps: ['.live-stream-v2 [class*="row"]'],
    kind: 'drawer',
    note: 'RequestLogDrawer 实时流点请求',
    manual:
      '唯一入口在 DashboardViewV2 的 LiveRequestStreamV2（onOpenRequest → SwimLane 的 tile-click），' +
      '实时流只推送**进行中**的请求，泳道里没有 tile 就点不出来。\n' +
      '本地造不出这个前置状态，三条路都实测堵死：\n' +
      '  ① 业务请求：/api/keys 只回 key_prefix（sk-xxx****），拿不到明文 key；\n' +
      '  ② 供应商探测：POST /api/providers/2/probe-url 返回 200 但 reachable:false' +
      '（base_url 在本机不可达），**不产生 request_logs 行**；\n' +
      '  ③ 周期自检 worker：request_logs 里确有它的行，但最近一条也是数小时前的**已完成**请求。\n' +
      '所以这是「环境造不出前置状态」，不是「驱动器的动作不够」—— 两者要分清。\n' +
      '人工覆盖：给网关灌一次慢速 streaming 请求，趁它在飞时点泳道 tile。',
  },
  {
    path: '/routing-v2?tab=resolve',
    steps: ['.row-actions .btn-ghost'],
    kind: 'drawer',
    note: 'RouteIncidentDrawer 诊断工作台',
    manual:
      '触发点在 DashboardViewV2 的 LiveRequestStreamV2 里（SwimLane 的 @diagnose 事件），' +
      '与 RequestLogDrawer 同一个前置状态：实时流只推**进行中**的请求，' +
      '本地造不出（无明文 key + base_url 不可达，详见上一条的实测记录）。\n' +
      '人工覆盖：给网关灌一次慢速 streaming 请求，趁它在飞时点泳道里的诊断入口。',
  },
  {
    // 裁切风险已由**静态门**判定（不是「未验证」，也不是我读 CSS 读出来的）：
    // ui-audit 的契约 B 有一条「限高必须在自身子树里存在可滚元素」的结构判据，
    // 它能把 AttachmentManager 从待确认桶里摘出来（0 项）。
    // 三态实测：真实代码 → 0；抽掉 .json-block 的 overflow:auto → 1；
    //           把 .json-block 移出弹层 → 1（证明不是「同组件有 overflow 就算」）。
    // <style scoped>，`.modal` / `.json-block` 这两个通用类名不会外泄。
    //
    // 之所以仍是 manual：本地 request_logs 里 has_attachments=true 的行数为 **0**，
    // 「查看 JSONB」那个按钮根本不会渲染 —— 没有可点的入口。
    // 要自动覆盖得先造一条带附件的请求，那样判据反映的是数据生成器。
    // **限制**：静态门验的是「存在一条可滚路径」，**没有在真页面上量过**
    // 长 JSON 的实际滚动行为；本仓待确认桶为 0 也意味着这条规则只有
    // 「全部正确」一个观测点，误报率未知。
    path: '/admin/data-lifecycle',
    steps: [{ click: 'a.link' }],
    kind: 'modal',
    note: 'AttachmentManager JSONB 弹窗（max-height 80vh + body overflow:auto）',
    manual:
      '本地无带附件的请求（request_logs.has_attachments=true 计数为 0），' +
      '「查看 JSONB」入口不渲染，没有可点的按钮。' +
      '限高裁切风险已按 CSS 形态静态核对为无（见 case 上方注释），但**未在真页面上量过**。',
  },
]

async function runOverlaySweep() {
  const { browser, from } = await launchBrowser()
  console.log(`浏览器：${from}\n目标：${BASE}\n`)
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    extraHTTPHeaders: TOKEN ? { Authorization: `Bearer ${TOKEN}` } : {},
  })

  const results = []
  for (const [i, c] of OVERLAY_CASES.entries()) {
    // manual 的 case 仍然记进结果，但不尝试点击、不参与合格/不合格判定。
    if (c.manual) {
      results.push({ ...c, opened: null, p0: [], p1: [], skipped: true })
      process.stdout.write(`○ [${i + 1}/${OVERLAY_CASES.length}] ${c.path} → ${c.kind}（${c.note}）\n    自动化覆盖不到：${c.manual}\n`)
      continue
    }
    const page = await ctx.newPage()
    const errors = []
    const httpErrs = []
    page.on('console', (m) => {
      if (m.type() !== 'error') return
      const t = m.text()
      // Chromium 会为**每个**失败请求打一条 "Failed to load resource: … 4xx/5xx"。
      // 这些已经在下面的 response 监听里按业务/非业务分类过了，留着就是同一条 400
      // 被数两遍（一次进 notes、一次进 P1），把「错误已按预期呈现」又变成红的。
      // 只放行非网络来源的 console 错误：Vue 警告、未捕获异常的伴随输出等。
      if (/^Failed to load resource/i.test(t)) return
      errors.push(t.slice(0, 200))
    })
    page.on('pageerror', (e) => errors.push(`[pageerror] ${String(e).slice(0, 200)}`))
    // 业务级 4xx 要和「前端炸了」分开记，见下面 p1 的分类注释
    page.on('response', async (r) => {
      if (r.status() < 400) return
      let detail = ''
      try {
        const t = await r.text()
        const j = JSON.parse(t)
        detail = j?.error?.detail || j?.detail || ''
      } catch { /* 非 JSON 错误体，按非业务错误处理 */ }
      httpErrs.push({ status: r.status(), url: r.url().replace(BASE, '').slice(0, 120), detail: String(detail).slice(0, 120) })
    })

    let probe = null
    let clickErr = null
    try {
      await page.goto(BASE + c.path, { waitUntil: 'domcontentloaded', timeout: 20000 })
      await page.waitForSelector('.main-body > *', { timeout: 15000 }).catch(() => {})
      await page.waitForTimeout(2500)
      // 点击前先拍一张：只有「这次触发新出现的」弹层才算数。
      // 页面上的常驻 fixed 元素（全局导航抽屉等）否则会被每条 case 都量一遍。
      const before = await page.evaluate(OVERLAY_PROBE)
      await runSteps(page, c.steps)
      // 抽屉有滑出动画，等它走完再量
      await page.waitForTimeout(1200)
      const after = await page.evaluate(OVERLAY_PROBE)
      probe = pickOpenedPanel(before, after)
    } catch (e) {
      clickErr = String(e).split('\n')[0].slice(0, 160)
    }

    // 必须在 close 之前断言「业务错误有没有被显示出来」——页面关了就问不到了。
    let surfaced = null
    if (c.expectErrorSurfaced) {
      surfaced = await page
        .locator(c.expectErrorSurfaced).first()
        .waitFor({ state: 'visible', timeout: 5000 })
        .then(() => true)
        .catch(() => false)
    }
    await page.close()

    const p0 = []
    const p1 = []
    if (clickErr) p0.push(`触发失败：${clickErr}`)
    if (!probe) p0.push('点击后未出现新弹层（选择器可能已失效，或该入口需要额外前置状态）')
    if (probe) {
      if (!probe.scrollable) p1.push(`内容放不下且无可滚路径：面板高 ${probe.h}px，文字 ${probe.textLen} 字`)
      if (probe.textLen < 20) p1.push(`弹层几乎无内容（${probe.textLen} 字），疑似打开后未填充数据`)
    }

    // console 错误分三类，**不是一律 P1**：
    //  ① 未捕获异常（pageerror）→ 一定是前端缺陷，P1。
    //  ② 5xx / 没有业务错误体的 4xx → 服务端异常或未预期，P1。
    //  ③ 带 `{"error":{"detail":…}}` 的 4xx → **服务端明确拒绝了这次请求并给了原因**，
    //     例如会话总结的「日志条数不足，至少需要 2 条同会话记录」。
    //     这类不是前端缺陷，但**前端必须把它显示出来** —— 静默吞掉才是缺陷。
    //     所以：case 声明了 expectErrorSurfaced 就断言那个选择器可见（把 400
    //     变成「错误有没有被正确呈现」的正向检查）；没声明就单列成 note，
    //     既不判红也不删掉。**限制**：本门只验「错误被显示」，不验「文案对不对」。
    const business4xx = httpErrs.filter((e) => e.status < 500 && e.detail)
    const hardHttp = httpErrs.filter((e) => !(e.status < 500 && e.detail))
    const notes = []
    if (business4xx.length) {
      if (c.expectErrorSurfaced) {
        if (surfaced) notes.push(`业务 4xx ${business4xx.length} 条已按预期呈现（${c.expectErrorSurfaced}）：${business4xx[0].detail}`)
        else p1.push(`服务端返回了业务错误但界面没显示（${c.expectErrorSurfaced} 不可见）：${business4xx[0].detail}`)
      } else {
        notes.push(`业务 4xx ${business4xx.length} 条（服务端明确拒绝，非前端缺陷）：${business4xx[0].url} → ${business4xx[0].detail}`)
      }
    }
    if (hardHttp.length) p1.push(`非业务 HTTP 错误 ${hardHttp.length} 条：${hardHttp[0].status} ${hardHttp[0].url}`)
    if (errors.length) p1.push(`console 错误 ${errors.length} 条：${errors[0].slice(0, 140)}`)

    results.push({
      ...c, opened: !!probe, p0, p1, notes, probe,
      errors: [...new Set(errors)].slice(0, 3),
      httpErrs: httpErrs.slice(0, 5),
    })
    const mark = p0.length ? '✗' : p1.length ? '!' : '✓'
    process.stdout.write(
      `${mark} [${i + 1}/${OVERLAY_CASES.length}] ${c.path} [${fmtSteps(c.steps)}] → ${c.kind}（${c.note}）` +
      `${probe ? ` 面板 .${probe.cls.split(/\s+/)[0]} ${probe.w}px / ${probe.textLen} 字` : ' 未打开'}\n`,
    )
    for (const n of notes) process.stdout.write(`    · ${n}\n`)
  }
  await browser.close()

  const bad = results.filter((r) => r.p0.length || r.p1.length)
  const manual = results.filter((r) => r.skipped)
  console.log(`\n══ 弹层遍历（${results.length} 个清单项：${results.length - manual.length} 个自动 + ${manual.length} 个待人工）══`)
  console.log(`✓ 合格：${results.length - bad.length - manual.length}    有问题：${bad.length}    待人工：${manual.length}`)
  if (manual.length) {
    console.log('\n── ○ 自动化覆盖不到（不算合格也不算不合格，需人工交互）──')
    for (const r of manual) console.log(`  ${r.path} — ${r.note}\n    原因：${r.manual}`)
  }
  for (const r of bad) {
    console.log(`\n  ${r.path} [${fmtSteps(r.steps)}] — ${r.note}`)
    for (const m of [...r.p0, ...r.p1]) console.log(`    · ${m}`)
  }
  if (args.includes('--json')) {
    const outPath = args[args.indexOf('--json') + 1] || 'reports/ui-overlay.json'
    const abs = resolve(WEB, outPath)
    mkdirSync(dirname(abs), { recursive: true })
    writeFileSync(abs, JSON.stringify({ base: BASE, at: new Date().toISOString(), results }, null, 2))
    console.log(`\nJSON：${outPath}`)
  }
  if (STRICT && bad.length) {
    console.error(`\n✗ 弹层遍历未通过（${bad.length}/${results.length}）`)
    process.exit(1)
  }
}

// ── 探针自检 ────────────────────────────────────────────────────────
// 运行时判据比静态判据更容易「恒绿」：探针 selector 写错、阈值定得不合理、
// 页面骨架换名，都会让所有页面安静地报 0 违规——而 0 违规正是我们要的结论。
// 所以每条运行时判据都必须先在一个**故意违规**的夹具上证明它会红，
// 再在一个干净夹具上证明它会绿。
//
// 判别力检查（不跑就知道）：把任意一条 expected 从 true 改成 false，
// 断言必须失败。夹具和断言同源于一个数据表，构造不出「判据恒假」，
// 但能构造出「判据恒真」——那才是真问题（探针对什么都报）。
const SELFTEST_FIXTURES = [
  {
    name: '干净全屏页：不得报任何违规',
    html: `<div class="main-body"><section class="page">正常内容，正常宽度。</section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{background:#fff;padding:20px;width:100%;box-sizing:border-box}</style>`,
    expect: { centered: false, verticalText: 0, clipped: 0, scannedNonZero: true },
  },
  {
    name: '居中窄列：max-width + margin auto 必须被抓住',
    html: `<div class="main-body"><section class="page">内容</section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{max-width:600px;margin:0 auto;background:#fff;padding:20px;box-sizing:border-box}</style>`,
    expect: { centered: true, verticalText: 0, clipped: 0, scannedNonZero: true },
  },
  {
    name: '全宽但左右小留白（padding 感）：不得被误判成居中',
    html: `<div class="main-body"><section class="page">内容</section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{margin:0 12px;background:#fff;padding:20px;box-sizing:border-box}</style>`,
    expect: { centered: false, verticalText: 0, clipped: 0, scannedNonZero: true },
  },
  {
    // 真实 bug 形态（commit 5e5022e21 引入、b4998e07 修复）：
    // table-layout:fixed + table width:auto（shrink-to-fit）+ 单元格 max-width:0
    // ⇒ 列宽全被压成 0，表格收到 ~70px，文字按字符竖排（实测单元格 2px 宽 × 776 高），
    // 而外层 .card{overflow-x:auto} 因为表已被压到 100% 宽而**无物可滚**。
    name: '逐字竖排：定宽表 width:auto + 单元格 max-width:0（供应商表 bug 签名）',
    html: `<div class="main-body"><section class="page"><div class="card"><table><tr>
        ${Array.from({ length: 8 }, (_, i) => `<td>${['HEADERNAME', 'https://api.example.com/v1/chat', 'glm-4.6'][i % 3]}</td>`).join('')}
        </tr></table></div></section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{width:100%;box-sizing:border-box}
      .card{overflow-x:auto}
      table{table-layout:fixed}
      td{max-width:0;word-break:break-word;white-space:normal;overflow-wrap:break-word}</style>`,
    expect: { centered: false, verticalText: 1, clipped: 0, scannedNonZero: true },
  },
  {
    // 反向对照：同一张表按 b4998e07 的修法（min-width + 去 max-width:0 + nowrap）
    // ⇒ 必须判绿。少了这一条，探针可能只是「见窄就报」，照样全绿。
    name: '同一张表的修复后形态：min-width + nowrap，不得误报折行',
    html: `<div class="main-body"><section class="page"><div class="card"><table><tr>
        ${Array.from({ length: 8 }, (_, i) => `<td>${['HEADERNAME', 'https://api.example.com/v1/chat', 'glm-4.6'][i % 3]}</td>`).join('')}
        </tr></table></div></section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{width:100%;box-sizing:border-box}
      .card{overflow-x:auto}
      table{table-layout:fixed;min-width:1800px}
      td{white-space:nowrap}</style>`,
    expect: { centered: false, verticalText: 0, clipped: 0, scannedNonZero: true },
  },
  {
    name: '窄但自带滚动的长文本：不得误报逐字折行（w30×h200，确实命中阈值）',
    html: `<div class="main-body"><section class="page">
        <div class="log">AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA</div></section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{background:#fff;padding:0;width:100%;box-sizing:border-box}
      .log{width:30px;height:200px;overflow-y:auto;word-break:break-all}</style>`,
    expect: { centered: false, verticalText: 0, clipped: 0, scannedNonZero: true },
  },
  {
    name: '内容裁切：overflow:hidden + 内容换行后更高 + 不可滚必须被抓住',
    html: `<div class="main-body"><section class="page">
        <div class="cell">${'这是一段会被挤成多行的说明文字，用于把盒子撑高到溢出高度。'.repeat(6)}</div></section></div>
      <style>.main-body{padding:16px;background:#eee}
      .page{background:#fff;padding:0;width:100%;box-sizing:border-box}
      .cell{width:100%;height:30px;overflow:hidden;line-height:20px}</style>`,
    expect: { centered: false, verticalText: 0, clipped: 1, scannedNonZero: true },
  },
]

async function runSelftest() {
  const { browser, from } = await launchBrowser()
  console.log(`  （浏览器来自 ${from}）`)
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  let pass = 0
  const fails = []
  for (const fx of SELFTEST_FIXTURES) {
    const page = await ctx.newPage()
    await page.setContent(fx.html)
    await page.waitForTimeout(120)
    const got = await page.evaluate(PAGE_PROBE)
    await page.close()
    const actual = {
      centered: !!got.fullWidth?.centered,
      verticalText: got.verticalText.length,
      clipped: got.clipped.length,
      // 第 4 条：作用域必须真的扫到元素。上面三条全 0 有两种成因——
      // 「页面干净」和「选择器没匹配到任何东西」，输出上一模一样。
      scannedNonZero: got.scanned > 0,
    }
    for (const k of ['centered', 'verticalText', 'clipped', 'scannedNonZero']) {
      const want = fx.expect[k]
      // 布尔判据用相等；计数判据用「至少」——一处真实 bug 常在同页复现多行/多列，
      // 探针也只在 8 处封顶，拿相等去卡会把「报得更多」误判成探针失灵。
      const isCount = typeof want === 'number'
      const ok = isCount ? actual[k] >= want : actual[k] === want
      if (ok) pass++
      else fails.push(`${fx.name}\n    判据 ${k}：期望 ${isCount ? '≥' : '='} ${want}，实测 ${actual[k]}`)
    }
  }
  await browser.close()
  const total = SELFTEST_FIXTURES.length * 4
  console.log(`\n══ 探针自检 ${pass}/${total} ══`)
  for (const f of fails) console.log(`  ✗ ${f}`)
  if (fails.length) {
    console.error(`\n✗ 探针自检未通过：${fails.length}/${total} 判据没有鉴别力，先修判据再扫页面。`)
    process.exit(1)
  }
  console.log(`✓ ${SELFTEST_FIXTURES.length} 个夹具 × 4 条判据，正负双向都能区分`)
}

// ── 主流程 ────────────────────────────────────────────────────────
const args = process.argv.slice(2)
if (args.includes('--selftest')) {
  await runSelftest()
  await runOverlaySelftest()
  await runStepSelftest()
  process.exit(0)
}
const BASE = (process.env.BASE_URL || 'http://127.0.0.1:5781').replace(/\/$/, '')
const TOKEN = process.env.GATEWAY_DEV_AUTH_TOKEN || ''
const STRICT = args.includes('--strict')
if (args.includes('--overlays')) {
  await runOverlaySweep()
  process.exit(0)
}

const parsedRoutes = parseRoutes()
const unresolvedDynamic = parsedRoutes.unresolved || []
let routesList = parsedRoutes.filter((r) => r.path)
if (args.includes('--only')) {
  const want = new Set(args[args.indexOf('--only') + 1].split(',').map((s) => s.trim()))
  routesList = routesList.filter((r) => want.has(r.path))
}

// 动态详情页：用真实 API 取 ID 拼 URL。取不到的不静默丢弃，报告里逐条点名。
let dynSkipped = []
if (!args.includes('--only') || DYNAMIC_ROUTES.some((d) => wantPath(args, d.path))) {
  process.stdout.write('解析动态详情页的真实 ID…\n')
  const r = await resolveDynamicRoutes(BASE, TOKEN)
  routesList = routesList.concat(r.out)
  dynSkipped = r.skipped
  for (const d of r.out) console.log(`  ${d.path} → ${d.path}（ID ${d.sampleId} 来自 ${d.from}）`)
  for (const s of dynSkipped) console.log(`  ○ ${s.path}（${s.comp}）未纳入：${s.why}`)
}
if (unresolvedDynamic.length) {
  console.log(`\n⚠ router.ts 里有 ${unresolvedDynamic.length} 条动态路由未登记进 DYNAMIC_ROUTES：`)
  for (const d of unresolvedDynamic) console.log(`    ${d.path} → ${d.comp}`)
  console.log('  它们不会出现在报告里。补进 DYNAMIC_ROUTES 或写明豁免理由。')
}

const { browser, from: browserFrom } = await launchBrowser()
console.log(`浏览器：${browserFrom}\n目标：${BASE}\n`)
const ctx = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  extraHTTPHeaders: TOKEN ? { Authorization: `Bearer ${TOKEN}` } : {},
})

const results = []
let i = 0
for (const r of routesList) {
  i++
  const page = await ctx.newPage()
  const errors = []
  const badReqs = []
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(m.text().slice(0, 300))
  })
  page.on('pageerror', (e) => errors.push(`[pageerror] ${String(e).slice(0, 300)}`))
  page.on('response', (res) => {
    const u = res.url()
    // 记**所有**失败响应，不只 /api/。曾经只记 /api/，结果 console 报 500 而
    // badReqs 是空数组 —— 「没记到」和「没有」在报告上长得一样。
    if (res.status() >= 400) badReqs.push(`${res.status()} ${u.replace(BASE, '').slice(0, 160)}`)
  })
  page.on('requestfailed', (r) => {
    const f = r.failure()?.errorText || 'unknown'
    badReqs.push(`[requestfailed] ${f} ${r.url().replace(BASE, '').slice(0, 140)}`)
  })

  let probe = null
  let err = null
  try {
    await page.goto(BASE + r.path, { waitUntil: 'domcontentloaded', timeout: 20000 })
    // 认证水合 + 首屏数据。
    // 等待值必须大于最慢页面的加载时间，否则「空壳」分不清是「空」还是「慢」：
    // /admin/tenants 富化查询要 ~1.5s，早期用 1200ms 时它被判成 <120 字空壳页，
    // 差点被当成「租户页没数据」去查数据问题。判据的阈值本身就是被测结论的一部分。
    await page.waitForSelector('.main-body > *', { timeout: 15000 }).catch(() => {})
    await page.waitForTimeout(3000)
    probe = await page.evaluate(PAGE_PROBE)
  } catch (e) {
    err = String(e).slice(0, 200)
  }
  await page.close()

  // 判级
  const P0 = [] // 阻断级：页面没渲染 / 未捕获异常
  const P1 = [] // 应修：console 错误 / 逐字折行 / 裁切 / 非全屏
  if (err) P0.push(`导航失败：${err}`)
  if (!probe || !probe.fullWidth) P0.push('未渲染出页面根容器（可能重定向到 /bootstrap 或鉴权失败）')
  // 判据失效不许伪装成干净页：扫到 0 个元素时，折行/裁切两条必然报 0，
  // 那不是「没问题」，那是「没看见」。
  if (probe && probe.scanned === 0) P0.push('判据作用域失效：.main-body 内 0 个元素，折行/裁切结果不可信')
  if (probe?.fullWidth && probe.fullWidth.centered) {
    P1.push(`居中模式：根容器 .${probe.rootClass} 只占主区 ${probe.fullWidth.ratio * 100}%，左右留白 ${probe.fullWidth.padLeft}/${probe.fullWidth.padRight}`)
  }
  if (probe?.verticalText?.length) {
    P1.push(`逐字折行 ${probe.verticalText.length} 处，例：<${probe.verticalText[0].tag} class="${probe.verticalText[0].cls}"> ${probe.verticalText[0].w}×${probe.verticalText[0].h} "${probe.verticalText[0].text}"`)
  }
  if (probe?.clipped?.length) {
    const c = probe.clipped[0]
    P1.push(`内容裁切 ${probe.clipped.length} 处，例：.${c.cls} clientH=${c.clientH} scrollH=${c.scrollH} 且不可滚`)
  }
  if (errors.length) P1.push(`console 错误 ${errors.length} 条，首条：${errors[0].slice(0, 160)}`)
  if (badReqs.length) P1.push(`失败请求 ${badReqs.length} 条：${badReqs.slice(0, 3).join(' | ').slice(0, 220)}`)

  results.push({
    path: r.path, comp: r.comp, p0: P0, p1: P1,
    scanned: probe?.scanned ?? 0, textLen: probe?.textLen ?? 0, visibleBlocks: probe?.visibleBlocks ?? 0,
    bodyWidth: probe?.bodyWidth ?? 0, rootWidth: probe?.rootWidth ?? 0,
    badReqs: [...new Set(badReqs)].slice(0, 5), errors: [...new Set(errors)].slice(0, 5),
  })
  const mark = P0.length ? '✗' : P1.length ? '!' : '✓'
  process.stdout.write(`${mark} [${String(i).padStart(3)}/${routesList.length}] ${r.path}  (元素 ${probe?.scanned ?? 0} / 文本 ${probe?.textLen ?? 0} 字)\n`)
}

await browser.close()

// ── 报告 ──────────────────────────────────────────────────────────
const p0 = results.filter((r) => r.p0.length)
const p1 = results.filter((r) => !r.p0.length && r.p1.length)
const clean = results.length - p0.length - p1.length
// 空壳页：0 违规但几乎没有内容。分出来单列，避免混进「干净」计数里。
const hollow = clean > 0 ? results.filter((r) => !r.p0.length && !r.p1.length && r.textLen < 120) : []

console.log(`\n══ 扫描结果（${BASE}，viewport 1440×900，共 ${results.length} 路由）══`)
console.log(`✗ P0 阻断（未渲染/判据失效）: ${p0.length}`)
console.log(`! P1 应修                   : ${p1.length}`)
console.log(`✓ 干净                      : ${clean - hollow.length}`)
console.log(`○ 内容可疑（<120 字，空壳）  : ${hollow.length}`)

if (p0.length) {
  console.log('\n── P0 ──')
  for (const r of p0) console.log(`  ${r.path}\n    ${r.p0.join('\n    ')}`)
}
if (p1.length) {
  console.log('\n── P1 ──')
  for (const r of p1) {
    console.log(`  ${r.path}`)
    for (const m of r.p1) console.log(`    · ${m}`)
  }
}

if (hollow.length) {
  console.log('\n── ○ 内容可疑 ──')
  for (const r of hollow) console.log(`  ${r.path}  文本仅 ${r.textLen} 字，块 ${r.visibleBlocks} 个`)
}
if (args.includes('--json')) {
  const outPath = args[args.indexOf('--json') + 1] || 'reports/ui-sweep.json'
  const abs = resolve(WEB, outPath)
  mkdirSync(dirname(abs), { recursive: true })
  writeFileSync(abs, JSON.stringify({ base: BASE, at: new Date().toISOString(), results }, null, 2))
  console.log(`\nJSON：${outPath}`)
}

if (STRICT && p0.length + p1.length) {
  console.error(`\n✗ 遍历扫描未通过（P0:${p0.length} P1:${p1.length}）`)
  process.exit(1)
}
