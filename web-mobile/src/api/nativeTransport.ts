/**
 * nativeTransport.ts — ④「壳内 `CapacitorHttp` 反代」的**前端侧安装器**
 * （UI规范 04 §7.2 ④ 裁决 / 10 §4.6.31 传输层接缝 / 14 §2 能力门）。
 *
 * ## ④ 的形态
 *
 * 引导页 `location.replace` 到业务 origin 这条路会**把桥一起丢掉**：桥的
 * document-start 注入被限定在 `appUrl` 的**单一 origin**（`Bridge.java:267-269`
 * 的 `Collections.singleton(allowedOrigin)`），而 `appUrl` 默认是 `http://localhost`。
 * ④ 因此**不改页面 origin**——页面停在 `http://localhost`，访问网关的请求
 * 改由原生 `HttpURLConnection` 发出（不经过页面 origin ⇒ 不受同源策略约束）。
 *
 * 本文件只做一件事：**把 `CapacitorHttp` 装成 transport.ts 的当前传输**。
 * 装不上就不装，`fetchTransport` 原样留着，网页形态零影响。
 *
 * ## 三处「看着能写、实际会坏」的原生差异（全部读源码，不靠推断）
 *
 * ① **JSON 响应无视 `responseType` 被强制反序列化。**
 *    Android `HttpRequestHandler.readData`（`HttpRequestHandler.java:250-252`）
 *    在无 error stream 且 `Content-Type` 含 `application/json` 时直接
 *    `parseJSON(...)`，**完全不看 `responseType`**（源码原注释 `backward compatibility`）；
 *    error stream + JSON content-type 同样走 `parseJSON`（:244-249）。
 *    web 实现同样：`responseType = 'text'` 会被 content-type 改回 `'json'`
 *    （`@capacitor/core/dist/index.js:510-517`）。
 *    ⇒ `HttpResponse.data` 是 **JS 值**（`parseJSON` :308-336 可返回
 *      `JSONObject.NULL` / boolean / `""` / String / Integer / Double / JSObject / JSArray），
 *      **不是字符串**。而 `client.ts` 末尾是 `JSON.parse(text)`。
 *    不做归一化 ⇒ 每次 API 调用都在 `JSON.parse("[object Object]")` 上炸。
 *
 * ② **`HttpResponse` 没有 `statusText`。**（`core-plugins.d.ts:170-189`：只有
 *    `data` / `status` / `headers` / `url`。）而 `client.ts` 两处消费它：
 *    `errorMessage(r.statusText, …)` 与 `msg = r.statusText` 兜底。
 *    ⇒ 空 body 的 502 在网页形态会显示 "Bad Gateway"，壳内会显示空串。
 *    `reasonPhrase()` 按 IANA 标准码表补齐（**常量查表，不是观测**）；
 *    表外码返回 `''`——与 HTTP/2 下 `Response.statusText` 的空值同形，不编造。
 *
 * ③ **`HttpOptions` 没有 `signal`（`core-plugins.d.ts:104-165`），且原生侧
 *    没有任何逐请求取消通道**：`CapacitorHttp.java` 只暴露
 *    `request/get/post/put/patch/delete`，唯一的 `disconnect()` 在
 *    `handleOnDestroy()`（:42-56）——那是**应用销毁**时清场，不是取消某个请求。
 *    ⇒ abort 只能 **race**（停止 await 并抛 `AbortError`），
 *    原生请求仍会跑完。这是能力边界，不是可以「优化」掉的实现细节。
 *
 * ## 一条必须写进文档的行为差异：原生传输**不带 cookie**
 *
 * 全量 grep：`HttpRequestHandler.java` / `CapacitorHttpUrlConnection.java`
 * 里 `CookieManager` **0 命中**，`setRequestProperty` 只有 3 处
 * （`Content-Type` multipart、`Accept-Language`、`x-cap-user-agent` 转 `User-Agent`）。
 * ⇒ 原生请求**不会**带上 WebView cookie jar，`llmgw_session`（HttpOnly）
 * 在 ④ 下**发不出去**；`client.ts` 的「cookie 优先」实际退化为
 * **Bearer 优先**。这不是本文件的 bug，是 ④ 的既定后果，但调用方必须知道。
 *
 * 顺带：`Origin` 头同样 0 命中 ⇒ CORS 中间件看不到 Origin 语义，
 * 与 04 §7.2 ④ 结论 1「CORS 白名单不再是接入前置」一致。
 *
 * ## 为什么**不静态 import** `@capacitor/core`
 *
 * web-mobile 的 `package.json` 依赖只有 pinia / vue / vue-router（实测），
 * 壳工程才有 `@capacitor/core`。静态 import 会把 Capacitor 拖进**网页形态**
 * 的构建产物，并让网页形态多一个装不上的依赖。这里只按**结构类型**描述
 * 用到的那几个成员，运行时从 `window.Capacitor` 取。
 *
 * ## 门与「安装」的关系（这里刻意分成两件事）
 *
 * - **前 5 关**（bridge / platform / origin / plugin / base_url）判定
 *   **能不能装**。任一不过 ⇒ 不装，保留 `fetchTransport`。
 *   理由：④ 之下页面停在 `http://localhost`，`fetch` 相对路径**必然**打不到网关，
 *   装不上原生传输时回落 `fetch` 是唯一的、也确实无望的退路。
 * - **真实往返（roundtrip）**只做**诊断**，**不参与安装判定**。
 *   理由：它量的是「网关此刻通不通」，那是**网关的状态**，不是**传输的能力**。
 *   把瞬时网关不可达当成「原生传输不可用」而拒绝安装，会让一次网络抖动
 *   把整个 App 永久钉死在更糟的那条路上。报告里 `reachable` 字段如实记读数。
 */

import {
  absoluteUrl,
  deriveOk,
  gatewayBaseUrl,
  setTransport,
  type Transport,
  type TransportResponse,
} from './transport'

// ---- Capacitor 的结构类型（镜像 @capacitor/core@8 真实签名） ----

/** 镜像 `HttpOptions`（`core-plugins.d.ts:104-165`）中本文件用到的成员。 */
interface NativeHttpOptions {
  url: string
  method?: string
  data?: unknown
  headers?: Record<string, string>
  connectTimeout?: number
  readTimeout?: number
  responseType?: 'arraybuffer' | 'blob' | 'json' | 'text' | 'document'
}

/** 镜像 `HttpResponse`（`core-plugins.d.ts:170-189`）——**注意没有 `statusText`**。 */
interface NativeHttpResponse {
  data: unknown
  status: number
  headers: Record<string, string>
  url: string
}

interface CapacitorHttpPlugin {
  request(options: NativeHttpOptions): Promise<NativeHttpResponse>
}

interface CapacitorGlobal {
  getPlatform?: () => string
  isNativePlatform?: () => boolean
  isPluginAvailable?: (name: string) => boolean
  Plugins?: Record<string, unknown>
}

const PLUGIN_NAME = 'CapacitorHttp'

/**
 * 默认允许的页面主机。④ 之下 `appUrl` origin 就是 `http://localhost`
 * （壳仓实测：壳页有 `window.Capacitor`，同 host 换端口就没有）。
 * 非本地 origin 竟然还挂着 Capacitor 全局，那正是本门要拒绝的异常。
 */
const DEFAULT_ALLOWED_HOSTNAMES = ['localhost', '127.0.0.1', '::1', '[::1]']

/** IANA 标准 reason phrase 常量表（`reasonPhrase()` 的唯一数据源）。 */
const REASON_PHRASE: Readonly<Record<number, string>> = {
  200: 'OK',
  201: 'Created',
  202: 'Accepted',
  204: 'No Content',
  205: 'Reset Content',
  206: 'Partial Content',
  301: 'Moved Permanently',
  302: 'Found',
  304: 'Not Modified',
  307: 'Temporary Redirect',
  308: 'Permanent Redirect',
  400: 'Bad Request',
  401: 'Unauthorized',
  403: 'Forbidden',
  404: 'Not Found',
  405: 'Method Not Allowed',
  408: 'Request Timeout',
  409: 'Conflict',
  410: 'Gone',
  413: 'Payload Too Large',
  415: 'Unsupported Media Type',
  422: 'Unprocessable Entity',
  429: 'Too Many Requests',
  500: 'Internal Server Error',
  501: 'Not Implemented',
  502: 'Bad Gateway',
  503: 'Service Unavailable',
  504: 'Gateway Timeout',
}

/**
 * 由 `status` 补 `statusText`（`HttpResponse` 不带，见本文件 ②）。
 *
 * **这是标准常量查表，不是对服务端 reason phrase 的观测**——原生层根本没把
 * 那一行传过来。表外码返回 `''`，与 HTTP/2 下 `Response.statusText` 的空值同形；
 * 宁可空着也不编一个看起来像观测的短语。
 */
export function reasonPhrase(status: number): string {
  return REASON_PHRASE[status] ?? ''
}

/**
 * 把 `HttpResponse.data` 归一成 `text()` 的字符串（见本文件 ①）。
 *
 * 对照 Android `parseJSON`（`HttpRequestHandler.java:308-336`）的每一个返回分支：
 * `JSONObject.NULL` → `null`、`true/false` → boolean、`""`（空 body）→ `''`、
 * 带引号字符串 → 去引号 String、整数/小数 → Integer/Double、其余 → JSObject/JSArray。
 * 字符串原样返回（那条路已经由 `readStreamAsString` 走完）。
 */
export function nativeBodyToText(data: unknown): string {
  if (typeof data === 'string') return data
  if (data === undefined) return ''
  // `JSONObject.NULL` 落到 JS 就是 `null`，对应 body 字面量 `null`——
  // 必须回写成 `'null'` 而不是 `''`，否则 `client.ts` 的 `if (!text) return undefined`
  // 会把一个合法的 null 响应体吞成 undefined。
  if (data === null) return 'null'
  if (typeof data === 'number' || typeof data === 'boolean') return String(data)
  if (typeof data === 'object') {
    // 循环引用 / 抛异常的 getter 在这里会被 JSON.stringify 抛掉；
    // 退化成空串而不是让整个请求失败。
    try {
      const s = JSON.stringify(data)
      return s === undefined ? '' : s
    } catch {
      return ''
    }
  }
  return String(data)
}

/** 传输层抛出的错误；`code` 供调用方分支，**不是用户可见文案**。 */
export class NativeTransportError extends Error {
  code: string
  constructor(code: string, message: string) {
    super(message)
    this.name = 'NativeTransportError'
    this.code = code
  }
}

/**
 * `client.ts` 的 `isAbortError()` 靠 `err.name === 'AbortError'` 判别，
 * 传输层必须产出同名错误，否则 abort 会被当成普通故障弹给用户。
 */
function abortError(): Error {
  const e = new Error('native request aborted by signal')
  e.name = 'AbortError'
  return e
}

/**
 * 把 `signal` 接到一个不可取消的 promise 上（见本文件 ③）。
 *
 * 语义边界：abort 时**停止 await 并抛 `AbortError`**；底层原生请求仍会跑完
 * （`CapacitorHttp` 没有逐请求取消通道）。settle 两条路都摘监听器，不留残挂。
 */
function withAbort<T>(p: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return p
  if (signal.aborted) return Promise.reject(abortError())
  return new Promise<T>((resolve, reject) => {
    const onAbort = () => reject(abortError())
    signal.addEventListener('abort', onAbort, { once: true })
    const detach = () => signal.removeEventListener('abort', onAbort)
    p.then(
      (v) => {
        detach()
        resolve(v)
      },
      (e) => {
        detach()
        reject(e)
      },
    )
  })
}

function capacitorGlobal(): CapacitorGlobal | null {
  if (typeof window === 'undefined') return null
  const c = (window as unknown as { Capacitor?: CapacitorGlobal }).Capacitor
  return c && typeof c === 'object' ? c : null
}

/** 每次请求现取，不缓存安装期的代理：页面一旦被导航走，必须当场失能而不是用旧引用。 */
function httpPluginOrNull(): CapacitorHttpPlugin | null {
  const c = capacitorGlobal()
  if (!c) return null
  const p = c.Plugins?.[PLUGIN_NAME] as CapacitorHttpPlugin | undefined
  return p && typeof p.request === 'function' ? p : null
}

function requirePlugin(): CapacitorHttpPlugin {
  const p = httpPluginOrNull()
  if (!p) {
    throw new NativeTransportError(
      'plugin_unavailable',
      `CapacitorHttp 未注册：原生传输已安装但桥不可用（页面被导航离开 appUrl origin？）`,
    )
  }
  return p
}

// ---- 能力门（14 §2：桥存在 → 协议版本相符 → origin 精确匹配 → 能力注册表非空） ----

export type NativeGateStep = 'bridge' | 'platform' | 'origin' | 'plugin' | 'base_url'

/** 顺序即执行顺序；`failedAt` 之后的关卡**不会**被检查。 */
export const NATIVE_GATE_ORDER: readonly NativeGateStep[] = [
  'bridge',
  'platform',
  'origin',
  'plugin',
  'base_url',
]

export interface NativeTransportOptions {
  /** 诊断用的真实往返路径。默认 `/healthz`——后端免鉴权（`middleware/auth_mw.go:54` 的 `ExactPaths`）。 */
  probePath?: string
  /** 诊断往返的超时（ms）。默认 8000；**到点只结束诊断，不影响安装判定**。 */
  probeTimeoutMs?: number
  /** 传给原生侧的 `connectTimeout` / `readTimeout`（ms）。不给就不设，沿用 Capacitor 默认。 */
  connectTimeoutMs?: number
  readTimeoutMs?: number
  /** 允许的页面主机名。默认只放回环（见 `DEFAULT_ALLOWED_HOSTNAMES`）。 */
  allowedHostnames?: string[]
}

export interface NativeProbeReport {
  /** 前 5 关是否**全部通过**——这才是「能不能装」的判据。 */
  gatesOk: boolean
  /** 第一道没过门的关卡；全过为 `null`。 */
  failedAt: NativeGateStep | null
  /** 机器可读原因码（不含用户可见文案 ⇒ 无需进 i18n 词典）。 */
  reason: string
  /** 已通过的关卡，用于诊断「卡在第几关」。 */
  passed: NativeGateStep[]
  /** 诊断往返的结果。`null` = 未执行（前面已挂）；`false` = 发出去但没拿到 HTTP 响应。 */
  reachable: boolean | null
  /** 诊断往返拿到的状态码（若拿到）。 */
  probeStatus?: number
  /** 诊断往返的失败原因（异常 message），供日志定位。 */
  probeError?: string
}

export interface NativeInstallReport extends NativeProbeReport {
  installed: boolean
}

function platformOf(c: CapacitorGlobal): string | null {
  if (typeof c.getPlatform === 'function') return c.getPlatform()
  // 老版本没有 getPlatform，退回 isNativePlatform 的布尔裁决
  if (typeof c.isNativePlatform === 'function') return c.isNativePlatform() ? 'native' : 'web'
  return null
}

/**
 * 跑 14 §2 的能力门。**不碰 transport 的当前值**——纯只读诊断。
 *
 * 逐关的判据（每关都能单独证伪，见 `nativeTransport.test.ts`）：
 *   bridge   `window.Capacitor` 是对象
 *   platform `getPlatform()` ∈ {android, ios}（无 `getPlatform` 时取 `isNativePlatform()`）
 *   origin   页面 scheme 是 `capacitor:`，或 hostname 在允许集内
 *   plugin   `Capacitor.isPluginAvailable('CapacitorHttp')` 不为 false **且** `request` 是函数
 *   base_url 能用 `setGatewayBaseUrl` 注入的基址拼出绝对地址
 */
export async function probeNativeTransport(
  options: NativeTransportOptions = {},
): Promise<NativeProbeReport> {
  const passed: NativeGateStep[] = []
  const fail = (failedAt: NativeGateStep, reason: string): NativeProbeReport => ({
    gatesOk: false,
    failedAt,
    reason,
    passed,
    reachable: null,
  })

  // ① bridge
  const c = capacitorGlobal()
  if (!c) return fail('bridge', 'window.Capacitor 不存在')
  passed.push('bridge')

  // ② platform（协议版本相符）
  const platform = platformOf(c)
  if (platform !== 'android' && platform !== 'ios' && platform !== 'native') {
    return fail('platform', `getPlatform()=${platform ?? 'null'}，不是原生平台`)
  }
  passed.push('platform')

  // ③ origin 精确匹配
  if (typeof window === 'undefined' || !window.location) {
    return fail('origin', '无 window.location')
  }
  const loc = window.location
  const allowed = new Set(options.allowedHostnames ?? DEFAULT_ALLOWED_HOSTNAMES)
  const localScheme = loc.protocol === 'capacitor:'
  if (!localScheme && !allowed.has(loc.hostname)) {
    return fail('origin', `页面 origin ${loc.protocol}//${loc.hostname} 不在允许集内`)
  }
  passed.push('origin')

  // ④ 能力注册表非空
  if (typeof c.isPluginAvailable === 'function' && !c.isPluginAvailable(PLUGIN_NAME)) {
    return fail('plugin', 'Capacitor.isPluginAvailable("CapacitorHttp") === false')
  }
  const plugin = httpPluginOrNull()
  if (!plugin) return fail('plugin', `Capacitor.Plugins.${PLUGIN_NAME}.request 不是函数`)
  passed.push('plugin')

  // ⑤ base_url：④ 之下页面停在 http://localhost，相对路径必然打不到网关
  const base = gatewayBaseUrl()
  const probePath = options.probePath ?? '/healthz'
  const probeUrl = absoluteUrl(probePath, base)
  if (!probeUrl) {
    return fail('base_url', '未注入网关基址（setGatewayBaseUrl），无法拼出绝对地址')
  }
  passed.push('base_url')

  // ---- 以下是**诊断**，不是安装门（理由见本文件头「门与安装的关系」） ----
  const timeoutMs = options.probeTimeoutMs ?? 8000
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  try {
    const res = await withAbort(plugin.request(nativeOptions(options, 'GET', probeUrl)), controller.signal)
    const status = Number.isFinite(res?.status) ? res.status : 0
    // 任何 100–599 都算「传输打通了」：502 说明上游网关挂了，但**传输本身可用**，
    // 此时回落 fetch 只会更糟（相对路径打不到网关）。异常（拒绝）才是不通。
    const reachable = status >= 100 && status <= 599
    return {
      gatesOk: true,
      failedAt: null,
      reason: 'ok',
      passed,
      reachable,
      probeStatus: status,
      ...(reachable ? {} : { probeError: `状态码 ${status} 不是合法 HTTP 状态` }),
    }
  } catch (err) {
    return {
      gatesOk: true,
      failedAt: null,
      // 网关此刻不通**不撤销**「门通过」这个事实，也不撤销安装。
      reason: 'ok',
      passed,
      reachable: false,
      probeError: err instanceof Error ? err.message : String(err),
    }
  } finally {
    // 诊断的超时计时器必须清掉，否则每次探测都漏一个 timer。
    clearTimeout(timer)
  }
}

/** 一次调用点的原生请求参数。`responseType:'text'` 会被 JSON content-type 覆盖（见 ①）。 */
function nativeOptions(
  options: NativeTransportOptions,
  method: string,
  url: string,
  body?: string,
  headers?: Record<string, string>,
): NativeHttpOptions {
  const out: NativeHttpOptions = { url, method, responseType: 'text' }
  if (body !== undefined) out.data = body
  if (headers) out.headers = headers
  if (options.connectTimeoutMs !== undefined) out.connectTimeout = options.connectTimeoutMs
  if (options.readTimeoutMs !== undefined) out.readTimeout = options.readTimeoutMs
  return out
}

/**
 * 构造原生传输。**不自行安装**——`installNativeTransport` 才做安装。
 *
 * 失败一律以异常出，绝不静默回落 `fetch`：一旦装了原生传输，静默回落会让
 * 「同一个 App 里时好时坏、且随 origin 变化」这类问题无从复现。
 * ④ 之下 fetch 本来也打不到网关，回落没有意义。
 */
export function createNativeTransport(options: NativeTransportOptions = {}): Transport {
  return async (r): Promise<TransportResponse> => {
    const plugin = requirePlugin()
    const url = absoluteUrl(r.path, r.baseUrl)
    if (!url) {
      throw new NativeTransportError(
        'no_base_url',
        `无法为 ${r.path} 拼出绝对地址：未注入网关基址（④ 形态下页面 origin 是 http://localhost）`,
      )
    }
    // ★ 已 abort 的 signal 必须**在发请求之前**拒绝。
    //   `withAbort` 只能「停止 await」，若先把 `plugin.request(...)` 求值出去，
    //   一次已经取消的调用仍然会在原生侧真的跑一趟完整请求。
    //   （这条是被 `nativeTransport.test.ts` 的「已经 aborted 的 signal 直接拒绝」
    //   抓出来的：断言 `request` 未被调用时红了。）
    if (r.signal?.aborted) throw abortError()
    const res = await withAbort(
      plugin.request(nativeOptions(options, r.method, url, r.body, r.headers)),
      r.signal,
    )
    const status = Number.isFinite(res?.status) ? res.status : 0
    return {
      status,
      statusText: reasonPhrase(status),
      ok: deriveOk(status),
      text: async () => nativeBodyToText(res.data),
    }
  }
}

/**
 * 探测 → 门全过则安装。
 *
 * **往返结果不影响 `installed`**：往返量的是网关此刻通不通，通不通是网关的状态，
 * 不是传输的能力。让一次瞬时不可达撤销安装，等于把 App 永久钉在必然失败的
 * `fetch` 上。`reachable` 字段如实带回读数，调用方自行决定要不要提示用户。
 *
 * 返回而不抛：安装失败是**可预期**的降级，不是异常。
 */
export async function installNativeTransport(
  options: NativeTransportOptions = {},
): Promise<NativeInstallReport> {
  const report = await probeNativeTransport(options)
  if (!report.gatesOk) return { ...report, installed: false }
  setTransport(createNativeTransport(options))
  return { ...report, installed: true }
}
