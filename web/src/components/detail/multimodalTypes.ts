// multimodalTypes.ts — shared multimodal media item types.
export type MediaKind = 'image' | 'audio' | 'video' | 'file'

export interface MultimodalMediaItem {
  kind: MediaKind
  /** Preview / download URL (http(s), data:, or /api/attachments/...). */
  url: string
  mime?: string
  label?: string
  messageIndex: number
  blockIndex: number
  source: 'body' | 'attachment'
  detail?: string
}

export function asRecord(v: unknown): Record<string, unknown> | null {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null
}

export function pickString(...vals: unknown[]): string {
  for (const v of vals) {
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  return ''
}

export function mimeFromDataURL(url: string): string | undefined {
  const m = /^data:([^;,]+)/i.exec(url)
  return m?.[1]
}

export function kindFromMime(mime: string | undefined, fallback: MediaKind = 'file'): MediaKind {
  if (!mime) return fallback
  if (mime.startsWith('image/')) return 'image'
  if (mime.startsWith('audio/')) return 'audio'
  if (mime.startsWith('video/')) return 'video'
  return fallback
}

export function dataURLFromBase64(mediaType: string, data: string): string {
  return `data:${mediaType};base64,${data.replace(/\s+/g, '')}`
}
