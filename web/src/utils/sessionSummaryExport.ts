import type { SessionSummaryResponse } from '../api/logs'

export function buildSessionSummaryExport(
  data: SessionSummaryResponse,
  headings: { docTitle: string; summaryHeading: string; keyPointsHeading: string },
): string {
  const lines: string[] = []
  lines.push(headings.docTitle)
  lines.push('')
  lines.push(`- Session ID: ${data.meta.session_id}`)
  lines.push(`- 时间范围: ${data.meta.data_from} ~ ${data.meta.data_to}`)
  lines.push(`- 日志条数: ${data.meta.log_count}`)
  lines.push(`- 生成时间: ${data.meta.generated_at}`)
  if (data.meta.model) lines.push(`- 模型: ${data.meta.model}`)
  lines.push('')
  lines.push(headings.summaryHeading)
  lines.push(data.summary)
  if (data.key_points?.length) {
    lines.push('')
    lines.push(headings.keyPointsHeading)
    for (const p of data.key_points) lines.push(`- ${p}`)
  }
  return lines.join('\n')
}

export function downloadSessionSummaryExport(
  data: SessionSummaryResponse,
  headings: { docTitle: string; summaryHeading: string; keyPointsHeading: string },
  format: 'md' | 'txt' = 'md',
): void {
  const content = buildSessionSummaryExport(data, headings)
  const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  const day = new Date().toISOString().slice(0, 10)
  a.href = url
  a.download = `session-summary-${data.meta.session_id}-${day}.${format}`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}
