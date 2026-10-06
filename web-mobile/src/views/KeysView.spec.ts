import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import KeysView from './KeysView.vue'
import { getKeys, disableKey } from '@/api/keys'
import { ApiError } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { t, setLocale, locale } from '@/i18n'
import type { ApiKey } from '@/api/keys'

/**
 * 密钥停用/启用的失败反馈（2026-10-06）。
 *
 * ★ 缺陷：原 `doDisable` 是 `try { … } finally { disableTarget.value = null }`
 *   —— **既无 catch 也无错误位**。停用失败（409 已被别处停用 / 403 权限 / 500）时，
 *   用户点完确认框，界面毫无反馈，误以为成功了。
 *   而同文件 `submitCreate` 有完整 catch + `createError` 提示位（:48/:59/:193）
 *   ⇒ 同页两套标准，是漏写不是有意设计。
 *
 * 判据：★ 失败必须**看得见**。反向锁定：若把 catch 去掉，本用例会红。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/keys', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/keys')>()
  return { ...actual, getKeys: vi.fn(), disableKey: vi.fn(), enableKey: vi.fn() }
})

const KEY = {
  id: 5,
  application_code: 'mobile',
  key_prefix: 'sk-test',
  enabled: true,
  status: 'active',
  total_requests: 0,
  total_cost_usd: 0,
  created_at: '2026-10-01T00:00:00Z',
} as unknown as ApiKey

let mounted: Array<{ unmount(): void }> = []

/** 模块加载时的 locale（即 detectLocale 的结果），afterEach 复位用。 */
const PROBE_ORIGIN_LOCALE = locale.value

async function mountView() {
  const pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore().userInfo = {
    id: 1, tenant_id: 'default', username: 'u', display_name: 'U',
    email: 'e', role: 'admin', enabled: true,
  }
  const w = mount(KeysView, { attachTo: document.body, global: { plugins: [pinia] } })
  mounted.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

/** 点密钥卡上的停用按钮 → 确认。 */
async function clickDisable(w: Awaited<ReturnType<typeof mountView>>): Promise<void> {
  // ★ 按钮文案查词典，不写死英文。写死 'Disable' / 'Confirm' 会让本助手**只在
  //   en-US 下可用**：一旦某条用例 setLocale('zh-CN')，按钮就变成「停用」「确认」，
  //   查找失败并抛「停用按钮不存在」——报错会把 locale 问题伪装成渲染问题。
  const disableLabel = t('keys.disable')
  const confirmLabel = t('common.confirm')
  const btn = w.findAll('.btn--danger').find((b) => b.text().includes(disableLabel))
  if (!btn) throw new Error(`停用按钮不存在（locale=${locale.value}，找 "${disableLabel}"）`)
  await btn.trigger('click')
  await flushPromises()
  // ★ AppConfirm 未传 confirm-label ⇒ 确认钮文案是 i18n 的 common.confirm
  //   （探针实测 confirm__actions 里是 ["Cancel","Confirm"]），不是 "Disable"。
  //   写成找 "Disable" 会永远找不到确认钮，报错还会误导成「确认框没渲染」。
  const confirm = Array.from(document.body.querySelectorAll('.confirm__actions .btn'))
    .find((b) => (b.textContent ?? '').trim() === confirmLabel) as HTMLButtonElement | undefined
  if (!confirm) throw new Error(`确认按钮不存在（locale=${locale.value}，找 "${confirmLabel}"）`)
  confirm.dispatchEvent(new Event('click'))
  await flushPromises()
  await flushPromises()
}

describe('KeysView 停用失败反馈', () => {
  beforeEach(() => {
    for (const w of mounted) {
      try { w.unmount() } catch { /* ignore */ }
    }
    mounted = []
    document.body.innerHTML = ''
    vi.resetAllMocks()
    ;(getKeys as ReturnType<typeof vi.fn>).mockResolvedValue([KEY])
  })
  afterEach(() => {
    // locale 是模块级 ref（@/i18n 的 export const locale），用例里 setLocale 会
    // 跨用例残留。不复位的话，本文件后面的用例会继承上一个用例的语言 ——
    // 而这类污染只在有人改了 locale 时才显形，属于典型的偶发红。
    setLocale(PROBE_ORIGIN_LOCALE)
    for (const w of mounted) {
      try { w.unmount() } catch { /* ignore */ }
    }
    mounted = []
  })

  it('★ 停用失败时必须显示错误（原来静默）', async () => {
    ;(disableKey as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new ApiError(500, 'boom'))
    const w = await mountView()
    await clickDisable(w)

    const err = w.find('.keys__error')
    if (!err.exists()) throw new Error('失败后没有任何错误提示 —— 这就是原缺陷')
    expect(err.text()).toContain('boom')
  })

  // ★ 下面两条断言「中文」的用例，原注释写着「本文件的 i18n 解析出的是中文
  //   （与 NodesView.spec 相反）」——**那是误判**。实测：
  //     jsdom 的 navigator.language 是 en-US ⇒ 本文件一直跑的是 en-US；
  //     之所以看到中文，是因为 en-US 里 keys.errForbidden / keys.errConflict
  //     当时**放错了词典**（落在 home 下），t() 缺键回退 zh-CN 才吐出中文。
  //   ⇒ 旧断言测到的是「zh 回退路径」，不是被测词典本身；en 词典补正后立刻变红。
  //   修法：显式钉 locale，并对**两侧**都断言真正要守的不变量。
  it('403 映射成人话，两侧 locale 都不显示后端英文原文', async () => {
    for (const [loc, needle] of [['zh-CN', '权限'], ['en-US', 'permission']] as const) {
      setLocale(loc)
      ;(disableKey as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new ApiError(403, 'Forbidden'))
      const w = await mountView()
      await clickDisable(w)

      const err = w.find('.keys__error')
      if (!err.exists()) throw new Error(`403 无提示（${loc}）`)
      // 关键不是文案本身，是「403 不该退化成后端英文原文 'Forbidden'」。
      expect(err.text(), loc).toContain(needle)
      expect(err.text(), loc).not.toContain('Forbidden')
    }
  })

  it('409 提示状态已变化（可能被别处停用），而不是笼统报错（两侧 locale）', async () => {
    for (const [loc, needle] of [['zh-CN', '请刷新后重试'], ['en-US', 'Refresh and try again']] as const) {
      setLocale(loc)
      ;(disableKey as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new ApiError(409, 'conflict'))
      const w = await mountView()
      await clickDisable(w)

      const err = w.find('.keys__error')
      if (!err.exists()) throw new Error(`409 无提示（${loc}）`)
      expect(err.text(), loc).toContain(needle)
    }
  })

  it('成功时刷新列表且不显示错误', async () => {
    ;(disableKey as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ message: 'disabled' })
    const w = await mountView()
    await clickDisable(w)

    expect(disableKey).toHaveBeenCalledWith(5)
    expect(w.find('.keys__error').exists()).toBe(false)
    // 成功后应重新拉列表
    expect((getKeys as ReturnType<typeof vi.fn>).mock.calls.length).toBeGreaterThan(1)
  })

  it('失败时确认框已关闭但错误位仍可见（错误不随框消失）', async () => {
    ;(disableKey as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new ApiError(500, 'boom'))
    const w = await mountView()
    await clickDisable(w)

    // 确认框关闭（AppConfirm onConfirm 立即置 false）
    expect(document.body.querySelectorAll('.confirm__actions').length).toBe(0)
    // ★ 但错误位必须在页面上
    expect(w.find('.keys__error').exists()).toBe(true)
  })
})
