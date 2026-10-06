import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  promoteCredential,
  demoteCredential,
  setConcurrencyAuto,
  toggleCredentialModel,
  validateReason,
  canToggleOnline,
  describeToggle,
  WRITE_REASON_MAX,
  MANUAL_OFFLINE_REASON,
  type ModelToggleResult,
} from './credentialWriteOps'

/**
 * 凭据写操作的契约测试（2026-10-07）。
 *
 * 本组端点最反直觉处：**reason 的强制性在各端点之间不一致**——
 * 只有 model-toggle 强制要求（空串 400），promote/demote/set-concurrency 不校验，
 * 而空 reason 会写进 auditLog，promote 那条还会落一个
 * "manual_promote: " 的悬空串进 state_reason_detail。
 * ⇒ 移动端一律强制。判据锁死这个立场：**即使后端不要求，也不发空 reason**。
 */

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

/** 取最近一次请求的 body（req() 传的是 JSON 字符串）。 */
function lastBody(): Record<string, unknown> {
  const init = fetchMock.mock.calls[0]![1] as { body: string }
  return JSON.parse(init.body) as Record<string, unknown>
}

const OK = { success: true, message: 'ok' }

describe('validateReason —— 后端不管的三个端点，移动端也要拦', () => {
  it('空 / 纯空白 ⇒ reasonRequired', () => {
    expect(validateReason('')).toBe('reasonRequired')
    expect(validateReason('   ')).toBe('reasonRequired')
  })

  it('超长 ⇒ reasonTooLong（阈值对齐 model-toggle 的 500）', () => {
    expect(validateReason('x'.repeat(WRITE_REASON_MAX))).toBeNull()
    expect(validateReason('x'.repeat(WRITE_REASON_MAX + 1))).toBe('reasonTooLong')
  })

  it('正常 reason 通过', () => {
    expect(validateReason('误判 broken，人工恢复')).toBeNull()
  })
})

describe('四个写操作都发非空 reason（trim 后）', () => {
  it('promote', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(OK))
    await promoteCredential(3, '  误判  ')
    expect(lastBody()).toEqual({ credential_id: 3, reason: '误判' })
  })

  it('demote', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(OK))
    await demoteCredential(3, '上游持续 5xx', 4)
    expect(lastBody()).toEqual({ credential_id: 3, reason: '上游持续 5xx', recover_after_hours: 4 })
  })

  // ★ 后端把 0/负数/缺省**默默改成 2 小时**（credential_monitor.go 的
  //   `if req.RecoverAfterHours == 0 { = 2 }`），不报错。
  //   界面上写「2 小时后恢复」就必须真的发 2，而不是指望后端默认。
  it('demote 的 recover_after_hours 非法时前端发 2（不靠后端默默改）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(OK))
    await demoteCredential(3, 'x', 0)
    expect(lastBody().recover_after_hours).toBe(2)
    fetchMock.mockResolvedValueOnce(jsonResponse(OK))
    await demoteCredential(3, 'x', -7)
    expect(lastBody().recover_after_hours).toBe(2)
  })

  it('set-concurrency-auto 带上整数并发值', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(OK))
    await setConcurrencyAuto(3, 12.7, '上游限流')
    expect(lastBody()).toEqual({ credential_id: 3, concurrency_limit_auto: 12, reason: '上游限流' })
  })

  it('model-toggle 原样透传 raw_model_name（不规范化）', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ success: true, available: false, prev_available: true, action: 'offline' }),
    )
    await toggleCredentialModel(3, 'claude-sonnet-4.6', 'offline', '误判')
    // ★ 原样：发规范化后的名字会命中不了（404 binding not found）
    expect(lastBody().raw_model_name).toBe('claude-sonnet-4.6')
    expect(lastBody().action).toBe('offline')
  })
})

describe('canToggleOnline —— ★ 只有 manual_offline 能切回 online', () => {
  it('恰好 manual_offline ⇒ 可切回', () => {
    expect(canToggleOnline(MANUAL_OFFLINE_REASON)).toBe(true)
  })

  // 后端 409 分支的原文：only manual_offline can be toggled back to online。
  // 自动判定（model_probe_broken 等）由探测共识持有，操作员点了必然 409。
  it('自动判定的原因 ⇒ 不可切回', () => {
    expect(canToggleOnline('model_probe_broken')).toBe(false)
    expect(canToggleOnline('quota_exhausted')).toBe(false)
  })
  it('无 reason / 空白 ⇒ 不可切回', () => {
    expect(canToggleOnline(null)).toBe(false)
    expect(canToggleOnline(undefined)).toBe(false)
    expect(canToggleOnline('')).toBe(false)
    // 大小写/前后空格都不算「恰好等于」——后端是 `*prevReason != "manual_offline"`
    expect(canToggleOnline('Manual_Offline')).toBe(false)
    expect(canToggleOnline(' manual_offline ')).toBe(false)
  })
})

describe('describeToggle —— 成功反馈要说明「改掉了什么」', () => {
  const base = { success: true, available: false, prev_available: true, action: 'offline' } as ModelToggleResult

  // model-toggle 是本组唯一回传变更前后状态的端点，界面要靠它告诉用户
  // 「刚才它还是可用的」，否则用户不知道自己刚改掉了什么。
  it('状态翻转 ⇒ 显式展示 from → to', () => {
    expect(describeToggle(base)).toBe('online → offline')
  })
  it('状态没变 ⇒ 只显示目标态', () => {
    expect(describeToggle({ ...base, prev_available: false })).toBe('offline')
  })
})
