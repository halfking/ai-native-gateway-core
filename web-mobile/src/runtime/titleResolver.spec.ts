import { describe, expect, it } from 'vitest'
import { resolveTitle } from './titleResolver'

describe('titleResolver（06 §2 优先级链）', () => {
  const base = { appName: '网关运维' }

  it('注册标题 > 弹层标题 > 继承快照 > 路由 > 应用名', () => {
    expect(resolveTitle({ ...base, registered: 'R', overlay: 'O', inherited: 'I', route: 'T' })).toBe('R')
    expect(resolveTitle({ ...base, registered: null, overlay: 'O', inherited: 'I', route: 'T' })).toBe('O')
    expect(resolveTitle({ ...base, registered: null, overlay: null, inherited: 'I', route: 'T' })).toBe('I')
    expect(resolveTitle({ ...base, registered: null, overlay: null, inherited: null, route: 'T' })).toBe('T')
    expect(resolveTitle({ ...base, registered: null, overlay: null, inherited: null, route: null })).toBe('网关运维')
  })

  it('空串按 null 处理（trim 后为空落下一级）', () => {
    expect(resolveTitle({ ...base, registered: '  ', overlay: null, inherited: 'I', route: null })).toBe('I')
  })

  it('超长标题截断到 ≤200 字符', () => {
    const out = resolveTitle({ ...base, registered: 'x'.repeat(500), overlay: null, inherited: null, route: null })
    expect(out.length).toBeLessThanOrEqual(200)
  })
})
