// liveStreamColors — single source of truth for swim-lane palette.
// Model categories are now based on original model creators/vendors for international compatibility.

export const MODEL_COLORS: Record<string, string> = {
  openai: '#4d8df7',      // OpenAI (GPT, O1, O3, O4)
  anthropic: '#a379f7',   // Anthropic (Claude)
  google: '#34a853',      // Google (Gemini, PaLM)
  alibaba: '#ff6a00',     // Alibaba (Qwen)
  zhipu: '#5470c6',       // Zhipu (GLM)
  deepseek: '#9333ea',    // DeepSeek
  bytedance: '#00d4ff',   // ByteDance (Doubao)
  baidu: '#2932e1',       // Baidu (ERNIE)
  moonshot: '#fbbf24',    // Moonshot
  '01ai': '#ec4899',      // 01.AI (Yi)
  baichuan: '#10b981',    // Baichuan
  meta: '#0668e1',        // Meta (Llama)
  mistral: '#f97316',     // Mistral AI
  microsoft: '#00a4ef',   // Microsoft (Phi)
  other: '#8b949e',       // Other/Unknown
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
  google: 'Google',
  alibaba: 'Alibaba',
  zhipu: 'Zhipu',
  deepseek: 'DeepSeek',
  bytedance: 'ByteDance',
  baidu: 'Baidu',
  moonshot: 'Moonshot',
  '01ai': '01.AI',
  baichuan: 'Baichuan',
  meta: 'Meta',
  mistral: 'Mistral AI',
  microsoft: 'Microsoft',
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
