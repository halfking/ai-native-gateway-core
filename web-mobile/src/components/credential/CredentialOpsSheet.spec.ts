import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import CredentialOpsSheet from './CredentialOpsSheet.vue'
import {
  promoteCredential,
  demoteCredential,
  setConcurrencyAuto,
  toggleCredentialModel,
} from '@/api/credentialWriteOps'
import { setLocale, locale } from '@/i18n'

/**
 * 写操作面板的三条不变量（2026-10-07）：
 *
 * 1. **reason 必填**，哪怕三个端点后端根本不校验。空 reason 会写进 auditLog，
 *    promote 还会落一个悬空的 `state_reason_detail = "manual_promote: "`。
 * 2. **「上线」只对 manual_offline 渲染**。后端 409：自动判定持有的绑定
 *    （model_probe_broken 等）由探测共识拥有，操作员点了必然 409。
 * 3. **失败必须看得见**且区分档位：403 是权限、404 是模型名没绑上、
 *    409 是被别处改动。原 KeysView.doDisable 缺 catch 是本专题修过的真实缺陷。
 */

vi.mock('@/api/credentialWriteOps', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/credentialWriteOps')>()
  return {
    ...actual,
    promoteCredential: vi.fn(),
    demoteCredential: vi.fn(),
    setConcurrencyAuto: vi.fn(),
    toggleCredentialModel: vi.fn(),
  };
})

const promote = promoteCredential as unknown as ReturnType<typeof vi.fn>
const demote = demoteCredential as unknown as ReturnType<typeof vi.fn>
const conc = setConcurrencyAuto as unknown as ReturnType<typeof vi.fn>
const toggle = toggleCredentialModel as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

const MODELS = [
  { raw_model_name: 'gpt-4o', available: true, unavailable_reason: null },
  { raw_model_name: 'claude-sonnet-4.6', available: false, unavailable_reason: 'manual_offline' },
  { raw_model_name: 'o3', available: false, unavailable_reason: 'model_probe_broken' },
]

async function mountSheet(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(CredentialOpsSheet, {
    attachTo: document.body,
    props: { open: true, credentialId: 3, credentialLabel: 'prod-key-a', models: MODELS },
    global: { plugins: [pinia] },
  })
  mountedList.push(w)
  await flushPromises()
  return w
}

/**
 * 按文案找按钮并点开确认框。
 *
 * ★ 必须查 `document.body` 而不是 wrapper：**AppSheet 是 teleport 的**，
 *   面板内容根本不在 wrapper 子树里。用 `w.findAll('button')` 会得到空数组，
 *   然后抛出「按钮不存在：下线」—— 一个与真实原因（查找位置错）完全无关的报错。
 *   同款坑见 KeysView.spec。
 */
async function clickByText(_w: ReturnType<typeof mount>, text: string): Promise<void> {
  const el = Array.from(document.body.querySelectorAll('button')).find(
    (b) => (b.textContent ?? '').trim() === text,
  )
  if (!el) throw new Error(`按钮不存在：${text}`)
  el.dispatchEvent(new Event('click'))
  await flushPromises()
}

/** 读面板内全部按钮文案（同样走 document.body）。 */
function buttonLabels(): string[] {
  return Array.from(document.body.querySelectorAll('button')).map((b) => (b.textContent ?? '').trim())
}

/** 读面板全文（teleport ⇒ wrapper.text() 恒为空串）。 */
function bodyText(): string {
  return document.body.textContent ?? ''
}

async function confirmSheet(typeText = '下线'): Promise<void> {
  const el = Array.from(document.body.querySelectorAll('.confirm__actions .btn')).find(
    (b) => (b.textContent ?? '').trim() === typeText,
  ) as HTMLElement | undefined
  if (!el) throw new Error(`确认框按钮不存在：${typeText}`)
  el.dispatchEvent(new Event('click'))
  await flushPromises()
  await flushPromises()
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  promote.mockResolvedValue({ success: true, message: 'ok' })
  demote.mockResolvedValue({ success: true, message: 'ok' })
  conc.mockResolvedValue({ success: true, message: 'ok' })
  toggle.mockResolvedValue({ success: true, available: false, prev_available: true, action: 'offline' })
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

describe('★ 上线只对 manual_offline 渲染', () => {
  it('自动判定持有的绑定不给「上线」按钮，且说明原因', async () => {
    await mountSheet()
    const labels = buttonLabels()
    // gpt-4o 在线 ⇒ 有「下线」
    expect(labels).toContain('下线')
    // manual_offline ⇒ 有「上线」
    expect(labels.filter((l) => l === '上线')).toHaveLength(1)
    // model_probe_broken ⇒ 只给说明，不给按钮
    const blocked = Array.from(document.body.querySelectorAll('.ops__blocked')).map((n) => n.textContent ?? '')
    expect(blocked.some((b) => b.includes('model_probe_broken'))).toBe(true)
    // ★ 全文只有一处可点的「上线」——给了第二个就意味着会自动判定被 409
    expect(labels.filter((l) => l === '上线')).toHaveLength(1)
  })
})

describe('★ reason 必填 —— 空着提交不了', () => {
  it('空 reason 点确认 ⇒ 不发请求，并提示必填', async () => {
    const w = await mountSheet()
    await clickByText(w, '下线')
    await confirmSheet()
    expect(toggle).not.toHaveBeenCalled()
    expect(bodyText()).toContain('必须填写理由')
  })

  it('填了 reason 才真的发请求，且原样透传模型名', async () => {
    const w = await mountSheet()
    await clickByText(w, '下线')
    const ta = document.body.querySelector('.ops__reason-input') as HTMLTextAreaElement
    ta.value = '误判 broken'
    ta.dispatchEvent(new Event('input'))
    await flushPromises()
    await confirmSheet()
    expect(toggle).toHaveBeenCalledTimes(1)
    expect(toggle).toHaveBeenCalledWith(3, 'gpt-4o', 'offline', '误判 broken')
  })
})

describe('promote / demote / concurrency 同样受 reason 必填约束', () => {
  it('promote 空 reason 不发', async () => {
    const w = await mountSheet()
    await clickByText(w, '手动提升')
    await confirmSheet('手动提升')
    expect(promote).not.toHaveBeenCalled()
  })

  it('demote 带上恢复小时数', async () => {
    const w = await mountSheet()
    await clickByText(w, '手动降级')
    const ta = document.body.querySelector('.ops__reason-input') as HTMLTextAreaElement
    ta.value = '上游 5xx'
    ta.dispatchEvent(new Event('input'))
    await flushPromises()
    await confirmSheet('手动降级')
    expect(demote).toHaveBeenCalledWith(3, '上游 5xx', 2)
  })

  it('concurrency 带并发值', async () => {
    const w = await mountSheet()
    await clickByText(w, '自动并发上限')
    const ta = document.body.querySelector('.ops__reason-input') as HTMLTextAreaElement
    ta.value = '上游限流'
    ta.dispatchEvent(new Event('input'))
    await flushPromises()
    await confirmSheet('自动并发上限')
    expect(conc).toHaveBeenCalledWith(3, 10, '上游限流')
  })
})

describe('★ 失败必须可见且区分档位', () => {
  it('403 说权限、404 说模型没绑上、409 说被别处改动', async () => {
    for (const [status, needle] of [
      [403, '没有执行凭据写操作的权限'],
      [404, '没有绑定这个模型'],
      [409, '状态已被别处改动'],
    ] as const) {
      toggle.mockRejectedValueOnce(Object.assign(new Error('boom'), { status }))
      const w = await mountSheet()
      await clickByText(w, '下线')
      const ta = document.body.querySelector('.ops__reason-input') as HTMLTextAreaElement
      ta.value = 'x'
      ta.dispatchEvent(new Event('input'))
      await flushPromises()
      await confirmSheet()
      expect(bodyText(), `status=${status}`).toContain(needle)
      w.unmount()
      document.body.innerHTML = ''
    }
  })

  it('成功时有可见反馈（不静默）', async () => {
    const w = await mountSheet()
    await clickByText(w, '下线')
    const ta = document.body.querySelector('.ops__reason-input') as HTMLTextAreaElement
    ta.value = 'x'
    ta.dispatchEvent(new Event('input'))
    await flushPromises()
    await confirmSheet()
    expect(document.body.querySelector('.ops__ok')).not.toBeNull()
  })
})
