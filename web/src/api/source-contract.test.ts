// source-contract.test.ts —— 「后端 source 字面量 ↔ 前端联合」契约门（R182 轮）。
//
// # 这道门治什么病
//
// 后端有**两个**域会通过 JSON 的 `source` 字段下发给前端：
//
//	① 凭据监控滑动窗口 —— admin/credential_monitor.go
//	   admin/credential_monitor_sliding_window.go
//	   取值 = {"redis", "session_turns"}
//	   ★ 前端联合曾写 `'redis' | 'request_logs'` ⇒ ⇒ 后端**永不可能**产出的
//	     那个值让抽屉把 session_turns 的数据**错标成 "request_logs"**
//	     ⇒ ★ 【退役表的旧名直接上屏】。
//
//	② 统一请求详情 —— domains/requestdetail/types.go
//	   取值 = {memory, file, **live_stream**, request_logs, session_turns}
//	   ★ 前端联合曾漏掉 `live_stream` ⇒ ⇒ 它落进渲染侧的兜底分支
//	     ⇒ ★ 裸串 `live_stream` 上屏。
//
// ★ 这类漂移在**类型系统里抓不到**：值是网络来的，TS 联合只是一份**声明**
//   ⇒ ⇒ 必须有一道**读后端源码**的门去对拍。
//
// ★ 提取器必须同时认**两种**后端字面量形态（★ 第一版只认一种 ⇒ 漏掉整个②域）：
//
//	形态 A：`source := "redis"` / `source = "session_turns"`   —— 局部变量赋值
//	形态 B：`SourceRequestLogs  Source = "request_logs"`        —— 带类型的常量声明
//
// ★ 判据（每个域都要）：
//
//	① 后端该域的字面量集合 == 前端联合成员集合（**双向**逐字相等）；
//	② 前端该域里不得再出现**退役表名**；
//	③ 渲染侧不得把退役表名当文案直接上屏。
//
// ★ 门自己的位置：`web/src/api/`（同层同级 ⇒ 解析 `../../..` 得到仓库根）。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'fs'
import { dirname, resolve } from 'path'
import { fileURLToPath } from 'url'

const __dirname = dirname(fileURLToPath(import.meta.url))
// web/src/api/ → ../../.. = 仓库根
const REPO = resolve(__dirname, '../../..')

/** 已退役的表名（890 物理删除）—— 不许再出现在前端的值/联合/文案里。 */
const RETIRED_NAMES = ['request_logs', '_request_logs', 'request_logs_hot', '_request_logs_hot']

function read(rel: string): string {
  return readFileSync(resolve(REPO, rel), 'utf8')
}

/**
 * 剥掉注释后再看内容。
 *
 * ★ 为什么必须剥：★ 本门第一版**被自己的注释打红**了 —— 修正说明里写了
 * 「原来写死 `'request_logs'`」这个字面量 ⇒ ⇒ 门把注释当成了上屏内容。
 * ⇒ ★ 注释**不上屏** ⇒ ⇒ 判据必须只针对【可渲染内容】⇒ ⇒ 否则这道门
 * 会被「写一段说明就把自己关掉」的行为绑架（★ 门不能靠改注释过关）。
 */
function stripComments(src: string): string {
  return src
    .replace(/<!--[\s\S]*?-->/g, ' ')
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/(^|[^:/])\/\/[^\n]*/g, '$1 ')
}

/** 形态 A：局部变量赋值 `source := "x"` / `source = "x"`。 */
function assignedSourceLiterals(src: string): Set<string> {
  const out = new Set<string>()
  const re = /\bsource\s*:?=\s*"([^"]+)"/g
  let m: RegExpExecArray | null
  while ((m = re.exec(src)) !== null) out.add(m[1])
  return out
}

/** 形态 B：带类型的常量声明 `Xxx  Source = "x"`。 */
function declaredSourceLiterals(src: string): Set<string> {
  const out = new Set<string>()
  const re = /\bSource\s*=\s*"([^"]+)"/g
  let m: RegExpExecArray | null
  while ((m = re.exec(src)) !== null) out.add(m[1])
  return out
}

/** 从前端的 `export type X = 'a' | 'b'` 里抽出联合成员。 */
function unionMembers(src: string, typeName: string): Set<string> {
  const m = src.match(new RegExp(`export\\s+type\\s+${typeName}\\s*=\\s*([^\\n]+)`))
  if (!m) throw new Error(`★ 没找到 ${typeName} 的类型声明 ⇒ 提取器坏了 ⇒ 门判红`)
  const members = new Set<string>()
  for (const mm of m[1].matchAll(/'([^']+)'/g)) members.add(mm[1])
  return members
}

// ── 域①：凭据监控滑动窗口 ───────────────────────────────────────────────
const CM_BACKEND = ['admin/credential_monitor.go', 'admin/credential_monitor_sliding_window.go']
const CM_API = 'web/src/api/credential-monitor.ts'
const CM_DRAWER = 'web/src/components/credential-monitor/CredentialDetailDrawer.vue'

// ── 域②：统一请求详情 ─────────────────────────────────────────────────
const RD_BACKEND = 'domains/requestdetail/types.go'
const RD_API = 'web/src/api/requestDetail.ts'
const RD_PANEL = 'web/src/components/detail/RequestOverviewPanel.vue'

describe('域① 凭据监控滑动窗口：source 前端联合 ↔ 后端字面量', () => {
  it('① 后端字面量集合与前端联合逐字相等（双向）', () => {
    const backend = new Set<string>()
    for (const f of CM_BACKEND) {
      for (const v of assignedSourceLiterals(read(f))) backend.add(v)
    }
    const frontend = unionMembers(read(CM_API), 'SlidingWindowSource')

    expect(backend.size).toBeGreaterThan(0)
    expect(frontend.size).toBeGreaterThan(0)
    for (const v of backend) {
      expect(frontend.has(v), `★ 后端能产出 source="${v}"，但前端联合里没有 ⇒ 联合漏值`).toBe(true)
    }
    for (const v of frontend) {
      expect(backend.has(v), `★ 前端联合里的 source="${v}" 后端【永不产出】⇒ 那是个假分支`).toBe(true)
    }
  })

  it('② 前端这个域里不得再出现退役表名', () => {
    const union = read(CM_API).match(/export\s+type\s+SlidingWindowSource\s*=\s*([^\n]+)/)
    expect(union, '★ 没找到 SlidingWindowSource 声明').toBeTruthy()
    for (const name of RETIRED_NAMES) {
      expect(union![1]).not.toContain(name)
    }
  })

  it('③ 抽屉不得把退役表名当文案上屏', () => {
    const drawer = stripComments(read(CM_DRAWER))
    for (const name of RETIRED_NAMES) {
      expect(drawer).not.toContain(`'${name}'`)
      expect(drawer).not.toContain(`"${name}"`)
    }
  })
})

describe('域② 统一请求详情：source 前端联合 ↔ 后端字面量', () => {
  it('① 后端常量声明的字面量集合与前端联合逐字相等（双向）', () => {
    const backend = declaredSourceLiterals(read(RD_BACKEND))
    const frontend = unionMembers(read(RD_API), 'DetailSource')

    expect(backend.size).toBeGreaterThan(0)
    expect(frontend.size).toBeGreaterThan(0)
    for (const v of backend) {
      expect(frontend.has(v), `★ 后端能产出 source="${v}"，但前端联合里没有 ⇒ 联合漏值 ⇒ 它会裸串上屏`).toBe(true)
    }
    for (const v of frontend) {
      expect(backend.has(v), `★ 前端联合里的 source="${v}" 后端【永不产出】⇒ 那是个假分支`).toBe(true)
    }
  })

  it('①b 移动端早就对齐了 5 个取值（★ 桌面端不得再落后于移动端）', () => {
    const mobile = read('web-mobile/src/api/requestDetail.ts')
    const m = mobile.match(/REQUEST_DETAIL_SOURCES\s*=\s*\[([\s\S]*?)\]/)
    expect(m, '★ 没找到移动端 REQUEST_DETAIL_SOURCES').toBeTruthy()
    const mobileValues = new Set<string>()
    for (const mm of m![1].matchAll(/'([^']+)'/g)) mobileValues.add(mm[1])
    const desktop = unionMembers(read(RD_API), 'DetailSource')
    for (const v of mobileValues) {
      expect(desktop.has(v), `★ 移动端有 "${v}"，桌面端联合里没有 ⇒ 两端漂移`).toBe(true)
    }
  })

  it('② 渲染侧不得把退役表名当文案上屏', () => {
    const panel = stripComments(read(RD_PANEL))
    for (const name of RETIRED_NAMES) {
      expect(panel).not.toContain(`'${name}'`)
      expect(panel).not.toContain(`"${name}"`)
    }
  })

  it('③ 兜底不许把未知值当文案上屏（★ 宁可空着，也不要摆内部标识符）', () => {
    const panel = stripComments(read(RD_PANEL))
    // ★ 旧形态是 `return map[s] || s` ⇒ ★ 未知值原样上屏 ⇒ ★ 现在必须是 ?? 兜底
    expect(panel).not.toMatch(/return\s+map\[s\]\s*\|\|\s*s\b/)
  })
})

describe('门自己的牙齿（★ 阳性对照）', () => {
  it('剥注释后判据仍然有牙齿：注释里写不算，代码里写就算', () => {
    const onlyComment = stripComments(`
      <template>
        <!-- 原来写死 'request_logs' -->
        <span>{{ x }}</span>
      </template>
    `)
    expect(onlyComment).not.toContain("'request_logs'")

    const realCode = stripComments(`
      <template>
        <span>{{ x === 'redis' ? 'Redis' : 'request_logs' }}</span>
      </template>
    `)
    expect(realCode).toContain("'request_logs'")
  })

  it('两种后端字面量形态都能被提取器认出来', () => {
    expect(assignedSourceLiterals('source := "redis"\nsource = "session_turns"')).toEqual(
      new Set(['redis', 'session_turns']),
    )
    expect(declaredSourceLiterals('SourceLive   Source = "live_stream"')).toEqual(new Set(['live_stream']))
  })
})
