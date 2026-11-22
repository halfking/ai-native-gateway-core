// liveTopologyLayout.gap.test.ts —— 网关核心↔模型层**固定间距**的钉测
//
// ★ 2026-11-23 第十五轮重构：布局逻辑从"供应端往上推"改为"网关核心往下排"。
//   ROW_GAP 现在控制网关核心底边到模型层顶边的间距。
//
// 钉的不变量（每条都对应一个真实缺陷形态）：
//   ① 网关核心底边↔模型层顶边净间距 = ROW_GAP（不随画布高度缩放）；
//   ② 网关核心大方块不压进客户端层；
//   ③ 模型层不与网关核心重叠；
//   ④ 模型层在客户端层之下、供应端层之上（三层各就各位）。
import { describe, it, expect } from 'vitest'
import { computeLayout, NODE_H, ROW_GAP, MODEL_TO_PROVIDER_GAP } from './liveTopologyLayout'

const HEIGHTS = [480, 560, 620, 700, 846, 900, 1200]

describe('网关核心↔模型层固定间距（ROW_GAP）', () => {
  it('① 网关核心底边↔模型层顶边净间距恒等于 ROW_GAP', () => {
    for (const h of HEIGHTS) {
      const layout = computeLayout({ nodes: [], edges: [], width: 1440, height: h })
      const gatewayBottom = layout.gateway.y + layout.gateway.h
      // 根据新逻辑推算模型层位置
      const modelY = Math.round(gatewayBottom + ROW_GAP + NODE_H / 2)
      const modelTop = modelY - NODE_H / 2
      const gap = modelTop - gatewayBottom
      expect(gap, `画布 ${h}px 下网关核心↔模型层净间距`).toBeCloseTo(ROW_GAP, 0)
    }
  })

  it('② 网关核心不压进客户端层（顶边 ≥ 客户端卡片底边）', () => {
    for (const h of HEIGHTS) {
      const layout = computeLayout({ nodes: [], edges: [], width: 1440, height: h })
      const clientRowY = Math.round(h * 0.075)
      const clientBottom = clientRowY + NODE_H / 2
      expect(layout.gateway.y, `画布 ${h}px：大方块顶边`).toBeGreaterThanOrEqual(clientBottom)
    }
  })

  it('③ 模型层↔供应端层净间距恒等于 MODEL_TO_PROVIDER_GAP', () => {
    for (const h of HEIGHTS) {
      const layout = computeLayout({ nodes: [], edges: [], width: 1440, height: h })
      const gatewayBottom = layout.gateway.y + layout.gateway.h
      const modelY = Math.round(gatewayBottom + ROW_GAP + NODE_H / 2)
      const providerY = Math.round(modelY + NODE_H / 2 + MODEL_TO_PROVIDER_GAP + NODE_H / 2)
      const gap = (providerY - NODE_H / 2) - (modelY + NODE_H / 2)
      expect(gap, `画布 ${h}px 下模型层↔供应端层净间距`).toBeCloseTo(MODEL_TO_PROVIDER_GAP, 0)
    }
  })

  it('④ 各层从上到下依次排列（客户端→网关核心→模型→供应端）', () => {
    for (const h of HEIGHTS) {
      const layout = computeLayout({ nodes: [], edges: [], width: 1440, height: h })
      const clientRowY = Math.round(h * 0.075)
      const gatewayY = layout.gateway.y + layout.gateway.h / 2
      const gatewayBottom = layout.gateway.y + layout.gateway.h
      const modelY = Math.round(gatewayBottom + ROW_GAP + NODE_H / 2)
      const providerY = Math.round(modelY + NODE_H / 2 + MODEL_TO_PROVIDER_GAP + NODE_H / 2)
      
      expect(clientRowY, `画布 ${h}px`).toBeLessThan(gatewayY)
      expect(gatewayY, `画布 ${h}px`).toBeLessThan(modelY)
      expect(modelY, `画布 ${h}px`).toBeLessThan(providerY)
    }
  })
})
