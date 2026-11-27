import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

// SettingsTab「协议自适应」面判据（2026-11-28 L141）
//
// 钉四条契约。每一条都对应一个**真实发生过的**缺陷形态，不是补格式：
//
//   1. 协议下拉必须能选到 adaptive。缺了这条 ⇒ 后端把 adaptive 实现得再对，
//      运营在页面上也选不到（功能不可达）。
//
//   2. 「未声明可用协议」的破坏性提示，必须同时由【协议=adaptive】与
//      【adaptiveAllowed===null】两个条件守。★ 本文件写之前它只由前者守 ⇒
//      声明完清单后仍显示「尚未声明」，文案与事实相反（已实测为真缺陷）。
//
//   3. 跨语言键一致：TS 侧的设置键必须与 Go 侧白名单键逐字相同。
//      漂了 ⇒ 每次保存都 400，而两侧各自的单测都绿（各测各的常量）。
//
//   4. 协议下拉不得再出现 google-gemini —— 服务端把它归一成 gemini-generate，
//      两项并存 ⇒ 选任一项落库相同值，页面显示却不同（A7）。

function resolveRepoFile(...parts: string[]): Promise<string> {
  // 测试可能以 web/ 或仓库根为 cwd 运行
  const roots = [process.cwd(), resolve(process.cwd(), '..')]
  return readFirst(roots.map((root) => resolve(root, ...parts)))
}

async function readFirst(paths: string[]): Promise<string> {
  const failures: string[] = []
  for (const path of paths) {
    try {
      return await readFile(path, 'utf8')
    } catch (err) {
      failures.push(`${path}: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
  throw new Error(`都读不到：\n${failures.join('\n')}`)
}

const settingsTab = () => resolveRepoFile('web/src/views/provider-detail/SettingsTab.vue')
const zhCnLocale = () => resolveRepoFile('web/src/locales/zh-CN/providerDetail.ts')
const goCatalog = () => resolveRepoFile('provider/catalog/adaptive_protocol.go')

describe('SettingsTab 协议自适应', () => {
  it('协议下拉里有 adaptive 选项', async () => {
    const src = await settingsTab()
    expect(src).toMatch(/<option value="adaptive">/)
  })

  it('未声明提示只在「自适应 ∧ 未声明」时渲染', async () => {
    const src = await settingsTab()
    // 找到渲染破坏性提示的那个 div，取其 v-if 条件
    const idx = src.indexOf("ps('protocolAdaptiveNeedsAllowed')")
    expect(idx, '找不到 protocolAdaptiveNeedsAllowed 的渲染点').toBeGreaterThan(-1)
    const before = src.slice(Math.max(0, idx - 400), idx)
    const vIf = before.match(/v-if="([^"]+)"/g)
    expect(vIf, '提示 div 上没有 v-if').not.toBeNull()
    const cond = vIf![vIf!.length - 1]
    expect(cond, '提示必须同时受「协议=adaptive」约束').toContain("editProtocol === 'adaptive'")
    expect(cond, '提示必须同时受「未声明」约束，否则声明后仍在说「尚未声明」').toContain(
      'adaptiveAllowed === null',
    )
  })

  it('提示不再声称「行为不变」（对非 completions 供应商保存即改变行为）', async () => {
    const src = await zhCnLocale()
    expect(src).not.toContain('与启用前行为一致')
    expect(src).toContain('保存后行为会随之改变')
  })

  it('TS 设置键与 Go 侧 AdaptiveAllowedSettingKey 逐字一致', async () => {
    const ts = await settingsTab()
    const go = await goCatalog()
    const tsKey = ts.match(/const ADAPTIVE_SETTING_KEY = '([^']+)'/)?.[1]
    const goKey = go.match(/const AdaptiveAllowedSettingKey = "([^"]+)"/)?.[1]
    expect(tsKey, 'TS 侧 ADAPTIVE_SETTING_KEY 未找到').toBeDefined()
    expect(goKey, 'Go 侧 AdaptiveAllowedSettingKey 未找到').toBeDefined()
    expect(tsKey).toBe(goKey)
  })

  it('协议下拉不再出现 google-gemini 重复项', async () => {
    const src = await settingsTab()
    expect(src).not.toContain('<option value="google-gemini">')
  })
})
