// SessionStatsPanel.probeScopeReload.test.ts —— 「包含探测」开关必须**真的生效**
// （2026-11-23 L7 修复轮）
//
// ★ 背景（生产实测）：页面上勾选「包含探测」后，会话统计的 KPI 纹丝不动
//   （会话 19.4K / 成本 $1,711.6368 / 请求 55.6K），而真库「含探测」口径应为
//   36,1xx / $8,5xx / 74,8xx ⇒ 看起来像「include_probes 参数没生效」。
//
// ★ 断点不在后端（后端 parseSessionWindow 一直解析这个参数），**全在前端**：
//   ① SessionStatsPanel 的 includeProbes watch 只写 localStorage、**不重载**
//      ⇒ 参数进了 URL，但请求根本没重发 ⇒ 数字不变；
//   ② 页头「刷新数据」只刷看板、**不刷会话统计面板**
//      ⇒ 用户手动点刷新也看不到变化 ⇒ 更像「参数被后端忽略了」。
//
// ★ 为什么用源码契约而不是 mount：这两个组件依赖 i18n / router / element-plus /
//   echarts，mount 成本高且与真实浏览器行为无关；本门钉的是**代码形状**
//   （口径变化 ⇒ 必须有重载入口），与仓内 sessionCharts.init.test.ts 同族做法。
//
// ★ 门禁的反向自证：把 watch 里的 refresh() 删掉、把 onRefresh 里的
//   sessionStatsRef 删掉 ⇒ 本门必须红（见「变异自证」说明）。

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

/** 截取从 start 标记起、到 end 标记止的一段源码（缺失标记时抛错而不是静默通过）。 */
function slice(src: string, start: string, end: string, label: string): string {
  const i = src.indexOf(start)
  expect(i, `${label}：找不到起点标记 ${start}`).toBeGreaterThan(-1)
  const j = src.indexOf(end, i + start.length)
  expect(j, `${label}：找不到终点标记 ${end}`).toBeGreaterThan(-1)
  return src.slice(i, j)
}

describe('会话统计「包含探测」开关必须生效（L7）', () => {
  it('① 口径开关的 watch 必须触发重载（否则参数进了 URL 也没人重新请求）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const block = slice(src, 'watch(includeProbes', '\n})', 'includeProbes watch')
    expect(block).toMatch(/refresh\(\)/)
    // 重载必须走 useDashboard 的 refresh（它会把 extraParams 合并进 query）
    expect(block).toMatch(/void\s+refresh\(\)/)
  })

  it('② extraParams 必须把 include_probes 传给后端（缺省 false ⇒ 不传）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    // ★ 2026-11-23 日期范围对齐轮：extraParams 里多了 start_date/end_date
    //   （显式日历窗口）⇒ 判据放宽到「对象里含 include_probes 且值为
    //   includeProbes.value || undefined」，**不**要求它是对象的最后一个键
    //   （否则加一个窗口参数就会把这条门变成「参数顺序门」）。
    expect(src).toMatch(/extraParams:\s*computed\(\(\)\s*=>\s*\(\{[\s\S]{0,400}?include_probes:\s*includeProbes\.value\s*\|\|\s*undefined/)
  })

  it('③ 面板必须 defineExpose({ refresh })——页头刷新按钮要能驱动它', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const block = slice(src, 'defineExpose({', '})', 'defineExpose')
    expect(block).toMatch(/refresh/)
  })

  it('④ 页头「刷新数据」必须同时刷看板与会话统计面板', async () => {
    const src = await read('views/DashboardViewV2.vue')
    const block = slice(src, 'async function onRefresh()', '\n}', 'onRefresh')
    // 看板
    expect(block).toMatch(/refreshBoard\(\)/)
    // 会话统计面板（缺它 ⇒ 用户点刷新看不到任何变化 ⇒ 更像「参数被忽略」）
    expect(block).toMatch(/sessionStatsRef\.value\?\.refresh\(\)/)
  })

  it('⑤ DashboardViewV2 必须把 ref 绑到 SessionStatsPanel 上', async () => {
    const src = await read('views/DashboardViewV2.vue')
    expect(src).toMatch(/<SessionStatsPanel\s+ref="sessionStatsRef"/)
    expect(src).toMatch(/const sessionStatsRef = ref<InstanceType<typeof SessionStatsPanel>/)
  })
})
