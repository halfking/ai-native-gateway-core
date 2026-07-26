import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import SwimLaneTrack from './SwimLaneTrack.vue'
import RequestTile from './RequestTile.vue'
import type { RequestTile as RequestTileType } from '../types/swimlane'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {},
    'en-US': {},
  },
})

// 2026-07-26: Direction reversed per user requirement — oldest on LEFT,
// newest on RIGHT. `tiles` prop is expected to already be sorted ASC
// (oldest first) by the caller (liveStreamStore); SwimLaneTrack only
// takes the tail (most recent `maxVisible`) and renders in that order.
describe('SwimLaneTrack', () => {
  const createTile = (id: string, timestamp: string): RequestTileType => ({
    request_id: id,
    timestamp,
    model: 'gpt-4o',
    vendor: 'openai',
    provider: 'openai-official',
    status: 'success',
  })

  it('displays tiles in correct order (oldest on left, newest on right)', () => {
    const tiles = [
      createTile('old', '2026-07-26T12:01:00Z'),
      createTile('new', '2026-07-26T12:02:00Z'),
    ]

    const wrapper = mount(SwimLaneTrack, {
      props: {
        tiles,
        mode: 'large' as const,
        groupBy: 'vendor',
        maxVisible: 10,
      },
      global: {
        plugins: [i18n],
      },
    })

    const renderedTiles = wrapper.findAllComponents(RequestTile)
    expect(renderedTiles).toHaveLength(2)
    expect(renderedTiles[0].props('tile').request_id).toBe('old')
    expect(renderedTiles[1].props('tile').request_id).toBe('new')
  })

  it('limits displayed tiles to maxVisible by keeping the newest (tail)', () => {
    // ASC input: req-0 is oldest, req-29 is newest.
    const tiles = Array.from({ length: 30 }, (_, i) =>
      createTile(`req-${i}`, new Date(Date.now() + i * 1000).toISOString())
    )

    const wrapper = mount(SwimLaneTrack, {
      props: {
        tiles,
        mode: 'small' as const,
        groupBy: 'model',
        maxVisible: 20,
      },
      global: {
        plugins: [i18n],
      },
    })

    const renderedTiles = wrapper.findAllComponents(RequestTile)
    expect(renderedTiles).toHaveLength(20)
    // Overflow drops the oldest (leftmost); the kept window is req-10..req-29.
    expect(renderedTiles[0].props('tile').request_id).toBe('req-10')
    expect(renderedTiles[19].props('tile').request_id).toBe('req-29')
  })

  it('shows all tiles when count <= maxVisible', () => {
    const tiles = [
      createTile('req-1', '2026-07-26T12:01:00Z'),
      createTile('req-2', '2026-07-26T12:02:00Z'),
    ]

    const wrapper = mount(SwimLaneTrack, {
      props: {
        tiles,
        mode: 'large' as const,
        groupBy: 'provider',
        maxVisible: 10,
      },
      global: {
        plugins: [i18n],
      },
    })

    const renderedTiles = wrapper.findAllComponents(RequestTile)
    expect(renderedTiles).toHaveLength(2)
  })

  it('emits tileClick event when tile is clicked', async () => {
    const tiles = [createTile('req-123', '2026-07-26T12:00:00Z')]

    const wrapper = mount(SwimLaneTrack, {
      props: {
        tiles,
        mode: 'large' as const,
        groupBy: 'vendor',
        maxVisible: 10,
      },
      global: {
        plugins: [i18n],
      },
    })

    const tile = wrapper.findComponent(RequestTile)
    await tile.trigger('click')

    expect(wrapper.emitted('tileClick')).toBeTruthy()
    expect(wrapper.emitted('tileClick')![0]).toEqual(['req-123'])
  })

  it('applies correct mode class', () => {
    const tiles = [createTile('req-1', '2026-07-26T12:00:00Z')]

    const wrapperSmall = mount(SwimLaneTrack, {
      props: { tiles, mode: 'small' as const, groupBy: 'vendor', maxVisible: 10 },
      global: {
        plugins: [i18n],
      },
    })

    const wrapperLarge = mount(SwimLaneTrack, {
      props: { tiles, mode: 'large' as const, groupBy: 'vendor', maxVisible: 10 },
      global: {
        plugins: [i18n],
      },
    })

    expect(wrapperSmall.find('.swim-lane-track__tiles--small').exists()).toBe(true)
    expect(wrapperLarge.find('.swim-lane-track__tiles--small').exists()).toBe(false)
  })
})
