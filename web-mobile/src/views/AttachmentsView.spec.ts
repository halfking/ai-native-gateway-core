import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AttachmentsView from './AttachmentsView.vue'
import {
  fetchAttachments,
  fetchAttachmentStats,
  fetchAttachmentPolicy,
  previewAttachmentCleanup,
  fetchAttachmentFilesystemStats,
  type AttachmentList,
  type AttachmentStats,
  type AttachmentPolicy,
  type AttachmentCleanupPreview,
  type AttachmentFilesystemStats,
  type AttachmentRow,
} from '@/api/attachments'
import { setLocale, locale } from '@/i18n'

/**
 * AttachmentsView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 同一个 `attachments` 字段，list 侧**可为 JSON 标量 null**，item 侧保证是数组。
 * 2. ★★★★ stats 的行范围比 list **窄** ⇒ 条数对不上是预期的，不是数据错。
 * 3. ★★★★ 时间参数**解析失败被静默丢弃** ⇒ 非法时间必须提示「窗口没生效」。
 * 4. ★★ limit/offset 是**两端 clamp**（不是回落默认）。
 * 5. ★★ `policy` 是硬编码常量。
 * 6. ★★ 抛错不许退化成空清单。
 * 7. ★★ 抽屉席**必须不设** requiresRole（admin 档）。
 */

const routeMock: { value: { query: Record<string, string> } } = { value: { query: {} } }
vi.mock('vue-router', () => ({ useRoute: () => routeMock.value }))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/attachments', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/attachments')>()
  return {
    ...actual,
    fetchAttachments: vi.fn(),
    fetchAttachmentStats: vi.fn(),
    fetchAttachmentPolicy: vi.fn(),
    fetchAttachmentFilesystemStats: vi.fn(),
    previewAttachmentCleanup: vi.fn(),
  }
})

const listMock = fetchAttachments as unknown as ReturnType<typeof vi.fn>
const statMock = fetchAttachmentStats as unknown as ReturnType<typeof vi.fn>
const polMock = fetchAttachmentPolicy as unknown as ReturnType<typeof vi.fn>
const fsMock = fetchAttachmentFilesystemStats as unknown as ReturnType<typeof vi.fn>
const prevMock = previewAttachmentCleanup as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function row(over: Record<string, unknown> = {}): AttachmentRow {
  return {
    request_id: 'req-1',
    ts: '2026-10-01T00:00:00Z',
    tenant_id: 'acme',
    client_model: 'gpt-4o',
    success: true,
    attachments: [{ type: 'image', size: 10 }],
    ...over,
  } as AttachmentRow
}

function listOf(items: AttachmentRow[] = [row()]): AttachmentList {
  return { items, limit: 50, offset: 0, count: items.length }
}

function statsOf(count = 1): AttachmentStats {
  return {
    breakdown: [{ type: 'image', content_type: 'image/png', count, total_bytes: count * 10 }],
    total_count: count,
    total_bytes: count * 10,
  }
}

function policyOf(): AttachmentPolicy {
  return {
    policy: {
      retention_days: 30,
      max_size_bytes: 20971520,
      auto_cleanup: false,
      delete_filesystem: false,
      description: '附件元数据保留 30 天；默认不自动清理。',
    },
    note: '可通过环境变量 LLM_GATEWAY_ATTACHMENT_DISABLED=1 完全关闭附件捕获。',
  } as AttachmentPolicy
}

function fsOf(over: Record<string, unknown> = {}): AttachmentFilesystemStats {
  return {
    attachment_dir: '/var/lib/att',
    total_files: 3,
    total_size_bytes: 300,
    total_size_human: '300 B',
    oldest_file_time: '2026-01-01T00:00:00Z',
    disk_total_bytes: 1000,
    disk_used_bytes: 200,
    disk_avail_bytes: 800,
    disk_usage_percent: 20,
    disk_warning_level: 'safe',
    ...over,
  } as AttachmentFilesystemStats
}

function previewOf(): AttachmentCleanupPreview {
  return {
    older_than_days: 30,
    affected_records: 7,
    total_bytes: 700,
    dry_run: true,
    action: '将把匹配记录的 attachments 列置为 NULL（保留行和元数据）',
  }
}

/** ★ helper 接受覆盖 + instanceof Error 分派。 */
function setAll(opts: {
  list?: unknown
  stats?: unknown
  policy?: unknown
  fs?: unknown
}): void {
  const put = (m: ReturnType<typeof vi.fn>, v: unknown, dft: unknown) => {
    if (v instanceof Error) m.mockRejectedValue(v)
    else m.mockResolvedValue(v ?? dft)
  }
  put(listMock, opts.list, listOf())
  put(statMock, opts.stats, statsOf())
  put(polMock, opts.policy, policyOf())
  put(fsMock, opts.fs, fsOf())
}

async function mountView(opts: Parameters<typeof setAll>[0] = {}): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  setAll(opts)
  prevMock.mockResolvedValue(previewOf())
  const w = mount(AttachmentsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  routeMock.value = { query: {} }
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

describe('★★★★★★ 抽屉席**必须不设** requiresRole（admin 档）', () => {
  it('★★★★★★ attachments 席没有 requiresRole', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'attachments')
    expect(seat).toBeDefined()
    expect(seat?.requiresRole).toBeUndefined()
  })
})

describe('★★★★★★★★ list 侧 `attachments` 可能是 JSON 标量 null', () => {
  it('★★★★★★★★ 明说「出现在列表里 ≠ 有附件」，不许当空数组渲染', async () => {
    const w = await mountView({ list: listOf([row({ attachments: null })]) })
    const warns = w.findAll('.att__note--warn').map((n) => n.text())
    const na = warns.find((x) => x.includes('JSON 标量 null'))
    expect(na).toBeDefined()
    expect(na).toContain('不等于')
  })

  it('★★★★★★★★ 该行显式标出「无法判断里面有没有附件」', async () => {
    const w = await mountView({ list: listOf([row({ attachments: null })]) })
    const notes = w.findAll('.att__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('无法判断'))).toBe(true)
  })

  it('★★★★★ 普通数组行**不**出现那条标注', async () => {
    const w = await mountView({ list: listOf([row()]) })
    const notes = w.findAll('.att__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('无法判断'))).toBe(false)
  })

  it('★★★★★ `attachments: []`（空数组）也不标 null —— 两者不同', async () => {
    const w = await mountView({ list: listOf([row({ attachments: [] })]) })
    const notes = w.findAll('.att__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('无法判断'))).toBe(false)
  })
})

describe('★★★★★★★ stats 的行范围比 list 窄', () => {
  it('★★★★★★★ 条数对不上 ⇒ 说明是口径差异，不是数据错', async () => {
    const w = await mountView({ list: listOf([row(), row({ request_id: 'req-2' }), row({ request_id: 'req-3' })]), stats: statsOf(1) })
    const notes = w.findAll('.att__note').map((n) => n.text())
    const diff = notes.find((x) => x.includes('行范围比列表'))
    expect(diff).toBeDefined()
    expect(diff).toContain('不是数据不一致')
  })

  it('★★★★★ 条数一致 ⇒ 不出那条说明', async () => {
    const w = await mountView()
    const notes = w.findAll('.att__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('行范围比列表'))).toBe(false)
  })

  it('★★★★★ 汇总值与明细对不上 ⇒ 报异常', async () => {
    const bad = statsOf(3)
    ;(bad as unknown as { total_count: number }).total_count = 999
    const w = await mountView({ stats: bad })
    const warns = w.findAll('.att__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('汇总值'))).toBe(true)
  })
})

describe('★★★★★★★ 时间参数被静默丢弃', () => {
  it('★★★★★★★ 非法时间 ⇒ 明说「窗口没生效、结果未过滤」', async () => {
    const w = await mountView()
    await w.find('#att-since').setValue('garbage')
    await flushPromises()
    await flushPromises()
    const warns = w.findAll('.att__note--warn').map((n) => n.text())
    const drop = warns.find((x) => x.includes('静默丢弃'))
    expect(drop).toBeDefined()
    expect(drop).toContain('未经过滤')
  })

  it('★★★★★★ 合法 RFC3339 ⇒ 不出那条警告', async () => {
    const w = await mountView()
    await w.find('#att-since').setValue('2026-10-01T00:00:00Z')
    await flushPromises()
    await flushPromises()
    const warns = w.findAll('.att__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('静默丢弃'))).toBe(false)
  })

  it('★★★★★ 形状对但月份越界（2026-13-01）也必须报「被丢弃」', async () => {
    const w = await mountView()
    await w.find('#att-since').setValue('2026-13-01T00:00:00Z')
    await flushPromises()
    await flushPromises()
    const warns = w.findAll('.att__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('静默丢弃'))).toBe(true)
  })

  it('★★★★★ 明说区间是**半开**的（含起始不含结束）', async () => {
    const w = await mountView()
    const notes = w.findAll('.att__note').map((n) => n.text())
    const r = notes.find((x) => x.includes('半开'))
    expect(r).toBeDefined()
    expect(r).toContain('含起始')
    expect(r).toContain('不含结束')
  })
})

describe('★★★ limit/offset 是两端 clamp（不是回落）', () => {
  it('★★★★★ 填 99999 ⇒ 明说「已夹到边界」并显示实际值 200', async () => {
    const w = await mountView()
    await w.find('#att-limit').setValue('99999')
    await flushPromises()
    await flushPromises()
    const warns = w.findAll('.att__note--warn').map((n) => n.text())
    const c = warns.find((x) => x.includes('夹到边界'))
    expect(c).toBeDefined()
    expect(c).toContain('200')
  })

  it('★★★★★ 正常值不出该提示', async () => {
    const w = await mountView()
    const warns = w.findAll('.att__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('夹到边界'))).toBe(false)
  })

  it('★★★★★ 改 limit 会**重新请求**（服务端参数，不是本地过滤）', async () => {
    const w = await mountView()
    await w.find('#att-limit').setValue('10')
    await flushPromises()
    await flushPromises()
    expect(listMock).toHaveBeenLastCalledWith(expect.objectContaining({ limit: 10 }))
  })
})

describe('★★★ policy 是硬编码常量', () => {
  it('★★★★★ 明说「写死、从配置不读，且不需要数据库」', async () => {
    const w = await mountView()
    const warns = w.findAll('.att__note--warn').map((n) => n.text())
    const p = warns.find((x) => x.includes('写死的常量'))
    expect(p).toBeDefined()
    expect(p).toContain('不需要数据库')
  })

  it('★★★★★ 显示出内建的 30 天 / 20971520 字节', async () => {
    const w = await mountView()
    expect(w.text()).toContain('30')
    expect(w.text()).toContain('20971520')
  })

  it('★★★★★ `note` 原样带出（说明只能整体关掉）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('LLM_GATEWAY_ATTACHMENT_DISABLED')
  })
})

describe('★★ 文件系统面', () => {
  it('★★ `oldest_file_time` 为 null ⇒ 显示「一个文件都没有」', async () => {
    const w = await mountView({ fs: fsOf({ oldest_file_time: null, total_files: 0 }) })
    expect(w.text()).toContain('一个文件都没有')
  })

  it('★★ 磁盘告警分档由使用率决定', async () => {
    const w = await mountView({ fs: fsOf({ disk_usage_percent: 95, disk_warning_level: 'danger' }) })
    expect(w.text()).toContain('danger')
  })
})

describe('★★ 清理预览', () => {
  it('★★ 明说「永远是 dry-run」且 handler 没有方法门', async () => {
    const w = await mountView()
    const notes = w.findAll('.att__note').map((n) => n.text())
    const d = notes.find((x) => x.includes('dry-run'))
    expect(d).toBeDefined()
    expect(d).toContain('没有执行分支')
  })

  it('★★ 点按钮跑预览并显示命中数', async () => {
    const w = await mountView()
    await w.find('.att__btn').trigger('click')
    await flushPromises()
    expect(prevMock).toHaveBeenCalled()
    expect(w.text()).toContain('7')
  })
})

describe('★★ 抛错不许退化成空清单', () => {
  it('★★ list 失败 ⇒ 错误态，且**不**显示「没有含附件的请求」', async () => {
    const w = await mountView({ list: new Error('boom') })
    expect(w.findAll('.att__msg--err').length).toBeGreaterThan(0)
    expect(w.text()).not.toContain('当前条件下没有含附件的请求')
    expect(w.findAll('.att__badge')).toHaveLength(0)
  })

  it('★★ stats 失败 ⇒ 整个清单面板不渲染（不许只缺统计那半）', async () => {
    const w = await mountView({ stats: new Error('boom') })
    expect(w.text()).not.toContain('附件总数')
  })

  it('★★ 503 `database unavailable` ⇒ 明说没数据库', async () => {
    const w = await mountView({ list: new Error('database unavailable') })
    expect(w.text()).toContain('没有配置数据库')
  })

  it('★★ `analytics_view_missing` ⇒ 单列（不是「没有附件」）', async () => {
    const w = await mountView({ list: new Error('analytics_view_missing') })
    const warns = w.findAll('.att__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('物化视图'))).toBe(true)
  })

  it('★★★★★★ 第二次取数失败 ⇒ **必须清空**上一轮的成功结果', async () => {
    setAll({})
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(AttachmentsView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('20971520')

    listMock.mockRejectedValue(new Error('boom'))
    await w.find('#att-limit').setValue('10')
    await flushPromises()
    await flushPromises()
    expect(w.findAll('.att__msg--err').length).toBeGreaterThan(0)
    expect(w.text()).not.toContain('20971520')
  })
})

describe('★★ 只读边界', () => {
  it('★★ 明说本页只读，且写端点是 superAdmin 档', async () => {
    const w = await mountView()
    const notes = w.findAll('.att__note').map((n) => n.text())
    const ro = notes.find((x) => x.includes('只读'))
    expect(ro).toBeDefined()
    expect(ro).toContain('superAdmin')
  })

  it('★★ 页面上只有预览一个按钮（没有执行类按钮）', async () => {
    const w = await mountView()
    expect(w.findAll('.att__btn')).toHaveLength(1)
  })
})