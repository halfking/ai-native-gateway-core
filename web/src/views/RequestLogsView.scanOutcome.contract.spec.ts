// RequestLogsView.scanOutcome.contract.spec.ts —— 请求日志页的「扫描故障判定」与
// 「清除全部筛选覆盖面」两道前端契约（2026-11-23 审计轮 P1-1 / P2-4）。
//
// ★ 为什么用源码契约而不是 mount：本页依赖 i18n / router / store / element-plus，
//   mount 成本高且与真实浏览器行为无关；本门钉的是**代码形状**（判据接线、
//   键集合），与仓内同族做法一致（RequestLogsView.deepLinkFilters.contract.spec.ts、
//   components/SessionStatsPanel.probeScopeReload.test.ts）。
//   ★ 判定逻辑本身的行为由 utils/requestLogsScanOutcome.spec.ts 钉住
//   （两种情形各自的判定结果）—— 本门只钉「视图有没有按那个口径接线」。
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

/** 去掉注释，只留可执行代码（防「文档写清了缺陷」被误判成「缺陷修好了」）。 */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .map((line) => {
      const idx = line.indexOf('//')
      if (idx === -1) return line
      const before = line.slice(0, idx)
      if (before.trimEnd().endsWith(':')) return line
      return before
    })
    .join('\n')
}

describe('P1-1 扫描故障判据必须与后端逐项对齐（页码越界不再误报）', () => {
  it('① 200 响应不再用「items 空 && count>0」判扫描故障', async () => {
    const code = stripComments(await read('views/RequestLogsView.vue'))
    // 旧判据（误报源）必须消失
    expect(code).not.toMatch(/items\.length === 0 && resp\.count > 0/)
    // 越界判定改由对齐后的纯函数给出
    expect(code).toContain('resolveLogsPageOverflow')
  })

  it('② 页码越界 ⇒ 拉回最后一页并重取（不是把页面留在空页上）', async () => {
    const code = stripComments(await read('views/RequestLogsView.vue'))
    expect(code).toMatch(/page\.value = overflowPage/)
    // 重取一次（同口径参数 + 同一份深链维度）
    const retries = code.match(/getRequestLogs\(\{/g) ?? []
    expect(retries.length).toBeGreaterThanOrEqual(2)
    expect(code).toContain('...deepLinkQuery()')
  })

  it('③ 真扫描故障（服务端 500）仍要报错，且走这一族专有文案', async () => {
    const code = stripComments(await read('views/RequestLogsView.vue'))
    expect(code).toContain('isBackendScanFailure')
    expect(code).toContain('scanFailureCount')
    // 文案键仍在用（不能因为误报被删掉就变成死键）
    expect(code).toContain("t('requests.list.error.scanningFailed'")
    // 其它错误仍走统一出口
    expect(code).toContain('surfaceError(e)')
  })
})

describe('P2-4 「清除全部筛选」必须覆盖页面用到的全部过滤键', () => {
  it('① URL 过滤键全集必须显式列出（含 model 与时间范围）', async () => {
    const src = await read('views/RequestLogsView.vue')
    const block = src.slice(src.indexOf('const URL_FILTER_KEYS'), src.indexOf(']', src.indexOf('const URL_FILTER_KEYS')))
    for (const key of [
      'success',
      'error_kind',
      'api_key_id',
      'preset',
      'hours',
      'from',
      'to',
      'owner_user',
      'gw_session_id',
      'gw_task_id',
      'model',
    ]) {
      expect(block, `URL_FILTER_KEYS 必须含 ${key}`).toContain(`'${key}'`)
    }
  })

  it('② clearAllFilters 必须把 URL_FILTER_KEYS 一并摘掉', async () => {
    const src = await read('views/RequestLogsView.vue')
    const block = src.slice(src.indexOf('function clearAllFilters'))
    expect(block).toContain('...URL_FILTER_KEYS')
    expect(block).toContain('stripDeepLinkFromQuery(')
  })

  it('③ owner 也是过滤键：必须被清空、也要计入生效条件数', async () => {
    const src = await read('views/RequestLogsView.vue')
    const block = src.slice(src.indexOf('function clearAllFilters'))
    expect(block).toContain("ownerUserFilter.value = ''")
    const counter = src.slice(src.indexOf('const activeFilterCount'))
    expect(counter).toContain('ownerUserFilter.value.trim()')
  })
})
