// boardPieDimensionsParity.test.ts —— Go 产出的每个饼图维度都必须有前端消费
//
// ## 这条门在挡什么
//
// 2026-10-03 做完 boardPieDegradation 后做了一次维度名对账：
// Go 端 `fallbackBoardPies` 产出 7 个维度，前端只消费了 6 个 ——
// `providers` 那一格渲染在 `BoardProviderSection.vue`（不在 BoardDistGrid），
// 于是漏了降级处理：降级时它显示「暂无数据」。
//
// **为什么这是个判据该管的问题**：服务端诚实降级了，前端 6/7 消费了，
// 前 6 格一切正常 ⇒ 现有任何判据都是绿的。
// 唯一能发现它的办法是**跨语言对账**：后端能产出什么，前端有没有接住。
//
// 这与 §9.2（递归扫描漏子包）、§16（「渲染得少」无人管）同族：
// 都是「后端做对了、前端没接住」，而且**从任何一侧看都自洽**。
//
// ## 判据怎么做的
//
// 从 Go 源码里**解析**出维度名（不是抄一份清单），
// 再从 Vue 文件里解析出 `isBoardPieDegraded(..., 'X')` 的实参，
// 断言前者 ⊆ 后者。
//
// 为什么必须解析而不是抄清单：本轮最初就是靠「手工核对一遍」发现的，
// 而手工清单下次新增维度时不会自己更新 —— 那正是本门要防的回归。
//
// 反向也查：前端引用了 Go 不产出的维度名 ⇒ 那是恒死分支（判据失效的信号）。

import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

// 从 web/src/components/board 上溯 3 级 = 仓库根。
// （第一版写成 '..','..','..' 少算了一级，指向 web/ 自身 ⇒ 三条一起报 ENOENT。
//   症状是「全红」，但原因只是路径层级，不是产品。）
const REPO = join(import.meta.dirname, '..', '..', '..', '..')

/** Go 端 fallbackBoardPies 的 types map：key 是维度名，value 是 dimType。 */
const goDimensionNames = (): Set<string> => {
  const src = readFileSync(join(REPO, 'admin', 'dashboard_board_fallback.go'), 'utf8')
  const block = src.slice(
    src.indexOf('types := map[string]string{'),
    src.indexOf('\n\t}', src.indexOf('types := map[string]string{')),
  )
  const names = new Set<string>()
  for (const line of block.split('\n')) {
    const m = line.match(/^\s*"([a-z_]+)":\s*"[a-z_]+",?\s*$/)
    if (m) names.add(m[1])
  }
  return names
}

/** 前端所有 isBoardPieDegraded(…, 'X') 的实参。 */
const tsConsumedDimensions = (): Set<string> => {
  const dir = join(REPO, 'web', 'src', 'components', 'board')
  const names = new Set<string>()
  for (const f of readdirSync(dir)) {
    if (!f.endsWith('.vue')) continue
    const src = readFileSync(join(dir, f), 'utf8')
    for (const m of src.matchAll(/isBoardPieDegraded\([^,]+,\s*'([a-z_]+)'\)/g)) {
      names.add(m[1])
    }
  }
  return names
}

describe('饼图维度：Go 产出 ⊆ 前端消费', () => {
  it('Go 端确实解析出了 7 个维度（防解析器静默失效）', () => {
    // 这条不是形式检查：解析器一旦写错，下面的差集断言会「全绿」
    // （空集 ⊆ 任何集合），那就成了一道恒绿的门。
    const go = goDimensionNames()
    expect(go.size).toBe(7)
    expect([...go].sort()).toEqual([
      'client_ips', 'clients', 'errors', 'identity_hashes', 'models', 'providers', 'tenants',
    ])
  })

  it('每个 Go 维度都有前端消费点', () => {
    const go = goDimensionNames()
    const ts = tsConsumedDimensions()
    const missing = [...go].filter((d) => !ts.has(d)).sort()
    expect(
      missing,
      `这些饼图维度服务端会降级，但前端没有 isBoardPieDegraded 判据 ——\n` +
      `降级时它们会显示「暂无数据」，与「真的没有该维度数据」同形。\n` +
      `缺失：${missing.join(', ')}`,
    ).toEqual([])
  })

  it('前端没有引用 Go 不产出的维度名（恒死分支信号）', () => {
    const go = goDimensionNames()
    const ts = tsConsumedDimensions()
    const ghost = [...ts].filter((d) => !go.has(d)).sort()
    expect(
      ghost,
      `前端消费了 Go 不产出的维度名 ${ghost.join(', ')} —— ` +
      `那个分支永远不会触发（降级判据恒假）。` +
      `要么 Go 改名了，要么前端写错了。`,
    ).toEqual([])
  })
})
