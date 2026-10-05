// deploySeq.spec.ts — 部署序号纯逻辑判据（UI规范 18 §3/§4 的门禁形态）。
// 变异对照（每条判据都要能杀掉一类错误实现）：
//   - 「远端读不到 ⇒ 一致」是第三禁，relateSeq 必须杀掉它；
//   - no-store 缺失会让检查读到旧序号（18 §3 末段）；
//   - 自动重载三条件缺一即降级提示条（18 §4）。
import { describe, expect, it } from 'vitest'
import {
  DEPLOY_SEQ_META,
  elementBlocksAutoReload,
  parseDeploySeq,
  readLocalDeploySeq,
  relateSeq,
  shouldAutoReload,
  UPDATE_FIRST_CHECK_DELAY_MS,
  UPDATE_IDLE_THRESHOLD_MS,
  UPDATE_POLL_MS,
} from './deploySeq'

function docWithMetas(seq: string | null, src?: string): Document {
  const html = [
    '<!doctype html><html><head>',
    seq === null ? '' : `<meta name="${DEPLOY_SEQ_META}" content="${seq}">`,
    seq === null || src === undefined ? '' : `<meta name="llmgw:deploy-seq-src" content="${src}">`,
    '</head><body></body></html>',
  ].join('')
  return new DOMParser().parseFromString(html, 'text/html')
}

describe('readLocalDeploySeq / parseDeploySeq', () => {
  it('读本页 meta：seq 与 src 都取到', () => {
    const doc = docWithMetas('2450', 'build-seq')
    expect(readLocalDeploySeq(doc)).toEqual({ seq: '2450', src: 'build-seq' })
  })

  it('src 缺失时落 unknown，不抛异常', () => {
    const doc = docWithMetas('abc12345')
    expect(readLocalDeploySeq(doc)).toEqual({ seq: 'abc12345', src: 'unknown' })
  })

  it('本页没有 seq meta ⇒ null（调用方必须按不可判定处理）', () => {
    expect(readLocalDeploySeq(docWithMetas(null))).toBeNull()
  })

  it('远端 HTML 解析：取同名 meta', () => {
    const html = `<html><head><meta name="${DEPLOY_SEQ_META}" content="2451"></head></html>`
    expect(parseDeploySeq(html)).toBe('2451')
  })

  it('远端 HTML 无 meta ⇒ null（服务端旧构建）', () => {
    expect(parseDeploySeq('<html><head></head></html>')).toBeNull()
  })
})

describe('relateSeq — 三条禁止的落点', () => {
  it('相等 ⇒ latest', () => {
    expect(relateSeq('2450', '2450')).toBe('latest')
  })

  it('不等 ⇒ available', () => {
    expect(relateSeq('2450', '2451')).toBe('available')
  })

  it('远端读不到 ⇒ indeterminate，绝不折叠成 latest（第三禁）', () => {
    expect(relateSeq('2450', null)).toBe('indeterminate')
  })

  it('本页读不到 ⇒ indeterminate', () => {
    expect(relateSeq(null, '2451')).toBe('indeterminate')
  })

  it('两侧都读不到 ⇒ indeterminate', () => {
    expect(relateSeq(null, null)).toBe('indeterminate')
  })
})

describe('自动重载三条件（18 §4）', () => {
  it('默认阈值与轮询周期按规范取值（5 分钟轮询 / 首查晚于首屏）', () => {
    expect(UPDATE_POLL_MS).toBe(5 * 60_000)
    expect(UPDATE_FIRST_CHECK_DELAY_MS).toBeGreaterThanOrEqual(3_000)
    expect(UPDATE_IDLE_THRESHOLD_MS).toBeGreaterThanOrEqual(10_000)
  })

  it('静置足够久且无输入焦点 ⇒ 允许自动重载', () => {
    expect(shouldAutoReload({ hasInputFocus: false, msSinceInteraction: 60_000 })).toBe(true)
  })

  it('焦点在输入控件 ⇒ 拒绝（哪怕静置再久）', () => {
    expect(shouldAutoReload({ hasInputFocus: true, msSinceInteraction: 600_000 })).toBe(false)
  })

  it('距上次交互未过阈值 ⇒ 拒绝', () => {
    expect(shouldAutoReload({ hasInputFocus: false, msSinceInteraction: UPDATE_IDLE_THRESHOLD_MS - 1 })).toBe(false)
  })

  it('从未交互（Infinity）允许 —— 冷启动即检查的场景', () => {
    expect(shouldAutoReload({ hasInputFocus: false, msSinceInteraction: Number.POSITIVE_INFINITY })).toBe(true)
  })

  it('elementBlocksAutoReload：输入类控件与 contenteditable 判定为阻塞', () => {
    const doc = docWithMetas('2450')
    const input = doc.createElement('input')
    const textarea = doc.createElement('textarea')
    const select = doc.createElement('select')
    const div = doc.createElement('div')
    const editable = doc.createElement('div')
    editable.contentEditable = 'true'
    expect(elementBlocksAutoReload(input)).toBe(true)
    expect(elementBlocksAutoReload(textarea)).toBe(true)
    expect(elementBlocksAutoReload(select)).toBe(true)
    expect(elementBlocksAutoReload(editable)).toBe(true)
    expect(elementBlocksAutoReload(div)).toBe(false)
    expect(elementBlocksAutoReload(null)).toBe(false)
  })
})
