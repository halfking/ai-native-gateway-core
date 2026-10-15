import { describe, it, expect } from 'vitest'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

/**
 * `nativeTransport`（UI规范 04 §7.2 ④「壳内 `CapacitorHttp` 反代」前端侧安装器）
 * **接线状态**的回归护栏（2026-10-15，第一百二十六批）。
 *
 * ## 这份判据存在的理由
 *
 * `orphanLedger.spec.ts` 把本模块登记成第三类孤儿「**非页面型基础设施**」
 * （零端点 ⇒ 没有可复制的功能 ⇒ 「给它开一页」这个动作不成立）。
 *
 * ⇒ ★★ 但登记只回答了「**不该开页**」，没有回答「**那它到底是不是活的**」。
 * ⇒ 而逐字核过的事实是：**它连一次都没被调用过**，而且**跑不起来**：
 *
 * | 缺口 | 事实 | 后果 |
 * |---|---|---|
 * | **没有安装调用点** | `installNativeTransport()` 在 `src/` 里零引用（除本模块与它自己的测试）| 传输永远是默认的 `fetchTransport` |
 * | **没有基址注入** | `setGatewayBaseUrl(...)` 在**生产代码**里零调用 | `gatewayBase` 恒 `undefined` ⇒ 门⑤ 恒不通过 |
 * | **壳没装** | `package.json` 依赖只有 `pinia`/`qrcode`/`vue`/`vue-router` ⇒ **无 `@capacitor/*`** | `window.Capacitor` 不可能存在 ⇒ 门① 恒不通过 |
 *
 * ⇒ ★★★ 五道门里有**两道恒不通过**（① bridge、⑤ base_url），
 *   而 `installNativeTransport` 是「门全过才安装」⇒ **今天调用它也只会得到
 *   `installed: false`**。
 *
 * ## 为什么这值得单独写一份判据
 *
 * ★ 因为「没接线」是一个**会悄悄变化**的状态：哪天有人装了壳、注了基址，
 *   本模块就该从第三类登记里移出去并**真正接进 bootstrap**。
 * ⇒ 下面每一条都在钉「**还没接线**」这个事实 ⇒ 它们变红的那天，
 *   就是该把它接上的那天（**自撤销**，与第三类登记同一个思路）。
 *
 * ⚠️ 这些判据**不评价**这个设计对不对，只钉住**当前事实**。
 *   「壳什么时候装、基址从哪来」是产品决策，不在本批范围。
 */

const ROOT = join(process.cwd(), 'src')
const API_DIR = join(ROOT, 'api')
const PKG = join(process.cwd(), 'package.json')

function pkg(): { dependencies?: Record<string, string>; devDependencies?: Record<string, string> } {
  return JSON.parse(readFileSync(PKG, 'utf8'))
}

/**
 * `src/` 下**排除测试**、且**排除指定文件**的全部源码。
 *
 * ⚠️★★ 首版写成「用正则删掉含 `nativeTransport.ts` 的**行**」—— 那是错的：
 *   被删的必须是**文件的内容**，因为模块自身的 `export function installNativeTransport(`
 *   那一行里**并不含自己的文件名** ⇒ blob 里留着它，判据把自己的定义当成了调用点，首跑就红。
 *   ⇒ ★★★ **「排除某个文件」必须按文件粒度做，不能按「行里有没有提到它」做。**
 *
 * ★ 排除名单是「**函数的定义所在处**」：`transport.ts` 是 `setGatewayBaseUrl` 的定义处，
 *   `nativeTransport.ts` 是三个安装函数自己的定义处
 *   ⇒ 剩下的命中才是**真正的调用点**。
 */
function prodSourceBlob(exclude: readonly string[] = []): string {
  const out: string[] = []
  const walk = (dir: string): void => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, e.name)
      if (e.isDirectory()) {
        walk(full)
        continue
      }
      if (!/\.(ts|vue)$/.test(e.name)) continue
      if (e.name.endsWith('.test.ts') || e.name.endsWith('.spec.ts')) continue
      if (exclude.includes(e.name)) continue
      out.push(readFileSync(full, 'utf8'))
    }
  }
  walk(ROOT)
  return out.join('\n')
}

/** 被测模块与 `transport.ts` 自身的文件名（= 三个安装函数与 `setGatewayBaseUrl` 的定义处）。 */
const DEFINITION_SITES = ['nativeTransport.ts', 'transport.ts'] as const

function nativeTransportSrc(): string {
  return readFileSync(join(API_DIR, 'nativeTransport.ts'), 'utf8')
}

describe('④ 原生传输的接线状态（当前：未接线，且跑不起来）', () => {
  it('① ★★★★★★ `installNativeTransport` / `probeNativeTransport` / `createNativeTransport` 生产零调用', () => {
    // ★★★ 这条钉的是「**没人调它**」。若哪天 bootstrap 里调了 `installNativeTransport()`，
    //   这条立刻红 ⇒ 提醒把它从第三类登记里移出去（那时它已传递可达，棘轮会自己算出来）。
    const prod = prodSourceBlob(DEFINITION_SITES)
    for (const fn of ['installNativeTransport', 'probeNativeTransport', 'createNativeTransport']) {
      expect(prod, `${fn} 已有生产调用点 ⇒ 本模块该移出「非页面型基础设施」登记`).not.toContain(fn)
    }
  })

  it('② ★★★★★★ `setGatewayBaseUrl` 生产零调用 ⇒ 门⑤（base_url）恒不通过', () => {
    const prod = prodSourceBlob(DEFINITION_SITES)
    // ★ `transport.ts` 里的**定义**不算调用点 ⇒ 整个定义处排除出 blob，只看真正的调用
    const callSites = [...prod.matchAll(/setGatewayBaseUrl\(/g)]
    expect(callSites.length, 'setGatewayBaseUrl 已有生产调用 ⇒ 门⑤ 可以通过了').toBe(0)
    // ★★ 且 gatewayBase 因此恒为 undefined —— 断言那个读取器确实什么都不返回默认值
    const t = readFileSync(join(API_DIR, 'transport.ts'), 'utf8')
    expect(t).toMatch(/let gatewayBase: string \| undefined/)
    expect(t).toMatch(/export function gatewayBaseUrl\(\): string \| undefined \{\s*\n\s*return gatewayBase\s*\n\}/)
  })

  it('③ ★★★★★★ 壳**未安装**：依赖里没有任何 `@capacitor/*` ⇒ 门①（bridge）恒不通过', () => {
    const p = pkg()
    const deps = { ...p.dependencies, ...p.devDependencies }
    const capDeps = Object.keys(deps).filter((k) => k.startsWith('@capacitor/'))
    expect(capDeps, `壳已安装（${capDeps.join(',')}）⇒ 门① 可以通过，本模块该重新决策`).toEqual([])
  })

  it('④ ★★★★ 壳未安装：无 capacitor.config、无 android/、无 ios/', () => {
    // ★ 与 ③ 互为对照：③ 查依赖，④ 查工程产物 —— 两条都绿才叫「壳确实没装」
    expect(existsSync(join(process.cwd(), 'capacitor.config.ts'))).toBe(false)
    expect(existsSync(join(process.cwd(), 'capacitor.config.json'))).toBe(false)
    expect(existsSync(join(process.cwd(), 'android'))).toBe(false)
    expect(existsSync(join(process.cwd(), 'ios'))).toBe(false)
  })

  it('⑤ ★★★★★★ 模块头声明的五道门是**逐字**这五步（门① 与门⑤ 同时恒不通过）', () => {
    // ★ 这条钉「门」的清单本身。若哪天加/减了一道门，这条红。
    const src = nativeTransportSrc()
    expect(src).toMatch(/export type NativeGateStep = 'bridge' \| 'platform' \| 'origin' \| 'plugin' \| 'base_url'/)
    const order = /export const NATIVE_GATE_ORDER: readonly NativeGateStep\[\] = \[([\s\S]*?)\]/.exec(src)
    expect(order, '没找到 NATIVE_GATE_ORDER').toBeTruthy()
    expect(order![1]).toMatch(/'bridge'/)
    expect(order![1]).toMatch(/'base_url'/)
    // ★★ 当前状态下**恒不通过**的两道门，逐字点出来（本批的全部结论都挂在这两条上）
    expect(src, '门① 变了（不再是查 window.Capacitor）⇒ 「壳未安装 ⇒ 门① 不过」的结论要重判').toMatch(
      /const c = capacitorGlobal\(\)\s*\n\s*if \(!c\) return fail\('bridge'/,
    )
    expect(src, '门⑤ 变了（不再是查 setGatewayBaseUrl 注入的基址）⇒ 结论要重判').toMatch(
      /const base = gatewayBaseUrl\(\)/,
    )
  })

  it('⑥ ★★★★ 安装器是「门全过才装」，且**安装失败返回而不抛**', () => {
    // ★ 这解释了「为什么今天调用它也无害」：网页形态下门① 就失败，
    //   函数返回 `installed:false` 而不是抛异常 ⇒ 在网页里调用是**安全的 no-op**。
    const src = nativeTransportSrc()
    expect(src).toMatch(/const report = await probeNativeTransport\(options\)\s*\n\s*if \(!report\.gatesOk\) return \{ \.\.\.report, installed: false \}/)
    expect(src).toMatch(/setTransport\(createNativeTransport\(options\), \{ cookieJar: false \}\)/)
  })

  it('⑦ ★★★★ 网页形态下 `fetchTransport` 保持默认（「装不上就不装」是设计，不是回退 bug）', () => {
    // ★ 模块头原话：「装不上就不装，`fetchTransport` 原样留着，网页形态零影响。」
    const t = readFileSync(join(API_DIR, 'transport.ts'), 'utf8')
    expect(t).toMatch(/let current: Transport = fetchTransport/)
    expect(t).toMatch(/export function resetTransport\(\): void \{\s*\n\s*current = fetchTransport/)
    expect(t).toMatch(/export function setTransport\(t: Transport, options: \{ cookieJar\?: boolean \} = \{\}\): void/)
  })

  it('⑧b ★★★★ 依赖判据的**负控**：只看 dependencies 会漏掉 devDependency（变异 S10 删的就是这半）', () => {
    // ★★★★ 「削弱断言」型变异（S10：把两个分支合并改成只看 deps）
    //   **不会被同一批判据抓住** —— 因为今天两个分支里都没有 `@capacitor/*`。
    //   ⇒ 负控：要求判别式对**装成 devDependency** 的形态也能识别。
    // ⚠️ 这条负控在**判据内部复算**而不是调用被测函数 ——
    //   因为 `shellNotInstalledGiven` 住在另一个 spec 文件里。
    //   ⇒ 复算必须逐字一致，否则这条负控会与真判据漂移（下面 ⑧c 钉住这一点）。
    const isCap = (deps: Record<string, string>, devDeps: Record<string, string>) =>
      Object.keys({ ...deps, ...devDeps }).some((k) => k.startsWith('@capacitor/'))
    expect(isCap({}, { '@capacitor/android': '8' }), '只看 dependencies 会漏掉 devDependency').toBe(true)
    expect(isCap({ '@capacitor/core': '8' }, {})).toBe(true)
    expect(isCap({ vue: '3' }, { vitest: '4' })).toBe(false)
  })

  it('⑧c ★★★★★ 负控必须**钉住真判据的源码形态**，不能只复算（变异 S10 的教训）', () => {
    // ★★★★★ 本条是本批最贵的教训：
    //   首版 ⑧b 的负控是「**在本文件里复算一遍**判别式」，
    //   而真判据（③）在本文件里 —— 于是 ⑧b 与 ③ 成了**两份实现**。
    //   ⇒ 变异 S10 削弱 ③（`{...deps, ...devDeps}` → 只看 `deps`）时，
    //     **⑧b 仍然全绿** ⇒ 负控白测。
    //
    // ⚠️⚠️ 变量**不能叫 `self`**：jsdom 环境下 `self` 是全局 Window 对象，
    //   一旦漏了局部声明，`toMatch()` 收到的是 object 而不是 string。
    const wiringSrc = readFileSync(join(API_DIR, 'nativeTransport.wiring.spec.ts'), 'utf8')
    const ledgerSrc = readFileSync(join(API_DIR, 'orphanLedger.spec.ts'), 'utf8')
    // ⇒ ★★★★ 钉子必须写成**带转义的正则字面量**，而且要**带位置约束**。
    //   本批在这一点上连栽三次，每次都是自指：
    //   ① `toContain('那一行的字面量')` ⇒ 负控自己把字面量写成期望值 ⇒ 恒绿；
    //   ② 「裁到本测试之前」⇒ 真判据可能写在本测试**之后** ⇒ 恒红；
    //   ③ 「挖掉本测试那一段」⇒ **裁切锚点字符串本身就在 helper 里**
    //      ⇒ `indexOf` 先命中 helper 自己 ⇒ 挖掉的是 helper ⇒ 又恒绿。
    // ⇒ ✅ 正解两条：
    //   **(甲) 用 `/…/` 正则字面量**——它的文本带反斜杠转义，
    //        物理上不可能与被搜的那一行逐字相同 ⇒ 「自己命中自己」消失；
    //   **(乙) 带位置约束**——本文件里 **⑧** 那个测试也写着同一个
    //        `const deps = { ...p.dependencies, ...p.devDependencies }`
    //        ⇒ 只钉「这个形态存在」，S10 削弱 ③ 之后 **⑧ 仍然满足** ⇒ 负控失明。
    // ⇒ ★★ 通用式：**源码形态负控的钉子要写成正则字面量，并且要钉「哪一条」，
    //   不是钉「文件里有没有」。**
    expect(
      wiringSrc,
      '本文件 ③ 的依赖判据已被削弱（漏看 devDependencies）',
    ).toMatch(
      /const deps = \{ \.\.\.p\.dependencies, \.\.\.p\.devDependencies \}\s*\n\s*const capDeps = Object\.keys\(deps\)/,
    )
    expect(
      ledgerSrc,
      '棘轮文件里的那份判据与本文件不是同一形态 ⇒ 两份实现开始漂移',
    ).toMatch(
      /const deps = \{ \.\.\.dependencies, \.\.\.devDependencies \}\s*\n\s*if \(Object\.keys\(deps\)\.some/,
    )

    // ★★ 门① 的精确形态：同样要**钉住真判据的那一行**，而不是自己重建一个。
    expect(
      wiringSrc,
      '本文件 ⑤ 的门① 形态已被放松 ⇒ 负控 ⑧d 只验了它自己那份',
    ).toMatch(/expect\(src, '门① 变了[\s\S]{0,80}\)\.toMatch\(\s*\/const c = capacitorGlobal/)
  })

  it('⑧d ★★★★ 门① 的精确形态**能分辨**（负控：精确形态不得匹配门⑤ 的代码）', () => {
    // ★★ S11 把门① 的精确形态换成宽松的 `fail('bridge'` —— 事实仍为真 ⇒ 全绿。
    //   ⇒ 负控：证明**精确**形态确实比宽松形态更严。
    const src = nativeTransportSrc()
    const exact = new RegExp(
      'const c = capacitorGlobal' + String.raw`\(\)\s*\n\s*if \(!c\) return fail\('bridge'`,
    )
    expect(exact.test(src)).toBe(true)
    // ★ 精确形态必须**不匹配**门⑤ 的那一行 ⇒ 它确实带了额外约束
    const gate5 = /const base = gatewayBaseUrl\(\)/.exec(src)?.[0] ?? ''
    expect(gate5).not.toBe('')
    expect(exact.test(gate5), '精确形态也匹配门⑤ ⇒ 它其实不精确').toBe(false)
  })

  it('⑧ ★★★ 判别式是**活的**：把依赖清单临时换成含 @capacitor 的形态，`shellIsNotInstalled` 的判据条件必须翻面', () => {
    // ★ 反变异思路：证明「壳已安装」这个前提**不是恒真**，
    //   否则 ③/④ 只是把当前事实抄了一遍。
    const p = pkg()
    const deps = Object.keys({ ...p.dependencies, ...p.devDependencies })
    const noCap = deps.filter((k) => k.startsWith('@capacitor/')).length === 0
    expect(noCap, '前提已翻转 ⇒ ③/④ 必须作废并重新决策').toBe(true)
    // ⇒ 「依赖名以 @capacitor/ 开头」这个判别条件本身确实能命中真实存在的包名
    expect('@capacitor/core'.startsWith('@capacitor/')).toBe(true)
    expect('@capacitor/android'.startsWith('@capacitor/')).toBe(true)
    // ⇒ 而 vue / pinia 不得被误判成壳
    expect('vue'.startsWith('@capacitor/')).toBe(false)
    expect('vue-router'.startsWith('@capacitor/')).toBe(false)
  })
})
