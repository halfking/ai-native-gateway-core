import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import RoutingOverridesView from './RoutingOverridesView.vue'
import {
  fetchRoutingOverrides,
  createRoutingOverride,
  deleteRoutingOverride,
  extendRoutingOverride,
} from '@/api/routingOverrides'
import { setLocale, locale } from '@/i18n'

/**
 * RoutingOverridesView 的三条不变量（2026-10-07）：
 *
 * 1. **软删后必须切到 active=true**。后端 DELETE 是
 *    `SET expires_at = NOW() - INTERVAL '1 second'` —— 行仍留在表里。
 *    停用后若还用不带 active 的列表查，刚删的规则**还在**，
 *    用户会以为删除失败而重复操作。
 * 2. **创建成功文案必须带「约 1 分钟生效」**。后端 201 的 message 明说
 *    OverrideStore 下一个 1-min reload 生效。不说的话用户会立刻去查
 *    路由解析、看不到新规则 ⇒ 重复提交 ⇒ 撞 409，越急越错。
 * 3. **task_type 与 reason 必填**。后端只校验这两个
 *    （control/routing/create.go:167-185），profile/mode 自由文本。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/routingOverrides', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/routingOverrides')>();
  return {
    ...actual,
    fetchRoutingOverrides: vi.fn(),
    createRoutingOverride: vi.fn(),
    deleteRoutingOverride: vi.fn(),
    extendRoutingOverride: vi.fn(),
  };
})

const list = fetchRoutingOverrides as unknown as ReturnType<typeof vi.fn>
const create = createRoutingOverride as unknown as ReturnType<typeof vi.fn>
const del = deleteRoutingOverride as unknown as ReturnType<typeof vi.fn>
const ext = extendRoutingOverride as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

const OVERRIDE = {
  id: 7,
  task_type: 'code',
  profile: 'default',
  mode: 'auto',
  model_chosen: 'gpt-4o',
  reason: '临时兜底',
  created_by: 'admin',
  expires_at: '2026-12-01T00:00:00Z',
  created_at: '2026-10-07T00:00:00Z',
  updated_at: '2026-10-07T00:00:00Z',
}

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(RoutingOverridesView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function bodyText(): string {
  return document.body.textContent ?? ''
}

async function clickButton(text: string): Promise<void> {
  const el = Array.from(document.body.querySelectorAll('button')).find(
    (b) => (b.textContent ?? '').trim() === text,
  )
  if (!el) throw new Error(`按钮不存在：${text}`)
  el.dispatchEvent(new Event('click'))
  await flushPromises()
}

/**
 * 点「新建」表单的提交钮。
 *
 * ★ 必须按 `.ov__submit` 选，**不能**按文案找：顶栏的「新建规则」按钮与
 *   面板里的提交钮文案完全一样，按文案点会点到顶栏那个（只是把面板又打开一次），
 *   现象是「表单填了但请求没发出去」—— 一个与真实原因无关的失败。
 */
async function clickSubmit(): Promise<void> {
  const el = document.body.querySelector('.ov__submit')
  if (!el) throw new Error('提交钮不存在（面板没打开？）')
  el.dispatchEvent(new Event('click'))
  await flushPromises()
  await flushPromises()
}

/** 从当前已打开的确认框里点确认。 */
async function clickConfirm(): Promise<void> {
  const btns = Array.from(document.body.querySelectorAll('.confirm__actions .btn'))
  const el = btns[btns.length - 1] as HTMLElement | undefined
  if (!el) throw new Error('确认框按钮不存在')
  el.dispatchEvent(new Event('click'))
  await flushPromises()
  await flushPromises()
}

beforeEach(() => {
  // ★ 显式钉 zh-CN：jsdom 的 navigator.language 是 en-US
  setLocale('zh-CN')
  vi.clearAllMocks()
  list.mockResolvedValue({ overrides: [OVERRIDE], count: 1, filter: { task_type: '', profile: '', active: 'true' } })
  create.mockResolvedValue({ id: 9, status: 'created', message: 'refreshes on the next 1-min reload' })
  del.mockResolvedValue({ status: 'deleted' })
  ext.mockResolvedValue({ status: 'extended' })
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
})
afterEach(() => {
  setLocale(ORIGIN_LOCALE)
  vi.clearAllMocks()
})

describe('列表', () => {
  it('渲染规则摘要与理由', async () => {
    await mountView()
    expect(bodyText()).toContain('code / default / auto → gpt-4o')
    expect(bodyText()).toContain('临时兜底')
  })

  it('默认只看生效中（首屏带 active=true）', async () => {
    await mountView()
    expect(list).toHaveBeenCalledWith({ active: true })
  })

  it('空列表给出「全部走默认路由」而不是「没有数据」', async () => {
    list.mockResolvedValue({ overrides: [], count: 0, filter: { task_type: '', profile: '', active: 'true' } })
    await mountView()
    expect(bodyText()).toContain('全部走默认路由')
  })
})

describe('★ 停用后切到 active=true（软删）', () => {
  /**
   * ★ 这条判据第一版是**恒真**的：我直接断言「停用后下一次拉取带 active=true」，
   *   而 activeOnly 初值本来就是 true ⇒ 把 `activeOnly.value = true` 那行删掉，
   *   变异**照样全绿**。
   *
   * 真实场景是：用户先**关掉**「只看生效中」（要审阅全部历史规则），
   * 这时停用一条 —— 软删的行仍会被不带 active 的列表查出来，
   * 用户会以为删除失败。⇒ 必须先切回 active 视图。
   * 所以判据的前置是「筛选处于关闭态」。
   */
  it('筛选关着时停用 ⇒ 下一次拉取必带 active=true', async () => {
    await mountView()
    // 关掉「只看生效中」—— 之后 list 被调用时不应带 active
    await clickButton('只看生效中')
    await flushPromises()
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith({})

    list.mockClear()
    await clickButton('停用')
    await clickConfirm()
    expect(del).toHaveBeenCalledWith(7)
    // ★ 判据落在「下一次请求实际带了什么」，不是内部变量
    expect(list).toHaveBeenLastCalledWith({ active: true })
  })
})

describe('★ 创建：必填校验 + 1 分钟生效提示', () => {
  async function fill(taskType: string, reason: string, modelChosen = ''): Promise<void> {
    const inputs = Array.from(document.body.querySelectorAll('.ov__input')) as HTMLInputElement[]
    // 顺序：task_type / profile / mode / model_chosen / reason(textarea)
    const set = (el: HTMLInputElement | undefined, v: string) => {
      if (!el) return
      el.value = v
      el.dispatchEvent(new Event('input'))
    }
    set(inputs[0], taskType)
    set(inputs[1], 'default')
    set(inputs[2], 'auto')
    set(inputs[3], modelChosen)
    set(inputs[4], reason)
    await flushPromises()
  }

  it('task_type 与 reason 都填了才发请求', async () => {
    await mountView()
    await clickButton('新建规则')
    await fill('code', '临时兜底')
    await clickSubmit()
    expect(create).toHaveBeenCalledWith(
      expect.objectContaining({ task_type: 'code', reason: '临时兜底', model_chosen: null }),
    )
  })

  it('★ 空 reason 不发请求', async () => {
    await mountView()
    await clickButton('新建规则')
    await fill('code', '')
    await clickSubmit()
    expect(create).not.toHaveBeenCalled()
    expect(bodyText()).toContain('必须填理由')
  })

  it('★ 空 task_type 不发请求', async () => {
    await mountView()
    await clickButton('新建规则')
    await fill('', '临时兜底')
    await clickSubmit()
    expect(create).not.toHaveBeenCalled()
  })

  it('★ 成功文案必须带「约 1 分钟内生效」', async () => {
    await mountView()
    await clickButton('新建规则')
    await fill('code', '临时兜底')
    await clickSubmit()
    expect(bodyText()).toContain('约 1 分钟内生效')
  })

  // ★ 空串 = 「指定了一个名字为空的模型」，规则永远不会匹配。必须发 null。
  it('★ model_chosen 留空 ⇒ 发 null 而不是空串', async () => {
    await mountView()
    await clickButton('新建规则')
    await fill('code', '兜底', '')
    await clickSubmit()
    const body = create.mock.calls[0]![0] as Record<string, unknown>
    expect(body.model_chosen).toBeNull()
  })
})

describe('错误分档', () => {
  it('409 重复 ⇒ 说清是四元组重复（否则用户会反复新建）', async () => {
    await mountView()
    await clickButton('新建规则')
    create.mockRejectedValueOnce(Object.assign(new Error('dup'), { status: 409 }))
    const inputs = Array.from(document.body.querySelectorAll('.ov__input')) as HTMLInputElement[]
    for (const [i, v] of ['code', 'default', 'auto', '', 'x'].entries()) {
      inputs[i]!.value = v
      inputs[i]!.dispatchEvent(new Event('input'))
    }
    await flushPromises()
    await clickSubmit()
    expect(bodyText()).toContain('已存在一条规则')
  })

  it('403 ⇒ 说权限而不是网络', async () => {
    list.mockRejectedValueOnce(Object.assign(new Error('x'), { status: 403 }))
    await mountView()
    expect(bodyText()).toContain('仅超管')
  })
})
