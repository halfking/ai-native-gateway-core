import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import LogOpsView from './LogOpsView.vue'
import { fetchLogStats, fetchLogFiles, fetchBodyCacheStats } from '@/api/logOps'
import { setLocale, locale } from '@/i18n'

/**
 * LogOpsView 的不变量（2026-10-08，第六十二批）。
 *
 * ★ 钉住的五处「不能都渲染成同一个东西」：
 *   ① **三种空态可分**：未启用 / 目录不存在 / 目录里没文件。
 *   ② **`disk_usage_pct` 的 0 可能是查询失败** ⇒ 口径随值一起显示。
 *   ③ **`hit_rate` 的 0 可能是无样本** ⇒ 显示「无样本」不是「0% 命中率」。
 *   ④ **files 空列表同样两种成因**，靠 `dir` 区分。
 *   ⑤ **时间指针为 null ⇒ 跨度算不出来**，不拿「现在」顶。
 * 外加：失败路径清空旧数据、契约漂移单独报警、503 的专属文案。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/logOps', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/logOps')>()
  return {
    ...actual,
    fetchLogStats: vi.fn(),
    fetchLogFiles: vi.fn(),
    fetchBodyCacheStats: vi.fn(),
  }
})

const statsMock = fetchLogStats as unknown as ReturnType<typeof vi.fn>
const filesMock = fetchLogFiles as unknown as ReturnType<typeof vi.fn>
const cacheMock = fetchBodyCacheStats as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(LogOpsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  return w
}

type W = ReturnType<typeof mount>

function sectionOf(w: W, index: number) {
  return w.findAll('.lo__section')[index]!
}
function loadBtn(w: W, n: number) {
  return w.findAll('.lo__load')[n]!
}
async function clickLoad(w: W, n: number): Promise<void> {
  await loadBtn(w, n).trigger('click')
  await flushPromises()
  await flushPromises()
}

/* ── 夹具：逐字照抄后端 ─────────────────────────────────────────────── */

const STATS_NORMAL = {
  log_dir: '/var/log/llmgw',
  exists: true,
  total_files: 12,
  total_size_bytes: 1048576,
  total_size_human: '1.0 MB',
  archive_files: 3,
  archive_size: 262144,
  oldest_mtime: '2026-10-01T00:00:00Z',
  newest_mtime: '2026-10-08T00:00:00Z',
  disk_usage_pct: 12.5,
}

/** ★ `cur.File == ""` ⇒ 零值直接下发（log_management.go:326-329）。 */
const STATS_NOT_ENABLED = {
  log_dir: '', exists: false, total_files: 0, total_size_bytes: 0,
  total_size_human: '', archive_files: 0, archive_size: 0,
  oldest_mtime: null, newest_mtime: null, disk_usage_pct: 0,
}

/** ★ 配了路径但目录不存在（:332-335）。 */
const STATS_DIR_MISSING = { ...STATS_NOT_ENABLED, log_dir: '/var/log/llmgw' }

/** ★ 目录在但没文件。 */
const STATS_EMPTY = { ...STATS_DIR_MISSING, exists: true, total_size_human: '0 B' }

const FILES_NORMAL = {
  files: [
    { name: 'gateway.log', size_bytes: 2048, mod_time: '2026-10-08T02:00:00Z', is_current: true, is_compressed: false, is_archived: false, size_human: '2.0 KB' },
    { name: 'gw.2026-10-01.log.gz', size_bytes: 1024, mod_time: '2026-10-01T02:00:00Z', is_current: false, is_compressed: true, is_archived: true, size_human: '1.0 KB' },
  ],
  total: 2,
  dir: '/var/log/llmgw',
}

const FILES_NOT_ENABLED = { files: [], total: 0, dir: '' }
const FILES_EMPTY = { files: [], total: 0, dir: '/var/log/llmgw' }

const CACHE_NORMAL = { size: 142, hits: 1023, misses: 287, evictions: 5, hit_rate: 0.781, cap: 1024 }

/** ★ 分母为 0 ⇒ hit_rate 留 0.0。 */
const CACHE_NO_SAMPLE = { size: 0, hits: 0, misses: 0, evictions: 0, hit_rate: 0, cap: 1024 }

beforeEach(() => {
  setLocale('zh-CN')
  statsMock.mockReset()
  filesMock.mockReset()
  cacheMock.mockReset()
})

afterEach(() => {
  mountedList.forEach((w) => w.unmount())
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

/* ═══════════════════════════════════════════════════════════════════════
 * ① 三种空态
 * ═══════════════════════════════════════════════════════════════════════ */

describe('LogOpsView ① 三种「什么都没有」', () => {
  it('未启用 ⇒ 说「文件日志未启用」', async () => {
    statsMock.mockResolvedValue(STATS_NOT_ENABLED)
    const w = await mountView()
    await clickLoad(w, 0)
    const txt = sectionOf(w, 0).text()
    expect(txt).toContain('文件日志未启用')
    // ★ 未启用时不该渲染体积/时间等数字
    expect(sectionOf(w, 0).findAll('.lo__kpi').length).toBe(0)
  })

  it('★ 目录不存在 ⇒ 说「路径本身没了」而不是「没有日志」', async () => {
    statsMock.mockResolvedValue(STATS_DIR_MISSING)
    const w = await mountView()
    await clickLoad(w, 0)
    const txt = sectionOf(w, 0).text()
    expect(txt).toContain('日志目录不存在')
    expect(txt).toContain('/var/log/llmgw')
    expect(txt).not.toContain('未启用')
    expect(txt).not.toContain('一个日志文件都没有')
  })

  it('★ 目录在但没文件 ⇒ 第三种文案', async () => {
    statsMock.mockResolvedValue(STATS_EMPTY)
    const w = await mountView()
    await clickLoad(w, 0)
    const txt = sectionOf(w, 0).text()
    expect(txt).toContain('一个日志文件都没有')
    expect(txt).not.toContain('未启用')
    expect(txt).not.toContain('目录不存在')
  })

  it('★★★ 目录不存在（exists=false 但有路径）不得被说成「没文件」', async () => {
    // ★★★ 这条是 #2 那条变异的**判别样本**：
    //   STATS_DIR_MISSING 的 exists 也是 false、total_files 也是 0 ——
    //   若把 `logStatsEmpty` 的 `exists === true` 检查去掉，
    //   上一条用例（notEnabled 先短路）完全不受影响，只有这条会红。
    //   两者在值上都满足「没文件」，但**语义相反**：
    //   一个是目录在而里面空，一个是路径本身没了。
    statsMock.mockResolvedValue(STATS_DIR_MISSING)
    const w = await mountView()
    await clickLoad(w, 0)
    const txt = sectionOf(w, 0).text()
    expect(txt).toContain('日志目录不存在')
    expect(txt).not.toContain('一个日志文件都没有')
  })

  it('★ 正常目录 ⇒ 三种空态文案一个都不出现', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    await clickLoad(w, 0)
    const txt = sectionOf(w, 0).text()
    expect(txt).not.toContain('未启用')
    expect(txt).not.toContain('目录不存在')
    expect(txt).not.toContain('一个日志文件都没有')
    expect(sectionOf(w, 0).findAll('.lo__kpi').length).toBe(2)
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ②⑤ 零值不可信 + 时间跨度算不出
 * ═══════════════════════════════════════════════════════════════════════ */

describe('LogOpsView ②⑤ 零值口径与时间跨度', () => {
  it('★★ disk_usage_pct = 0 ⇒ 必须带「可能是查询失败」的口径说明', async () => {
    // log_management.go:367-369 查失败时只是不赋值 ⇒ 0 与「真的 0%」不可分
    statsMock.mockResolvedValue({ ...STATS_NORMAL, disk_usage_pct: 0 })
    const w = await mountView()
    await clickLoad(w, 0)
    const row = sectionOf(w, 0).findAll('.lo__kv-row').find((r) => r.text().includes('磁盘占比'))!
    expect(row.text()).toContain('可能是查询失败')
    expect(row.find('dd').classes()).toContain('lo__nodata')
  })

  it('磁盘占比非 0 ⇒ 不加免责说明', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    await clickLoad(w, 0)
    const row = sectionOf(w, 0).findAll('.lo__kv-row').find((r) => r.text().includes('磁盘占比'))!
    expect(row.text()).not.toContain('可能是查询失败')
    expect(row.find('dd').classes()).not.toContain('lo__nodata')
  })

  it('★ 时间指针为 null ⇒ 说「算不出来」而不是显示 0 或「现在」', async () => {
    statsMock.mockResolvedValue({ ...STATS_NORMAL, oldest_mtime: null })
    const w = await mountView()
    await clickLoad(w, 0)
    const row = sectionOf(w, 0).findAll('.lo__kv-row').find((r) => r.text().includes('时间跨度'))!
    expect(row.text()).toContain('算不出')
    expect(row.find('dd').classes()).toContain('lo__nodata')
  })

  it('时间指针齐全 ⇒ 显示相对时间跨度', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    await clickLoad(w, 0)
    const row = sectionOf(w, 0).findAll('.lo__kv-row').find((r) => r.text().includes('时间跨度'))!
    expect(row.text()).not.toContain('算不出')
  })

  it('★ 归档占比分母为 0 ⇒ 说「算不出」而不是 0%', async () => {
    statsMock.mockResolvedValue({ ...STATS_NORMAL, total_size_bytes: 0, archive_size: 0 })
    const w = await mountView()
    await clickLoad(w, 0)
    const row = sectionOf(w, 0).findAll('.lo__kv-row').find((r) => r.text().includes('归档占比'))!
    expect(row.text()).toContain('算不出')
    expect(row.text()).not.toContain('0.0%')
  })

  it('归档占比可算 ⇒ 显示真实百分比', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    await clickLoad(w, 0)
    const row = sectionOf(w, 0).findAll('.lo__kv-row').find((r) => r.text().includes('归档占比'))!
    expect(row.text()).toContain('25.0%')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ③ body 缓存：无样本 ≠ 0% 命中率
 * ═══════════════════════════════════════════════════════════════════════ */

describe('LogOpsView ③ 详情 body 缓存', () => {
  it('★★ 无样本 ⇒ 显示「无样本」而不是 0.0% 命中率', async () => {
    cacheMock.mockResolvedValue(CACHE_NO_SAMPLE)
    const w = await mountView()
    await clickLoad(w, 2)
    const sec = sectionOf(w, 2)
    expect(sec.text()).toContain('无样本')
    expect(sec.text()).not.toContain('0.0%')
  })

  it('有样本 ⇒ 显示真实命中率', async () => {
    cacheMock.mockResolvedValue(CACHE_NORMAL)
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).toContain('78.1%')
  })

  it('★ 缓存满 + 有淘汰 ⇒ 说「正在淘汰」', async () => {
    cacheMock.mockResolvedValue({ ...CACHE_NORMAL, size: 1024, evictions: 5 })
    const w = await mountView()
    await clickLoad(w, 2)
    expect(sectionOf(w, 2).text()).toContain('正在淘汰')
  })

  it('★ 缓存满但零淘汰 ⇒ 只说「已满」，不说「正在淘汰」', async () => {
    // 这条是上一条的判别样本：evictions=0 时不能断言在淘汰
    cacheMock.mockResolvedValue({ ...CACHE_NORMAL, size: 1024, evictions: 0 })
    const w = await mountView()
    await clickLoad(w, 2)
    const txt = sectionOf(w, 2).text()
    expect(txt).toContain('缓存已满')
    expect(txt).not.toContain('正在淘汰')
  })

  it('★ 503（缓存未初始化）⇒ 专属文案而不是裸错误串', async () => {
    cacheMock.mockRejectedValue(Object.assign(new Error('body cache not initialized'), { status: 503 }))
    const w = await mountView()
    await clickLoad(w, 2)
    const txt = sectionOf(w, 2).text()
    expect(txt).toContain('缓存未初始化')
    expect(txt).not.toContain('body cache not initialized')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ④ files：两种空态 + 标志位 + 契约漂移
 * ═══════════════════════════════════════════════════════════════════════ */

describe('LogOpsView ④ 日志文件清单', () => {
  it('未启用 ⇒ 空列表是「没有日志可列」', async () => {
    filesMock.mockResolvedValue(FILES_NOT_ENABLED)
    const w = await mountView()
    await clickLoad(w, 1)
    const txt = sectionOf(w, 1).text()
    expect(txt).toContain('没有日志可列')
    expect(txt).not.toContain('没有可列出的日志文件')
  })

  it('★ 已启用但没文件 ⇒ 另一种文案', async () => {
    filesMock.mockResolvedValue(FILES_EMPTY)
    const w = await mountView()
    await clickLoad(w, 1)
    const txt = sectionOf(w, 1).text()
    expect(txt).toContain('没有可列出的日志文件')
    expect(txt).not.toContain('没有日志可列')
  })

  it('★ 归档 / 压缩 / 当前 三个标志各自独立渲染', async () => {
    filesMock.mockResolvedValue(FILES_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    const items = sectionOf(w, 1).findAll('.lo__item')
    expect(items.length).toBe(2)
    // 第一行：仅「当前活动」
    expect(items[0]!.findAll('.lo__flag').map((f) => f.text())).toEqual(['当前活动'])
    // 第二行：归档 + 压缩（两个独立标志，不合成一个）
    expect(items[1]!.findAll('.lo__flag').map((f) => f.text()).sort())
      .toEqual(['在归档目录', '已压缩'])
  })

  it('★ total 与实际条数不一致 ⇒ 报警', async () => {
    filesMock.mockResolvedValue({ ...FILES_NORMAL, total: 99 })
    const w = await mountView()
    await clickLoad(w, 1)
    const txt = sectionOf(w, 1).text()
    expect(txt).toContain('契约异常')
    expect(txt).toContain('99')
    expect(txt).toContain('2')
  })

  it('正常清单不报契约异常', async () => {
    filesMock.mockResolvedValue(FILES_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).not.toContain('契约异常')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * 加载语义与失败路径
 * ═══════════════════════════════════════════════════════════════════════ */

describe('LogOpsView 加载与失败路径', () => {
  it('★ 失败 ⇒ 清掉旧数据，不保留上一次结果', async () => {
    // ★ 必须「先成功、再失败」：只测首次失败时「是否清空」是恒真的。
    filesMock.mockResolvedValueOnce(FILES_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).findAll('.lo__item').length).toBe(2)

    filesMock.mockRejectedValueOnce(new Error('boom'))
    await clickLoad(w, 1)
    const sec = sectionOf(w, 1)
    expect(sec.text()).toContain('boom')
    expect(sec.findAll('.lo__item').length).toBe(0)
  })

  it('加载成功后按钮切到「重新加载」', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    expect(loadBtn(w, 0).text()).toBe('加载')
    await clickLoad(w, 0)
    expect(loadBtn(w, 0).text()).toBe('重新加载')
  })

  it('403 ⇒ 无权文案', async () => {
    statsMock.mockRejectedValue(Object.assign(new Error('Forbidden'), { status: 403 }))
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('无权查看日志运维面')
  })

  it('★ 三段互不串扰：只加载一段时其余段不渲染数字', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).findAll('.lo__item').length).toBe(0)
    expect(sectionOf(w, 2).findAll('.lo__kpi').length).toBe(0)
  })
})
