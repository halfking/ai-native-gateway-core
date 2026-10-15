/**
 * ModelsView（模型目录）—— 目标：「在模型目录中应该有一个选择，显示我们的
 * 特色模型，并且可以对特色模型进行管理，并且可以点击模型，修改模型的各个属性。」
 *
 * ⛔ 本文件是**补写**的（2026-10-14）：此前 `ModelsView.spec.ts` **根本不存在**，
 * 而这一页同时承担了「模型目录浏览」「特色模型管理」「模型属性编辑」三件事
 * ⇒ 目标点名的三项能力此前**零单测覆盖**。
 * 移动端 Maestro flow 17 也盖不住详情编辑（见该 flow 的坑 8）。
 *
 * ★ 判据分工（别把两边的覆盖混成一句）：
 *   · 本文件钉**逻辑**：tab 切换、canManage 分档、特色草稿与保存、
 *     点卡片进详情、属性编辑提交。
 *   · 本文件**钉不住**「列表里没有重复行」——那是后端 `model_families`
 *     数据重复导致 join 扇出，jsdom 里拿不到真数据。
 *     那条要靠真实库核对（见 .maestro/CURRENT-STATE.md 第三十一节）。
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ModelsView from './ModelsView.vue'
import {
  fetchCanonicalModels,
  fetchCanonicalModel,
  fetchFeaturedModels,
  fetchAvailableModels,
  saveFeaturedModels,
  updateCanonicalModel,
} from '@/api/models'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/components/shell/topbarActions', () => ({ useTopbarAction: () => ({}) }))

vi.mock('@/api/models', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/models')>()
  return {
    ...actual,
    fetchCanonicalModels: vi.fn(),
    fetchCanonicalModel: vi.fn(),
    fetchFeaturedModels: vi.fn(),
    saveFeaturedModels: vi.fn(),
    updateCanonicalModel: vi.fn(),
    fetchAvailableModels: vi.fn(),
    createCanonicalAlias: vi.fn(),
    updateCanonicalAlias: vi.fn(),
    updateCanonicalTags: vi.fn(),
  }
})

const M1 = {
  id: 11, canonical_name: 'fable-5-1', display_name: 'Fable 5.1', family: 'anthropic-claude',
  modality: 'multimodal', context_window: 200000, parameters_b: null, notes: '',
  status: 'active', disabled_reason: '', source: 'db', tags: '[]', tags_locked: false,
  tags_updated_at: null, updated_at: '2026-10-01T00:00:00Z', vendor: 'Anthropic',
  alias_count: 0, offer_count: 6, released_at: null, strengths: null, version_rank: null,
  cost_tier: null, standard_iq: null, modality_source: '',
}
const M2 = {
  ...M1, id: 12, canonical_name: 'qwen-max', display_name: 'Qwen Max', family: 'qwen',
  vendor: 'Alibaba', offer_count: 2,
}

let mounted: ReturnType<typeof mount>[] = []

async function mountView(role = 'super_admin') {
  const pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore().userInfo = {
    id: 1, tenant_id: 't1', username: 'admin', display_name: 'Admin',
    email: 'a@x', role, enabled: true,
  }
  vi.mocked(fetchCanonicalModels).mockResolvedValue({ items: [M1, M2], total: 2 } as never)
  vi.mocked(fetchFeaturedModels).mockResolvedValue({ featured_models: ['fable-5-1'] } as never)
  vi.mocked(fetchAvailableModels).mockResolvedValue({ families: [] } as never)
  const w = mount(ModelsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mounted.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => { document.body.innerHTML = '' })
afterEach(() => {
  for (const m of mounted) m.unmount()
  mounted = []
  vi.clearAllMocks()
})

describe('ModelsView 模型目录双 tab', () => {
  it('两个 tab 都在，★推荐 带计数', async () => {
    const w = await mountView()
    const tabs = w.findAll('[role="tab"]')
    expect(tabs.length).toBe(2)
    expect(tabs[0]!.text()).toContain(t('models.allModels'))
    expect(tabs[1]!.text()).toContain('★')
    expect(tabs[1]!.text()).toContain(String(1)) // featured_models 长度
  })

  it('切到★推荐只列精选模型，切回全部又都回来', async () => {
    const w = await mountView()
    await w.findAll('[role="tab"]')[1]!.trigger('click')
    await flushPromises()
    expect(w.findAll('.model-catalog__card').length).toBe(1)
    expect(w.text()).toContain('fable-5-1')
    await w.findAll('[role="tab"]')[0]!.trigger('click')
    await flushPromises()
    expect(w.findAll('.model-catalog__card').length).toBe(2)
  })
})

describe('ModelsView 特色模型管理（canManage 分档）', () => {
  it('super_admin 看得到星标开关与保存按钮', async () => {
    const w = await mountView('super_admin')
    expect(w.findAll('.model-catalog__star').length).toBe(2)
    await w.findAll('[role="tab"]')[1]!.trigger('click')
    await flushPromises()
    expect(w.find('.model-catalog__save, .btn--primary').exists()).toBe(true)
  })

  it('非 super_admin 看不到星标开关与保存按钮', async () => {
    const w = await mountView('admin')
    expect(w.findAll('.model-catalog__star').length).toBe(0)
    await w.findAll('[role="tab"]')[1]!.trigger('click')
    await flushPromises()
    expect(w.find('.model-catalog__save').exists()).toBe(false)
  })

  it('★ 改一颗星后保存按钮才可用；保存要带上完整的精选列表', async () => {
    vi.mocked(saveFeaturedModels).mockResolvedValue({ status: 'ok' } as never)
    const w = await mountView('super_admin')
    await w.findAll('[role="tab"]')[1]!.trigger('click')
    await flushPromises()
    const saveBtn = () => w.findAll('button').find((b) => b.text() === t('models.saveFeatured'))
    // 未改动 ⇒ 禁用
    expect(saveBtn()?.attributes('disabled')).toBeDefined()

    await w.findAll('.model-catalog__star')[0]!.trigger('click')   // 取消 fable-5-1
    await flushPromises()
    expect(saveBtn()?.attributes('disabled')).toBeUndefined()

    vi.mocked(saveFeaturedModels).mockClear()
    saveBtn()!.trigger('click')
    await flushPromises()
    expect(vi.mocked(saveFeaturedModels)).toHaveBeenCalledWith([])
  })
})

describe('ModelsView 点模型进详情并改属性', () => {
  it('点卡片打开详情，展示规范名称与可编辑字段', async () => {
    vi.mocked(fetchCanonicalModel).mockResolvedValue({ ...M1, aliases: [], offers: [] } as never)
    const w = await mountView()
    await w.find('.model-catalog__open').trigger('click')
    await flushPromises()
    await flushPromises()
    const sheet = document.body.textContent ?? ''
    expect(sheet).toContain(t('models.canonicalName'))
    expect(sheet).toContain('fable-5-1')
    // 目标原文：「修改模型的各个属性」⇒ 至少这几个字段可编辑
    for (const label of [t('models.displayName'), t('models.family'), t('models.status'), t('models.modality')]) {
      expect(sheet, `详情应可编辑「${label}」`).toContain(label)
    }
  })

  it('★ 保存详情时把改过的字段发给 PATCH（不是静默丢弃）', async () => {
    vi.mocked(fetchCanonicalModel).mockResolvedValue({ ...M1, aliases: [], offers: [] } as never)
    vi.mocked(updateCanonicalModel).mockResolvedValue(M1 as never)
    const w = await mountView()
    await w.find('.model-catalog__open').trigger('click')
    await flushPromises()
    await flushPromises()

    const input = document.querySelector<HTMLInputElement>('.model-catalog__form input[type="text"]')
      ?? [...document.querySelectorAll<HTMLInputElement>('.model-catalog__form input')]
        .find((i) => (i.previousElementSibling?.textContent ?? '') === t('models.displayName'))
    expect(input, '应找到 display_name 输入框').toBeTruthy()
    input!.value = 'Fable 5.1 改'
    input!.dispatchEvent(new Event('input'))
    await flushPromises()

    const save = [...document.querySelectorAll<HTMLButtonElement>('.model-catalog__save')]
    save[0]?.click()
    await flushPromises()
    expect(vi.mocked(updateCanonicalModel)).toHaveBeenCalledWith(
      11,
      expect.objectContaining({ display_name: 'Fable 5.1 改' }),
    )
  })
})
