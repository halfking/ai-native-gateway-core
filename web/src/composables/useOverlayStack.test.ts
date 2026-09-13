import { describe, expect, it } from 'vitest'
import {
  isTopmostOverlayLayer,
  nextOverlayLayerId,
  overlayStackDepth,
  popOverlayLayer,
  pushOverlayLayer,
} from './useOverlayStack'

// Audit R20 (2026-09-13): stacked AppModal/AppDrawer must close one layer per
// Escape — only the topmost registered layer may act.
describe('useOverlayStack', () => {
  it('treats the last pushed layer as topmost', () => {
    const drawer = nextOverlayLayerId()
    const modal = nextOverlayLayerId()
    pushOverlayLayer(drawer)
    pushOverlayLayer(modal)

    expect(isTopmostOverlayLayer(drawer)).toBe(false)
    expect(isTopmostOverlayLayer(modal)).toBe(true)

    popOverlayLayer(modal)
    expect(isTopmostOverlayLayer(drawer)).toBe(true)

    popOverlayLayer(drawer)
    expect(overlayStackDepth()).toBe(0)
  })

  it('ignores duplicate pushes and unknown pops', () => {
    const id = nextOverlayLayerId()
    pushOverlayLayer(id)
    pushOverlayLayer(id)
    expect(overlayStackDepth()).toBe(1)

    const unknown = nextOverlayLayerId()
    popOverlayLayer(unknown)
    expect(overlayStackDepth()).toBe(1)

    popOverlayLayer(id)
    expect(overlayStackDepth()).toBe(0)
  })
})
