/**
 * markdown.ts — minimal dependency-free Markdown subset parser.
 *
 * Scope: the syntax actually used by the in-app help manual
 * (`web/content/help-manual.zh-CN.md`):
 *
 *   # title / ## chapter / ### section   headings
 *   ```lang ... ```                       fenced code blocks
 *   | a | b |                             pipe tables (with separator row)
 *   - item / 1. item                      ordered + unordered lists
 *   > quote                              block quotes
 *   **bold** *italic* `code` [text](url)  inline spans
 *
 * Design notes
 * ------------
 * · No HTML is ever produced. The parser returns a structured tree that the
 *   view renders with ordinary Vue templates, so manual content can never
 *   inject markup and no sanitizer is required.
 * · Inline spans are tokenised, not string-substituted: bold inside a code
 *   span stays literal, and code spans inside bold stay code.
 * · Heading ids are assigned by document order (`h2-1`, `h3-1-2`), which keeps
 *   anchors stable and readable even for non-latin headings.
 */

/** A rendered piece of a line of text. */
export type InlineSegment =
  | { kind: 'text'; text: string }
  | { kind: 'code'; text: string }
  | { kind: 'bold'; text: string }
  | { kind: 'italic'; text: string }
  | { kind: 'link'; text: string; href: string }

/** One block-level element of the manual. */
export type ManualBlock =
  | { kind: 'title'; text: string }
  | { kind: 'heading'; level: 2 | 3; text: string; id: string }
  | { kind: 'paragraph'; segments: InlineSegment[] }
  | { kind: 'list'; ordered: boolean; items: InlineSegment[][] }
  | { kind: 'code'; lang: string; text: string }
  | { kind: 'table'; headers: InlineSegment[][]; rows: InlineSegment[][][] }
  | { kind: 'quote'; segments: InlineSegment[] }

const INLINE_TOKEN_RE = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\s][^*]*\*)|(\[[^\]]+\]\([^)\s]+\))/g

/** Split a line of text into renderable inline segments. */
export function parseInline(text: string): InlineSegment[] {
  const segments: InlineSegment[] = []
  let cursor = 0
  INLINE_TOKEN_RE.lastIndex = 0
  let match: RegExpExecArray | null
  while ((match = INLINE_TOKEN_RE.exec(text)) !== null) {
    if (match.index > cursor) {
      segments.push({ kind: 'text', text: text.slice(cursor, match.index) })
    }
    const token = match[0]
    if (token.startsWith('`')) {
      segments.push({ kind: 'code', text: token.slice(1, -1) })
    } else if (token.startsWith('**')) {
      segments.push({ kind: 'bold', text: token.slice(2, -2) })
    } else if (token.startsWith('[')) {
      const linkMatch = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(token)
      if (linkMatch) {
        segments.push({ kind: 'link', text: linkMatch[1], href: linkMatch[2] })
      } else {
        segments.push({ kind: 'text', text: token })
      }
    } else {
      segments.push({ kind: 'italic', text: token.slice(1, -1) })
    }
    cursor = match.index + token.length
  }
  if (cursor < text.length) {
    segments.push({ kind: 'text', text: text.slice(cursor) })
  }
  return segments
}

/** Flatten inline segments back to plain text (used for search + TOC). */
export function inlineText(segments: InlineSegment[]): string {
  return segments.map((s) => s.text).join('')
}

function isTableSeparator(line: string): boolean {
  const cells = splitTableRow(line)
  if (cells.length === 0) return false
  return cells.every((cell) => /^:?-{2,}:?$/.test(cell.trim()))
}

function splitTableRow(line: string): string[] {
  let trimmed = line.trim()
  if (trimmed.startsWith('|')) trimmed = trimmed.slice(1)
  if (trimmed.endsWith('|')) trimmed = trimmed.slice(0, -1)
  return trimmed.split('|').map((cell) => cell.trim())
}

function isListItem(line: string): boolean {
  return /^\s*([-*+]|\d+\.)\s+/.test(line)
}

function listIsOrdered(line: string): boolean {
  return /^\s*\d+\.\s+/.test(line)
}

function stripListMarker(line: string): string {
  return line.replace(/^\s*([-*+]|\d+\.)\s+/, '')
}

/**
 * Parse a Markdown document into block elements.
 *
 * @param source raw Markdown text
 */
export function parseMarkdown(source: string): ManualBlock[] {
  const lines = source.replace(/\r\n/g, '\n').split('\n')
  const blocks: ManualBlock[] = []
  const chapterCounter = { 2: 0, 3: 0 }
  let chapterIndex = 0
  let sectionIndex = 0
  let i = 0

  const pushHeading = (level: 2 | 3, text: string) => {
    if (level === 2) {
      chapterIndex += 1
      sectionIndex = 0
      chapterCounter[2] = chapterIndex
      blocks.push({ kind: 'heading', level, text, id: `h2-${chapterIndex}` })
    } else {
      sectionIndex += 1
      blocks.push({ kind: 'heading', level, text, id: `h3-${chapterIndex}-${sectionIndex}` })
    }
  }

  while (i < lines.length) {
    const line = lines[i]

    if (line.trim() === '') {
      i += 1
      continue
    }

    // Fenced code block.
    const fence = /^```\s*([\w+-]*)\s*$/.exec(line)
    if (fence) {
      const lang = fence[1] ?? ''
      const body: string[] = []
      i += 1
      while (i < lines.length && !/^```\s*$/.test(lines[i])) {
        body.push(lines[i])
        i += 1
      }
      i += 1 // consume the closing fence (or run off the end)
      blocks.push({ kind: 'code', lang, text: body.join('\n') })
      continue
    }

    // ATX heading.
    const heading = /^(#{1,3})\s+(.*\S)\s*$/.exec(line)
    if (heading) {
      const level = heading[1].length
      const text = heading[2]
      if (level === 1) {
        blocks.push({ kind: 'title', text })
      } else {
        pushHeading(level as 2 | 3, text)
      }
      i += 1
      continue
    }

    // Block quote.
    if (/^>\s?/.test(line)) {
      const body: string[] = []
      while (i < lines.length && /^>\s?/.test(lines[i])) {
        body.push(lines[i].replace(/^>\s?/, ''))
        i += 1
      }
      blocks.push({ kind: 'quote', segments: parseInline(body.join(' ')) })
      continue
    }

    // Pipe table (header row followed by a separator row).
    if (line.trim().startsWith('|') && i + 1 < lines.length && isTableSeparator(lines[i + 1])) {
      const headers = splitTableRow(line).map((cell) => parseInline(cell))
      i += 2
      const rows: InlineSegment[][][] = []
      while (i < lines.length && lines[i].trim().startsWith('|')) {
        rows.push(splitTableRow(lines[i]).map((cell) => parseInline(cell)))
        i += 1
      }
      blocks.push({ kind: 'table', headers, rows })
      continue
    }

    // List.
    if (isListItem(line)) {
      const ordered = listIsOrdered(line)
      const items: InlineSegment[][] = []
      while (i < lines.length && isListItem(lines[i]) && listIsOrdered(lines[i]) === ordered) {
        items.push(parseInline(stripListMarker(lines[i])))
        i += 1
      }
      blocks.push({ kind: 'list', ordered, items })
      continue
    }

    // Paragraph: consume until a blank line or the start of another block.
    const paragraph: string[] = []
    while (
      i < lines.length &&
      lines[i].trim() !== '' &&
      !isListItem(lines[i]) &&
      !lines[i].trim().startsWith('|') &&
      !/^```/.test(lines[i]) &&
      !/^#{1,3}\s/.test(lines[i]) &&
      !/^>\s?/.test(lines[i])
    ) {
      paragraph.push(lines[i].trim())
      i += 1
    }
    if (paragraph.length > 0) {
      blocks.push({ kind: 'paragraph', segments: parseInline(paragraph.join(' ')) })
    } else {
      i += 1
    }
  }

  return blocks
}

/** Plain text of a block (used for search matching and the table of contents). */
export function blockText(block: ManualBlock): string {
  switch (block.kind) {
    case 'title':
    case 'heading':
      return block.text
    case 'paragraph':
    case 'quote':
      return inlineText(block.segments)
    case 'list':
      return block.items.map((item) => inlineText(item)).join(' ')
    case 'code':
      return block.text
    case 'table':
      return [
        block.headers.map((cell) => inlineText(cell)).join(' '),
        ...block.rows.map((row) => row.map((cell) => inlineText(cell)).join(' ')),
      ].join(' ')
  }
}

/** A chapter entry of the table of contents. */
export interface TocEntry {
  id: string
  title: string
  sections: Array<{ id: string; title: string }>
}

/** Build the table of contents from parsed blocks. */
export function buildToc(blocks: ManualBlock[]): TocEntry[] {
  const toc: TocEntry[] = []
  for (const block of blocks) {
    if (block.kind !== 'heading') continue
    if (block.level === 2) {
      toc.push({ id: block.id, title: block.text, sections: [] })
    } else {
      const parent = toc[toc.length - 1]
      if (parent) parent.sections.push({ id: block.id, title: block.text })
    }
  }
  return toc
}

/**
 * Filter blocks by keyword.
 *
 * A heading survives when the heading itself matches, or when any block in
 * its section matches — otherwise a keyword hit deep inside a chapter would
 * leave the chapter title missing from the result.
 */
export function filterManualBlocks(blocks: ManualBlock[], keyword: string): ManualBlock[] {
  const needle = keyword.trim().toLowerCase()
  if (!needle) return blocks

  const matches = blocks.map((block) => blockText(block).toLowerCase().includes(needle))

  // Mark headings whose section contains a hit.
  const headingKept = new Array<boolean>(blocks.length).fill(false)
  for (let i = 0; i < blocks.length; i += 1) {
    const block = blocks[i]
    if (block.kind !== 'heading') continue
    let end = blocks.length
    for (let j = i + 1; j < blocks.length; j += 1) {
      const next = blocks[j]
      if (next.kind === 'heading' && next.level <= block.level) {
        end = j
        break
      }
    }
    let sectionHit = false
    for (let j = i + 1; j < end; j += 1) {
      if (matches[j]) {
        sectionHit = true
        break
      }
    }
    headingKept[i] = matches[i] || sectionHit
  }

  return blocks.filter((block, index) => {
    if (block.kind === 'heading') return headingKept[index]
    return matches[index]
  })
}
