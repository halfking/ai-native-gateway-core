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
  const MIN_TAP = 44   // iOS HIG 44pt —— **只用于无可见文字的图标类**目标（方形）
const MIN_TAP_H = 48 // 本仓 R1：新增触控控件一律 ≥48 CSS px。与 scripts/verify-touch-targets.mjs 的 TARGET 同源，见下方 ② 的说明。
  // ⚠️ 48 有一条**书面豁免**（17 §4-R1 原文 + 仓门第 15-18 行）：
  //   「44px 是存量控件下限，不是新标准」，且仓门**明确不扫** shared.css 的
  //   `.btn` / `.btn--sm`（它们是 44px 存量基线）。
  //   实测代价（10 §4.6.61）：/m/keys 一屏 **249~250 个** `btn--sm`（高 44px）
  //   在 medium 档被判成 249 处 medium 缺陷 ⇒ 离群的是**本判据**，不是仓门。
  //   ⇒ 分三档报，判据与规则对账，不靠调阈值糊过去：
  //     < 44        medium  违反硬底线（连 Apple 44pt 都不够）
  //     44 ~ 48     low     存量区间（R1 允许），只作台账，不当缺陷
  //     ≥ 48        —       合规
  const R1_LEGACY_H = 44

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

    // ── ② 触控热区 ──
  const small = []
  const legacy = []
  document.querySelectorAll('button,a,[role="button"],input,select,textarea,[onclick]').forEach((el) => {
    if (!vis(el)) return
    const s = getComputedStyle(el)
    if (s.pointerEvents === 'none') return
    const r = el.getBoundingClientRect()
    // ⚠️ 量的是**有效命中区**，不是元素自身矩形（实测 /m/request-anomalies）：
    //   `label.ra__check > input[type=checkbox]`，input 自身只有 **20×20**，
    //   但它被整个 label 包着，点 label 任意位置都会切换 checkbox
    //   ⇒ 实测 label = **270×48**，零违规（WCAG 2.5.8 AA 要 24×24，
    //   本仓 R1 要 min-height 48，都达标）。只量 input 会报出一条**不存在的缺陷**。
    //   「命中区」= 最近的可点祖先（label 会把点击转发给内部控件）。
    const hit = el.closest('label,button,a,[role="button"]')
    const target = hit && hit !== el ? hit : el
    const hr = target.getBoundingClientRect()
    const txt = (el.innerText || el.textContent || '').trim()
    // ★ 方案 A（2026-10-07，doc 10 §4.6.55）：**判据与仓门对齐**，不再各自一套。
    //   本仓两条并存的规则此前从没对过账：
    //     · `scripts/verify-touch-targets.mjs`：TARGET=48，**只量 min-height**
    //       （依据 17 §4-R1「新增控件一律 ≥48 CSS px；44px 是存量下限」）
    //     · 本模块此前的 MIN_TAP=44：**宽高都量**（依据「iOS 44 / Android 48 取小者」）
    //   两者因此对同一批 chip 给出相反读数（本模块红、仓门绿），
    //   而 `/m/heatmap` 的 1h/1d 两个 chip 被本模块挂了四节。
    //   ⇒ 改成：**图标类**（无可见文字）才量方形 44×44；**文字类**只量高度 48，
    //     因为文字 chip 的宽度由标签长度决定（「1h」两个字符），
    //     要求它 ≥44 是本仓从未采纳过的约束。
    //   依据核对：WCAG 2.2 SC 2.5.8 的 AA **规范下限是 24×24**（面积 2040 vs 576 远超），
    //   Apple 44pt / Material 48dp **都是指南不是规范**，本仓 R1 要 min-height ≥48。
    //
    // ⚠️⚠️ 二次对账（2026-10-08，doc 10 §4.6.61）：上面那句「与仓门同源」**只对了一半** ——
    //   仓门有**书面豁免**：`verify-touch-targets.mjs` 第 15-18 行明写「不扫 shared.css 的
    //   .btn / .btn--sm —— 它们是 44px 的**存量**基线」，依据 17 §4-R1
    //   「新增一律 ≥48；**44px 是存量控件下限，不是新标准**」。
    //   实测代价：`/m/keys` 的 `.btn--sm`（高 44px）一屏 **249~250 个**，
    //   在 medium 档被本判据判成 249 处 medium ⇒ **离群的是本判据，不是仓门**。
    //   ⇒ 文字类改三档：<44 medium（连硬底线都没有）/ 44~47 low（存量台账）/ ≥48 合规。
    const iconLike = txt.length === 0
    const tooSmall = iconLike
      ? (hr.width < MIN_TAP || hr.height < MIN_TAP)
      : (hr.height < R1_LEGACY_H)
    const isLegacy = !iconLike && !tooSmall && hr.height < MIN_TAP_H
    if (tooSmall) {
      small.push({ sel: path(el), w: Math.round(hr.width), h: Math.round(hr.height),
        rule: iconLike ? `图标类 <${MIN_TAP}×${MIN_TAP}` : `文字类 高度<${R1_LEGACY_H}`,
        via: hit && hit !== el ? path(target) : undefined,
        own: (iconLike ? (r.width < MIN_TAP || r.height < MIN_TAP) : (r.height < R1_LEGACY_H))
          ? `${Math.round(r.width)}×${Math.round(r.height)}` : undefined,
        text: txt.slice(0, 20) })
    } else if (isLegacy) {
      legacy.push({ sel: path(el), w: Math.round(hr.width), h: Math.round(hr.height),
        rule: `文字类 存量 ${R1_LEGACY_H}~${MIN_TAP_H - 1}（R1 允许，非缺陷）`,
        text: txt.slice(0, 20) })
    }
  })
  I.stats.smallTargets = small.length
  I.stats.legacyTargets = legacy.length
  if (small.length) {
    I.issues.push({ kind: 'tap-target', sev: 'medium',
      detail: small.length + ' 个可点元素不足（图标类 <44×44 / 文字类 高度<44）', items: small.slice(0, 12) })
  }
  if (legacy.length) {
    I.issues.push({ kind: 'tap-target-legacy', sev: 'low',
      detail: legacy.length + ' 个文字类控件落在 R1 存量区间 44~47px（书面允许，只作台账）',
      items: legacy.slice(0, 6) })
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
  // ⚠️ 「固定栏」必须是**外壳 chrome**，不能是内容里的 sticky（实测 /m/matrix）：
  //   该页有 **581 个** `position:sticky` 的 `.mx__rowhead`（表格行头），
  //   只要有一个的 top 落在视口下 40% 就被 `find()` 选中当「底部固定栏」，
  //   于是判出一句「内容底部伸入底部固定栏 122px」——而那一页**根本没有吸底栏**
  //   （实测 `navs: []`），滚到底后被视口裁掉的单元格 = 0。
  //   判别式：栏在滚动宿主**之外**（`nav.bottomnav` 的祖先里没有滚动容器），
  //   内容里的 sticky 行头在 `.mx__scroll` **里面**。⇒ 用 inScroller 分。
  const bars = fixed.filter((f) => !inScroller(f.el))
  const bottomBar = bars.find((f) => f.r.top > innerHeight * 0.6)
  const topBar = bars.find((f) => f.r.bottom < innerHeight * 0.4)

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
  //
  // ★★ 这里曾有一个**恒真的假阳性**，2026-10-08 真机横屏才暴露（§4.6.69）：
  //   1. 原选择器 `'main button, main a, …, #app button, #app a'` 的**兜底
  //      `#app button` 会匹配到 topbar 自己的按钮**。而 `querySelector` 对逗号
  //      列表是按**文档顺序**返回，不是按选择器书写顺序 ⇒ topbar 在 DOM 里
  //      先于 `<main>`，于是命中的永远是 `BUTTON.topbar__btn`。
  //      实测 `/m/keys` 的 `<main>` 里有 **249 个**按钮，照样被 topbar 的那个抢走。
  //   2. 拿 `header.topbar` 去和**它自己的子元素**比「top < bar.bottom-2 &&
  //      top >= 0」，对任何贴顶的栏都是**恒真**（栏内子元素必然落在栏的区间里）。
  //   ⇒ 修法不是「把某个条件调严」，而是让这类比较**在结构上不可能发生**：
  //      候选只从 `<main>` 里取，并剔除任何落在任一根栏内部的元素。
  //
  // ⚠️ 为什么 headless 的 930 组 + 字号 122 组全绿时它没响：那是**巧合**。
  //   headless 的 `--app-safe-top` 恒 0 ⇒ 顶栏高只有 48px，栏内按钮的 top 落到
  //   **−0.5px**，被 `top >= 0` 差一点点挡下；真机 top=24/48px 时按钮 top=23.6/47.6，
  //   恒真立刻成立。⇒ 「headless 全绿」在这一条上**不构成正确性证据**。
  if (topBar) {
    const inAnyBar = (el) => bars.some((b) => b.el === el || b.el.contains(el))
    const mainCand = [...document.querySelectorAll(
      'main button, main a, main input, main [role="button"], main [tabindex]:not([tabindex="-1"])')]
      .filter((el) => !inAnyBar(el) && vis(el) && el.getBoundingClientRect().height > 0)
    // `<main>` 里没有可交互元素时（Overview 全是 div），**没有可判的对象**，
    // 此时报「被压住」是没有意义的 —— 宁可不报，也不要拿栏自己的按钮凑数。
    const first = mainCand[0] || null
    I.stats.topBarCandidates = mainCand.length
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
  //
  // ⚠️ 读的是**算出来的** `--app-safe-*`，不是 env()：自定义属性在 computed-value
  //   阶段就完成变量替换，壳注入 `--safe-area-inset-*` 后这里直接拿到 px；
  //   浏览器/桌面两条通道都是 0 ⇒ 本节整体静默（不制造噪声）。
  const cs = getComputedStyle(de)
  const safe = {
    top: (cs.getPropertyValue('--app-safe-top') || '').trim(),
    bottom: (cs.getPropertyValue('--app-safe-bottom') || '').trim(),
    left: (cs.getPropertyValue('--app-safe-left') || '').trim(),
    right: (cs.getPropertyValue('--app-safe-right') || '').trim(),
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

  // ── ⑥-b 横向 safe-area（left / right）────────────────────────
  // 为什么必须单列一节：壳根 `.hyper-app` 用 `padding-inline: var(--app-safe-left/right)`
  // 消费横向 inset（10 §4.6.32），但**那只约束流内元素**。抽屉 / Sheet / FocusLayer
  // 是 `position:fixed` 且 `Teleport to="body"`，底栏与更新条也是 fixed
  // ⇒ 它们的包含块是**视口**，`left:0` 就是 x=0，壳根那点 padding 一丁点都碰不到它们。
  // 横屏握持时左右两侧正是系统手势区 / 刘海所在，这块从来没被任何一档视口量到过
  // （headless 的 env() 恒 0，视口宽度变化也改变不了 inset 带的位置）。
  //
  // 分级按几何谓词，不按元素类型：
  //   high   可点目标的**命中区中心**落在 inset 带内 ⇒ 最有把握的那一点就点不到
  //   medium 命中区探进 inset 带但中心在外 ⇒ 边缘死区（图标被刘海物理盖住 / 边缘误触返回）
  const needL = parseFloat(safe.left) || 0
  const needR = parseFloat(safe.right) || 0
  I.stats.safeAreaLR = { left: needL, right: needR }
  if (needL > 0 || needR > 0) {
    const dead = [], graze = []
    // 命中区口径与 ② 同源（label 会把点击转发给内部控件）
    document.querySelectorAll('button,a,[role="button"],input,select,textarea,[onclick]').forEach((el) => {
      if (!vis(el)) return
      if (getComputedStyle(el).pointerEvents === 'none') return
      const hit = el.closest('label,button,a,[role="button"]')
      const target = hit && hit !== el ? hit : el
      const r = target.getBoundingClientRect()
      if (r.right <= 0 || r.left >= innerWidth) return
      const cx = (r.left + r.right) / 2
      // 最近的定位祖先：fixed/sticky/absolute 才是「逃出壳根 padding」的那一类
      let pn = target.parentElement, anchored = ''
      while (pn && pn.nodeType === 1) {
        const ps = getComputedStyle(pn).position
        if (ps === 'fixed' || ps === 'sticky' || ps === 'absolute') { anchored = ps; break }
        pn = pn.parentElement
      }
      for (const side of ['left', 'right']) {
        const need = side === 'left' ? needL : needR
        if (!(need > 0)) continue
        const over = side === 'left' ? need - r.left : r.right - (innerWidth - need)
        if (over < 3) continue   // 亚像素/浮点噪声不算
        const rec = { sel: path(target), side, over: Math.round(over), need: Math.round(need),
          rect: [Math.round(r.left), Math.round(r.right)], anchored,
          text: (target.innerText || target.textContent || '').trim().slice(0, 20) }
        if (side === 'left' ? cx < need : cx > innerWidth - need) dead.push(rec)
        else graze.push(rec)
      }
    })
    if (dead.length) {
      I.issues.push({ kind: 'tappable-in-inset', sev: 'high',
        detail: dead.length + ' 个可点目标的命中区**中心**落在横向 inset 内（' +
                'left=' + needL + ' right=' + needR + 'px）：最可靠的那一点就落在系统手势区/刘海里',
        items: dead.slice(0, 10) })
    }
    if (graze.length) {
      I.issues.push({ kind: 'tappable-grazes-inset', sev: 'medium',
        detail: graze.length + ' 个可点目标的命中区探进横向 inset 带（' +
                'left=' + needL + ' right=' + needR + 'px）：边缘是死区，图标会被刘海压住',
        items: graze.slice(0, 10) })
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

  // ── ⑨ 文本被省略号截断（2026-10-08，§4.6.67 真机读数后补）────
  // 为什么补这一类：此前六类判据在 font_scale=2.0 的真机上全绿，
  // 但 `/m/` 的 Tokens 卡显示 `261,1…` —— 数字**读不出来**。
  // 根因是它 `overflow-x:hidden + text-overflow:ellipsis + white-space:nowrap`，
  // 而值宽 224px > 槽宽 152px ⇒ 被省略号吃掉 72px。
  // ⇒ 「不可见字」「低对比度」「横向溢出」全部测不到这一类：
  //   文字**在**盒子里、颜色**正常**、只是**少了一截**。
  //
  // ⚠️ 只报「明确声明了 ellipsis / line-clamp 且确实溢出」的；
  //    不去猜「看起来像被切了」—— 那类肉眼判断已被真机 CDP 证伪过
  //    （见 §4.6.67：吸底栏 Overview 看着像被屏幕左缘切掉，
  //      实测 scrollWidth == clientWidth、overflow-x:visible ⇒ 无裁切）。
  //
  // ★★ 为什么要分「量值 / 标识符」两桶（第一版没分，量出来 59 处一个数，
  //    等于把两种相反的结论混成一句废话）：
  //    · **量值**（数字 / 金额 / 百分比 / 时长 / 字节量）：被截断 ⇒ 用户
  //      **读不出这个数**，而它是仪表盘的主体内容 ⇒ high。
  //    · **标识符**（长 key 名 / 模型名 / 版本号）：截断通常就是**设计意图**
  //      （长名字必然溢出，展开会破版）⇒ 不是缺陷。可见比例 <50% 时，
  //      两个相近的名字（如 `deploy-smoke-245-…` 与 `deploy-smoke-154-…`）
  //      在列表里**无法区分** ⇒ medium；否则 low，只作台账。
  //
  // ⚠️ 第一版用「文本含不含字母」当判据，被**回读自证当场拦下**：
  //    `9,077ms`（延迟）因含 `m`/`s` 被判成标识符，落进 low 桶，
  //    而它其实是溢出 74px 的量值。⇒ 判据必须是「**剥掉单位与货币符号后
  //    整体仍是一个数**」，而不是「有没有字母」。
  //    数字形态收紧成 `\d+(\.\d+)?`（至多一个小数点），
  //    这样版本号 `1.2.3` 不会被误当成量值。
  //    误判方向是**保守**的：判不出来的都落进 low 的标识符桶，不会虚报 high。
  const UNIT_RE = /(ms|us|µs|min|hrs?|sec|days?|KB|MB|GB|TB|KiB|MiB|GiB|%|x|req|reqs|tokens?|calls?)$/i
  const isMetric = (t) => {
    let s = t.replace(/[\s,]/g, '').replace(/^[$¥€£]/, '')
    s = s.replace(UNIT_RE, '')
    return /^\d+(\.\d+)?$/.test(s)
  }
  // 真实可见内容：**逐字符取几何，看哪些字符真的被绘制**。
  // 为什么不用「从第 0 个字符起能放几个」那种 LTR 算法：
  //   名称列为了保住末尾的区分字符用了 `direction: rtl`（§4.6.76），
  //   此时溢出边在**行首**，可见的是**后缀**。LTR 算法会量出一个从未显示过的串，
  //   于是判据报出一个**已经不存在**的碰撞 —— 假阳性。
  //   逐字符取 Range 矩形、落在盒子内的才算绘制、再按 (top,left) 排视觉顺序，
  //   对 ltr / rtl / 换行都成立，**不预设方向**。
  // 成本护栏：只对**已判定截断**的标识符量，且单条超过 CAP 个字符就不量。
  const VISIBLE_CAP = 40
  const visiblePrefix = (el) => {
    const node = [...el.childNodes].find((n) => n.nodeType === 3 && n.textContent.trim())
    if (!node) return null
    const full = node.textContent
    if (full.length > VISIBLE_CAP) return null
    const box = el.getBoundingClientRect()
    const cs = getComputedStyle(el)
    const r = document.createRange()
    const painted = []
    for (let i = 0; i < full.length; i++) {
      r.setStart(node, i); r.setEnd(node, i + 1)
      const b = r.getBoundingClientRect()
      if (b.width === 0 && b.height === 0) continue
      if (b.right > box.left + 0.5 && b.left < box.right - 0.5 &&
          b.bottom > box.top + 0.5 && b.top < box.bottom - 0.5) {
        painted.push({ i, left: b.left, top: b.top })
      }
    }
    if (!painted.length) return null
    const ordered = painted.sort((a, b) => (a.top - b.top) || (a.left - b.left)).map((c) => full[c.i]).join('')
    const cut = painted.length < full.trimEnd().length
    const ell = cut && cs.textOverflow === 'ellipsis'
    const shown = (ell && cs.direction === 'rtl' ? '…' : '') + ordered +
                  (ell && cs.direction !== 'rtl' ? '…' : '')
    return { full, shown }
  }
  const truncated = []
  document.querySelectorAll('body *').forEach((el) => {
    if (el.children.length > 0) return          // 只看叶子，避免同一处截断被父子各报一次
    const s = getComputedStyle(el)
    const declaresCut = s.textOverflow === 'ellipsis' ||
                        (s.webkitLineClamp && s.webkitLineClamp !== 'none')
    if (!declaresCut) return
    if (el.clientWidth <= 0) return
    const over = el.scrollWidth - el.clientWidth
    if (over <= 1) return                        // 没真溢出就不报
    const txt = (el.textContent || '').trim()
    if (!txt) return
    const bucket = isMetric(txt) ? 'metric' : 'label'
    const ratio = over / el.scrollWidth          // 被吃掉的比例
    truncated.push({ sel: path(el), text: txt.slice(0, 24), over, bucket,
                     ratio: +ratio.toFixed(2),
                     clientW: el.clientWidth, scrollW: el.scrollWidth,
                     visible: bucket === 'label' ? visiblePrefix(el) : null })
  })
  I.stats.textTruncated = truncated.length
  I.stats.textTruncatedMetric = truncated.filter((x) => x.bucket === 'metric').length
  const byOver = (a, b) => b.over - a.over
  const metrics = truncated.filter((x) => x.bucket === 'metric').sort(byOver)
  const labels = truncated.filter((x) => x.bucket === 'label').sort(byOver)
  if (metrics.length) {
    const w = metrics[0]
    I.issues.push({ kind: 'text-truncated', sev: 'high',
      detail: metrics.length + ' 处**量值**被省略号截断（读不出数值）：最重 ' +
              w.sel + ' 「' + w.text + '」少 ' + w.over + 'px（吃掉 ' +
              Math.round(w.ratio * 100) + '%），槽宽 ' + w.clientW +
              'px / 需要 ' + w.scrollW + 'px',
      items: metrics.slice(0, 8) })
  }
  if (labels.length) {
    const hard = labels.filter((x) => x.ratio >= 0.5)
    const w = labels[0]
    I.issues.push({ kind: 'text-truncated', sev: hard.length ? 'medium' : 'low',
      detail: labels.length + ' 处**标识符**被省略号截断（长名称，多为设计意图；' +
              '其中 ' + hard.length + ' 处可见不足一半、相近名字可能无法区分）。' +
              '最重 ' + w.sel + ' 「' + w.text + '」少 ' + w.over +
              'px（吃掉 ' + Math.round(w.ratio * 100) + '%），槽宽 ' +
              w.clientW + 'px / 需要 ' + w.scrollW + 'px',
      items: labels.slice(0, 8) })
  }

  // ★★ 同一列表内**两个不同名字截断后显示成同一串** ⇒ 用户无法区分（§4.6.76）。
  // 逐条判「可见不足一半」看不见这个失效：两个名字可以各自都可见过半，
  // 却因为区分字符在**末尾**、被截断掉，而变成同一串。
  // 分组键去掉 `:nth-of-type(n)` ⇒ 只在**同一个容器**内两两比较。
  // 同名重复不算（`Set` 去重后长度为 1 ⇒ 那不是「无法区分」，是「确实一样」）。
  const collide = []
  {
    const byContainer = new Map()
    for (const x of labels) {
      if (!x.visible) continue
      const c = String(x.sel || '').replace(/:nth-of-type\(\d+\)/g, '')
      if (!byContainer.has(c)) byContainer.set(c, [])
      byContainer.get(c).push(x)
    }
    for (const [c, xs] of byContainer) {
      const byShown = new Map()
      for (const x of xs) {
        const k = x.visible.shown
        if (!byShown.has(k)) byShown.set(k, [])
        byShown.get(k).push(x)
      }
      for (const [shown, ys] of byShown) {
        const names = [...new Set(ys.map((y) => y.visible.full))]
        if (ys.length > 1 && names.length > 1) collide.push({ shown, names, sel: ys[0].sel })
      }
    }
  }
  if (collide.length) {
    const c = collide[0]
    I.issues.push({ kind: 'text-truncated', sev: 'medium',
      detail: collide.length + ' 组**标识符截断后显示为同一串**（原名不同 ⇒ 用户无法区分）：' +
              '最重一组 ' + c.names.join(' / ') + ' 都显示为「' + c.shown + '」' +
              '（区分字符在末尾、被截断掉；每一条单独看都还「可见过半」，' +
              '所以逐条判可见比例的判据看不见它）',
      items: collide.slice(0, 8).map((x) => ({ text: x.names.join(' / '), shown: x.shown, sel: x.sel })) })
  }

  // ── ⑧ 字体没加载出来（回退字形 = 版式整体偏移）────────────────
  I.stats.fontStatus = document.fonts ? document.fonts.status : 'unknown'

  // ── ⑨ 近白屏 ───────────────────────────────────────────────
  I.stats.textChars = (document.body?.innerText || '').trim().length
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
  // ⚠️ 判「滚动容器」**不能只看 overflow 的取值**（一次真实假阳性的根因）：
  //   原实现要求 `overflowY === 'auto' | 'scroll'`。但本仓的滚动宿主是
  //   `main#main-content.hyper-app__main`，它的 overflow 是 **hidden**
  //   （HyperApp.vue 的 `.hyper-app__main { overflow: hidden }` —— 注释写明
  //   「main 只做布局容器不滚动」），而实测它 **sh=1208 / ch=752 真实溢出**。
  //   ⇒ 谓词返回 false ⇒ 下一行的「滚动内容 × 固定栏」排除**从不触发**，
  //   `bottomnav__item` 与页面按钮的每一次「从下方经过」都被报成 tap-overlap
  //   （实测 /m/maas-orders 滚到底后被盖 0 个 ⇒ 全部是假阳性）。
  //
  //   真正决定「内容能不能移开」的**不是 overflow 取值，而是这个元素有没有溢出**。
  //   `scrollTop` 对 `overflow:hidden` 的容器照样可编程设置，滚动宿主也照样用它。
  function inScroller(el) {   // 函数声明：会提升。fixedBars（165 行）先用到它，
    let n = el.parentElement
    while (n && n !== document.body) {
      if (n.scrollHeight > n.clientHeight + 4) return true
      n = n.parentElement
    }
    return false
  }
  // ⚠️ 必须**向上看祖先**：实测底栏里 `nav.bottomnav > a.bottomnav__item` 自身是
  // position:static，fixed 挂在父级 nav.bottomnav 上。
  // 只看元素自身 ⇒ 这类（最常见的）固定栏永远判不出来，假阳性就一直报。
  function isFixedBar(el) {
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
  // ⚠️ `document.body` 必须可选链：导航切换的瞬间它可能是 null，
  //   无保护地读 `.innerText` 会抛 `TypeError: Cannot read properties of null`。
  //   实测 495 组里有 **181 组（36.6%）** 因此在 settle 采样阶段抛错，
  //   靠 `rescuedByRetry` 兜住 —— 兜住的是**流程**，不是那 36.6% 的噪声。
  //   body 为 null 本就等于「此刻什么都没渲染」，而「有没有内容」另有结构判据
  //   `nonBlank()`（节点数门槛 20，与语言无关），两者互不替代。
  const bodyText = () => (document.body?.innerText || '').replace(/\s+/g, ' ')
  const sig = () => {
    const t = bodyText().trim()
    return `${t.length}:${document.querySelectorAll('*').length}:${t.slice(0, 40)}`
  }
  const chars = () => bodyText().trim().length
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
