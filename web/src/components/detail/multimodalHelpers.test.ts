import { describe, expect, it } from 'vitest'
import {
  attachmentsToMediaItems,
  collectMultimodalMedia,
  extractMultimodalFromBody,
} from './multimodalHelpers'

describe('extractMultimodalFromBody', () => {
  it('extracts OpenAI image_url object + bare string', () => {
    const items = extractMultimodalFromBody({
      messages: [{
        role: 'user',
        content: [
          { type: 'text', text: 'see' },
          { type: 'image_url', image_url: { url: 'https://e.com/a.png', detail: 'low' } },
          { type: 'image_url', image_url: 'https://e.com/b.png' },
        ],
      }],
    })
    expect(items).toHaveLength(2)
    expect(items[0].kind).toBe('image')
    expect(items[0].url).toBe('https://e.com/a.png')
    expect(items[1].url).toBe('https://e.com/b.png')
  })

  it('extracts Anthropic base64 image source', () => {
    const items = extractMultimodalFromBody({
      messages: [{
        role: 'user',
        content: [{
          type: 'image',
          source: { type: 'base64', media_type: 'image/png', data: 'Zm9vYmFy' },
        }],
      }],
    })
    expect(items).toHaveLength(1)
    expect(items[0].url).toMatch(/^data:image\/png;base64,Zm9vYmFy$/)
    expect(items[0].mime).toBe('image/png')
  })

  it('extracts input_audio and video_url', () => {
    const items = extractMultimodalFromBody({
      messages: [{
        role: 'user',
        content: [
          { type: 'input_audio', input_audio: { data: 'YWFh', format: 'wav' } },
          { type: 'video_url', video_url: { url: 'https://e.com/v.mp4' } },
        ],
      }],
    })
    expect(items.map((i) => i.kind)).toEqual(['audio', 'video'])
    expect(items[0].url).toMatch(/^data:audio\/wav;base64,/)
    expect(items[1].url).toBe('https://e.com/v.mp4')
  })
})

describe('attachmentsToMediaItems + collect', () => {
  it('maps persisted attachments and merges with body', () => {
    const items = collectMultimodalMedia(
      {
        messages: [{
          role: 'user',
          content: [{ type: 'image_url', image_url: { url: 'https://e.com/a.png' } }],
        }],
      },
      [{
        type: 'image',
        content_type: 'image/jpeg',
        size: 12,
        path: '2026/08/req/x.jpg',
        hash: 'abcd1234567890',
      }],
    )
    expect(items[0].source).toBe('attachment')
    expect(items[0].url).toContain('/api/attachments/')
    expect(items.some((i) => i.url === 'https://e.com/a.png')).toBe(true)
  })

  it('returns empty for text-only body', () => {
    expect(extractMultimodalFromBody({ messages: [{ role: 'user', content: 'hi' }] })).toEqual([])
    expect(attachmentsToMediaItems([])).toEqual([])
  })
})
