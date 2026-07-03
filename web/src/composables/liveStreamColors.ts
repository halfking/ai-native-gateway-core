// liveStreamColors — single source of truth for swim-lane palette.

export const MODEL_COLORS: Record<string, string> = {
  openai: '#4d8df7',
  anthropic: '#a379f7',
  domestic: '#f97316',
  oss: '#10b981',
  other: '#8b949e',
}

export const STATUS_COLORS: Record<string, string> = {
  success: '#16a34a',
  in_progress: '#d97706',
  failure: '#e74545',
}

export const STATUS_BORDER_COLORS: Record<string, string> = {
  success: 'rgba(34, 197, 94, 0.85)',
  in_progress: 'rgba(245, 158, 11, 0.95)',
  failure: 'rgba(239, 68, 68, 0.95)',
  default: 'rgba(139, 148, 158, 0.4)',
}

export const STATUS_BORDER_WIDTHS: Record<string, string> = {
  success: '2',
  in_progress: '2',
  failure: '3',
  default: '2',
}

export const MODEL_FAMILY_LABELS: Record<string, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  domestic: 'Domestic',
  oss: 'Open Source',
  other: 'Other',
}

export const STATUS_LABELS: Record<string, string> = {
  success: 'Success',
  in_progress: 'In progress',
  failure: 'Failure',
}

export function getModelCategoryColor(cat: string | undefined | null): string {
  if (!cat) return MODEL_COLORS.other
  return MODEL_COLORS[cat] || MODEL_COLORS.other
}

export function getStatusColor(status: string | undefined | null): string {
  if (!status) return STATUS_COLORS.in_progress
  return STATUS_COLORS[status] || STATUS_COLORS.in_progress
}
