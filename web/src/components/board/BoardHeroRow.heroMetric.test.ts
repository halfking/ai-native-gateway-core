// BoardHeroRow.heroMetric.test.ts —— 「看板英雄区」不许再是 hero-metric 模板（第 32 增量）
//
// ★ 为什么需要这道测试（★ 运行时门看不见这一处）：
//   `BoardHeroRow` 挂在 `DashboardViewV2` 的 **board tab** 上，而 board 接口
//   在本机是**降级**的（`degraded_summary` / `credits_missing_view`）⇒
//   **这一排卡在本机根本不渲染** ⇒ 运行时尺（design-hero-metric-runtime-check）
//   量到的是「没扫到」，不是「真没有」。
//   ⇒ ★★ 因此这一处只能靠**源码守卫 + 既有渲染测试**守住 ⇒ 本文件就是那道守卫。
//
// ★ 守卫的是什么（impeccable craft-floor 的 hero-metric 模板）：
//   「big number, small label, supporting stats, **accent**」
//   ⇒ 本仓已批准的形态是 `StatCard`（小标签 + 24px 数值 + 口径行，**数值用正文色**）
//   ⇒ ⇒ 本守卫钉的是 **accent**：卡底不得染主色、高亮卡不得用主色描边/发光。
//   ★ 数值字号（24px）与辅助统计（成功率 / 错误数 / 输入输出 / 积分口径）**保留**
//     —— 那是「指标要有语境」的部分，砍掉反而丢信息。
import { describe, it, expect } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import BoardHeroRow from './BoardHeroRow.vue'

const SRC = resolve(process.cwd(), 'src/components/board/BoardHeroRow.vue')

async function readSource(): Promise<string> {
  const raw = await readFile(SRC, 'utf8').catch(() =>
    readFile(resolve(process.cwd(), 'web/src/components/board/BoardHeroRow.vue'), 'utf8'),
  )
  // ★★ **先剥注释再判**（★ 仓内纪律：注释里可以写「这里原来是什么」，
  //   但**不许写被禁声明的字面形态** —— 守卫脚本会把它当真违规）。
  //   ⇒ 本守卫只对**生效的声明**与**模板里的类名**下判断。
  return raw
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/^\s*\/\/.*$/gm, '')
}

const board = (summary: Record<string, unknown>) => ({
  summary: {
    total_requests: 120,
    total_prompt_tokens: 0,
    total_completion_tokens: 0,
    total_tokens: 0,
    total_cost_usd: 1.5,
    total_credits_charged: 42,
    success_rate: 0.98,
    ...summary,
  },
  pies: { clients: [], client_ips: [], identity_hashes: [], models: [], errors: [], tenants: [], providers: [] },
  trends: [],
  days: 7,
})

const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': {} } })

describe('看板英雄区不许是 hero-metric 模板', () => {
  it('① 卡底不得染主色（原来是一层 accent 渐变底）', async () => {
    const src = await readSource()
    // ★ 判据落在**声明本身**上：卡底不许出现 accent 参与的渐变/混色。
    const cardBlock = src.slice(src.indexOf('.hero-card {'), src.indexOf('.hero-card__label'))
    expect(cardBlock).not.toMatch(/linear-gradient[^;]*var\(--accent/)
    expect(cardBlock).not.toMatch(/background[^;]*color-mix\([^;]*var\(--accent/)
    expect(cardBlock).toContain('background: var(--card)')
  })

  it('② 不许有主色描边 / 主色发光的高亮卡', async () => {
    const src = await readSource()
    expect(src).not.toContain('hero-card--highlight')
    expect(src).not.toMatch(/border-color:\s*[^;]*var\(--accent/)
    expect(src).not.toMatch(/box-shadow:[^;]*var\(--accent/)
  })

  it('③ 辅助统计与状态语义色必须保留（语境不能一起砍掉）', async () => {
    const src = await readSource()
    // 成功率 / 错误数（状态语义色）+ 输入输出拆分 + 积分口径 —— 这是「指标要有语境」的部分。
    expect(src).toContain('success_rate')
    expect(src).toContain('heroErrors')
    expect(src).toContain('heroInput')
    expect(src).toContain('heroOutput')
    expect(src).toContain('creditsSub')
    // 状态色仍然是状态色（不是装饰性主色）。
    expect(src).toContain('var(--success)')
    expect(src).toContain('var(--danger)')
  })

  it('④ 渲染仍然给全四个指标（修法不许改语义）', () => {
    const w = mount(BoardHeroRow, {
      props: { board: board({}) as never },
      global: { plugins: [i18n] },
    })
    const text = w.text()
    expect(text).toContain('120') // 总请求
    expect(text).toContain('1.50') // 总费用
    expect(text).toContain('42') // 总积分
    expect(w.findAll('.hero-card')).toHaveLength(4)
  })

  it('⑤ 连注释里都不许写那个高亮类名（★ 仓内纪律的机械化）', async () => {
    // ★ 仓内第 10 增量就立过「注释里不许写形态样例」的纪律（★ 那次是 `t('…')` 形态）：
    //   理由是**源码守卫会把注释当成真违规** ⇒ 注释既不该骗过门，也不该制造假红。
    // ⇒ 这一条把那条纪律变成**可执行断言**（★ 而不是靠人记得）。
    const raw = await readFile(SRC, 'utf8').catch(() =>
      readFile(resolve(process.cwd(), 'web/src/components/board/BoardHeroRow.vue'), 'utf8'),
    )
    expect(raw).not.toContain('hero-card--highlight')
  })
})
