import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ModulesView from './ModulesView.vue'
import { fetchModules, type ModuleList, type ModuleWithStatus } from '@/api/modules'
import { setLocale, locale } from '@/i18n'

/**
 * ModulesView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ `enabled:true + source:"default"` **不许**渲染成「启用」——
 *     那是 `resolveModuleEnabled` 五条失败路径 + `EffectiveValue` 回落 spec 默认值
 *     的**共同**形态，两种成因在响应里分不开。
 * 2. ★★★ `source` 只有 {db, env, default}；只有 db/env 算「真读到」。
 * 3. ★★★ `blocked_reason` 带 omitempty ⇒ 键存在就是被挡住了。
 * 4. ★★ `setting_key` 为空 ⇒ 后端没查任何设置。
 * 5. ★★ 抛错不许退化成空清单 ⇒ 「面板没渲染」要用**只存在于正确形态里**的锚判。
 * 6. ★★ 不碰 toggle（写）与 test（**真给飞书发消息**）。
 * 7. ★★ 抽屉席**必须不设** requiresRole（admin 档）。
 */

const routerMock: { value: { push: ReturnType<typeof vi.fn> } } = { value: { push: vi.fn() } }
vi.mock('vue-router', () => ({ useRouter: () => routerMock.value }))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/modules', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/modules')>()
  return { ...actual, fetchModules: vi.fn() }
})

const listMock = fetchModules as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/** 抄自 `ModuleWithStatus`（admin/modules.go:55-61）。 */
function mod(over: Record<string, unknown> = {}): ModuleWithStatus {
  return {
    key: 'compression',
    name: '会话压缩',
    description: '智能压缩超长对话上下文',
    capabilities: ['多模式压缩'],
    icon: '🗜️',
    category: 'compression',
    setting_key: 'compression.enabled',
    config_keys: ['compression.mode'],
    docs_url: '/admin/compression',
    danger_level: 'warning',
    enabled: true,
    source: 'db',
    can_toggle_enabled: true,
    ...over,
  } as ModuleWithStatus
}

function listOf(items: ModuleWithStatus[] = [mod()]): ModuleList {
  return { items }
}

/** ★ helper 接受覆盖 + `instanceof Error` 分派。 */
function setList(v: unknown): void {
  if (v instanceof Error) listMock.mockRejectedValue(v)
  else listMock.mockResolvedValue(v ?? listOf())
}

async function mountView(list?: unknown): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  setList(list)
  const w = mount(ModulesView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

/** ★★ 只取条目上的状态徽标文本 —— 全页文本里「启用」被我自己的免责文案引述过。 */
function itemStates(w: ReturnType<typeof mount>): string[] {
  return w.findAll('.mods__item .mods__badge-t').map((n) => n.text())
}

/** ★★ 条目级 note —— 面板级免责（含 fallbackNote）**不在**这个作用域里。 */
function itemNotes(w: ReturnType<typeof mount>): string[] {
  return w.findAll('.mods__item .mods__note').map((n) => n.text())
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
  it('★★★★★★ modules 席存在且没有 requiresRole', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'modules')
    expect(seat).toBeDefined()
    expect(seat?.requiresRole).toBeUndefined()
    // ★ tenant_admin 必须能看到它（admin 档 ⇒ 导航层不额外过滤）
    const { navItemsFor } = await import('@/config/appNav')
    expect(navItemsFor(DRAWER_NAV, 'tenant_admin').some((i) => i.key === 'modules')).toBe(true)
  })

  it('★★★★★ 详情页 /modules/:key **不占**抽屉席', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    expect(DRAWER_NAV.some((i) => i.to === '/modules/:key')).toBe(false)
    expect(DRAWER_NAV.some((i) => i.to.startsWith('/modules/'))).toBe(false)
  })
})

describe('★★★★★★★★ `enabled:true + source:"default"` 不许渲染成「启用」', () => {
  it('★★★★★★★★ 兜底态显示「没能读到配置」', async () => {
    const w = await mountView(listOf([mod({ enabled: true, source: 'default' })]))
    // ★ 按**条目内**的徽标取文本：全页文本里「启用」被我自己的免责文案引述过
    expect(itemStates(w)).toEqual(['没能读到配置'])
  })

  it('★★★★★★★ 真的读到 db ⇒ 显示「启用」，不是「没能读到」', async () => {
    const w = await mountView(listOf([mod({ enabled: true, source: 'db' })]))
    expect(itemStates(w)).toEqual(['启用'])
  })

  it('★★★★★★★ 真的读到 env ⇒ 同上（source 逐个点名，不靠「不是 default」）', async () => {
    const w = await mountView(listOf([mod({ enabled: true, source: 'env' })]))
    expect(itemStates(w)).toEqual(['启用'])
  })

  it('★★★★★★★ enabled:false + source:"default" ⇒ 显示「停用」但来源「未能确定」', async () => {
    // ★★ source:"default" + enabled:false 同样可能是「没配」而不是「关了」
    //   ⇒ 状态位显示「停用」，但来源位必须说「未能确定」，不许显示 default 让人以为那是层级名
    const w = await mountView(listOf([mod({ enabled: false, source: 'default' })]))
    expect(itemStates(w)).toEqual(['停用'])
    const metas = w.findAll('.mods__item .mods__meta').map((n) => n.text())
    expect(metas.some((x) => x.includes('未能确定'))).toBe(true)
  })

  it('★★★★★ 真读到时来源位显示**原值** db / env', async () => {
    const w = await mountView(listOf([mod({ source: 'db' }), mod({ key: 'b', source: 'env' })]))
    const metas = w.findAll('.mods__item .mods__meta').map((n) => n.text())
    expect(metas.some((x) => x.includes('db'))).toBe(true)
    expect(metas.some((x) => x.includes('env'))).toBe(true)
    expect(metas.some((x) => x.includes('未能确定'))).toBe(false)
  })
})

describe('★★★★★★ 兜底与未知来源的免责文案', () => {
  it('★★★★★★ 页面上存在头号免责（不依赖具体措辞）', async () => {
    const w = await mountView()
    const warns = w.findAll('.mods__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('没读到配置'))).toBe(true)
  })

  it('★★★★★★ 兜底计数只在真有兜底时出现', async () => {
    const w1 = await mountView(listOf([mod({ enabled: true, source: 'default' })]))
    expect(w1.findAll('.mods__note--warn').map((n) => n.text()).some((x) => x.includes('没能读到配置'))).toBe(
      true,
    )
    const w2 = await mountView(listOf([mod({ enabled: true, source: 'db' })]))
    // ★ 计数文案含「没能读到配置」；没有兜底时**不该**出现
    expect(w2.findAll('.mods__note--warn').map((n) => n.text()).some((x) => x.includes('没能读到配置'))).toBe(
      false,
    )
  })

  it('★★★★★ 出现集合外的 source ⇒ 告警（后端实现上不可能）', async () => {
    const w = await mountView(listOf([mod({ source: 'platform' as string })]))
    expect(w.findAll('.mods__note--warn').map((n) => n.text()).some((x) => x.includes('异常上报'))).toBe(true)
  })

  it('★★★★★ 全是合法 source ⇒ 不出那条告警', async () => {
    const w = await mountView(listOf([mod({ source: 'db' }), mod({ key: 'b', source: 'env' })]))
    expect(w.findAll('.mods__note--warn').map((n) => n.text()).some((x) => x.includes('异常上报'))).toBe(false)
  })
})

describe('★★★★★★ blocked_reason 带 omitempty', () => {
  it('★★★★★★ 键存在（被挡住）⇒ 原样显示后端那句话', async () => {
    const w = await mountView(
      listOf([mod({ blocked_reason: '需先启用依赖模块: 会话缓存', can_toggle_enabled: false })]),
    )
    const notes = w.findAll('.mods__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('需先启用依赖模块: 会话缓存'))).toBe(true)
  })

  it('★★★★★★ 键不存在（无阻塞）⇒ 不显示任何阻塞文案', async () => {
    const ok = mod()
    // ★ 后端 omitempty ⇒ 键**整个不存在**，不是空串
    expect('blocked_reason' in ok).toBe(false)
    const w = await mountView(listOf([ok]))
    const notes = w.findAll('.mods__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('需先启用依赖模块'))).toBe(false)
  })

  it('★★★★★ 阻塞计数与条目一致', async () => {
    const w = await mountView(
      listOf([
        mod({ key: 'a' }),
        mod({ key: 'b', blocked_reason: '需先启用依赖模块: X', can_toggle_enabled: false }),
        mod({ key: 'c', blocked_reason: '需先启用依赖模块: Y', can_toggle_enabled: false }),
      ]),
    )
    const badges = w.findAll('.mods__badge-t').map((n) => n.text())
    expect(badges.some((x) => x.includes('3'))).toBe(true)
    expect(badges.some((x) => x.includes('2 个'))).toBe(true)
  })
})

describe('★★★★★★ setting_key 为空 ⇒ 后端压根没查', () => {
  it('★★★★★★ 显示「常开」免责', async () => {
    const w = await mountView(listOf([mod({ setting_key: '', enabled: true, source: 'default' })]))
    expect(w.findAll('.mods__note').map((n) => n.text()).some((x) => x.includes('没有开关键'))).toBe(true)
  })

  it('★★★★★ setting_key 非空 ⇒ 不出那条免责', async () => {
    const w = await mountView(listOf([mod({ setting_key: 'compression.enabled' })]))
    // ★★★ 全页 not.toContain('没有开关键') 会被**我自己写的免责文案**判红：
    //   fallbackNote 把五条失败路径**逐条列出来**，其中一条就是「没有开关键」。
    //   ⇒ 否定断言一律按**节点作用域**缩到条目内（`.mods__item .mods__note`），
    //     面板级免责不在这个作用域里。
    expect(itemNotes(w).some((x) => x.includes('没有开关键'))).toBe(false)
    // 且面板级免责**确实**在（证明上面不是靠「什么都没渲染」蒙对的）
    expect(w.findAll('.mods__note').length).toBeGreaterThan(itemNotes(w).length)
  })
})

describe('★★★★★★ 抛错不许退化成空清单', () => {
  it('★★★★★★ 报错时**整个清单面板不出现**（用只存在于正确形态里的锚判）', async () => {
    const w = await mountView(new Error('boom'))
    // ★★★ 「条目为 0」证明不了「面板没渲染」—— 错误态和空态在**条目**维度长得一样。
    //   锚取 .mods__badge：它只活在 v-if="list" 内部，空态时**也在**
    //   ⇒ 判「面板没渲染」必须看**徽标**在不在。
    expect(w.findAll('.mods__badge')).toHaveLength(0)
    expect(w.findAll('.mods__item')).toHaveLength(0)
    expect(w.find('.mods__msg--err').exists()).toBe(true)
  })

  // ★ 首屏就失败证明不了「出错时必须清空」—— 必须造「先成功 → 再失败」
  it('★★★★★★★ 先成功、再失败 ⇒ 徽标消失、清单清空', async () => {
    const w = await mountView(listOf([mod()]))
    expect(w.findAll('.mods__badge').length).toBeGreaterThan(0)
    expect(w.findAll('.mods__item').length).toBe(1)

    listMock.mockRejectedValue(new Error('later boom'))
    // ★ 成功态也必须有重取入口 —— 只读页原本一个刷新按钮都没有（真缺陷，已补）
    expect(w.findAll('button.mods__btn--refresh')).toHaveLength(1)
    await w.find('button.mods__btn--refresh').trigger('click')
    await flushPromises()
    await flushPromises()

    // ★ 关键：不是「条目变成 0」而是**面板整体不再渲染**
    expect(w.findAll('.mods__badge')).toHaveLength(0)
    expect(w.findAll('.mods__item')).toHaveLength(0)
    expect(w.find('.mods__msg--err').text()).toContain('later boom')
  })

  it('★★★★★ 空清单是**合法响应**，面板仍在渲染', async () => {
    const w = await mountView(listOf([]))
    // ★ 与「报错」的区别：徽标在（面板渲染了）、空态文案在、没有错误条
    expect(w.findAll('.mods__badge').length).toBeGreaterThan(0)
    expect(w.find('.mods__msg--err').exists()).toBe(false)
    expect(w.text()).toContain('后端一个模块定义都没返回')
  })
})

describe('★★★★★★ 503 的两种文案都不是权限问题', () => {
  it('★★★★★★ `settings registry not initialised` ⇒ 说注册表没起', async () => {
    const w = await mountView(new Error('settings registry not initialised'))
    const warns = w.findAll('.mods__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('设置注册表没初始化'))).toBe(true)
    // ★ 不得把它说成「没有模块」—— 那是两回事
    expect(w.text()).not.toContain('后端一个模块定义都没返回')
  })

  it('★★★★★ `settings not initialised`（另一种文案）也能识别', async () => {
    const w = await mountView(new Error('settings not initialised'))
    expect(w.findAll('.mods__note--warn').map((n) => n.text()).some((x) => x.includes('设置注册表没初始化'))).toBe(
      true,
    )
  })

  it('★★★★★ 其它错误 ⇒ 不出那条注册表文案', async () => {
    const w = await mountView(new Error('database not configured'))
    expect(w.findAll('.mods__note--warn').map((n) => n.text()).some((x) => x.includes('设置注册表没初始化'))).toBe(
      false,
    )
  })
})

describe('★★★★★★ 只读页：不碰写操作与有副作用的「测试」', () => {
  it('★★★★★★ 只发了一次列表请求，没有任何别的请求', async () => {
    await mountView()
    expect(listMock).toHaveBeenCalledTimes(1)
  })

  // ★★★ 这条是**变异 V9 逼出来的**：把尾行的
  //   `m.can_toggle_enabled ? '可' : '被依赖挡住'` 换成恒显示「可」时，
  //   原来的用例**照样全绿** —— 因为它只喂了 `can_toggle_enabled: true`。
  //   ⇒ 缺口是「不可切换」那一侧从没被渲染过。
  it('★★★★★★★ 被依赖挡住的模块 ⇒ 尾行说「否（被依赖挡住）」', async () => {
    const w = await mountView(
      listOf([mod({ can_toggle_enabled: false, blocked_reason: '需先启用依赖模块: X' })]),
    )
    const tails = w.findAll('.mods__item .mods__tail').map((n) => n.text())
    expect(tails).toHaveLength(1)
    expect(tails[0]).toContain('否（被依赖挡住）')
    expect(tails[0]).not.toContain('可否切换 可')
  })

  it('★★★★★ 页面上没有任何 toggle 控件', async () => {
    const w = await mountView(listOf([mod({ can_toggle_enabled: true })]))
    // ★ PUT /{key}/toggle 是写操作；页面上不许有能触发它的控件。
    //   ⚠️ 判据不能选 `.mods__chip--warning` —— 那是 **danger 档**芯片
    //   （默认夹具 danger_level:'warning' 就会命中），与「可否切换」无关。
    //   ⇒ 改按**条目尾部的可切换标签**取文本。
    const tails = w.findAll('.mods__item .mods__tail').map((n) => n.text())
    expect(tails).toHaveLength(1)
    expect(tails[0]).toContain('可')
    expect(tails[0]).not.toContain('被依赖挡住')
  })

  it('★★★★★ 页面明说「有外部副作用的测试端点没被调用」', async () => {
    const w = await mountView()
    // ★ 列表页的措辞是「会真的给飞书机器人发一条消息」；
    //   「webhook」那句在**详情页**（modsDetail.testSideEffectNote）—— 别跨页找。
    expect(w.findAll('.mods__note').map((n) => n.text()).some((x) => x.includes('会真的给飞书机器人发一条消息'))).toBe(
      true,
    )
  })
})

describe('★★★★★ 点条目进详情', () => {
  it('★★★★★ 跳到 /modules/<key> 且 key 要 encode', async () => {
    const w = await mountView(listOf([mod({ key: 'a/b' })]))
    await w.find('button.mods__row').trigger('click')
    expect(routerMock.value.push).toHaveBeenCalledWith('/modules/a%2Fb')
  })
})
