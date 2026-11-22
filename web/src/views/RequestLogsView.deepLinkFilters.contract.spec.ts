/**
 * RequestLogsView.deepLinkFilters.contract.spec.ts —— 「排行卡片点击 → 请求日志」深链筛选契约门。
 *
 * 存在理由（2026-11-23 实测，两头都缺）：
 *
 * ① 后端：`/api/logs` 此前**只实现了 model 一维**过滤，其余五维
 *    （client_model / end_user_id / api_key_prefix / application_code / task_type）
 *    被原样忽略 ⇒ 实测传一个**根本不存在**的值
 *    `end_user_id=ZZZ_NOT_A_REAL_USER_12345` 与传真值返回的 100 条**完全一样**。
 *
 * ② 前端：请求日志页 `onMounted` 此前**只读**
 *    success/error_kind/api_key_id/preset/hours/from/to/owner_user/
 *    gw_session_id/gw_task_id ⇒ 排行维度值**被整段丢掉**
 *    ⇒ 点排行卡片过来看到的是**未筛选的全量请求**。
 *    ⇒ ★★ 所以「点击进去可以看到该维度的请求」此前**一次都没成立过**
 *      （哪怕后端实现了过滤，页面不转发也没用 ⇒ 两头都得修）。
 *
 * 本门钉住两端的**契约**（参数名一致 + 真的被转发 + 可见可清）：
 *   ① 排行卡片（UsageTrendExplorer.vue）推的六个参数名；
 *   ② 请求日志页（RequestLogsView.vue）从 route.query 读这六个参数；
 *   ③ 请求日志页把其中五个**转发**给 /api/logs（model 走已有的 ModelPicker）；
 *   ④ API 客户端（api/logs.ts）真的把五个参数拼进查询串；
 *   ⑤ 深链筛选**可见**（chip）且**可清**（计数 + 一键清空 + 单个清除）。
 *
 * 通用式：**「深链参数落地页不读」是最容易被当成"后端没实现"的缺陷**——
 *   先分清是「参数没被发出去」还是「参数发出去了但被忽略」。
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'

const LOGS_VIEW = resolve(process.cwd(), 'src/views/RequestLogsView.vue')
const USAGE_TREND = resolve(process.cwd(), 'src/views/admin/UsageTrendExplorer.vue')
const LOGS_API = resolve(process.cwd(), 'src/api/logs.ts')

const VIEW_SRC = readFileSync(LOGS_VIEW, 'utf8')
const TREND_SRC = readFileSync(USAGE_TREND, 'utf8')
const API_SRC = readFileSync(LOGS_API, 'utf8')

/** 排行六维对应的查询参数名（排行侧推、落地页读、后端消费，三处必须同名）。 */
const DIMENSION_PARAMS = [
  'model',
  'client_model',
  'end_user_id',
  'api_key_prefix',
  'application_code',
  'task_type',
] as const

describe('排行卡片 → 请求日志 深链契约', () => {
  it('排行卡片把六个维度的值放进 URL', () => {
    // UsageTrendExplorer.vue 的 navigateToRequests() 逐维设置 query
    for (const key of DIMENSION_PARAMS) {
      expect(
        TREND_SRC,
        `排行卡片必须把 ${key} 放进跳转 query（否则落地页无从筛选）`,
      ).toContain(`query.${key} =`)
    }
    // api_key_prefix 走的是 '...' 之前那段（排行 name 形如 prefix...alias）
    expect(TREND_SRC).toContain("value.split('...')[0]")
    expect(TREND_SRC).toContain('query.api_key_prefix = prefix')
  })

  it('请求日志页从 route.query 读这六个参数', () => {
    expect(VIEW_SRC).toContain('const q = route.query')
    // 五个深链维度走 deepLinkFilters（循环读），model 单独读
    expect(VIEW_SRC).toContain('deepLinkFilters.value')
    expect(VIEW_SRC).toContain("fromQuery('model')")
    // deepLinkFilters 的键必须覆盖其余五维
    for (const key of DIMENSION_PARAMS) {
      if (key === 'model') continue
      expect(VIEW_SRC, `deepLinkFilters 必须含 ${key}`).toContain(`${key}: ''`)
    }
  })

  it('请求日志页把五个深链维度转发给 /api/logs', () => {
    expect(
      VIEW_SRC,
      'load() 必须把 deepLinkQuery() 展开进 getRequestLogs 的参数（漏了就是「点了卡片看到全量」）',
    ).toMatch(/getRequestLogs\(\{[\s\S]*?\.\.\.deepLinkQuery\(\)/)
  })

  it('API 客户端真的把五个深链维度拼进查询串', () => {
    for (const key of DIMENSION_PARAMS) {
      if (key === 'model') continue
      expect(API_SRC, `getRequestLogs 必须把 ${key} 拼进查询串`).toContain(
        `qs.set('${key}', params.${key})`,
      )
      expect(API_SRC, `getRequestLogs 的参数类型必须含 ${key}`).toContain(`${key}?: string`)
    }
  })

  it('深链筛选可见（chip）且可清（计数 + 一键清空 + 单个清除）', () => {
    // 可见
    expect(VIEW_SRC).toContain('data-testid="logs-deep-link-filters"')
    expect(VIEW_SRC).toContain('deepLinkChips')
    // 计入「生效条件数」
    expect(VIEW_SRC).toContain('Object.values(deepLinkFilters.value).filter')
    // 一键清空
    expect(VIEW_SRC).toContain('for (const key of Object.keys(deepLinkFilters.value)) deepLinkFilters.value[key] =')
    // 单个清除
    expect(VIEW_SRC).toContain('clearDeepLinkFilter')
  })
})

describe('getRequestLogs 的深链参数（行为层）', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('五个深链维度都会出现在请求 URL 上', async () => {
    const { getRequestLogs } = await import('../api/logs')
    const seen: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        seen.push(String(input))
        return new Response(JSON.stringify({ items: [], count: 0 }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      }),
    )

    await getRequestLogs({
      client_model: 'cli-1',
      end_user_id: 'dev-A',
      api_key_prefix: 'sk-aaa****',
      application_code: 'app-1',
      task_type: 'unknown',
    })

    expect(seen).toHaveLength(1)
    const url = new URL(seen[0], 'https://example.invalid')
    expect(url.pathname).toBe('/api/logs')
    expect(url.searchParams.get('client_model')).toBe('cli-1')
    expect(url.searchParams.get('end_user_id')).toBe('dev-A')
    expect(url.searchParams.get('api_key_prefix')).toBe('sk-aaa****')
    expect(url.searchParams.get('application_code')).toBe('app-1')
    expect(url.searchParams.get('task_type')).toBe('unknown')
  })

  it('未传的深链维度不会出现在请求 URL 上（不得发空值参数）', async () => {
    const { getRequestLogs } = await import('../api/logs')
    const seen: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        seen.push(String(input))
        return new Response(JSON.stringify({ items: [], count: 0 }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      }),
    )

    await getRequestLogs({ client_model: 'cli-1' })

    const url = new URL(seen[0], 'https://example.invalid')
    expect(url.searchParams.get('client_model')).toBe('cli-1')
    expect(url.searchParams.has('end_user_id')).toBe(false)
    expect(url.searchParams.has('api_key_prefix')).toBe(false)
    expect(url.searchParams.has('application_code')).toBe(false)
    expect(url.searchParams.has('task_type')).toBe(false)
  })
})
