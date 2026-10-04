/**
 * _layout-audit.mjs —— 移动端**显示问题**检测器（页面内执行体）。
 *
 * ⚠️ 为什么必须单源：同一批页面会被两辆车跑（本地 Chrome 移动视口分诊 +
 * 模拟器 WebView 取证）。两套判据会出现最坏的一种情况 —— **分诊说没问题、
 * 取证说有问题**，而没人知道该信哪个。两套判据里必有一套是坏的。
 * ⇒ 检测逻辑只写在这里，两个 driver 都 import 同一份。
 *
 * 本文件只导出「要在页面里跑的函数」。它被 `.toString()` 后注入页面执行，
 * 所以：
 *   1. 不能引用本模块的任何其它导出；
 *   2. 不能用 ESM 语法（import/export 会在页面里报语法错）；
 *   3. 返回值必须可 JSON 序列化。
 *
 * 检测项按「会不会毁版式 / 会不会让人点不到」排序，不按实现难度排序。
 */

/**
 * 在页面上下文里跑一次完整体检。
 * @returns {{url,vw,vh,dpr,issues:Array,stats:Object}}
 */
export function layoutAudit() {
  const I = { url: location.href, vw: innerWidth, vh: innerHeight, dpr: devicePixelRatio, issues: [], stats: {} }
  const de = document.documentElement
  const MIN_TAP = 44 // iOS HIG 44pt / Android 48dp 取小的那个，两边都不破

  function path(el) {
    if (!el || el === de) return 'html'
    const bits = []
    let n = el, d = 0
    while (n && n.nodeType === 1 && d < 5) {
      let s = n.tagName.toLowerCase()
      if (n.id) { bits.unshift(s + '#' + n.id); break }
      const cls = (n.getAttribute('class') || '').trim().split(/\s+/).filter(Boolean).slice(0, 2)
      if (cls.length) s += '.' + cls.join('.')
      const p = n.parentElement
      if (p) {
        const sibs = Array.from(p.children).filter((c) => c.tagName === n.tagName)
        if (sibs.length > 1) s += ':nth-of-type(' + (sibs.indexOf(n) + 1) + ')'
      }
      bits.unshift(s)
      n = n.parentElement; d++
    }
    return bits.join('>')
  }
  function vis(el) {
    const r = el.getBoundingClientRect()
    const s = getComputedStyle(el)
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none' && +s.opacity > 0.05
  }
  function hasOwnText(el) {
    return Array.from(el.childNodes).some((n) => n.nodeType === 3 && n.textContent.trim().length > 0)
  }
  function lum(c) {
    const m = String(c).match(/[\d.]+/g)
    if (!m) return null
    if (m.length > 3 && +m[3] < 0.6) return null // 半透明底算不准，不报
    const [r, g, b] = [0, 1, 2].map((i) => {
      const v = +m[i] / 255
      return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4)
    })
    return 0.2126 * r + 0.7152 * g + 0.0722 * b
  }
  function bgOf(el) {
    let n = el
    while (n && n.nodeType === 1) {
      const c = getComputedStyle(n).backgroundColor
      if (c && c !== 'rgba(0, 0, 0, 0)' && c !== 'transparent') return c
      n = n.parentElement
    }
    return getComputedStyle(de).backgroundColor || 'rgb(255,255,255)'
  }

  // ── ① 横向溢出 ──────────────────────────────────────────────
  // 移动端第一号版式杀手：一个 flex 子项没设 min-width:0 就能把整页撑宽，
  // 而它在宽屏开发时完全看不出来。
  const sw = Math.max(de.scrollWidth, document.body.scrollWidth)
  I.stats.scrollWidth = sw
  I.stats.scrollHeight = Math.max(de.scrollHeight, document.body.scrollHeight)
  if (sw > innerWidth + 1) {
    const off = []
    document.querySelectorAll('body *').forEach((el) => {
      if (!vis(el)) return
      const r = el.getBoundingClientRect()
      if (!(r.right > innerWidth + 1 || r.left < -1)) return
      // 只报「自己越界」的：被越界父级连带撑开的不报，否则满屏噪声
      if (el.scrollWidth <= el.clientWidth + 1 && r.width <= innerWidth * 1.02) return
      off.push({ sel: path(el), left: Math.round(r.left), right: Math.round(r.right), w: Math.round(r.width) })
    })
    I.issues.push({
      kind: 'h-overflow', sev: 'high',
      detail: 'scrollWidth=' + sw + ' > innerWidth=' + innerWidth + '（多 ' + (sw - innerWidth) + 'px）',
      offenders: off.slice(0, 10),
    })
  }

  // ── ② 触控热区过小 ──────────────────────────────────────────
  const small = []
  document.querySelectorAll('button,a,[role="button"],input,select,textarea,[onclick]').forEach((el) => {
    if (!vis(el)) return
    const s = getComputedStyle(el)
    if (s.pointerEvents === 'none') return
    const r = el.getBoundingClientRect()
    if (r.width < MIN_TAP || r.height < MIN_TAP) {
      small.push({ sel: path(el), w: Math.round(r.width), h: Math.round(r.height),
        text: (el.innerText || el.textContent || '').trim().slice(0, 20) })
    }
  })
  I.stats.smallTargets = small.length
  if (small.length) {
    I.issues.push({ kind: 'tap-target', sev: 'medium',
      detail: small.length + ' 个可点元素 < ' + MIN_TAP + 'px', items: small.slice(0, 12) })
  }

  // ── ③ 文字与背景几乎同色（看不见的字）────────────────────────
  const invisible = []
  document.querySelectorAll('body *').forEach((el) => {
    if (!hasOwnText(el) || !vis(el)) return
    const s = getComputedStyle(el)
    const lf = lum(s.color), lb = lum(bgOf(el))
    if (lf == null || lb == null) return
    const ratio = (Math.max(lf, lb) + 0.05) / (Math.min(lf, lb) + 0.05)
    if (ratio < 1.25) invisible.push({ sel: path(el), color: s.color, bg: bgOf(el),
      text: el.textContent.trim().slice(0, 24) })
  })
  I.stats.invisibleText = invisible.length
  if (invisible.length) {
    I.issues.push({ kind: 'invisible-text', sev: 'high',
      detail: invisible.length + ' 处文字与背景几乎同色', items: invisible.slice(0, 8) })
  }

  // ── ④ 文字对比度不足（WCAG AA 正文 4.5:1）───────────────────
  const lowc = []
  document.querySelectorAll('body *').forEach((el) => {
    if (!hasOwnText(el) || !vis(el)) return
    const s = getComputedStyle(el)
    const size = parseFloat(s.fontSize)
    const bold = (parseInt(s.fontWeight, 10) || 400) >= 700
    // 大字（≥18.66px 粗体 / ≥24px）门槛 3:1，其余 4.5:1
    const need = (size >= 24 || (size >= 18.66 && bold)) ? 3 : 4.5
    const lf = lum(s.color), lb = lum(bgOf(el))
    if (lf == null || lb == null) return
    const ratio = (Math.max(lf, lb) + 0.05) / (Math.min(lf, lb) + 0.05)
    if (ratio < need) {
      lowc.push({ sel: path(el), ratio: +ratio.toFixed(2), need, px: size,
        color: s.color, bg: bgOf(el), text: el.textContent.trim().slice(0, 24) })
    }
  })
  I.stats.lowContrast = lowc.length
  if (lowc.length) {
    I.issues.push({ kind: 'low-contrast', sev: 'medium',
      detail: lowc.length + ' 处文字对比度低于 WCAG AA', items: lowc.slice(0, 10) })
  }

  // ── ⑤ 固定栏遮挡内容 ────────────────────────────────────────
  const fixed = []
  document.querySelectorAll('body *').forEach((el) => {
    const s = getComputedStyle(el)
    if (s.position !== 'fixed' && s.position !== 'sticky') return
    if (!vis(el)) return
    const r = el.getBoundingClientRect()
    if (r.height < 8 || r.width < 8) return
    fixed.push({ el, r, pos: s.position })
  })
  I.stats.fixedBars = fixed.map((f) => ({ sel: path(f.el), pos: f.pos,
    top: Math.round(f.r.top), h: Math.round(f.r.height) }))
  const bottomBar = fixed.find((f) => f.r.top > innerHeight * 0.6)
  const topBar = fixed.find((f) => f.r.bottom < innerHeight * 0.4)

  // 内容容器底部是否伸进了底部固定栏
  if (bottomBar) {
    const main = document.querySelector('main') || document.querySelector('#app')
    if (main) {
      const mr = main.getBoundingClientRect()
      const cs = getComputedStyle(main)
      const padB = parseFloat(cs.paddingBottom) || 0
      // main 是可滚动容器时，用它的 scrollHeight 与 clientHeight 判更准
      if (main.scrollHeight > main.clientHeight + 1) {
        const slack = main.clientHeight - (main.scrollHeight - main.scrollTop)
        if (slack < bottomBar.r.height - 2 && padB < bottomBar.r.height - 2) {
          I.issues.push({ kind: 'covered-by-fixed', sev: 'high',
            detail: '滚动容器末尾剩余 ' + Math.round(slack) + 'px < 底部固定栏高 ' +
                    Math.round(bottomBar.r.height) + 'px（最后一行内容点不到）',
            items: [{ bar: path(bottomBar.el), container: path(main) }] })
        }
      } else if (mr.bottom > bottomBar.r.top + 2 && padB < bottomBar.r.height - 2) {
        I.issues.push({ kind: 'covered-by-fixed', sev: 'high',
          detail: '内容底部伸入底部固定栏 ' + Math.round(mr.bottom - bottomBar.r.top) + 'px',
          items: [{ bar: path(bottomBar.el), container: path(main) }] })
      }
    }
  }
  // 顶部固定栏遮挡：首个可交互元素是否被压在 header 下面
  if (topBar) {
    const first = document.querySelector('main button, main a, main input, #app button, #app a')
    if (first) {
      const fr = first.getBoundingClientRect()
      if (fr.top < topBar.r.bottom - 2 && fr.top >= 0) {
        I.issues.push({ kind: 'covered-by-fixed', sev: 'high',
          detail: '首个可交互元素被顶部固定栏压住 ' + Math.round(topBar.r.bottom - fr.top) + 'px',
          items: [{ bar: path(topBar.el), el: path(first) }] })
      }
    }
  }

  // ── ⑥ safe-area 避让 ───────────────────────────────────────
  // 移动端壳（Capacitor）里 SystemBars 用 insetsHandling:'css'，
  // 于是 env(safe-area-inset-*) 是唯一的避让来源；底栏不加就会被 Home 指示条压住。
  const cs = getComputedStyle(de)
  const safe = {
    top: (cs.getPropertyValue('--app-safe-top') || '').trim(),
    bottom: (cs.getPropertyValue('--app-safe-bottom') || '').trim(),
  }
  I.stats.safeArea = safe
  I.stats.hasViewportFitCover = /viewport-fit=cover/.test(
    (document.querySelector('meta[name=viewport]') || {}).content || '')
  if (bottomBar) {
    const bs = getComputedStyle(bottomBar.el)
    const pb = parseFloat(bs.paddingBottom) || 0
    const bb = parseFloat(bs.borderBottomWidth) || 0
    const need = parseFloat(safe.bottom) || 0
    if (need > 0 && pb + bb < need - 0.5) {
      I.issues.push({ kind: 'safe-area', sev: 'medium',
        detail: '底部固定栏 padding(' + pb + ')+border(' + bb + ') < safe-area-inset-bottom=' +
                need + 'px（内容会压到 Home 指示条）',
        items: [{ bar: path(bottomBar.el) }] })
    }
  }

  // ── ⑦ 资源加载失败 ─────────────────────────────────────────
  const broken = []
  document.querySelectorAll('img').forEach((el) => {
    if (el.complete && el.naturalWidth === 0) broken.push(path(el))
  })
  I.stats.brokenImages = broken.length
  if (broken.length) {
    I.issues.push({ kind: 'broken-image', sev: 'high',
      detail: broken.length + ' 张图加载失败', items: broken.slice(0, 8).map((s) => ({ sel: s })) })
  }

  // ── ⑧ 字体没加载出来（回退字形 = 版式整体偏移）────────────────
  I.stats.fontStatus = document.fonts ? document.fonts.status : 'unknown'

  // ── ⑨ 近白屏 ───────────────────────────────────────────────
  I.stats.textChars = (document.body.innerText || '').trim().length
  I.stats.domNodes = document.querySelectorAll('*').length
  // 「页面几乎是空的」有两种完全不同的成因，只有一种是缺陷：
  //   · 终态视图（空态 / 错误态）本来就只有一句话 —— /alerts 的「暂无数据 | 近期无告警」实测 35 字符；
  //   · 骨架屏 / 挂起 —— 那才是白屏。
  // ⇒ 存在 .state-view（且不在 loading 态）时**不报** near-blank。
  //
  // ⚠️ 判据必须**与语言无关**，否则它跟着 i18n 走。实测反例（同一份 DOM、同样 34 个节点）：
  //     zh-CN  28 字符：「登录网关 使用网关管理员账号登录 用户名 密码 登录」
  //     en-US  77 字符：同一个登录页
  //   旧的 `textChars < 40` 把 zh-CN 的登录页判成 near-blank（假阳性），
  //   英文页则通过 —— 同一个缺陷在两种语言下结论相反。
  // ⇒ 改用**节点数**判「空不空」：登录页 34 节点，真空白页 6 节点，门槛 20。
  const terminal = document.querySelector('.state-view:not([aria-busy="true"])')
  I.stats.terminalState = terminal ? (terminal.innerText || '').replace(/\s+/g, ' ').trim().slice(0, 60) : null
  const STRUCT_FLOOR = 20
  I.stats.structFloor = STRUCT_FLOOR
  if (I.stats.domNodes < STRUCT_FLOOR && !terminal) {
    I.issues.push({ kind: 'near-blank', sev: 'high',
      detail: `整页仅 ${I.stats.domNodes} 个节点 / ${I.stats.textChars} 个可见字符（门槛 ${STRUCT_FLOOR} 节点，疑似白屏或挂起）` })
  }

  // ── ⑩ 触控目标重叠（点 A 意外点到 B）────────────────────────
  // 判「点不到」而不是「矩形相交」——这两件事在滚动容器上完全不同。
  // 实测：/keys 滚到底时最后一张卡 bottom=746、底栏 top=799，可点元素在栏上方的有 0 个；
  // 而滚到顶部时列表内容从半透明底栏下方经过，矩形相交 33–49%。
  // ⇒ 「滚动内容 × 固定底栏」一律不算重叠：**能不能点到底**由下面的
  //   covered-by-fixed（按 scrollHeight/clientHeight 判）单独回答，不在这里重复报。
  const inScroller = (el) => {
    let n = el.parentElement
    while (n && n !== document.body) {
      const s = getComputedStyle(n)
      if ((s.overflowY === 'auto' || s.overflowY === 'scroll') && n.scrollHeight > n.clientHeight + 4) return true
      n = n.parentElement
    }
    return false
  }
  // ⚠️ 必须**向上看祖先**：实测底栏里 `nav.bottomnav > a.bottomnav__item` 自身是
  // position:static，fixed 挂在父级 nav.bottomnav 上。
  // 只看元素自身 ⇒ 这类（最常见的）固定栏永远判不出来，假阳性就一直报。
  const isFixedBar = (el) => {
    let n = el, d = 0
    while (n && n.nodeType === 1 && d < 6) {
      const s = getComputedStyle(n)
      if (s.position === 'fixed' || s.position === 'sticky') return true
      n = n.parentElement; d++
    }
    return false
  }
  const taps = Array.from(document.querySelectorAll('button,a,[role="button"]'))
    .filter(vis)
    .map((el) => ({ el, r: el.getBoundingClientRect() }))
    .filter((t) => t.r.width > 8 && t.r.height > 8)
  const overlaps = []
  for (let a = 0; a < taps.length; a++) {
    for (let b = a + 1; b < taps.length; b++) {
      const A = taps[a], B = taps[b]
      if (A.el.contains(B.el) || B.el.contains(A.el)) continue
      const ox = Math.min(A.r.right, B.r.right) - Math.max(A.r.left, B.r.left)
      const oy = Math.min(A.r.bottom, B.r.bottom) - Math.max(A.r.top, B.r.top)
      if (ox > 2 && oy > 2) {
        // 滚动内容 × 固定栏 = 正常的「从下方经过」，不是遮挡（理由见上）
        if ((isFixedBar(A.el) && inScroller(B.el)) || (isFixedBar(B.el) && inScroller(A.el))) continue
        const area = ox * oy
        const min = Math.min(A.r.width * A.r.height, B.r.width * B.r.height)
        if (area / min > 0.3) {
          overlaps.push({ a: path(A.el), b: path(B.el), overlapPct: Math.round(100 * area / min) })
        }
      }
    }
  }
  I.stats.overlappingTaps = overlaps.length
  if (overlaps.length) {
    I.issues.push({ kind: 'tap-overlap', sev: 'medium',
      detail: overlaps.length + ' 对可点元素重叠 > 30%', items: overlaps.slice(0, 8) })
  }

  I.stats.issueCount = I.issues.length
  return I
}

/**
 * 等页面进入**稳定态**再采样。
 *
 * ⚠️ 为什么必须有这一步（一次真实的假阳性）：
 *   `/nodes` 的数据来自 admin/credential_monitor.go:657，服务端查询要 15s 才超时回 500。
 *   早先那张「整页只有骨架屏」的截图是在导航后 ~4s 抓的 —— 那一刻它**本来就该**是骨架屏。
 *   再加 15s 观察窗，页面自己就进了错误态（「加载失败 / 重试」，实测 t+18s）。
 *   ⇒ 「骨架屏」与「永久骨架屏」是两个完全不同的结论，差一个观察窗。
 *   本函数返回页面是否已脱离 initialLoading，采样前必须先问它。
 *
 * @param {number} timeoutMs 最多等多久
 * @returns {Promise<{settled:boolean, waitedMs:number, state:string, rows:number}>}
 */
export async function waitForSettle(timeoutMs) {
  const cap = timeoutMs || 25000
  const t0 = Date.now()
  const isSkeleton = () => document.querySelectorAll('[class*="skeleton"],[class*="Skeleton"]').length > 0
  const stateView = () => {
    const el = document.querySelector('.state-view')
    return el ? (el.innerText || '').replace(/\s+/g, ' ').trim().slice(0, 80) : ''
  }
  const rows = () => document.querySelectorAll('.hyper-list__row, .card-list > *').length
  // 「还在加载吗」必须**与组件无关**。AppStateView 的三种终态（AppStateView.vue:23-47）：
  //   loading → .state-view[aria-busy=true] + skeleton；error/empty → .state-view--center；
  //   content → **裸 <slot>，连 .state-view 都不渲染**。
  // ⇒ 「没有 .state-view」不等于「还在加载」，它也可能是**已经渲染完内容了**。
  const busy = () =>
    isSkeleton() || !!document.querySelector('.state-view[aria-busy="true"]')
  // 「不再变化」的指纹：文字长度 + 节点数 + 开头 40 字（只比长度会被等长替换骗过）。
  const sig = () => {
    const t = (document.body.innerText || '').replace(/\s+/g, ' ').trim()
    return `${t.length}:${document.querySelectorAll('*').length}:${t.slice(0, 40)}`
  }
  const chars = () => (document.body.innerText || '').replace(/\s+/g, ' ').trim().length
  // ⚠️ 「这页有东西吗」**不能用文字字符数**判 —— 它随语言变：
  //   同一个 /m/login，zh-CN 是 28 字符（登录网关 使用网关管理员账号登录 用户名 密码 登录），
  //   en-US 是 77 字符（同一个 DOM、同样 34 个节点）。按字符数判 ⇒ 中文页被判白屏、
  //   英文页通过，判据跟着 i18n 走。
  // ⇒ 改用**结构**判：节点数与语言无关。
  //   登录页 34 节点 / 真空白页 6 节点 —— 门槛取 20，两侧都留出余量。
  const structFloor = 20
  const nonBlank = () => document.querySelectorAll('*').length >= structFloor

  const probe = () => {
    if (isSkeleton()) return { settled: false, state: 'initialLoading', rows: 0, sig: sig() }
    const sv = stateView()
    if (sv) return { settled: true, state: sv, rows: rows(), sig: sig() }
    if (rows() > 0) return { settled: true, state: 'content', rows: rows(), sig: sig() }
    // 无骨架、无状态视图、无列表行 —— 可能是「已渲染完但不是列表」（Home 的
    // .data-card / Usage 的 .table / 登录的 form 都是这种形状），也可能是真的挂住。
    // 这里**只报告指纹，不自己下结论**：终态由下面的连续采样计数决定。
    return { settled: false, state: 'pending', rows: 0, sig: sig() }
  }
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  let last = probe()
  let prevSig = null
  let hits = 0
  while (Date.now() - t0 < cap) {
    if (last.settled) break
    await sleep(700)
    const s = sig()
    hits = prevSig != null && prevSig === s ? hits + 1 : 0
    prevSig = s
    last = probe()
    // 终态 = 连续 3 个采样指纹一致（hits>=2）+ 页面不在忙态 + **结构上非空**。
    // 「非空」用节点数不用字符数（见 nonBlank 注释）—— 用字符数会让中文页永远 pending。
    if (!last.settled && hits >= 2 && !busy() && nonBlank()) {
      last = { settled: true, state: 'stable', rows: rows(), sig: s }
      break
    }
  }
  return { settled: last.settled, waitedMs: Date.now() - t0, state: last.state, rows: last.rows }
}
