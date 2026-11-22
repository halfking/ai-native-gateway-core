// liveTopologyLayout.gap.test.ts —— 模型层↔供应端层**固定间距**的钉测
//
// ★ 为什么单独钉（2026-11-23 第十二轮实测取证）：
//   生产实测画布只有 480px，比例定位被压成 **1px** 净间距（肉眼＝贴在一起）。
//   ⇒ 契约从「比例」改成「**固定像素 ROW_GAP**」⇒ 必须有钉测守着，
//   否则下一次「顺手调一下比例」就会把间距悄悄改回 1px 而无人察觉。
//
// 钉的不变量（每条都对应一个真实缺陷形态）：
//   ① 净间距恒定 = ROW_GAP（不随画布高度缩放）——比例方案的病根；
//   ② 供应端层不超出画布底边；
//   ③ 网关核心大方块不压进模型层；
//   ④ 模型层不与客户端层重叠（三层各就各位）。
import { describe, it, expect } from 'vitest'
import { computeLayout, computeRowY, computeGatewayBox, NODE_H, ROW_GAP } from './liveTopologyLayout'

const HEIGHTS = [480, 560, 620, 700, 846, 900, 1200]

describe('模型层↔供应端层固定间距（ROW_GAP）', () => {
  it('① 净间距恒等于 ROW_GAP，不随画布高度缩放', () => {
    for (const h of HEIGHTS) {
      const rows = computeRowY(h)
      const gap = (rows.provider - NODE_H / 2) - (rows.model + NODE_H / 2)
      expect(gap, `画布 ${h}px 下的净间距`).toBe(ROW_GAP)
    }
  })

  it('② 供应端层卡片底边不超出画布', () => {
    for (const h of HEIGHTS) {
      const rows = computeRowY(h)
      expect(rows.provider + NODE_H / 2, `画布 ${h}px`).toBeLessThanOrEqual(h)
    }
  })

  it('③ 网关核心大方块不压进模型层', () => {
    for (const h of HEIGHTS) {
      const rows = computeRowY(h)
      const box = computeGatewayBox(1440, h, rows)
      const modelTop = rows.model - NODE_H / 2
      expect(box.y + box.h, `画布 ${h}px：大方块底边压进模型层`).toBeLessThanOrEqual(modelTop)
    }
  })

  it('③b 网关核心大方块不压进客户端层（顶边 ≥ 客户端卡片底边）', () => {
    for (const h of HEIGHTS) {
      const rows = computeRowY(h)
      const box = computeGatewayBox(1440, h, rows)
      const clientBottom = rows.client + NODE_H / 2
      expect(box.y, `画布 ${h}px：大方块顶边压进客户端层`).toBeGreaterThanOrEqual(clientBottom)
    }
  })

  it('④ 模型层在客户端层之下、供应端层之上（三层各就各位）', () => {
    for (const h of HEIGHTS) {
      const rows = computeRowY(h)
      expect(rows.client, `画布 ${h}px`).toBeLessThan(rows.model)
      expect(rows.model, `画布 ${h}px`).toBeLessThan(rows.provider)
    }
  })

  it('⑤ 真实布局里卡片边缘的净间距同样恒定（走 computeLayout 全链路）', () => {
    for (const h of HEIGHTS) {
      const layout = computeLayout({ nodes: [], edges: [], width: 1440, height: h })
      const modelBottom = layout.rowY.model + NODE_H / 2
      const providerTop = layout.rowY.provider - NODE_H / 2
      expect(providerTop - modelBottom, `画布 ${h}px`).toBe(ROW_GAP)
    }
  })
})
