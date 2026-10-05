// exportResponse.ts — 受控取流：导出前把文件体**限死在堆里**（docs/UI规范 19 §4 导入/导出契约，10 §4.6.18）。
//
// ## 为什么不能用「先读 Content-Length 再设上限」
//
// 参考仓 19 §4.3 的 C3 判据是「先读 Content-Length 并设上限」。
// **本仓 4 个导出端点里 3 个拿不到该头**——它们是 `csv.NewWriter(w)` 流式写出，
// Go 在 body 超过 `net/http` 的 `bufferBeforeChunkingSize`（**2048 字节**，源码
// `server.go:342`）后转 `Transfer-Encoding: chunked`，此时长度不可知
// （实测二分定位 2048/2049 分界，详见 10 §4.6.18）。
// ⇒ 照抄那条判据会得到一条**恒绿判据**：分支永远进不去，门永远 PASS。
//
// ## 本实现的形态：对两种端点都成立
//
//   1. 响应头**已声明长度** ⇒ 读 `Content-Length`，超限**在读任何字节前**就拒绝；
//   2. 响应头**未声明**（chunked）⇒ `getReader()` 边读边累加，超限即 `reader.cancel()`。
//
// 上限值只有一个权威源 `DEFAULT_MAX_FILE_BYTES`（19 §4.3b C5 的纪律），
// 业务代码不得另写字面量——门禁 C5 盯这条。
//
// ## 为什么 `exportFile` 的签名不变
//
// 降级链 ①分享面 / ②壳桥 / ③blob 全部以 `Blob` 为输入，且已有 8 条判据
// （`exportFile.spec.ts`）锁住桌面零回归。改签名会让那 8 条全红，
// 而本条契约不需要它们变红 ⇒ **新增前置守卫，不动既有接口**。
import { DEFAULT_MAX_FILE_BYTES } from './exportFile'

/** 导出体超过上限（用户被自己的旧上限拦住前就已被拦下）。 */
export class ExportTooLargeError extends Error {
  /** 实际观测到的字节数；超限被 `cancel()` 提前掐断时为**下界**（已读到此量）。 */
  readonly observed: number
  /** 生效的上限。 */
  readonly limit: number

  constructor(observed: number, limit: number) {
    super(`export body exceeds limit: at least ${observed} bytes, limit ${limit}`)
    this.name = 'ExportTooLargeError'
    this.observed = observed
    this.limit = limit
  }
}

/**
 * 把 Response 读成一个**受上限约束**的 Blob。
 *
 * @param res      已判定 `res.ok` 的响应
 * @param limit    字节上限；省略时取 `DEFAULT_MAX_FILE_BYTES`（唯一权威源）
 * @throws ExportTooLargeError  超限时抛出；**已通过 `reader.cancel()` 掐断传输**，
 *         所以不会把整个大文件读进 WebView 堆——这正是 C3 要防的 OOM 面。
 */
export async function readExportBlob(res: Response, limit = DEFAULT_MAX_FILE_BYTES): Promise<Blob> {
  // 形态 1：长度已知 ⇒ 读之前就能判。这是唯一能「零字节拒绝」的路径。
  const declared = res.headers.get('Content-Length')
  if (declared !== null) {
    const n = Number(declared)
    // ⚠️ 判据 T5 抓到过这里两个真实陷阱（早期版本都栽过）：
    //   · `Content-Length: ''` ⇒ Number('') === **0** ⇒ `0 > limit` 为假 ⇒ 误放行；
    //   · `Content-Length: not-a-number` ⇒ **NaN** ⇒ `NaN > limit` **恒为 false** ⇒ 误放行。
    // 两者都会**静默退化成「长度不可知」**，让预判分支形同虚设。
    // 处置：无法解析出可信长度时，**不假装它小**，而是当作「不可知」交给形态 2 边读边计数
    //（那条路径是 OOM 安全的），并在此显式记一笔，避免下次又被「优化」掉。
    if (Number.isFinite(n) && n > limit) {
      throw new ExportTooLargeError(n, limit)
    }
  }

  // 形态 2：长度未知（chunked）⇒ 边读边计数。reader 在超限时被 cancel。
  if (typeof res.body?.getReader !== 'function') {
    // 老宿主无 ReadableStream ⇒ 只能退回整体读。
    // ⚠️ 这是**降级**，不是等效：此时确实无法限流。C1 门禁登记了这个缺口。
    const buf = await res.arrayBuffer()
    if (buf.byteLength > limit) throw new ExportTooLargeError(buf.byteLength, limit)
    return new Blob([buf], { type: res.headers.get('Content-Type') || 'application/octet-stream' })
  }

  const reader = res.body.getReader()
  const chunks: Uint8Array[] = []
  let total = 0
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    if (value) {
      total += value.byteLength
      if (total > limit) {
        // 关键：先掐断再抛。不掐断的话 cancel 不会被调用，
        // 传输继续、堆继续涨——判据看着对，防的却没防住。
        await reader.cancel().catch(() => {})
        throw new ExportTooLargeError(total, limit)
      }
      chunks.push(value)
    }
  }
  return new Blob(chunks as BlobPart[], {
    type: res.headers.get('Content-Type') || 'application/octet-stream',
  })
}
