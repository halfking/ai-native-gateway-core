/** Client-side vendor / tag helpers aligned with backend catalog package. */

const FAMILY_VENDOR: Record<string, string> = {
  'openai-gpt': 'OpenAI', gpt: 'OpenAI', o3: 'OpenAI', o4: 'OpenAI',
  'anthropic-claude': 'Anthropic', anthropic: 'Anthropic', claude: 'Anthropic',
  'google-gemini': 'Google', gemini: 'Google', gemma: 'Google',
  deepseek: 'DeepSeek',
  qwen: 'Alibaba', qwen2: 'Alibaba', qwen3: 'Alibaba', qwq: 'Alibaba', wan2: 'Alibaba',
  doubao: 'ByteDance',
  'zhipu-glm': 'Zhipu AI', glm: 'Zhipu AI',
  'meta-llama': 'Meta', llama: 'Meta', llama2: 'Meta', llama3: 'Meta',
  minimax: 'MiniMax',
  mimo: '小米', 'xiaomi-mimo': '小米',
  moonshot: 'Moonshot AI', kimi: 'Moonshot AI',
  xai: 'xAI', grok: 'xAI',
  mistral: 'Mistral AI', mixtral: 'Mistral AI',
}

const NAME_PREFIX_VENDOR: [string, string][] = [
  ['minimax-m3', 'MiniMax'], ['minimax', 'MiniMax'],
  ['gpt-', 'OpenAI'], ['claude-', 'Anthropic'], ['gemini-', 'Google'],
  ['qwen', 'Alibaba'], ['deepseek', 'DeepSeek'], ['glm-', 'Zhipu AI'],
  ['llama-', 'Meta'], ['kimi-', 'Moonshot AI'], ['grok-', 'xAI'],
]

export function inferVendorFromName(canonicalName: string): string {
  const n = canonicalName.trim().toLowerCase()
  if (!n) return ''
  for (const [prefix, vendor] of NAME_PREFIX_VENDOR) {
    if (n.startsWith(prefix)) return vendor
  }
  return ''
}

export function resolveVendor(
  canonicalName: string,
  family?: string | null,
  dbVendor?: string | null,
): string {
  const v = dbVendor?.trim()
  if (v) return v
  const fam = family?.trim() ?? ''
  if (fam && FAMILY_VENDOR[fam]) return FAMILY_VENDOR[fam]
  const fromName = inferVendorFromName(canonicalName)
  if (fromName) return fromName
  return fam || '其他'
}

export function normalizeTags(tags: unknown): string[] {
  if (Array.isArray(tags)) {
    return tags.filter((t): t is string => typeof t === 'string')
  }
  if (typeof tags === 'string' && tags.trim()) {
    try {
      const parsed = JSON.parse(tags)
      if (Array.isArray(parsed)) {
        return parsed.filter((t): t is string => typeof t === 'string')
      }
    } catch {
      /* ignore */
    }
  }
  return []
}

const VENDOR_ZH: Record<string, string> = {
  OpenAI: 'OpenAI',
  Anthropic: 'Anthropic',
  Google: 'Google',
  DeepSeek: 'DeepSeek',
  Alibaba: '阿里巴巴',
  ByteDance: '字节跳动',
  'Zhipu AI': '智谱 AI',
  Meta: 'Meta',
  MiniMax: 'MiniMax',
  'Moonshot AI': '月之暗面',
  'Mistral AI': 'Mistral',
  xAI: 'xAI',
  小米: '小米',
  其他: '其他',
}

export function vendorLabelZh(vendor: string): string {
  return VENDOR_ZH[vendor] ?? vendor
}

/** Match model row against picker value + optional free-text (family / tags). */
export function matchesModelCatalogSearch(
  canonicalName: string,
  displayName: string,
  vendor: string,
  pickedModel: string,
  textSearch: string,
  extras: string[] = [],
): boolean {
  const pick = pickedModel.trim().toLowerCase()
  if (pick) {
    const hay = [canonicalName, displayName, vendor, ...extras].join(' ').toLowerCase()
    if (!hay.includes(pick)) return false
  }
  const q = textSearch.trim().toLowerCase()
  if (!q) return true
  const hay = [canonicalName, displayName, vendor, vendorLabelZh(vendor), ...extras]
    .join(' ')
    .toLowerCase()
  return hay.includes(q)
}
