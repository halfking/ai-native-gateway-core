import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  NATIVE_GATE_ORDER,
  NativeTransportError,
  createNativeTransport,
  installNativeTransport,
  nativeBodyToText,
  probeNativeTransport,
  reasonPhrase,
} from './nativeTransport'
import { getTransport, fetchTransport, resetTransport, setGatewayBaseUrl, setTransport } from './transport'
import { ApiError, bindAuthContext, isAbortError, req, resetUnauthorizedForTests } from './client'

// ④ 壳内 CapacitorHttp 反代的前端侧安装器（04 §7.2 ④ / 14 §2 能力门）。
//
// 本文件量的是**三处原生与网页的真实差异**（nativeTransport.ts 头 ①②③）+ 五关能力门。
// 断言全部落在「读得到的具体值」上：归一化后的字符串、`statusText` 的确切文案、
// 抛出的错误 `name`/`code`、以及**装没装上**（`getTransport()` 身份），不量散文。

type FakeResponse = { data: unknown; status: number; headers?: Record<string, string>; url?: string }

interface FakeCapacitorOptions {
  /** 缺省给一个「一切正常」的原生侧。 */
  request?: (options: unknown) => Promise<FakeResponse>
  getPlatform?: () => string
  isPluginAvailable?: (name: string) => boolean
  /** 直接给一个非函数，用来测「注册表里名字在、request 不是函数」这一形态。 */
  pluginEntry?: unknown
}

const BASE = 'https://gw.example.com'

/**
 * 装一个**默认健康**的假原生侧。
 * 每道门的用例都只把**自己那一关**弄坏 —— 依赖方保持健康，
 * 这样「转红」才只可能来自被验的那一关。
 */
function installFakeCapacitor(o: FakeCapacitorOptions = {}): void {
  const request = o.request ?? (async () => jsonReply({ status: 'ok' }))
  const entry = 'pluginEntry' in o ? o.pluginEntry : { request }
  const cap = {
    getPlatform: o.getPlatform ?? (() => 'android'),
    ...(o.isPluginAvailable ? { isPluginAvailable: o.isPluginAvailable } : {}),
    Plugins: { CapacitorHttp: entry },
  }
  vi.stubGlobal('Capacitor', cap)
  vi.stubGlobal('location', {
    protocol: 'http:',
    hostname: 'localhost',
    origin: 'http://localhost',
    pathname: '/',
    search: '',
  })
}

function jsonReply(data: unknown, status = 200): FakeResponse {
  // 复刻 Android parseJSON 对 JSON 响应产出的形态：**JS 值，不是字符串**
  return { data, status, headers: { 'content-type': 'application/json' }, url: `${BASE}/x` }
}

describe('④ CapacitorHttp 原生传输安装器', () => {
  beforeEach(() => {
    resetTransport()
    resetUnauthorizedForTests()
    setGatewayBaseUrl(undefined)
    bindAuthContext({ getUserInfo: () => null, getBearer: () => '', isAuthenticated: () => false })
  })
  afterEach(() => {
    resetTransport()
    setGatewayBaseUrl(undefined)
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  // ================= ① JSON 响应无视 responseType 被强制反序列化 =================

  describe('① data 归一化（Android parseJSON 的每个返回分支）', () => {
    it('对象响应经归一化后 req() 拿到的是对象，不是崩在 JSON.parse("[object Object]")', async () => {
      installFakeCapacitor({ request: async () => jsonReply({ ok: 1, rows: [{ id: 7 }] }) })
      setGatewayBaseUrl(BASE)
      setTransport(createNativeTransport())
      await expect(req('GET', '/api/keys')).resolves.toEqual({ ok: 1, rows: [{ id: 7 }] })
    })

    it('数组响应同样归一化（顶层是数组时 JSON.stringify 仍要能 parse 回去）', async () => {
      installFakeCapacitor({ request: async () => jsonReply([1, 2, 3]) })
      setGatewayBaseUrl(BASE)
      setTransport(createNativeTransport())
      await expect(req('GET', '/api/keys')).resolves.toEqual([1, 2, 3])
    })

    it('JSONObject.NULL（JS 侧就是 null）必须回写成 "null"，不能被 client.ts 的 !text 吞成 undefined', async () => {
      installFakeCapacitor({ request: async () => jsonReply(null) })
      setGatewayBaseUrl(BASE)
      setTransport(createNativeTransport())
      const v = await req('GET', '/api/keys')
      expect(v).toBeNull()
      expect(v).not.toBeUndefined()
    })

    it('字符串响应原样透传（readStreamAsString 那条路，不做任何加工）', () => {
      expect(nativeBodyToText('plain')).toBe('plain')
      expect(nativeBodyToText('')).toBe('')
    })

    it('字符串响应若是合法 JSON，req() 仍能 parse 出对象（不与对象分支混淆）', async () => {
      installFakeCapacitor({ request: async () => ({ data: '{"a":1}', status: 200 }) })
      setGatewayBaseUrl(BASE)
      setTransport(createNativeTransport())
      await expect(req('GET', '/api/keys')).resolves.toEqual({ a: 1 })
    })

    it('数字/布尔（parseJSON 的 Integer/Double/boolean 分支）归一化成可 parse 的字面量', () => {
      expect(nativeBodyToText(42)).toBe('42')
      expect(nativeBodyToText(1.5)).toBe('1.5')
      expect(nativeBodyToText(true)).toBe('true')
      expect(nativeBodyToText(false)).toBe('false')
    })

    it('undefined（原生层没有 data 键）归一化成空串', () => {
      expect(nativeBodyToText(undefined)).toBe('')
    })

    it('循环引用对象不炸请求：归一化失败退化成空串而不是抛', () => {
      const cyclic: Record<string, unknown> = {}
      cyclic['self'] = cyclic
      expect(nativeBodyToText(cyclic)).toBe('')
    })
  })

  // ================= ② HttpResponse 没有 statusText =================

  describe('② statusText 补齐', () => {
    it('空 body 的 502：壳内与网页形态给出同一条消息（Bad Gateway），不显示空串', async () => {
      installFakeCapacitor({ request: async () => ({ data: '', status: 502 }) })
      setGatewayBaseUrl(BASE)
      setTransport(createNativeTransport())
      const err = await req('GET', '/api/keys').catch((e: unknown) => e)
      expect(err).toBeInstanceOf(ApiError)
      expect((err as ApiError).status).toBe(502)
      expect((err as ApiError).detail).toBe('Bad Gateway')
    })

    it('401 的 statusText 同样补齐（错误消息优先取 body，body 为空时回落 statusText）', () => {
      expect(reasonPhrase(401)).toBe('Unauthorized')
    })

    it('表外码返回空串，不编一个看起来像观测的短语', () => {
      expect(reasonPhrase(599)).toBe('')
      expect(reasonPhrase(0)).toBe('')
    })
  })

  // ================= ③ 没有 signal：只能 race，不能真取消 =================

  describe('③ AbortSignal 的能力边界', () => {
    it('signal 触发 → 抛 name 为 AbortError 的错误（client.ts 的 isAbortError 认得）', async () => {
      installFakeCapacitor({ request: () => new Promise<FakeResponse>(() => {}) })
      setGatewayBaseUrl(BASE)
      setTransport(createNativeTransport())
      const controller = new AbortController()
      const p = req('GET', '/api/keys', undefined, { signal: controller.signal })
      controller.abort()
      const err = await p.catch((e: unknown) => e)
      expect(err).toBeInstanceOf(Error)
      expect((err as Error).name).toBe('AbortError')
      expect(isAbortError(err)).toBe(true)
    })

    it('已经 aborted 的 signal 直接拒绝，不发请求', async () => {
      const request = vi.fn(async () => jsonReply({ ok: 1 }))
      installFakeCapacitor({ request })
      setGatewayBaseUrl(BASE)
      setTransport(createNativeTransport())
      const controller = new AbortController()
      controller.abort()
      await expect(
        req('GET', '/api/keys', undefined, { signal: controller.signal }),
      ).rejects.toSatisfy(isAbortError)
      expect(request).not.toHaveBeenCalled()
    })

    it('正常 settle 后摘掉 abort 监听器（不漏残挂）', async () => {
      installFakeCapacitor({ request: async () => jsonReply({ ok: 1 }) })
      setGatewayBaseUrl(BASE)
      setTransport(createNativeTransport())
      const controller = new AbortController()
      const remove = vi.spyOn(controller.signal, 'removeEventListener')
      await req('GET', '/api/keys', undefined, { signal: controller.signal })
      expect(remove).toHaveBeenCalledWith('abort', expect.any(Function))
    })

    it('原生请求失败时也摘监听器（reject 是 settle）', async () => {
      installFakeCapacitor({ request: async () => { throw new Error('ECONNREFUSED') } })
      setGatewayBaseUrl(BASE)
      setTransport(createNativeTransport())
      const controller = new AbortController()
      const remove = vi.spyOn(controller.signal, 'removeEventListener')
      await req('GET', '/api/keys', undefined, { signal: controller.signal }).catch(() => undefined)
      expect(remove).toHaveBeenCalledWith('abort', expect.any(Function))
    })
  })

  // ================= 14 §2 五关能力门：每关都能单独证伪 =================

  describe('能力门（14 §2）', () => {
    it('无 window.Capacitor → 卡在 bridge，且不安装（仍是 fetchTransport）', async () => {
      vi.stubGlobal('location', { protocol: 'http:', hostname: 'localhost', pathname: '/', search: '' })
      const r = await installNativeTransport()
      expect(r.gatesOk).toBe(false)
      expect(r.failedAt).toBe('bridge')
      expect(r.installed).toBe(false)
      expect(getTransport()).toBe(fetchTransport)
    })

    it('getPlatform() 返回 web → 卡在 platform，bridge 已过', async () => {
      installFakeCapacitor({ getPlatform: () => 'web' })
      setGatewayBaseUrl(BASE)
      const r = await installNativeTransport()
      expect(r.failedAt).toBe('platform')
      expect(r.passed).toEqual(['bridge'])
      expect(r.installed).toBe(false)
      expect(getTransport()).toBe(fetchTransport)
    })

    it('页面被导航到远端 origin → 卡在 origin（本壳桥只注入 appUrl 的单一 origin）', async () => {
      installFakeCapacitor()
      vi.stubGlobal('location', {
        protocol: 'https:',
        hostname: 'gw.example.com',
        pathname: '/',
        search: '',
      })
      setGatewayBaseUrl(BASE)
      const r = await installNativeTransport()
      expect(r.failedAt).toBe('origin')
      expect(r.passed).toEqual(['bridge', 'platform'])
      expect(r.installed).toBe(false)
    })

    it('allowedHostnames 可以显式放行一个非回环主机（不必改代码）', async () => {
      installFakeCapacitor()
      vi.stubGlobal('location', { protocol: 'https:', hostname: 'gw.example.com', pathname: '/', search: '' })
      setGatewayBaseUrl(BASE)
      const r = await installNativeTransport({ allowedHostnames: ['gw.example.com'] })
      expect(r.failedAt).toBeNull()
      expect(r.installed).toBe(true)
    })

    it('isPluginAvailable 返回 false → 卡在 plugin', async () => {
      installFakeCapacitor({ isPluginAvailable: () => false })
      setGatewayBaseUrl(BASE)
      const r = await installNativeTransport()
      expect(r.failedAt).toBe('plugin')
      expect(r.installed).toBe(false)
    })

    it('注册表里 CapacitorHttp 存在但 request 不是函数 → 同样卡在 plugin（形态级判据）', async () => {
      installFakeCapacitor({ pluginEntry: { request: 'not-a-function' } })
      setGatewayBaseUrl(BASE)
      const r = await installNativeTransport()
      expect(r.failedAt).toBe('plugin')
      expect(r.installed).toBe(false)
    })

    it('未注入网关基址 → 卡在 base_url（④ 之下相对路径必然打不到网关）', async () => {
      installFakeCapacitor()
      const r = await installNativeTransport()
      expect(r.failedAt).toBe('base_url')
      expect(r.installed).toBe(false)
      expect(getTransport()).toBe(fetchTransport)
    })

    it('五关全过 → 装上，且 getTransport() 不再是 fetchTransport', async () => {
      installFakeCapacitor()
      setGatewayBaseUrl(BASE)
      const r = await installNativeTransport()
      expect(r.gatesOk).toBe(true)
      expect(r.failedAt).toBeNull()
      expect(r.installed).toBe(true)
      expect(r.passed).toEqual([...NATIVE_GATE_ORDER])
      expect(getTransport()).not.toBe(fetchTransport)
    })

    it('probeNativeTransport 是只读的：不碰 transport 的当前值', async () => {
      installFakeCapacitor()
      setGatewayBaseUrl(BASE)
      await probeNativeTransport()
      expect(getTransport()).toBe(fetchTransport)
    })
  })

  // ================= 往返只是诊断，不参与安装判定 =================

  describe('往返结果与安装判定的关系（本次设计的关键裁决）', () => {
    it('网关不可达（原生抛异常）仍安装 —— 网关状态不是传输能力', async () => {
      installFakeCapacitor({ request: async () => { throw new Error('ECONNREFUSED') } })
      setGatewayBaseUrl(BASE)
      const r = await installNativeTransport()
      expect(r.gatesOk).toBe(true)
      expect(r.installed).toBe(true)
      expect(r.reachable).toBe(false)
      expect(r.probeError).toContain('ECONNREFUSED')
    })

    it('往返拿到 502：传输是通的（上游挂了），reachable 仍为 true', async () => {
      installFakeCapacitor({ request: async () => ({ data: '', status: 502 }) })
      setGatewayBaseUrl(BASE)
      const r = await installNativeTransport()
      expect(r.installed).toBe(true)
      expect(r.reachable).toBe(true)
      expect(r.probeStatus).toBe(502)
    })

    it('往返超时会结束诊断并清掉计时器（不把安装判定拖死）', async () => {
      vi.useFakeTimers()
      try {
        installFakeCapacitor({ request: () => new Promise<FakeResponse>(() => {}) })
        setGatewayBaseUrl(BASE)
        const p = installNativeTransport({ probeTimeoutMs: 50 })
        await vi.advanceTimersByTimeAsync(60)
        const r = await p
        expect(r.installed).toBe(true)
        expect(r.reachable).toBe(false)
        expect(vi.getTimerCount()).toBe(0)
      } finally {
        vi.useRealTimers()
      }
    })
  })

  // ================= 传输层的 fail-loud =================

  describe('传输层失败必须响亮，不静默回落 fetch', () => {
    it('请求时没有基址 → 传输层抛 NativeTransportError(code=no_base_url)', async () => {
      installFakeCapacitor()
      const t = createNativeTransport()
      const err = await t({ method: 'GET', path: '/api/keys', headers: {} }).catch((e: unknown) => e)
      expect(err).toBeInstanceOf(NativeTransportError)
      expect((err as NativeTransportError).code).toBe('no_base_url')
    })

    // ⚠️ 这里**刻意不断言**「经 req() 之后错误被归一成 ApiError(0, network_error)」。
    //   那条契约的 SSOT 是 `client.spec.ts`（断网 → status 0）——main 上已有，
    //   归一化由 `client.ts` 自己的 try/catch 负责（b3f9698c1 引入）。
    //   在本文件重复断言它 = 把两个独立单元焊在一起：
    //   分支树（169 文件、从未合到 main 的 web-mobile 大改）的 client.ts **没有**那段
    //   try/catch ⇒ 这条断言会在分支上转红，而红的既不是产品也不是本文件被验的语义。
    //   判据只量自己那一关，别替别的单元的契约上锁。

    it('请求途中桥消失（页面被导航走）→ 抛 plugin_unavailable，而不是用旧代理继续打', async () => {
      installFakeCapacitor()
      setGatewayBaseUrl(BASE)
      const t = createNativeTransport()
      vi.stubGlobal('Capacitor', { getPlatform: () => 'android', Plugins: {} })
      const err = await t({
        method: 'GET',
        path: '/api/keys',
        baseUrl: BASE,
        headers: {},
      }).catch((e: unknown) => e)
      expect(err).toBeInstanceOf(NativeTransportError)
      expect((err as NativeTransportError).code).toBe('plugin_unavailable')
    })

    it('相对路径由基址拼成绝对地址后才发给原生侧', async () => {
      // 形参必须声明：否则 vi.fn 推出 calls 为 `[][]`，取 calls[0][0] 直接是 TS 错
      // （`noUnusedParameters` 放行下划线前缀参数）。
      const request = vi.fn(async (_options: unknown) => jsonReply({ ok: 1 }))
      installFakeCapacitor({ request })
      setGatewayBaseUrl('https://gw.example.com/')
      setTransport(createNativeTransport())
      await req('GET', '/api/keys')
      const opts = request.mock.calls[0]![0] as { url: string; method: string; responseType: string }
      expect(opts.url).toBe('https://gw.example.com/api/keys')
      expect(opts.method).toBe('GET')
      expect(opts.responseType).toBe('text')
    })
  })
})
