import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

async function readSource(rel: string): Promise<string> {
  const candidates = [
    resolve(process.cwd(), 'web', rel),
    resolve(process.cwd(), rel),
  ]
  for (const path of candidates) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      /* try next */
    }
  }
  throw new Error(`${rel} not found`)
}

describe('simple mode overview view contract', () => {
  it('is the simple mode home and the old access-list view is gone', async () => {
    const home = await readSource('src/views/HomeView.vue')
    expect(home).toContain("import SimpleOverviewView from './SimpleOverviewView.vue'")
    expect(home).toContain('<SimpleOverviewView v-if="isLoggedIn && isSimple" />')
    expect(home).not.toContain('SimpleAccessListView')
    await expect(readSource('src/views/SimpleAccessListView.vue')).rejects.toThrow()
  })

  it('answers the three beginner questions in one screen', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    // ① 能不能用 → 状态条
    expect(src).toContain('data-testid="overview-status"')
    expect(src).toContain('status-dot')
    // ② 今天跑了多少 → 统计卡
    expect(src).toContain('data-testid="overview-stats"')
    expect(src).toContain('<StatCard')
    expect(src).toContain('<StatsRow')
    // ③ 该做什么 → 常用操作
    expect(src).toContain('data-testid="overview-quick-actions"')
    // ④ 我的客户端 / 上游
    expect(src).toContain('data-testid="overview-access-list"')
    expect(src).toContain('data-testid="overview-provider-health"')
  })

  it('loads three sources instead of fanning out one request per key', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('fetchDashboardBoard')
    expect(src).toContain('getProviders()')
    expect(src).toContain('getKeys()')
    // ★ 旧实现对每个密钥发一次 /api/usage/{id}/models（线上 208 个密钥 ⇒ 208 个请求）
    expect(src).not.toContain('getKeyUsageByModel')
    expect(src).not.toMatch(/keys\.value\.map\(/)
  })

  it('degrades each source independently', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('Promise.allSettled')
    expect(src).toContain('boardFailed')
    expect(src).toContain('providersFailed')
    expect(src).toContain('keysFailed')
    // 权限不足与服务异常分开说
    expect(src).toContain('providersDenied')
    expect(src).toContain('providerDenied')
    // 原始 error.message 不直接上屏
    expect(src).toContain('surfaceError')
    expect(src).not.toMatch(/error\.value = e instanceof Error \? e\.message/)
  })

  it('keeps the quick action zone within the action density budget', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    const block = src.slice(src.indexOf('data-testid="overview-quick-actions"'))
    const zone = block.slice(0, block.indexOf('</section>'))
    const buttons = zone.match(/<button/g)?.length ?? 0
    // design-action-density: 一个动作区最多 4 个可见动作
    expect(buttons).toBeGreaterThan(0)
    expect(buttons).toBeLessThanOrEqual(4)
  })

  it('never hardcodes Chinese in the view', async () => {
    // ★ 用**官方计数器**判，不自己重写一遍规则（同一口径只能有一份实现）：
    //   hardcodedCjk.countFile 会按 parser 剥掉 HTML 注释与脚本注释，
    //   自己写正则必然与门禁口径漂移（这正是本仓吃过亏的地方）。
    const { countFile } = await import('../i18n/hardcodedCjk')
    const abs = resolve(process.cwd(), 'web/src/views/SimpleOverviewView.vue')
    const fallback = resolve(process.cwd(), 'src/views/SimpleOverviewView.vue')
    const count = await (async () => {
      try {
        return countFile(abs)
      } catch {
        return countFile(fallback)
      }
    })()
    expect(count).toBe(0)
  })

  it('keeps the folded inactive keys visible with a count', async () => {
    const src = await readSource('src/views/SimpleOverviewView.vue')
    expect(src).toContain('data-testid="overview-access-fold"')
    expect(src).toContain('inactiveRows.length')
    expect(src).toContain('count-badge')
  })
})

describe('simple mode providers view contract', () => {
  it('uses localizable strings for every referenced common key', async () => {
    const src = await readSource('src/views/SimpleProvidersView.vue')
    expect(src).toContain("t('common.status.healthy')")
    expect(src).toContain("t('common.status.warning')")
    expect(src).toContain("t('common.status.unknown')")
    expect(src).toContain("t('common.credentials')")
    expect(src).toContain("t('common.autoFilled')")
    expect(src).toContain("t('common.protocol')")
    // 不再有硬编码中文标签
    expect(src).not.toContain('主凭据')
    expect(src).toContain("t('simpleMode.providers.primaryCredentialLabel'")
  })

  it('routes error copy through the localization exit', async () => {
    const src = await readSource('src/views/SimpleProvidersView.vue')
    expect(src).toContain('surfaceError')
    expect(src).not.toMatch(/ElMessage\.error\(msg\)/)
  })
})

describe('simple mode keys view contract', () => {
  it('sends the user back to a route that exists', async () => {
    const src = await readSource('src/views/SimpleKeysView.vue')
    // '/simple/access-list' 从来没有注册过 ⇒ 「返回」落到空路由
    expect(src).not.toContain("router.push('/simple/access-list')")
    expect(src).toContain("router.push('/dashboard')")
  })
})
