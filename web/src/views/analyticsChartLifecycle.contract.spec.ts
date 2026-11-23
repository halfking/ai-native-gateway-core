// analyticsChartLifecycle.contract.spec.ts —— ECharts 实例的「创建即必须销毁」契约
// （2026-11-23 审计轮 P1-2）。
//
// ★ 缺陷形态（实测）：ClientAnalyticsView.vue 与 TaskAnalyticsView.vue 是全仓
//   仅剩的两个「echarts.init 了但从不 dispose、也没有卸载钩子」的视图
//   ⇒ 路由来回切换时实例泄漏、canvas 叠加、旧数据留在内存里。
//   ★ 同页的第二个缺陷：两页都读 route.params.id 却**没有 watch**
//   ⇒ 换路由参数后图表还是上一个主体的（标题变了、数据没变）。
//
// ★ 为什么用源码契约而不是 mount：echarts.init 在 jsdom 无 canvas 会炸
//   （同族取舍见 components/analytics/sessionCharts.init.test.ts）。
//   ★ 本门同时是**全仓反向门**：任何 .vue 只要 init 了 echarts，就必须有
//   dispose + 卸载钩子 ⇒ 以后新增视图不会重蹈覆辙。
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'

const CANDS = (p: string) => [
  resolve(process.cwd(), `web/src/${p}`),
  resolve(process.cwd(), `src/${p}`),
]

function resolveSrcDir(): string {
  for (const c of CANDS('')) {
    try {
      if (statSync(c).isDirectory()) return c
    } catch {
      /* next */
    }
  }
  throw new Error('src dir not found')
}

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

/** 递归收集 .vue 文件。 */
function collectVueFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name.startsWith('.') || entry.name === 'node_modules' || entry.name === 'dist') continue
    const p = join(dir, entry.name)
    if (entry.isDirectory()) collectVueFiles(p, out)
    else if (entry.name.endsWith('.vue')) out.push(p)
  }
  return out
}

describe('P1-2 ECharts 实例生命周期', () => {
  it('① 全仓每个 echarts.init 的视图都必须有 dispose + 卸载钩子（反向门）', () => {
    const srcDir = resolveSrcDir()
    const offenders: string[] = []
    for (const file of collectVueFiles(srcDir)) {
      const src = readFileSync(file, 'utf8')
      if (!src.includes('echarts.init')) continue
      const hasDispose = src.includes('.dispose()')
      const hasUnmountHook = /onBeforeUnmount\s*\(|onUnmounted\s*\(/.test(src)
      if (!hasDispose || !hasUnmountHook) {
        offenders.push(`${file.replace(srcDir, 'src')} (dispose=${hasDispose}, unmount=${hasUnmountHook})`)
      }
    }
    expect(offenders, `这些视图 init 了 echarts 却没有销毁：\n${offenders.join('\n')}`).toEqual([])
  })

  it('② ClientAnalyticsView：dispose + 路由参数 watch（换 id 必须重画）', async () => {
    const src = await read('views/ClientAnalyticsView.vue')
    expect(src).toContain('onBeforeUnmount')
    expect(src).toMatch(/chartInstance\.dispose\(\)/)
    expect(src).toMatch(/watch\(clientId/)
    // 换 id ⇒ 丢弃旧实例 + 重新取数
    expect(src).toMatch(/watch\(clientId[\s\S]*?disposeChart\(\)[\s\S]*?void load\(\)/)
  })

  it('③ TaskAnalyticsView：dispose + 路由参数 watch（换 id 必须重画）', async () => {
    const src = await read('views/TaskAnalyticsView.vue')
    expect(src).toContain('onBeforeUnmount')
    expect(src).toMatch(/chartInstance\.dispose\(\)/)
    expect(src).toMatch(/watch\(taskId/)
    expect(src).toMatch(/watch\(taskId[\s\S]*?disposeChart\(\)[\s\S]*?void load\(\)/)
  })

  it('④ 两页都必须在 data 赋值后等 nextTick 再画（chartRef 在 v-if 内）', async () => {
    for (const rel of ['views/ClientAnalyticsView.vue', 'views/TaskAnalyticsView.vue']) {
      const src = await read(rel)
      const loadBlock = src.slice(src.indexOf('async function load()'))
      expect(loadBlock.slice(0, 800), `${rel} 必须在 renderChart 前 await nextTick()`).toContain(
        'await nextTick()',
      )
    }
  })
})
