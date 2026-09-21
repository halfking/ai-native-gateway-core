// multimodalExtract.ts — pull image / audio / video blocks from request bodies.
import { extractMessagesFromBody } from './messageHelpers'
import {
  asRecord,
  dataURLFromBase64,
  kindFromMime,
  mimeFromDataURL,
  pickString,
  type MultimodalMediaItem,
} from './multimodalTypes'

function pushItem(out: MultimodalMediaItem[], item: MultimodalMediaItem | null) {
  if (!item || (!item.url && !item.detail)) return
  out.push(item)
}

function fromImageLike(
  block: Record<string, unknown>,
  messageIndex: number,
  blockIndex: number,
): MultimodalMediaItem | null {
  const type = String(block.type || '')
  if (type !== 'image_url' && type !== 'input_image' && type !== 'image') return null

  const imageURL = block.image_url
  if (typeof imageURL === 'string' && imageURL) {
    return {
      kind: 'image', url: imageURL, mime: mimeFromDataURL(imageURL),
      label: type, messageIndex, blockIndex, source: 'body',
    }
  }
  const obj = asRecord(imageURL)
  if (obj) {
    const url = pickString(obj.url)
    if (url) {
      return {
        kind: 'image', url, mime: mimeFromDataURL(url),
        label: pickString(obj.detail, type) || type, messageIndex, blockIndex, source: 'body',
      }
    }
  }
  const source = asRecord(block.source)
  if (!source) return null
  const mediaType = pickString(source.media_type, source.mime_type) || 'image/png'
  const url = pickString(source.url)
  if (url) {
    return {
      kind: 'image', url, mime: mediaType, label: type,
      messageIndex, blockIndex, source: 'body',
    }
  }
  const data = pickString(source.data)
  if (!data) return null
  return {
    kind: 'image',
    url: dataURLFromBase64(mediaType, data),
    mime: mediaType,
    label: type,
    messageIndex,
    blockIndex,
    source: 'body',
    detail: `base64 ${mediaType} (${Math.round(data.length * 0.75)} bytes est.)`,
  }
}

function fromAudioLike(
  block: Record<string, unknown>,
  messageIndex: number,
  blockIndex: number,
): MultimodalMediaItem | null {
  const type = String(block.type || '')
  if (type !== 'input_audio' && type !== 'audio' && type !== 'audio_url') return null

  const audioURL = block.audio_url
  if (typeof audioURL === 'string' && audioURL) {
    return {
      kind: 'audio', url: audioURL, mime: mimeFromDataURL(audioURL),
      label: type, messageIndex, blockIndex, source: 'body',
    }
  }
  const urlObj = asRecord(audioURL)
  if (urlObj) {
    const url = pickString(urlObj.url)
    if (url) {
      return {
        kind: 'audio', url, mime: mimeFromDataURL(url),
        label: type, messageIndex, blockIndex, source: 'body',
      }
    }
  }
  const input = asRecord(block.input_audio) || asRecord(block.audio) || block
  const data = pickString(input.data)
  if (!data) return null
  const format = pickString(input.format, input.media_type) || 'wav'
  const mime = format.includes('/') ? format : `audio/${format}`
  return {
    kind: 'audio',
    url: dataURLFromBase64(mime, data),
    mime,
    label: type,
    messageIndex,
    blockIndex,
    source: 'body',
    detail: `base64 ${mime} (${Math.round(data.length * 0.75)} bytes est.)`,
  }
}

function fromVideoLike(
  block: Record<string, unknown>,
  messageIndex: number,
  blockIndex: number,
): MultimodalMediaItem | null {
  const type = String(block.type || '')
  if (type !== 'video_url' && type !== 'input_video' && type !== 'video') return null

  const videoURL = block.video_url ?? block.image_url
  if (typeof videoURL === 'string' && videoURL) {
    return {
      kind: 'video', url: videoURL, mime: mimeFromDataURL(videoURL),
      label: type, messageIndex, blockIndex, source: 'body',
    }
  }
  const obj = asRecord(videoURL) || asRecord(block.source)
  if (!obj) return null
  const mediaType = pickString(obj.media_type) || 'video/mp4'
  const url = pickString(obj.url)
  if (url) {
    return {
      kind: 'video', url, mime: mediaType, label: type,
      messageIndex, blockIndex, source: 'body',
    }
  }
  const data = pickString(obj.data)
  if (!data) return null
  return {
    kind: 'video',
    url: dataURLFromBase64(mediaType, data),
    mime: mediaType,
    label: type,
    messageIndex,
    blockIndex,
    source: 'body',
    detail: `base64 ${mediaType}`,
  }
}

function fromFileLike(
  block: Record<string, unknown>,
  messageIndex: number,
  blockIndex: number,
): MultimodalMediaItem | null {
  const type = String(block.type || '')
  if (type !== 'input_file' && type !== 'file' && type !== 'document') return null
  const file = asRecord(block.file) || asRecord(block.source) || block
  const name = pickString(file.filename, file.name, type)
  const mime = pickString(file.media_type, file.content_type)
  const url = pickString(file.url, file.file_url)
  if (url) {
    return {
      kind: kindFromMime(mime, 'file'), url, mime: mime || undefined,
      label: name, messageIndex, blockIndex, source: 'body',
    }
  }
  const data = pickString(file.data, file.file_data)
  if (data && mime) {
    return {
      kind: kindFromMime(mime, 'file'),
      url: dataURLFromBase64(mime, data),
      mime,
      label: name,
      messageIndex,
      blockIndex,
      source: 'body',
      detail: name,
    }
  }
  if (!name) return null
  return {
    kind: 'file', url: '', mime: mime || undefined, label: name,
    messageIndex, blockIndex, source: 'body', detail: name,
  }
}

function extractFromBlock(
  block: unknown,
  messageIndex: number,
  blockIndex: number,
): MultimodalMediaItem | null {
  const rec = asRecord(block)
  if (!rec) return null
  return (
    fromImageLike(rec, messageIndex, blockIndex)
    || fromAudioLike(rec, messageIndex, blockIndex)
    || fromVideoLike(rec, messageIndex, blockIndex)
    || fromFileLike(rec, messageIndex, blockIndex)
  )
}

/** Walk chat/completions (or Anthropic) body messages and collect media parts. */
export function extractMultimodalFromBody(body: unknown): MultimodalMediaItem[] {
  const out: MultimodalMediaItem[] = []
  extractMessagesFromBody(body).forEach((msg, mi) => {
    const content = msg.content
    if (Array.isArray(content)) {
      content.forEach((block, bi) => pushItem(out, extractFromBlock(block, mi, bi)))
      return
    }
    pushItem(out, extractFromBlock(msg, mi, -1))
  })
  return out
}
