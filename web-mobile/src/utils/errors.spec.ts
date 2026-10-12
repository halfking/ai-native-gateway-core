import { describe, expect, it } from 'vitest'
import { ApiError } from '@/api/client'
import { t } from '@/i18n'
import { formatApiError } from '@/utils/errors'

// ↓ 401 文案与事实相反（2026-10-13 实测坐实的残留缺陷）
//
// 修复 `sessionReallyDead` 之前，「进费用对账页 → 整页踢回登录页」的根因是
// 客户端把**任何** 401 当会话过期。修完跳转后真机复测（245，iPhone 17 Pro /
// iOS 26.3，Maestro 27 命令 0 失败）发现：用户不再被踢了，但页面级错误态
// **仍在说「登录状态已过期，请重新登录」**，而同一时刻探针 `/api/auth/me`
// 刚返回 200 ⇒ 文案与事实相反，把用户朝「去重新登录」的错误方向引导。
//
// ⛔ 这组判据钉的是**文案分流**，不是「会话有没有被踢」——
//   跳转那条已由 client.spec.ts 的三条 401 判据覆盖。
// ⛔ 必须区分**三态**：`sessionAlive === true` 才换文案；
//   `false`（探针也失败 ⇒ 真过期）与 `undefined`（本次没探）都保留原文案。
//   把 undefined 当 false 会让「没探过」的 401 也说成「会话有效但功能不可用」，
//   那是把一个真错误换成了另一个。
// ⛔ 断言一律对 `t(...)` 取值本身做，**不写死任何一种语言的字面量**：
//   i18n 默认语言由测试环境决定（实测是 en-US），
//   写死中文会让这组判据在换语言包时集体假红 ——
//   而「两个 key 的值不同」这件事恰恰是本修复成立的前提，必须单独钉住。
// ⛔ 变异自证时**要改测试真正读的那份语言包**：第一版变异只改了 zh-CN，
//   而测试默认语言是 en-US ⇒ 5 条判据全绿 ——
//   「变异没红」的第一嫌疑是**注入没真发生**，不是判据不承重。
//   对着 en-US 重打，3 条转红；两种语言同时打，同样 3 条转红。

describe('formatApiError：401 的三态分流', () => {
  it('两个 key 的文案本身必须不同（否则整个修复是空操作）', () => {
    // 护栏类门必须配阳性对照：若有人把新 key 复制成旧文案，
    // 下面三条断言会同时恒绿。此条先发制人。
    expect(t('common.sessionAliveUnauthorized')).not.toBe(t('common.sessionExpired'))
  })

  it('sessionAlive===true（探针证明会话有效）⇒ 走「会话有效但功能不可用」', () => {
    const msg = formatApiError(new ApiError(401, 'authentication required', '', true))
    expect(msg).toBe(t('common.sessionAliveUnauthorized'))
    expect(msg).not.toBe(t('common.sessionExpired'))
  })

  it('sessionAlive===false（探针也失败 ⇒ 会话真的死了）⇒ 保留原文案', () => {
    expect(formatApiError(new ApiError(401, 'no session', '', false))).toBe(t('common.sessionExpired'))
  })

  it('sessionAlive===undefined（本次没探）⇒ 保留原文案，不当成 true', () => {
    // 「没探」不等于「探了说是死的」，更不等于「探了说是活的」。
    const msg = formatApiError(new ApiError(401, 'no session'))
    expect(msg).toBe(t('common.sessionExpired'))
    expect(msg).not.toBe(t('common.sessionAliveUnauthorized'))
  })

  it('非 401 状态不受影响（403/404/网络错误各走各的分支）', () => {
    // 防「改 401 分支时顺手把别的分支也改了」——四条分别独立，缺一即红。
    expect(formatApiError(new ApiError(403, 'x'))).toBe(t('common.forbidden'))
    expect(formatApiError(new ApiError(404, 'x'))).toBe(t('common.notFoundOrDenied'))
    expect(formatApiError(new ApiError(0, 'network_error'))).toBe(t('common.networkError'))
    expect(formatApiError(new ApiError(500, 'x'))).toBe(t('common.serverError'))
  })
})