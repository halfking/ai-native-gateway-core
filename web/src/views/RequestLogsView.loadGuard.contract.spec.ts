/**
 * RequestLogsView.loadGuard.contract.spec.ts —— 「切换时间范围后列表数据正确 ＋
 * 加载态可见」的契约门（2026-11-25）。
 *
 * 存在理由（211 生产实测，两头都缺）：
 *
 * ① 后端「探测流量窗口自动截断到 12h」以未来时刻 end 为锚 ⇒ 今天 / 昨天 /
 *    近 N 天恒为空（后端根修见 admin/logs_page_topk_test.go）。
 * ② 前端 `load()` 没有任何过期判定 ⇒ 连续切换时间范围时，先发出的慢请求会在
 *    之后返回，把列表覆盖成旧范围的数据、或把它的超时写进 error；先完成的那个
 *    还会提前关掉 loading ⇒「切了范围后列表没数据 / 冒出旧报错」。
 * ③ 加载态只在表格里放了一行 **visibility:hidden** 的「加载中…」＋一个被裁掉的
 *    覆盖层 ⇒ 用户看到的是空表 ⇒ 误以为「没有数据」。
 *
 * 本门钉住前端这一半：过期请求防护的接线顺序、后台刷新不弹加载态、加载态可见且
 * 有读数、「查询失败」不写成「无记录」、中止信号真的透传到 fetch。
 * 纯逻辑（序号守卫 / 文案分档）的行为测试在 utils/latestRequestGuard.spec.ts 与
 * utils/logsLoadingCopy.spec.ts；真实页面表现由 Maestro 在 211 上验证。
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'

const WEB_ROOT = resolve(process.cwd(), 'src')
const VIEW_SRC = readFileSync(resolve(WEB_ROOT, 'views/RequestLogsView.vue'), 'utf8')
const API_SRC = readFileSync(resolve(WEB_ROOT, 'api/logs.ts'), 'utf8')

/**
 * 去掉注释，只留可执行代码（防「文档写清了缺陷」被误判成「缺陷修好了」）。
 *
 * ★ 先剥行注释、再剥块注释（顺序不能反）：视图里有一条既有注释写着
 *   `web/src/locales/` 加一个「星号点 ts」—— 先剥块注释时，那个「斜杠＋星号」
 *   会被当成块注释的起点，一直吞到下一个「星号＋斜杠」（在样式区），
 *   把中间几十行**可执行代码**一起吃掉 ⇒ 断言找不到刚写的那一行
 *   （2026-11-25 实测踩到）。
 */
function stripComments(src: string): string {
  return src
    .split('\n')
    .map((line) => {
      const idx = line.indexOf('//')
      if (idx === -1) return line
      const before = line.slice(0, idx)
      if (before.trimEnd().endsWith(':')) return line
      return before
    })
    .join('\n')
    .replace(/\/\*[\s\S]*?\*\//g, '')
}

/** 取出 `async function load() { … }` 的函数体（到第一个顶格 `}` 为止）。 */
function loadFunctionBody(): string {
  const code = stripComments(VIEW_SRC)
  const start = code.indexOf('async function load() {')
  expect(start, 'RequestLogsView.vue 里找不到 async function load()').toBeGreaterThan(-1)
  const end = code.indexOf('\n}\n', start)
  expect(end, 'load() 的结尾 } 找不到').toBeGreaterThan(start)
  return code.slice(start, end)
}

describe('load() 的过期请求防护', () => {
  it('每次加载领一个序号，并中止上一个在途请求', () => {
    const body = loadFunctionBody()
    expect(body).toContain('const seq = latestLoad.next()')
    // 先中止旧的，再建新的控制器（顺序反了会把自己中止掉）。
    expect(body).toMatch(/loadAbort\?\.abort\(\)\s+const abort = new AbortController\(\)/)
  })

  it('每个 await 之后、用响应之前，都必须先对号（过期响应丢弃）', () => {
    const body = loadFunctionBody()
    // 第二处 await 在 `if (overflowPage != null) { … }` 块里 ⇒ 信号参数与判定之间
    // 隔着块的闭合 `}`；两处之间都不许再出现别的 await。
    const guarded = body.match(
      /await getRequestLogs\(\{[\s\S]*?\}, \{ signal: abort\.signal \}\)\s*\}?\s*if \(!latestLoad\.isCurrent\(seq\)\) return/g,
    )
    expect(
      guarded,
      '两处 getRequestLogs（首发 + 页码越界重取）之后都必须紧跟 isCurrent 判定',
    ).toHaveLength(2)
    // 最后一次对号之后才落地：中间不许再有 await。
    expect(body).toMatch(/if \(!latestLoad\.isCurrent\(seq\)\) return\s+rows\.value = resp\.items/)
  })

  it('过期请求的失败不进错误面；被中止的请求（离开页面）也不进', () => {
    const body = loadFunctionBody()
    expect(body).toMatch(
      /catch \(e: unknown\) \{\s*if \(!latestLoad\.isCurrent\(seq\) \|\| abort\.signal\.aborted\) return/,
    )
    // 守卫不是吞错：最新那次请求的失败照常走原有文案逻辑。
    expect(body).toContain('error.value = surfaceError(e)')
  })

  it('只有最新那次请求可以收尾（不许提前关掉 loading）', () => {
    const body = loadFunctionBody()
    expect(body).toMatch(
      /finally \{\s*if \(latestLoad\.isCurrent\(seq\)\) \{\s*loading\.value = false\s+stopLoadingClock\(\)/,
    )
  })

  it('在途请求带的是本次加载的中止信号', () => {
    const body = loadFunctionBody()
    expect(body.match(/\{ signal: abort\.signal \}/g)).toHaveLength(2)
  })

  it('离开页面时中止在途请求（没人接收的查询不该白跑）', () => {
    const code = stripComments(VIEW_SRC)
    const at = code.indexOf('onBeforeUnmount(() => {')
    expect(at).toBeGreaterThan(-1)
    const block = code.slice(at, code.indexOf('})', at))
    expect(block).toContain('loadAbort?.abort()')
    expect(block).toContain('stopLoadingClock()')
  })
})

describe('自动刷新是后台行为，不弹加载态', () => {
  it('定时器走 backgroundRefresh()，由它标记静默加载', () => {
    const code = stripComments(VIEW_SRC)
    const start = code.indexOf('function startAutoRefresh() {')
    const fn = code.slice(start, code.indexOf('\n}\n', start))
    expect(fn).toContain('backgroundRefresh()')
    expect(fn).not.toMatch(/[^a-zA-Z]load\(\)/)
    expect(code).toMatch(/function backgroundRefresh\(\) \{\s*silentLoad = true/)
  })

  it('load() 按静默标记决定是否弹加载态 / 起计时', () => {
    const body = loadFunctionBody()
    expect(body).toContain('const silent = silentLoad')
    expect(body).toContain('loading.value = !silent')
    expect(body).toMatch(/if \(!silent\) startLoadingClock\(\)/)
  })
})

describe('加载态必须看得见、有读数', () => {
  it('表格体里有一整块加载态（role=status），带「已等待 N 秒」', () => {
    expect(VIEW_SRC).toContain('data-testid="logs-loading"')
    expect(VIEW_SRC).toMatch(/class="logs-loading" role="status" aria-live="polite"/)
    expect(VIEW_SRC).toContain('data-testid="logs-loading-elapsed"')
    // 「已等待 N 秒」走词条（requests.list.loading.elapsed），不写死中文。
    expect(VIEW_SRC).toMatch(/t\('requests\.list\.loading\.elapsed', \{ n: loadingElapsed \}\)/)
  })

  it('卡片顶部只要在查就走一条进度条（含已有行的刷新），且带结构锚点', () => {
    // ★ 必须带 data-testid：E2E 的「等这一轮查询收尾」就是等这个锚点消失
    //   （Maestro flow 19 / CDP 探针都靠它；仓内 audit-flow-anchor-existence.py 把关）。
    expect(VIEW_SRC).toMatch(
      /<div v-if="loading" class="logs-loading-bar" data-testid="logs-loading-bar" aria-hidden="true"><\/div>/,
    )
    expect(VIEW_SRC).toMatch(/'is-refreshing': loading && rows\.length > 0/)
    expect(VIEW_SRC).toContain('data-testid="logs-query-button"')
    expect(VIEW_SRC).toContain('data-testid="logs-total"')
  })

  it('不再用 visibility:hidden 的「加载中…」占位（那是一张用户看得见的空表）', () => {
    expect(VIEW_SRC).not.toMatch(/visibility:\s*hidden[^>]*>加载中/)
    expect(VIEW_SRC).not.toContain('class="loading-overlay"')
    expect(VIEW_SRC).not.toContain('loading-spinner')
  })

  it('文案不再按查询参数预判「可能需要 1-2 分钟」', () => {
    // ★ 对**去注释后的代码**判：本文件的注释与视图里的说明都会引用这句旧文案
    //   来解释「为什么改掉」——对原文做负向断言会打到这些说明（负向判据的自伤）。
    const code = stripComments(VIEW_SRC)
    expect(code).not.toMatch(/1-2\s*分钟/)
    expect(code).not.toContain('isLongQuery')
    expect(code).not.toContain('loadingMessage')
    // 文案来自按已等待秒数分档的纯函数（词条经 t() 取，不在视图里写死）。
    expect(code).toContain('logsLoadingCopy(loadingElapsed.value, t, includeProbe.value)')
  })

  it('「查询失败」与「无记录」是两种状态，表格体分开说', () => {
    const code = stripComments(VIEW_SRC)
    const loadingRow = code.indexOf('<tr v-if="loading && !rows.length">')
    const errorRow = code.indexOf('<tr v-else-if="error && !rows.length">')
    const emptyRow = code.indexOf('<tr v-else-if="!rows.length">')
    expect(loadingRow).toBeGreaterThan(-1)
    expect(errorRow).toBeGreaterThan(loadingRow)
    expect(emptyRow).toBeGreaterThan(errorRow)
    // 失败行用的是独立的词条（而不是复用「无记录」那一行的文本）。
    expect(code.slice(errorRow, emptyRow)).toContain("t('requests.list.queryFailedRow')")
    expect(code.slice(errorRow, emptyRow)).not.toContain('无记录')
    expect(code.slice(emptyRow, emptyRow + 200)).toContain('无记录')
  })

  it('样式走主题变量、尊重「减弱动态效果」', () => {
    expect(VIEW_SRC).toContain('var(--accent)')
    expect(VIEW_SRC).not.toContain('rgba(255, 255, 255, 0.95)')
    expect(VIEW_SRC).toMatch(/@media \(prefers-reduced-motion: reduce\)/)
  })
})

describe('getRequestLogs 的中止信号（行为层）', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('传入的 signal 原样透传给 fetch', async () => {
    const { getRequestLogs } = await import('../api/logs')
    const seenSignals: unknown[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
        seenSignals.push(init?.signal)
        return new Response(JSON.stringify({ items: [], count: 0 }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      }),
    )
    const controller = new AbortController()
    await getRequestLogs({ page: 1 }, { signal: controller.signal })
    expect(seenSignals).toHaveLength(1)
    expect(seenSignals[0]).toBe(controller.signal)
  })

  it('不传 signal 时请求照常发出（其它调用方不受影响）', async () => {
    const { getRequestLogs } = await import('../api/logs')
    const seenSignals: unknown[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
        seenSignals.push(init?.signal)
        return new Response(JSON.stringify({ items: [], count: 0 }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      }),
    )
    await getRequestLogs({ page: 1 })
    expect(seenSignals).toHaveLength(1)
    expect(seenSignals[0]).toBeUndefined()
  })

  it('源码上：req() 的第四个参数确实带了 signal（不是只在类型上声明）', () => {
    expect(API_SRC).toMatch(
      /req<RequestLogsResponse>\('GET', `\/api\/logs\$\{s \? '\?' \+ s : ''\}`, undefined, \{ signal: opts\?\.signal \}\)/,
    )
  })
})
