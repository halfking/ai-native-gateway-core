// SelfUpdateView.test.ts —— 自更新页的「结论不可得 ≠ 已是最新」判据。
//
// ## 挡住的是什么
//
// 自更新页的唯一危险形态是**把「没查成」显示成「已是最新」**：
// 用户看到绿色标签就以为补丁已经检查过、没有新版本 ⇒ 直接跳过安全更新。
//
// 本仓已有同族血案（web/src/views/lifecycle/upgradeLatestVsUnknown.test.ts）：
// 旧实现在检查失败时说「已是最新」，并且把「最新版本」栏回退成当前版本号，
// 两栏同数看起来像「确认过是最新的」。
//
// 本页把检索结论收敛成四态（notConfigured / unknown / upToDate / available），
// 只有**真的查成了**才允许出现「已是最新」这类结论。
//
// ## 判据钉住的是什么
//
//   ① 初始结论就是失败   → 出现「状态未知」，不出现「已是最新」
//   ② 点「立即检查」失败  → 同上（失败必须是 unknown，不能是 upToDate）
//   ③ 未配置远端源        → 明确说「未配置」，不出现「已是最新」
//   ④ 「最新版本」栏      → 结论不可得时**不回退成当前版本号**（两栏不能同数）
//   ⑤ 有更新 → 点「立即升级」→ 调用 install 端点（带版本号）
//   ⑥ 「从源码编译并升级」→ 调用 source-build 端点并带上 ref
//   ⑦ 正向对照：查成且有更新 → 显示版本号 + 升级按钮
//   ⑧ 正向对照：查成且确为最新 → **才**显示「已是最新」
//
// ⑦⑧ 是正向对照：没有它们，①②③④ 可能因为「量具坏了」而恒绿
//（例如整页根本没渲染出状态行，断言「不含『已是最新』」就会假绿）。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ElMessage, ElMessageBox } from 'element-plus'
import testI18n from '../../test-utils/i18n'
import {
  getCheckStatus,
  getInstallStatus,
  getNodeUpgradeStatus,
  getNotifications,
  installVersion,
  runCheck,
  sourceBuildVersion,
} from '../../api/selfUpdate'

vi.mock('../../api/selfUpdate', () => ({
  getCheckStatus: vi.fn(),
  getInstallStatus: vi.fn(),
  getNodeUpgradeStatus: vi.fn(),
  getNotifications: vi.fn(),
  installVersion: vi.fn(),
  runCheck: vi.fn(),
  sourceBuildVersion: vi.fn(),
}))

/** 查成且有更新的结论。 */
const RESULT_WITH_UPDATE = {
  current_version: '2.9.0',
  current_build_seq: 10,
  latest: {
    version: '3.0.0',
    build_seq: 11,
    channel: 'stable',
    mandatory: false,
    release_notes: 'fixes a routing bug',
  },
  up_to_date: false,
  checked_at: '2026-11-16T08:00:00Z',
  notified: true,
}

/** 查成且确为最新的结论。 */
const RESULT_UP_TO_DATE = {
  current_version: '2.9.0',
  current_build_seq: 10,
  up_to_date: true,
  checked_at: '2026-11-16T08:00:00Z',
  notified: false,
}

/** 查失败的结论（后端把失败也记进 lastResult）。 */
const RESULT_FAILED = {
  current_version: '2.9.0',
  current_build_seq: 10,
  up_to_date: false,
  checked_at: '2026-11-16T08:00:00Z',
  notified: false,
  error: 'dial tcp 10.0.0.9:443: connect: connection refused',
}

const CHECK_FAILED_OUTCOME = {
  ok: false as const,
  error: 'version check failed',
  detail: 'dial tcp 10.0.0.9:443: connect: connection refused',
}

const CHECK_OK_OUTCOME = { ok: true as const, result: RESULT_WITH_UPDATE }

async function render() {
  const View = (await import('./SelfUpdateView.vue')).default
  const w = mount(View, {
    global: { plugins: [testI18n] },
  })
  // onMounted 里三个取数面并发 ⇒ 两次 flush 足够跑到断言点。
  await flushPromises()
  await flushPromises()
  return w
}

/** 取「当前版本 / 最新版本」两栏的取值（锚到具体节点，不做全文正则）。 */
async function versionCells(w: Awaited<ReturnType<typeof render>>) {
  const current = w.find('.stat--current .stat__value')
  const latest = w.find('.stat--latest .stat__value')
  expect(current.exists(), '状态卡应有「当前版本」栏').toBe(true)
  expect(latest.exists(), '状态卡应有「最新版本」栏').toBe(true)
  return {
    current: current.text().trim(),
    latest: latest.text().trim(),
  }
}

describe('自更新页：检查失败 ≠ 已是最新', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    // 默认「一切正常」：每条用例只改自己关心的那一个端点。
    vi.mocked(getCheckStatus).mockResolvedValue({
      configured: true,
      checked: true,
      interval_secs: 21600,
      current_version: '2.9.0',
      result: RESULT_WITH_UPDATE,
    } as never)
    vi.mocked(getInstallStatus).mockResolvedValue({ configured: true, running: false } as never)
    vi.mocked(getNotifications).mockResolvedValue({ items: [], total: 0, history_total: 0 } as never)
    // 公开端点兜底（未配置远端源时 admin 族端点不返回版本号）。
    vi.mocked(getNodeUpgradeStatus).mockResolvedValue({
      current_version: '2.9.0',
      current_build_seq: 10,
      channel: 'stable',
      has_update: false,
    } as never)
    vi.mocked(runCheck).mockResolvedValue(CHECK_OK_OUTCOME as never)
    vi.mocked(installVersion).mockResolvedValue({
      id: 'job-1',
      version: '3.0.0',
      mode: 'artifact',
      status: 'pending',
      started_at: '2026-11-16T08:10:00Z',
      steps: ['downloading artifact'],
    } as never)
    vi.mocked(sourceBuildVersion).mockResolvedValue({
      id: 'job-2',
      version: '3.0.0',
      mode: 'source',
      status: 'pending',
      started_at: '2026-11-16T08:20:00Z',
      steps: ['checkout worktree'],
    } as never)
    // 安装类动作必须二次确认 ⇒ 测试里桩掉确认框（确认框本身由 ElMessageBox 负责渲染）。
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as never)
    vi.spyOn(ElMessage, 'error').mockImplementation(() => undefined as never)
  })

  it('① 初始结论就是失败 → 「状态未知」而不是「已是最新」', async () => {
    vi.mocked(getCheckStatus).mockResolvedValue({
      configured: true,
      checked: true,
      interval_secs: 21600,
      current_version: '2.9.0',
      result: RESULT_FAILED,
    } as never)
    const w = await render()
    const text = w.text()
    expect(text).toContain('状态未知')
    expect(text).not.toContain('已是最新')
    // 失败原因必须落到页面上（否则「状态未知」没有下一步线索）。
    expect(text).toContain('connection refused')
  })

  it('② 点「立即检查更新」失败 → 同样是「状态未知」，绝不改口说「已是最新」', async () => {
    vi.mocked(runCheck).mockResolvedValue(CHECK_FAILED_OUTCOME as never)
    // 检查失败后，后端状态快照里的结论也是失败那份。
    vi.mocked(getCheckStatus).mockResolvedValue({
      configured: true,
      checked: true,
      interval_secs: 21600,
      current_version: '2.9.0',
      result: RESULT_FAILED,
    } as never)
    const w = await render()
    await w.find('[data-test="check-now"]').trigger('click')
    await flushPromises()
    await flushPromises()
    const text = w.text()
    expect(text).toContain('状态未知')
    expect(text).not.toContain('已是最新')
  })

  it('③ 未配置远端版本源 → 明确说未配置，且不说「已是最新」', async () => {
    vi.mocked(getCheckStatus).mockResolvedValue({ configured: false } as never)
    const w = await render()
    const text = w.text()
    expect(text).toContain('未配置远端版本源')
    expect(text).not.toContain('已是最新')
    // 未配置远端源时 admin 族端点不带版本号 ⇒ 「当前版本」仍要显示（公开端点兜底），
    // 否则状态卡在最需要它的时候是空的。
    const { current } = await versionCells(w)
    expect(current).toBe('2.9.0')
  })

  it('④ 结论不可得 → 「最新版本」不回退成当前版本号（两栏不能同数）', async () => {
    vi.mocked(getCheckStatus).mockResolvedValue({
      configured: true,
      checked: true,
      interval_secs: 21600,
      current_version: '2.9.0',
      result: RESULT_FAILED,
    } as never)
    const w = await render()
    const { current, latest } = await versionCells(w)
    expect(current).toBe('2.9.0')
    // 关键断言：这一栏是占位符，不是 2.9.0。
    expect(latest).not.toBe(current)
    expect(latest).toBe('—')
  })

  it('⑤ 有更新 → 点「立即升级」调用 install 端点（带版本号）', async () => {
    const w = await render()
    const btn = w.find('[data-test="install-now"]')
    expect(btn.exists(), '有更新时应出现「立即升级」按钮').toBe(true)
    await btn.trigger('click')
    await flushPromises()
    expect(ElMessageBox.confirm).toHaveBeenCalled() // 二次确认必须发生
    expect(installVersion).toHaveBeenCalledTimes(1)
    expect(vi.mocked(installVersion).mock.calls[0][0]).toBe('3.0.0')
    expect(vi.mocked(installVersion).mock.calls[0][1]).toMatchObject({
      mode: 'artifact',
      restart: true,
    })
    w.unmount() // 清掉安装轮询定时器
  })

  it('⑥ 「从源码编译并升级」→ 调用 source-build 端点并带上 ref', async () => {
    const w = await render()
    await w.find('.source-form__input input').setValue('release-3.0.0')
    await w.find('[data-test="source-build"]').trigger('click')
    await flushPromises()
    expect(ElMessageBox.confirm).toHaveBeenCalled()
    expect(sourceBuildVersion).toHaveBeenCalledTimes(1)
    expect(vi.mocked(sourceBuildVersion).mock.calls[0][0]).toBe('3.0.0')
    expect(vi.mocked(sourceBuildVersion).mock.calls[0][1]).toMatchObject({
      source: { ref: 'release-3.0.0' },
    })
    w.unmount()
  })

  it('⑦ 正向对照：查成且有更新 → 显示最新版本号与升级按钮', async () => {
    const w = await render()
    const { latest } = await versionCells(w)
    expect(latest).toBe('3.0.0')
    expect(w.find('[data-test="latest-version"]').text()).toBe('3.0.0')
    expect(w.find('[data-test="install-now"]').exists()).toBe(true)
    expect(w.text()).not.toContain('状态未知')
  })

  it('⑧ 正向对照：查成且确为最新 → 才显示「已是最新」', async () => {
    vi.mocked(getCheckStatus).mockResolvedValue({
      configured: true,
      checked: true,
      interval_secs: 21600,
      current_version: '2.9.0',
      result: RESULT_UP_TO_DATE,
    } as never)
    const w = await render()
    const text = w.text()
    expect(text).toContain('已是最新')
    expect(text).not.toContain('状态未知')
    // 同时证明「最新版本」这一栏在另一种数据下确实会显示版本号
    //（排除 ④ 恒绿是因为锚点根本读不到东西）。
    const { current, latest } = await versionCells(w)
    expect(latest).toBe('2.9.0')
    expect(latest).toBe(current)
  })
})
