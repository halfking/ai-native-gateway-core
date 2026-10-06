import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import LogAdminView from './LogAdminView.vue'
import {
  fetchBodyCacheStats,
  fetchLogFiles,
  fetchLogStats,
  fetchLogArchiveList,
  type BodyCacheStats,
  type LogFilesList,
  type LogStats,
  type LogArchiveList,
} from '@/api/logsAdmin'
import { setLocale, locale } from '@/i18n'

/**
 * LogAdminView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 同一个「文件日志没启用」在**三个端点上是三个判据**：
 *     files `dir === ''` / stats `log_dir === ''` / archive-list **`dir` 键不存在`。
 * 2. ★★★★★ `files` 的 `is_archived` **恒 false**，且列表**不含归档**。
 * 3. ★★★★ 三个「0 是二义的」：`hit_rate` / `disk_usage_pct` /（间接）`exists:false`。
 * 4. ★★★★ 异形端点：①与② 的 archives/total **完全一样**，只有键在不在能分开。
 * 5. ★★★ `oldest/newest_mtime` 为 null ⇒ 一个非归档文件都没有。
 * 6. ★★ 四条**独立取数**：一条失败不许把别的清空。
 * 7. ★★ 抽屉席**必须不设** requiresRole（admin 档）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/logsAdmin', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/logsAdmin')>()
  return {
    ...actual,
    fetchBodyCacheStats: vi.fn(),
    fetchLogFiles: vi.fn(),
    fetchLogStats: vi.fn(),
    fetchLogArchiveList: vi.fn(),
  }
})

const cacheMock = fetchBodyCacheStats as unknown as ReturnType<typeof vi.fn>
const filesMock = fetchLogFiles as unknown as ReturnType<typeof vi.fn>
const statsMock = fetchLogStats as unknown as ReturnType<typeof vi.fn>
const archMock = fetchLogArchiveList as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/** 抄自 `admin/logs_body_cache.go:142-149`。 */
function cacheOf(over: Record<string, unknown> = {}): BodyCacheStats {
  return { size: 142, hits: 1023, misses: 287, evictions: 5, hit_rate: 1023 / 1310, cap: 1024, ...over } as BodyCacheStats
}

/** 抄自 `LogFilesListResponse`（admin/log_management.go:79-83）。 */
function filesOf(over: Record<string, unknown> = {}): LogFilesList {
  return {
    files: [
      {
        name: 'gateway.log',
        size_bytes: 524288,
        mod_time: '2026-10-08T12:00:00Z',
        is_current: true,
        is_compressed: false,
        is_archived: false,
        size_human: '512 KB',
      },
    ],
    total: 1,
    dir: '/var/log/gateway',
    ...over,
  } as LogFilesList
}

/** 抄自 `LogStatsResponse`（admin/log_management.go:65-76），10 键齐全。 */
function statsOf(over: Record<string, unknown> = {}): LogStats {
  return {
    log_dir: '/var/log/gateway',
    exists: true,
    total_files: 7,
    total_size_bytes: 1048576,
    total_size_human: '1 MB',
    archive_files: 3,
    archive_size: 524288,
    oldest_mtime: '2026-09-01T00:00:00Z',
    newest_mtime: '2026-10-08T12:00:00Z',
    disk_usage_pct: 12.5,
    ...over,
  } as LogStats
}

/** ★ 正常态（admin/log_management.go:609-614）。 */
function archOk(over: Record<string, unknown> = {}): LogArchiveList {
  return {
    archives: [{ name: 'gateway-20260901.log.gz', size_bytes: 2048, size_human: '2 KB', mod_time: '2026-09-02T00:00:00Z' }],
    total: 1,
    dir: '/var/log/gateway/archive',
    exists: true,
    ...over,
  } as LogArchiveList
}

/** ★★ 「未启用」那一支（:574）—— **`dir` / `exists` 两个键根本不存在**。 */
function archDisabled(): LogArchiveList {
  return { archives: [], total: 0 }
}

/** ★ 「归档目录读不到」那一支（:580）。 */
function archDirMissing(): LogArchiveList {
  return { archives: [], total: 0, dir: '/var/log/gateway/archive', exists: false }
}

interface Opts {
  cache?: unknown
  files?: unknown
  stats?: unknown
  archives?: unknown
}

/** ★ helper 接受覆盖 + `instanceof Error` 分派。 */
function setAll(o: Opts = {}): void {
  const put = (m: ReturnType<typeof vi.fn>, v: unknown, dft: unknown) => {
    if (v instanceof Error) m.mockRejectedValue(v)
    else m.mockResolvedValue(v ?? dft)
  }
  put(cacheMock, o.cache, cacheOf())
  put(filesMock, o.files, filesOf())
  put(statsMock, o.stats, statsOf())
  put(archMock, o.archives, archOk())
}

async function mountView(o: Opts = {}): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  setAll(o)
  const w = mount(LogAdminView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

/** ★★ 只取某个面板里的条目 —— `.la__item` 在多个面板都存在，全页计数会串。 */
function panelItems(w: ReturnType<typeof mount>, title: string) {
  const p = w
    .findAll('.la__panel')
    .find((x) => (x.find('.la__panel-title').exists() ? x.find('.la__panel-title').text() : '') === title)
  return p ? p.findAll('.la__item') : []
}

/** ★★ 找某个**面板**里的告警（面板按 `__panel-title` 定位，判据不能跨面板）。 */
function panelText(w: ReturnType<typeof mount>, title: string): string {
  const p = w.findAll('.la__panel').find((x) => (x.find('.la__panel-title').exists() ? x.find('.la__panel-title').text() : '') === title)
  return p?.text() ?? ''
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

describe('★★★★★★ 抽屉席**必须不设** requiresRole（admin 档）', () => {
  it('★★★★★★ log-admin 席存在且没有 requiresRole', async () => {
    const { DRAWER_NAV, navItemsFor } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'log-admin')
    expect(seat).toBeDefined()
    expect(seat?.requiresRole).toBeUndefined()
    expect(navItemsFor(DRAWER_NAV, 'tenant_admin').some((i) => i.key === 'log-admin')).toBe(true)
  })
})

describe('★★★★★★★★ 同一个「未启用」是**三个不同判据**', () => {
  // ★★★★★★ 本批核心：files 用空串、stats 用空串、archive-list 用「键不存在」
  //   ⇒ 三处的措辞必须**各不相同**，否则其中一处就是在说错话。
  it('★★★★★★★ files 判据 = `dir === ""`（空串）', async () => {
    const w = await mountView({ files: filesOf({ files: [], total: 0, dir: '' }) })
    expect(panelText(w, '日志文件')).toContain('目录这一栏是空串')
  })

  it('★★★★★★★ stats 判据 = `log_dir === ""`（空串）', async () => {
    const off = statsOf({
      log_dir: '',
      exists: false,
      total_files: 0,
      total_size_bytes: 0,
      total_size_human: '0 B',
      archive_files: 0,
      archive_size: 0,
      oldest_mtime: null,
      newest_mtime: null,
      disk_usage_pct: 0,
    })
    const w = await mountView({ stats: off })
    expect(panelText(w, '日志目录统计')).toContain('压根没启用')
  })

  it('★★★★★★★ archive-list 判据 = **键整个不存在**', async () => {
    const w = await mountView({ archives: archDisabled() })
    // ★ 措辞必须点明是「键不存在」而不是「值为空」
    expect(panelText(w, '归档列表')).toContain('这个键压根不存在')
  })

  it('★★★★★★★ 三处措辞互相不同（不许共用一句话）', async () => {
    const off = statsOf({ log_dir: '', exists: false, oldest_mtime: null, newest_mtime: null })
    const w = await mountView({ files: filesOf({ files: [], total: 0, dir: '' }), stats: off, archives: archDisabled() })
    const f = panelText(w, '日志文件')
    const s = panelText(w, '日志目录统计')
    const a = panelText(w, '归档列表')
    expect(f).toContain('空串')
    expect(s).toContain('压根没启用')
    expect(a).toContain('键压根不存在')
    // ★ 三段文本两两不同
    expect(f).not.toBe(s)
    expect(s).not.toBe(a)
    expect(f).not.toBe(a)
  })

  // ★★★★ 这条是**变异 V2 逼出来的**：把 `filesDisabled` 从 `dir === ''`
  //   改成 `files.length === 0` 时，原来所有用例**照样全绿** ——
  //   因为喂的「未启用」夹具**同时**满足 `dir === ''` 且 `files` 为空。
  //   ⇒ 缺口是「启用了但目录里没有文件」这一态**从没喂过**。
  it('★★★★★★★ `dir` 有值但文件为空 ⇒ 那是「没文件」，**不是**「未启用」', async () => {
    const w = await mountView({ files: filesOf({ files: [], total: 0, dir: '/var/log/gateway' }) })
    const f = panelText(w, '日志文件')
    expect(f).toContain('没有任何日志文件')
    expect(f).not.toContain('目录这一栏是空串')
  })

  it('★★★★★ archive-list 的 `exists:false` **不是**「未启用」', async () => {
    const w = await mountView({ archives: archDirMissing() })
    const a = panelText(w, '归档列表')
    expect(a).toContain('在磁盘上读不到')
    expect(a).not.toContain('这个键压根不存在')
  })

  it('★★★★★ stats 的「目录不存在」**不是**「未启用」', async () => {
    const w = await mountView({ stats: statsOf({ exists: false, oldest_mtime: null, newest_mtime: null }) })
    const s = panelText(w, '日志目录统计')
    expect(s).toContain('在磁盘上不存在')
    expect(s).not.toContain('压根没启用')
  })
})

describe('★★★★★★★★ 异形端点：①与② 只有键在不在能分开', () => {
  it('★★★★★★★ 未启用与目录读不到：内容一样，措辞不同', async () => {
    const w1 = await mountView({ archives: archDisabled() })
    const w2 = await mountView({ archives: archDirMissing() })
    // ★ 两者的归档列表都是空、总数都是 0。
    //   ⚠️ `.la__item` **同时存在于 files 与 archives 两个面板** ⇒ 全页计数会被
    //   默认夹具里那个日志文件算进去。必须按面板作用域取。
    expect(panelItems(w1, '归档列表')).toHaveLength(0)
    expect(panelItems(w2, '归档列表')).toHaveLength(0)
    // 但面板文案必须不同
    expect(panelText(w1, '归档列表')).not.toBe(panelText(w2, '归档列表'))
  })

  it('★★★★★ 正常态渲染出归档条目', async () => {
    const w = await mountView({ archives: archOk() })
    const a = panelText(w, '归档列表')
    expect(a).toContain('gateway-20260901.log.gz')
    expect(a).toContain('2 KB')
  })

  it('★★★★★ 页面明说「形状不固定，只能靠键在不在」', async () => {
    const w = await mountView({ archives: archDisabled() })
    expect(panelText(w, '归档列表')).toContain('形状不固定')
  })
})

describe('★★★★★★★★ `is_archived` 恒 false，且列表不含归档', () => {
  it('★★★★★★★★ 页面明说这个字段是写死的、且列表不含归档', async () => {
    const w = await mountView()
    const f = panelText(w, '日志文件')
    expect(f).toContain('永远是 false')
    expect(f).toContain('本身就不含归档')
    // ★ 关键：必须明说**别**用它推断「没有归档」
    expect(f).toContain('别用这个字段推断')
  })

  it('★★★★★ 行上不渲染任何「归档」标签（因为后端给不出 true）', async () => {
    const w = await mountView({
      files: filesOf({
        files: [
          {
            name: 'gateway.log',
            size_bytes: 100,
            mod_time: '2026-10-08T12:00:00Z',
            is_current: true,
            is_compressed: false,
            is_archived: false,
            size_human: '100 B',
          },
        ],
        total: 1,
      }),
    })
    const tail = w.find('.la__item .la__tail')
    expect(tail.exists()).toBe(true)
    // 归档标签不存在；压缩标签也在（is_compressed 才是真算的）
    expect(tail.text()).not.toContain('归档')
  })

  it('★★★★★★ `is_compressed` 与 `is_archived` 是**两个不同**的字段（一个真算一个写死）', async () => {
    const w = await mountView({
      files: filesOf({
        files: [
          {
            name: 'gateway-20260901.log.gz',
            size_bytes: 100,
            mod_time: '2026-10-08T12:00:00Z',
            is_current: false,
            is_compressed: true,
            is_archived: false,
            size_human: '100 B',
          },
        ],
        total: 1,
      }),
    })
    const tail = w.find('.la__item .la__tail').text()
    // ★ 夹具里 is_current:false ⇒ 只有「已压缩」；**不能**断言「当前活动」
    expect(tail).toContain('已压缩')
    expect(tail).not.toContain('当前活动')
  })
})

describe('★★★★★★ 三个「0 是二义的」', () => {
  it('★★★★★★★ hit_rate 0 + 无流量 ⇒ 提示「不是 0% 命中率」', async () => {
    const w = await mountView({ cache: cacheOf({ hits: 0, misses: 0, hit_rate: 0 }) })
    expect(panelText(w, '请求体缓存')).toContain('不是 0% 命中率')
  })

  it('★★★★★★★ hit_rate 0 + 有流量 ⇒ 提示「这才是真的 0%」', async () => {
    const w = await mountView({ cache: cacheOf({ hits: 0, misses: 7, hit_rate: 0 }) })
    const c = panelText(w, '请求体缓存')
    expect(c).toContain('一次都没命中')
    expect(c).not.toContain('不是 0% 命中率')
  })

  it('★★★★★ 正常命中 ⇒ 两条提示都不出', async () => {
    const w = await mountView({ cache: cacheOf() })
    const c = panelText(w, '请求体缓存')
    expect(c).not.toContain('不是 0% 命中率')
    expect(c).not.toContain('一次都没命中')
  })

  it('★★★★★ 命中率复算不上 ⇒ 告警', async () => {
    const w = await mountView({ cache: cacheOf({ hit_rate: 0.9 }) })
    expect(panelText(w, '请求体缓存')).toContain('复算不出来')
  })

  it('★★★★★ size > cap ⇒ 告警', async () => {
    const w = await mountView({ cache: cacheOf({ size: 2000, cap: 1024 }) })
    expect(panelText(w, '请求体缓存')).toContain('超过了它自己声明的容量')
  })

  it('★★★★★ disk_usage_pct 0 ⇒ 提示分不出「0%」与「探测失败」', async () => {
    const w = await mountView({ stats: statsOf({ disk_usage_pct: 0 }) })
    expect(panelText(w, '日志目录统计')).toContain('探测磁盘失败')
  })

  it('★★★★★ disk_usage_pct 非 0 ⇒ 不出那条提示（不能恒真）', async () => {
    const w = await mountView({ stats: statsOf({ disk_usage_pct: 12.5 }) })
    expect(panelText(w, '日志目录统计')).not.toContain('探测磁盘失败')
  })
})

describe('★★★★★★ stats 的其余契约', () => {
  it('★★★★★★ 「总文件数」不含归档：页面自己说明口径', async () => {
    const w = await mountView({ stats: statsOf({ total_files: 7, archive_files: 3 }) })
    const s = panelText(w, '日志目录统计')
    expect(s).toContain('不含归档目录里的文件')
    expect(s).toContain('10')
  })

  it('★★★★★★ mtime 为 null ⇒ 提示「一个非归档文件都没有」', async () => {
    const w = await mountView({ stats: statsOf({ oldest_mtime: null, newest_mtime: null }) })
    // ★ 断言串必须从**实际 i18n 文案**里挑，不是凭印象改写。
    //   文案是「最早/最新修改时间是空值，说明这个目录下没有一个非归档日志文件。」
    expect(panelText(w, '日志目录统计')).toContain('最早/最新修改时间是空值')
  })

  // ★★★ 变异 V12 把这段说明删掉时**全绿** —— 因为我写了这个 i18n 键却**从没断言过它**。
  it('★★★★★ 页面明说「存在」字段分不出上面两种状态', async () => {
    const w = await mountView()
    expect(panelText(w, '日志目录统计')).toContain('这个字段分不出上面两种情况')
  })

  it('★★★★★ 有人读大小复算不上 ⇒ 告警', async () => {
    const w = await mountView({ stats: statsOf({ total_size_human: '999 GB' }) })
    expect(panelText(w, '日志目录统计')).toContain('复算不出来')
  })
})

describe('★★★★★★ 四条独立取数', () => {
  // ★ 一条失败**不许**把另外三条清空 —— Promise.all 就会那样
  it('★★★★★★★ files 失败 ⇒ 其余三块**照常渲染**', async () => {
    const w = await mountView({ files: new Error('list log files') })
    expect(panelText(w, '请求体缓存')).toContain('命中')
    expect(panelText(w, '日志目录统计')).toContain('日志目录统计')
    expect(panelText(w, '归档列表')).toContain('gateway-20260901.log.gz')
    // 且 files 那块显示的是错误，不是「没有文件」
    expect(panelText(w, '日志文件')).toContain('list log files')
    expect(panelText(w, '日志文件')).not.toContain('没有任何日志文件')
  })

  it('★★★★★ 四条各请求一次，没有别的', async () => {
    await mountView()
    expect(cacheMock).toHaveBeenCalledTimes(1)
    expect(filesMock).toHaveBeenCalledTimes(1)
    expect(statsMock).toHaveBeenCalledTimes(1)
    expect(archMock).toHaveBeenCalledTimes(1)
  })

  it('★★★★★ 503 只影响那一块，且文案说「没初始化」而不是「无权限」', async () => {
    const w = await mountView({ cache: new Error('body cache not initialized') })
    expect(panelText(w, '请求体缓存')).toContain('没初始化')
    // 另三块不受影响
    expect(panelText(w, '日志目录统计')).toContain('正常')
  })
})

describe('★★★★★★ 读面只有重取，没有任何写控件', () => {
  it('★★★★★ 页面上没有任何归档/删除/改配置的控件', async () => {
    const w = await mountView()
    const labels = w.findAll('button').map((b) => b.text())
    expect(labels).toHaveLength(1) // 只有刷新
    expect(labels[0]).toContain('刷新')
    // ★ 全页 not.toContain('删除') / ('归档') 都会被**我自己写的 readOnlyNote** 判红
    //   （那句免责里就写着「归档、删除都是 superAdmin 档的写操作」）
    //   ⇒ 判据只能落在**可点元素**上：按钮数与按钮文案。
    const actionable = w.findAll('button, a, input, select')
    expect(actionable.map((n) => n.text())).toEqual(['刷新'])
  })

  it('★★★★★ 页面明说三个 superAdmin 写操作都没碰', async () => {
    const w = await mountView()
    expect(w.text()).toContain('归档、删除')
  })
})
