import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(process.cwd(), 'src/components/LiveRequestStreamV2.vue'), 'utf8')

describe('LiveRequestStreamV2 responsive controls', () => {
  it('keeps the mobile control bar horizontally scrollable', () => {
    const mobileStyles = source.slice(source.indexOf('@media (max-width: 768px)'))

    expect(mobileStyles).toMatch(/\.stream-controls\s*{[^}]*flex-direction:\s*row/s)
    expect(mobileStyles).toMatch(/\.stream-controls\s*{[^}]*flex-wrap:\s*nowrap/s)
    expect(mobileStyles).toMatch(/\.stream-controls\s*{[^}]*overflow-x:\s*auto/s)
    expect(mobileStyles).toMatch(/\.control-group,\s*\.filter-group\s*{[^}]*flex:\s*0 0 auto/s)
  })

  it('shows grouping controls in the requested order without the removed vendor tab', () => {
    // ★ 2026-10-27 第 21 增量：分组容器不再着色 ⇒ 修饰类 `control-group--dimension`
    //   随之删除（它只服务于旧的着色规则）。这里改用**分区注释**做锚点 ——
    //   注释是这一段的身份，类名只是样式钩子，改样式不该动测试。
    const dimensionControls = source.slice(
      source.indexOf('<!-- 分组切换 -->'),
      source.indexOf('<!-- 2026-07-23: 大/小 模式切换'),
    )
    const positions = [
      "handleGroupByChange('queue')",
      "handleGroupByChange('credential')",
      "handleGroupByChange('provider')",
      "handleGroupByChange('model')",
    ].map((value) => dimensionControls.indexOf(value))

    expect(positions.every((position) => position >= 0)).toBe(true)
    expect(positions).toEqual([...positions].sort((a, b) => a - b))
    expect(dimensionControls).not.toContain("handleGroupByChange('vendor')")
  })
})
