/**
 * transport.ts — web-mobile 请求传输层接缝（UI规范 10 §4.6.31 / 04 §7.2 ④）。
 *
 * ## 为什么要有这一层
 *
 * `api/client.ts` 的 `req()` 是整个 api 层**唯一**的 `fetch(` 调用点（实测）。
 * ④ 方案要求「页面仍在 `http://localhost`、请求由壳用 `CapacitorHttp` 发出」，
 * 所以**必须在这一处**换传输，而不是在每个 api 模块里改。
 *
 * ## 本文件**不改变任何现行行为**
 *
 * 默认传输就是 `fetch(path, { … })`——与引入本文件之前**逐字相同**
 * （相对路径、同源 credentials、同一份 headers/body/signal）。
 * 因此：
 *   · 网页形态零回归（现有 `client.spec.ts` 用 `vi.stubGlobal('fetch', …)` 全部照过）；
 *   · 壳内原生传输**必须显式安装**（`setTransport`），不自动生效；
 *   · 回退成本 = 一次 `resetTransport()`。
 *
 * ## 红线（沿用 hyper/capabilities.ts）
 *
 * **存在 `window.Capacitor` 不证明 `CapacitorHttp` 可用**。
 * 桥只注入到 `appUrl` 的单一 origin（`Bridge.java:267-269` 的
 * `Collections.singleton(allowedOrigin)`），页面被 `location.replace` 到远端后
 * 桥整个不存在。所以原生传输的安装路径是「**探测到真实响应**」，
 * 不是「看到那个全局对象」。
 */

export interface TransportResponse {
  status: number
  statusText: string
  /** `ok` 由 `status` 派生，语义与 `Response.ok` 一致（200–299）。 */
  readonly ok: boolean
  text(): Promise<string>
}

export interface TransportRequest {
  method: string
  /** 调用方给的路径。web 下是**相对路径**（同源）；壳内由 baseUrl 拼绝对地址。 */
  path: string
  /** 壳内 native transport 用它拼绝对地址；网页形态为 undefined。 */
  baseUrl?: string
  headers: Record<string, string>
  body?: string
  signal?: AbortSignal
  /** 网页形态沿用同源 cookie（17 §5「cookie 优先」）。native 传输不读它。 */
  credentials?: RequestCredentials
}

export type Transport = (req: TransportRequest) => Promise<TransportResponse>

/** 默认传输：与引入本文件之前逐字相同的 `fetch` 调用。 */
export const fetchTransport: Transport = async (r) => {
  const res = await fetch(r.path, {
    method: r.method,
    headers: r.headers,
    credentials: r.credentials ?? 'same-origin',
    body: r.body,
    signal: r.signal,
  })
  return {
    status: res.status,
    statusText: res.statusText,
    ok: res.ok,
    text: () => res.text(),
  }
}

let current: Transport = fetchTransport

export function getTransport(): Transport {
  return current
}

/** 安装传输（壳侧原生传输 / 测试替身）。**显式调用，不自动生效。 */
export function setTransport(t: Transport): void {
  current = t
}

/** 回到网页形态的默认传输。 */
export function resetTransport(): void {
  current = fetchTransport
}

/** 由 status 派生 ok，保持与 `Response.ok`（200–299）同语义。 */
export function deriveOk(status: number): boolean {
  return status >= 200 && status <= 299
}

// ---- 网关基址（仅壳内 native transport 读它） ----

/**
 * 壳内把网关基址交给应用的唯一入口（引导页写 localStorage，壳侧装配时注入到这里）。
 *
 * ⚠️ **这不是凭据**：只含「连哪台机器」的地址，不含账号/令牌/密钥
 * （零凭据红线，19 号与引导页注释同款约束）。
 *
 * 网页形态恒为 `undefined` ⇒ 默认传输走相对路径同源 fetch，行为不变。
 */
let gatewayBase: string | undefined

export function setGatewayBaseUrl(url: string | undefined): void {
  gatewayBase = url
}

export function gatewayBaseUrl(): string | undefined {
  return gatewayBase
}

/** 拼绝对地址；未设基址时返回 null（native transport 据此拒绝发请求，而不是瞎猜）。 */
export function absoluteUrl(path: string, base?: string): string | null {
  const b = base ?? gatewayBase
  if (!b) return null
  if (/^https?:\/\//i.test(path)) return path
  return b.replace(/\/+$/, '') + (path.startsWith('/') ? path : `/${path}`)
}