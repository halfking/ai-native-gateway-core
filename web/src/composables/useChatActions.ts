import {
  chatCompletion,
  messagesForApi,
  type ChatCompletionMessage,
  type TokenUsage,
} from './useChatCompletions'

export type ExportableMessage = ChatCompletionMessage & {
  requestedModel?: string
  usage?: TokenUsage
  resolvedModel?: string
}

const TITLE_MAX = 18

export interface SummarizeResult {
  title: string
  summary: string
  usage: TokenUsage | null
  resolvedModel: string | null
}

function parseSummarizeOutput(raw: string): Pick<SummarizeResult, 'title' | 'summary'> {
  const text = raw.trim()
  const titleMatch = text.match(/^标题[：:]\s*(.+?)(?:\n|$)/m)
  const title = titleMatch?.[1]?.trim().slice(0, TITLE_MAX) || ''
  const body = text
    .replace(/^标题[：:].+?\n+/m, '')
    .replace(/^【标题】.+?\n+/m, '')
    .replace(/^总结[：:]\s*/m, '')
    .replace(/^【总结】\s*/m, '')
    .trim()
  return { title, summary: body || text }
}

export function formatSessionExport(opts: {
  title: string
  modelLabel: string
  messages: ExportableMessage[]
  summary?: string
  usage?: TokenUsage
}): string {
  const lines = [
    opts.title,
    `模型: ${opts.modelLabel}`,
    `导出时间: ${new Date().toLocaleString('zh-CN')}`,
  ]
  if (opts.usage && (opts.usage.promptTokens > 0 || opts.usage.completionTokens > 0)) {
    lines.push(
      `Token 消耗: 输入 ${opts.usage.promptTokens} / 输出 ${opts.usage.completionTokens} / 合计 ${opts.usage.totalTokens}`,
    )
  }
  if (opts.summary?.trim()) {
    lines.push('', '── 会话总结 ──', opts.summary.trim())
  }
  lines.push('', '── 对话记录 ──', '')
  for (const m of opts.messages) {
    if (m.role === 'system') continue
    const role = m.role === 'user' ? '用户' : '助手'
    const meta: string[] = []
    if (m.role === 'user' && m.requestedModel) meta.push(`模型: ${m.requestedModel}`)
    if (m.role === 'assistant' && m.resolvedModel) meta.push(`模型: ${m.resolvedModel}`)
    if (m.role === 'assistant' && m.usage) {
      meta.push(`tokens: ${m.usage.promptTokens}+${m.usage.completionTokens}=${m.usage.totalTokens}`)
    }
    lines.push(`[${role}]${meta.length ? ` (${meta.join(', ')})` : ''}`)
    lines.push(m.content)
    lines.push('')
  }
  return lines.join('\n')
}

export function downloadTextFile(filename: string, content: string) {
  const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

export async function generateSessionTitle(opts: {
  apiKey: string
  model: string
  firstUserMessage: string
  taskId: string
  gwSessionId: string | null
}): Promise<{ title: string; usage: TokenUsage | null; resolvedModel: string | null }> {
  const result = await chatCompletion({
    apiKey: opts.apiKey,
    model: opts.model,
    taskId: opts.taskId,
    gwSessionId: opts.gwSessionId,
    maxTokens: 40,
    messages: [
      {
        role: 'system',
        content:
          '根据用户消息用中文生成简短会话标题（不超过18字），概括用户意图。只输出标题，不要引号或解释。',
      },
      { role: 'user', content: opts.firstUserMessage },
    ],
  })
  const t = result.content.trim().replace(/^["「『]|["」』]$/g, '')
  const title = t.length <= TITLE_MAX ? t : `${t.slice(0, TITLE_MAX)}…`
  return { title, usage: result.usage, resolvedModel: result.resolvedModel }
}

export async function summarizeConversation(opts: {
  apiKey: string
  model: string
  messages: ChatCompletionMessage[]
  taskId: string
  gwSessionId: string | null
}): Promise<SummarizeResult> {
  const dialog = messagesForApi(opts.messages)
    .filter((m) => m.role === 'user' || m.role === 'assistant')
    .map((m) => `${m.role === 'user' ? '用户' : '助手'}: ${m.content}`)
    .join('\n\n')

  const result = await chatCompletion({
    apiKey: opts.apiKey,
    model: opts.model,
    taskId: opts.taskId,
    gwSessionId: opts.gwSessionId,
    maxTokens: 1024,
    messages: [
      {
        role: 'system',
        content: `你是会话助手。根据对话生成简洁中文总结。
输出格式（严格遵循）：
第一行：标题：（不超过18字的会话标题）
空一行
然后输出总结正文（3-8句话，涵盖主要话题、结论与待办）。`,
      },
      { role: 'user', content: dialog },
    ],
  })
  const parsed = parseSummarizeOutput(result.content)
  return { ...parsed, usage: result.usage, resolvedModel: result.resolvedModel }
}

export function safeExportFilename(title: string): string {
  const base = title.replace(/[\\/:*?"<>|]/g, '_').trim() || '对话'
  const date = new Date().toISOString().slice(0, 10)
  return `${base}_${date}.txt`
}
