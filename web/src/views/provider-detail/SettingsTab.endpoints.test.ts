import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

// SettingsTab 端点表判据（2026-11-28 L148，方案 A4）
//
// 钉五条契约，每条都对应一个**会让后端守卫在界面上失效**的形态：
//
//   1. 三个 HTTP 动词必须都接上（GET 列表 / PUT 保存 / DELETE 删除）。
//      只接 GET ⇒ 页面能看不能改，A4 的后端等于没有入口。
//
//   2. 新增行的协议下拉必须**排除已配置的协议**。同协议再 PUT 在后端是
//      「更新」而非「新建」，运营会以为多了一条端点。
//
//   3. 首条端点默认勾选「主端点」。不勾 ⇒ 供应商落到「零主端点」，
//      而选择器 Stage 4 的回落语义依赖有主端点。
//
//   4. 保存失败必须**保留编辑态**。409（主端点保护）要运营改完再试，
//      清空编辑态等于让他重填一遍。
//
//   5. 协议枚举必须与后端表 CHECK 一致（不含 adaptive 选择器）。
//      TS 里多一个 adaptive ⇒ 每次新增都 400，而后端单测全绿。

function resolveRepoFile(...parts: string[]): Promise<string> {
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
const endpointsApi = () => resolveRepoFile('web/src/api/provider-endpoints.ts')
const goEndpoints = () => resolveRepoFile('admin/provider_endpoints.go')
const tableSql = () => resolveRepoFile('sql/objects/tables/provider_endpoint_protocols.sql')

describe('SettingsTab 端点表', () => {
  it('三个动词都接上（GET / PUT / DELETE）', async () => {
    const src = await settingsTab()
    expect(src, '缺 GET：页面只能显示不能读').toContain('getProviderEndpoints(')
    expect(src, '缺 PUT：无法新建/更新').toContain('putProviderEndpoint(')
    expect(src, '缺 DELETE：无法删除').toContain('deleteProviderEndpoint(')
    // 三个都从同一模块导入，防「只接了其中两个」的半条链
    expect(src).toContain('} from \'../../api\'')
  })

  it('新增行协议下拉排除已配置的协议', async () => {
    const src = await settingsTab()
    // 新增行下拉用 availableEndpointProtocols（computed）
    expect(
      src,
      '新增行必须用 availableEndpointProtocols（过滤已用协议）',
    ).toContain('v-for="p in availableEndpointProtocols"')
    // computed 定义里必须有 filter 表达式
    const computedIdx = src.indexOf('const availableEndpointProtocols = computed(')
    expect(computedIdx, '找不到 availableEndpointProtocols computed').toBeGreaterThan(-1)
    const computedSection = src.slice(computedIdx, computedIdx + 400)
    expect(
      computedSection,
      '必须过滤已配置的协议：!endpoints.value.some((e) => e.protocol === p)',
    ).toMatch(/!endpoints\.value\.some\(\(e\) => e\.protocol === p\)/)
  })

  it('首条端点默认勾选为主端点', async () => {
    const src = await settingsTab()
    const idx = src.indexOf('function startAddEndpoint()')
    expect(idx, '找不到 startAddEndpoint').toBeGreaterThan(-1)
    const fn = src.slice(idx, idx + 900)
    expect(
      fn,
      'empty⇒primary：首条端点必须是主端点，否则供应商落到「零主端点」',
    ).toMatch(/editEndpointPrimary\.value\s*=\s*endpoints\.value\.length === 0/)
  })

  it('保存失败保留编辑态（409 后可以改完再试）', async () => {
    const src = await settingsTab()
    const idx = src.indexOf('async function saveEndpoint()')
    expect(idx, '找不到 saveEndpoint').toBeGreaterThan(-1)
    const fn = src.slice(idx, idx + 1200)
    // 成功才清 editingEndpointId：catch 分支里不得出现
    const catchIdx = fn.indexOf('} catch')
    expect(catchIdx).toBeGreaterThan(-1)
    const catchBlock = fn.slice(catchIdx, catchIdx + 400)
    expect(
      catchBlock,
      'catch 分支不得清空 editingEndpointId，否则 409 后已填内容全丢',
    ).not.toContain('editingEndpointId.value = null')
  })

  it('协议枚举与后端表 CHECK 一致，且不含 adaptive', async () => {
    const ts = await settingsTab()
    const go = await goEndpoints()
    const sql = await tableSql()

    // TS 侧选项来源：endpointProtocolChoices（端点表专用，5 个协议）
    const tsChoices = ts.match(/const endpointProtocolChoices = \[([\s\S]*?)\] as const/)?.[1]
    expect(tsChoices, 'TS 侧 endpointProtocolChoices 未找到').toBeDefined()
    const tsList = [...tsChoices!.matchAll(/'([a-z-]+)'/g)].map((m) => m[1])

    // Go 侧：providerEndpointProtocols 数组是常量引用（providercatalog.ProtocolXxx），
    // 不是字面量。把常量名映射回线格式串，再与 TS 侧对拍。
    const goArray = go.match(/var providerEndpointProtocols = \[\]string\{([\s\S]*?)\n\}/)?.[1]
    expect(goArray, 'Go 侧 providerEndpointProtocols 数组未找到').toBeDefined()
    const constToWire: Record<string, string> = {
      ProtocolOpenAICompletions: 'openai-completions',
      ProtocolOpenAIResponses: 'openai-responses',
      ProtocolAnthropicMessages: 'anthropic-messages',
      ProtocolGeminiGenerate: 'gemini-generate',
      ProtocolOllamaNative: 'ollama-native',
    }
    const goConsts = [...goArray!.matchAll(/providercatalog\.(Protocol\w+)/g)].map((m) => m[1])
    expect(goConsts.length, 'Go 侧 providerEndpointProtocols 项数异常').toBe(5)
    const goList = goConsts.map((c) => {
      const wire = constToWire[c]
      expect(wire, `Go 侧出现未登记的协议常量 ${c}：请同步本判据的映射表`).toBeDefined()
      return wire
    })

    // SQL 侧 CHECK
    expect(sql).toContain('provider_endpoint_protocols_protocol_check')

    // TS 与 Go 必须完全相等（端点表含 ollama-native，不含 adaptive）
    expect(tsList.length, 'TS 侧协议项数必须是 5').toBe(5)
    expect(
      tsList.sort(),
      'TS endpointProtocolChoices 必须与 Go providerEndpointProtocols 逐字相等',
    ).toEqual(goList.sort())
    expect(
      tsList,
      'adaptive 是**选择器**不是线格式，端点表必须拒绝它',
    ).not.toContain('adaptive')
  })
})
