// multimodalHelpers.ts — merge body media + persisted attachments.
import { attachmentURL, type AttachmentInfo } from '../../api/logs'
import { extractMultimodalFromBody } from './multimodalExtract'
import {
  kindFromMime,
  pickString,
  type MediaKind,
  type MultimodalMediaItem,
} from './multimodalTypes'

export type { MediaKind, MultimodalMediaItem }
export { extractMultimodalFromBody }

/** Map persisted AttachmentInfo rows into MultimodalMediaItem. */
export function attachmentsToMediaItems(atts: AttachmentInfo[] | null | undefined): MultimodalMediaItem[] {
  if (!atts?.length) return []
  return atts.map((a, i) => {
    const url = a.path ? attachmentURL(a.path) : pickString(a.original_url)
    const kind: MediaKind = a.type === 'image' ? 'image' : kindFromMime(a.content_type, 'file')
    return {
      kind,
      url,
      mime: a.content_type || undefined,
      label: a.path?.split('/').pop() || a.content_type || `attachment-${i}`,
      messageIndex: a.message_index ?? -1,
      blockIndex: a.block_index ?? -1,
      source: 'attachment' as const,
      detail: [
        a.content_type,
        a.size != null ? `${a.size} B` : '',
        a.hash ? `sha256:${a.hash.slice(0, 12)}…` : '',
      ].filter(Boolean).join(' · '),
    }
  }).filter((m) => m.url || m.detail)
}

/** Merge body-derived media with persisted attachments (attachments first). */
export function collectMultimodalMedia(
  body: unknown,
  attachments?: AttachmentInfo[] | null,
): MultimodalMediaItem[] {
  const fromAtt = attachmentsToMediaItems(attachments)
  const fromBody = extractMultimodalFromBody(body)
  const seen = new Set<string>()
  const out: MultimodalMediaItem[] = []
  for (const item of [...fromAtt, ...fromBody]) {
    const key = item.url || `${item.kind}:${item.label}:${item.detail}`
    if (seen.has(key)) continue
    seen.add(key)
    out.push(item)
  }
  return out
}
