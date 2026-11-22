// saTop5Providers.contract.spec.ts —— 「Top5 供应商必须显示供应商、不是模型名」前端契约钉测
// （2026-11-23 复核轮）。
//
// ★ 背景（owner 报障 + 生产取证）：
//
//	/admin/session-analytics/users 的「Top 5 供应商」表格，供应商列显示的是
//	**模型名**（minimax-m3.1-flash / glm-5.3-flash / claude-sonnet-5-5 …）。
//	原缺陷形态：前端拿 top_models 的模型名按 "/" 切前缀当供应商，而本仓模型名是
//	**裸名**（无 provider/ 前缀）⇒ 切出来的"供应商"就是模型名本身；且同名模型
//	会同时挂在多个供应商上（211 真库实测：glm-5.3-flash 同时走 zhipu 与 nvidia）
//	⇒ 模型→供应商**不是函数关系**，模型名推不出供应商。
//
// ★ 上游 `614b72e9c` 已把口径改为**按 provider 维度聚合**
//	（后端 /provider-ranking 端点：GROUP BY provider + COUNT(DISTINCT model)），
//	本文件钉住**前端消费面**不再退回"从模型名反推"的形态：
//	① 供应商卡片必须来自 provider 维度行（providerRows），不是模型行；
//	② 前端**代码**里不得再出现"按 '/' 切前缀"的供应商推导；
//	③ API 层确有 provider-ranking 端点；
//	④ Dashboard 侧（SessionStatsPanel）不再消费任何供应商排行。
//
// ★ 为什么用源码契约而不是 mount：本组件是纯展示 + 路由跳转，
//   jsdom 下没有布局/画布（仓内既有 sessionCharts.init.test.ts 同款取舍），
//   且真库口径另有 Go 真库钉测（provider_ranking_realdb_test.go）兜底。
//
// ★ 断言必须**先去注释**：上游组件的头注释里**故意引用了旧缺陷代码**
//   （`split('/')[0]`、"模型名当 provider"），那是**说明文字**不是实现；
//   不去注释就会把"文档写清了缺陷"误判成"缺陷复活"。
import { describe, it, expect } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

const CANDS = (p: string) => [
  resolve(process.cwd(), `web/src/${p}`),
  resolve(process.cwd(), `src/${p}`),
]

async function read(rel: string): Promise<string> {
  for (const c of CANDS(rel)) {
    try {
      return await readFile(c, 'utf8')
    } catch {
      /* next */
    }
  }
  throw new Error(`${rel} not found`)
}

/** 去掉块注释与行注释，只留可执行代码（用于"缺陷形态不得复活"的反面断言）。 */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .map((line) => {
      const idx = line.indexOf('//')
      if (idx === -1) return line
      const before = line.slice(0, idx)
      // 'https://…' 这类字符串里的 '//' 不是注释
      if (before.trimEnd().endsWith(':')) return line
      return before
    })
    .join('\n')
}

describe('Top5 供应商：显示 provider 而不是模型名', () => {
  it('① 供应商卡片渲染 provider 字段（且表格有供应商列）', async () => {
    const src = await read('views/admin/session-analytics/components/SaTop5Rankings.vue')
    expect(src).toContain('{{ row.provider }}')
    expect(src).toContain("t('sessionAnalytics.col.provider')")
  })

  it('② 供应商维度来自 provider 排行这条独立数据腿（不是模型排行推出来的）', async () => {
    const view = await read('views/admin/session-analytics/SessionAnalyticsUsersView.vue')
    const code = stripComments(view)
    // 供应商行来自 provider 维度端点的结果，与模型行互不推导
    expect(code).toMatch(/providerRows/)
    expect(code).not.toMatch(/split\(['"]\/['"]\)/)

    const comp = stripComments(
      await read('views/admin/session-analytics/components/SaTop5Rankings.vue'),
    )
    expect(comp).not.toMatch(/split\(['"]\/['"]\)/)
  })

  it('③ 前端 API 层确有 provider-ranking 端点（供应商维度真源）', async () => {
    const src = await read('api/sessionAnalytics.ts')
    expect(src).toContain('provider-ranking')
  })

  it('④ Dashboard 侧（SessionStatsPanel）不再消费任何供应商排行', async () => {
    const panel = stripComments(await read('components/SessionStatsPanel.vue'))
    expect(panel).not.toContain('provider-ranking')
    expect(panel).not.toContain('top_providers')
    expect(panel).not.toMatch(/split\(['"]\/['"]\)/)
  })
})
