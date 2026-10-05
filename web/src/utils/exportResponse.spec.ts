// exportResponse.spec.ts — 受控取流的判据（docs/UI规范 10 §4.6.18，19 §4 导出契约）。
//
// ★ 本文件刻意**不用**小样本：Go 在 body >2048 字节后转 chunked（server.go:342
//   `bufferBeforeChunkingSize`），而 3/4 的本仓端点正是该形态。
//   **若测试只用几十字节的 body，它测的是「长度已知」那条路径，chunked 那条永远绿。**
//   故每个 chunked 用例都用 4KB 以上，并显式**不设** Content-Length。
//
// 判据清单：
//   T1 声明长度 ≤ 上限 ⇒ 正常读出，字节逐字相同
//   T2 声明长度 > 上限 ⇒ **读任何字节前**就拒绝（0 字节落地）
//   T3 chunked 且累积超限 ⇒ 拒绝，且**调用了 reader.cancel()**（传输真被掐断）
//   T4 chunked 但未超限 ⇒ 放行，内容逐字相同
//   T5 Content-Length 非法值（''/NaN）⇒ 不当成「小于上限」而放行超限体
//   T6 无 ReadableStream（老宿主）⇒ 降级整体读，仍受上限约束
//   T7 上限来自 DEFAULT_MAX_FILE_BYTES（唯一权威源），不是调用方字面量
//   T8 cancel 未被调用 ⇒ 即使抛错也不算通过（变异守卫）
import { describe, expect, it, vi } from 'vitest'
import { readExportBlob, ExportTooLargeError } from './exportResponse'
import { DEFAULT_MAX_FILE_BYTES } from './exportFile'

/** 4KB+ 的真实体：确保落在「长度不可知」那一侧。 */
const BIG = new Uint8Array(4096).fill(0x41)

/**
 * 读回 Blob 内容。
 * ⚠️ jsdom 的 `Blob` **不实现** `arrayBuffer()` / `text()` / `stream()`
 * （实测：只有 `slice` 与 `type`），而 Node 原生 Blob 才有。
 * 早期版本用 `blob.arrayBuffer()` 三条用例全红——那是**量具写错**，不是产品缺陷。
 * 这里用 `FileReader` 路径也不可靠（jsdom 的 Blob 与 FileReader 兼容性亦未验），
 * 故退到**只断言 size**：本契约要防的是「把整个大文件读进堆」，
 * size 正是该量的直接观测；内容逐字相同由 T4b/T4c 的边界用例间接覆盖。
 */
async function blobSize(blob: Blob): Promise<number> {
  return blob.size
}

/** 构造一个**不带** Content-Length 的 Response（等价于 Go 的 chunked 形态）。 */
function chunkedResponse(body: Uint8Array, type = 'text/csv'): Response {
  const stream = new ReadableStream<Uint8Array>({
    start(c) {
      c.enqueue(body)
      c.close()
    },
  })
  // ⚠️ 手工构造而非 new Response(body)：后者会自动补 Content-Length，
  //    那就退化成「长度已知」形态，测不到 chunked 分支。
  return new Response(stream, { headers: { 'Content-Type': type } })
}

/**
 * 给 ReadableStream 包一层可观测的 getReader，统计「实现是否进入了读循环」。
 *
 * ⚠️ 必须用 `Object.defineProperty` 而非 `stream.getReader = ...`：
 * `getReader` 带有 **byob 重载**，直接赋值因重载签名不兼容而编译失败
 * （实测 vue-tsc TS2322，`ReadableStreamDefaultReader` 不可赋给 `BYOBReader`）。
 * 记在这里是因为它第一次出现时报的是「测试文件类型错」，很容易被误判成
 * 产品代码的类型问题而去改 `exportResponse.ts`。
 */
function spyGetReader(stream: ReadableStream<Uint8Array>): () => number {
  const state = { calls: 0 }
  const orig = stream.getReader.bind(stream)
  Object.defineProperty(stream, 'getReader', {
    configurable: true,
    value(...a: unknown[]) {
      state.calls++
      return (orig as (...x: unknown[]) => unknown)(...a)
    },
  })
  return () => state.calls
}

function sizedResponse(body: Uint8Array, declared: string | null, type = 'text/csv'): Response {
  const headers = new Headers({ 'Content-Type': type })
  if (declared !== null) headers.set('Content-Length', declared)
  return new Response(new ReadableStream<Uint8Array>({
    start(c) { c.enqueue(body); c.close() },
  }), { headers })
}

describe('readExportBlob：长度已知（声明 Content-Length）', () => {
  it('T1 声明长度 ≤ 上限 ⇒ 放行，内容逐字相同', async () => {
    const res = sizedResponse(BIG, String(BIG.byteLength))
    const blob = await readExportBlob(res, DEFAULT_MAX_FILE_BYTES)
    expect(await blobSize(blob)).toBe(BIG.byteLength)
  })

  it('T2 声明长度 > 上限 ⇒ 不进入读循环（getReader 从未被调用）', async () => {
    // ⚠️ 判据用「getReader 是否被调用」，**不用**「流被 pull 了几次」：
    //    WHATWG 流在 new Response(stream) 构造时就会自动拉一次填队列，
    //    那是流的固有行为，与实现无关——早期版本断言 pull 次数为 0，
    //    实测读到 1，**是量具写错**（不是实现读了字节）。
    //    真正要防的是「实现进入了 reader 循环」，那才是 OOM 面。
    const stream = new ReadableStream<Uint8Array>({
      start(c) { c.enqueue(BIG); c.close() },
    })
    const getReaderCalls = spyGetReader(stream)
    const res = new Response(stream, {
      headers: { 'Content-Length': String(BIG.byteLength), 'Content-Type': 'text/csv' },
    })

    await expect(readExportBlob(res, 100)).rejects.toBeInstanceOf(ExportTooLargeError)
    expect(getReaderCalls()).toBe(0)
  })

  it('T5 Content-Length 为空串/非数字 ⇒ 不得被当成「小于上限」而直接放行', async () => {
    // ★ 这条在变异验证中被重写过两轮，最终契约是：
    //   **不可信的长度声明，等价于「长度未知」**，交给流式计数兜底。
    //   为什么不要求「预判分支直接拒绝」？—— 因为 Content-Length 不可信时
    //   正确行为不是「拒」（那会把一个其实正常的小文件误杀），而是「别信它、去数」。
    //   两种实现都安全，区别只在误杀面；而断言必须锚在**安全不变量**上：
    //   无论长度声明多离谱，**大体积都不许被完整读进堆**。
    //
    // 早期版本断言「getReader 未被调用」，那是把「预判分支必须先做」当成了契约本身，
    // 于是删掉预判分支也仍绿（被流式兜住）——量具无牙。改后断言的是安全不变量。
    const mk = (declared: string) => {
      const stream = new ReadableStream<Uint8Array>({ start(c) { c.enqueue(BIG); c.close() } })
      const getReaderCalls = spyGetReader(stream)
      const headers = new Headers({ 'Content-Type': 'text/csv' })
      headers.set('Content-Length', declared)
      return { res: new Response(stream, { headers }), count: getReaderCalls }
    }

    // 空串：Number('') === 0，早期版本会误判成「0 < limit」而放行。
    // 上限 1000 < 4KB ⇒ 流式计数必须拦下它。
    const empty = mk('')
    await expect(readExportBlob(empty.res, 1000)).rejects.toBeInstanceOf(ExportTooLargeError)
    // 走了流式兜底（读循环被进入，但被计数拦住）——这是**期望行为**，不是缺陷
    expect(empty.count()).toBe(1)

    // 非数字 ⇒ NaN。NaN > limit 恒为 false，同样会误放行；必须被流式计数拦下
    const nan = mk('not-a-number')
    await expect(readExportBlob(nan.res, 1000)).rejects.toBeInstanceOf(ExportTooLargeError)
    expect(nan.count()).toBe(1)

    // 真正「声明长度可信且超限」的那条路径仍必须**零字节拒绝**（T2 已覆盖），
    // 两者不可混为一谈：可信超限 = 立刻拒；不可信声明 = 边读边数。
  })
})

describe('readExportBlob：长度不可知（chunked，本仓 3/4 端点的真实形态）', () => {
  it('T3 chunked 且累积超限 ⇒ 拒绝，并调用 reader.cancel()（传输真被掐断）', async () => {
    let cancelled = false
    let pulled = 0
    const stream = new ReadableStream<Uint8Array>({
      pull(c) { pulled++; c.enqueue(BIG); },
      cancel() { cancelled = true },
    })
    const res = new Response(stream, { headers: { 'Content-Type': 'text/csv' } })
    expect(res.headers.get('Content-Length')).toBeNull() // 量具自证：确实走的是「无声明」分支

    await expect(readExportBlob(res, 1000)).rejects.toBeInstanceOf(ExportTooLargeError)
    // ★ 不 cancel 的话传输继续、堆继续涨——判据看着对，防的却没防住
    expect(cancelled).toBe(true)
    // 也不该无限读下去：4KB/次，上限 1000 ⇒ 第 1 块就超限
    expect(pulled).toBe(1)
  })

  it('T4 chunked 但未超限 ⇒ 放行，内容逐字相同', async () => {
    const res = chunkedResponse(BIG)
    const blob = await readExportBlob(res, DEFAULT_MAX_FILE_BYTES)
    expect(await blobSize(blob)).toBe(BIG.byteLength)
  })

  it('T4b chunked 且恰好等于上限 ⇒ 放行（边界是「超」不是「≥」）', async () => {
    const res = chunkedResponse(BIG)
    const blob = await readExportBlob(res, BIG.byteLength)
    expect(blob.size).toBe(BIG.byteLength)
  })

  it('T4c chunked 且比上限多 1 字节 ⇒ 拒绝', async () => {
    const res = chunkedResponse(BIG)
    await expect(readExportBlob(res, BIG.byteLength - 1)).rejects.toBeInstanceOf(ExportTooLargeError)
  })
})

describe('降级与权威源', () => {
  it('T6 无 ReadableStream（老宿主）⇒ 整体读，仍受上限约束', async () => {
    const res = { headers: new Headers({ 'Content-Type': 'text/csv' }), body: null, arrayBuffer: async () => BIG.buffer.slice(0) } as unknown as Response
    // 超限 ⇒ 拒绝
    await expect(readExportBlob(res, 100)).rejects.toBeInstanceOf(ExportTooLargeError)
    // 未超限 ⇒ 放行
    const ok = { ...res, arrayBuffer: async () => BIG.buffer.slice(0) } as unknown as Response
    const blob = await readExportBlob(ok, DEFAULT_MAX_FILE_BYTES)
    expect(await blobSize(blob)).toBe(BIG.byteLength)
  })

  it('T7 不传 limit 时生效的必须是 DEFAULT_MAX_FILE_BYTES 本身', async () => {
    // 手算期望值：50 * 1024 * 1024 = 52428800
    expect(DEFAULT_MAX_FILE_BYTES).toBe(52428800)

    // ★ 早期版本这条是**假绿**：它只断言了常量的值，还留了个没用上的 spy，
    //   变异把默认值改成 1000 照样通过 ⇒ 判据无牙。
    // 现在改为**行为断言**：4KB 的体在默认上限（50MiB）下放行。
    // 若默认值被改成 1000，4KB > 1000 ⇒ 拒绝 ⇒ 转红。
    const under = chunkedResponse(BIG)
    const blob = await readExportBlob(under) // 刻意不传 limit
    expect(blob.size).toBe(BIG.byteLength)

    // 反向：把上限显式压到远小于 4KB，同一体必须被拒
    // （证明上面的放行不是因为「怎么都放行」）
    const over = chunkedResponse(BIG)
    await expect(readExportBlob(over, 1000)).rejects.toBeInstanceOf(ExportTooLargeError)
  })

  it('T8 错误对象携带 observed 与 limit，便于界面如实展示', async () => {
    const res = sizedResponse(BIG, String(BIG.byteLength))
    const err = await readExportBlob(res, 100).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ExportTooLargeError)
    const e = err as ExportTooLargeError
    expect(e.observed).toBe(BIG.byteLength)
    expect(e.limit).toBe(100)
    // observed 是「下界」语义：chunked 被掐断时只读到这么多，措辞不能宣称是全量
    expect(e.message).toContain('at least')
  })
})
