/** Client-side vendor / tag helpers aligned with backend catalog package. */

const FAMILY_VENDOR: Record<string, string> = {
  'openai-gpt': 'OpenAI', gpt: 'OpenAI', o3: 'OpenAI', o4: 'OpenAI',
  'openai-embedding': 'OpenAI', 'openai-image': 'OpenAI', 'openai-audio': 'OpenAI', codex: 'OpenAI',
  'anthropic-claude': 'Anthropic', anthropic: 'Anthropic', claude: 'Anthropic',
  'google-gemini': 'Google', gemini: 'Google', gemma: 'Google',
  codegemma: 'Google', recurrentgemma: 'Google', diffusiongemma: 'Google', 'google-palm': 'Google', deplot: 'Google', lyria: 'Google',
  deepseek: 'DeepSeek',
  qwen: 'Alibaba', qwen2: 'Alibaba', qwen2.5: 'Alibaba', qwen3: 'Alibaba', 'qwen3.5': 'Alibaba', 'qwen3.6': 'Alibaba', 'qwen3.7': 'Alibaba', 'qwen3.8': 'Alibaba', qwq: 'Alibaba', wan2: 'Alibaba', 'wan2.6': 'Alibaba',
  doubao: 'ByteDance', seed: 'ByteDance',
  'zhipu-glm': 'Zhipu AI', glm: 'Zhipu AI', glm5.2: 'Zhipu AI', chatglm: 'Zhipu AI',
  'meta-llama': 'Meta', llama: 'Meta', llama2: 'Meta', llama3: 'Meta', codellama: 'Meta',
  minimax: 'MiniMax', 'abab5.5': 'MiniMax', 'abab6.5s': 'MiniMax',
  mimo: '小米', 'xiaomi-mimo': '小米',
  moonshot: 'Moonshot AI', kimi: 'Moonshot AI',
  xai: 'xAI', grok: 'xAI',
  mistral: 'Mistral AI', mixtral: 'Mistral AI', devstral: 'Mistral AI', voxtral: 'Mistral AI',
  nvidia: 'NVIDIA', nemotron: 'NVIDIA', nv: 'NVIDIA', 'nvidia-nemotron': 'NVIDIA', nvclip: 'NVIDIA', neva: 'NVIDIA', riva: 'NVIDIA', nemoretriever: 'NVIDIA', cosmos: 'NVIDIA', vila: 'NVIDIA',
  hunyuan: 'Tencent', ernie: 'Baidu', pangu: 'Huawei', spark: 'iFlytek', longcat: 'Meituan', ling: 'InclusionAI', bge: 'BAAI', youdao: 'Youdao', dots: 'rednote', kat: 'Kwaipilot',
  'naver-hyperclova': 'Naver', starcoder2: 'BigCode', 'bigscience-bloom': 'BigScience', olmo: 'AI2', jamba: 'AI21', falcon: 'TII', dbrx: 'Databricks', arctic: 'Snowflake', palmyra: 'Writer', stability: 'Stability AI', hermes: 'Nous Research', solar: 'Upstage', sakana: 'Sakana AI', sarvam: 'Sarvam AI', stockmark: 'Stockmark', eleutherai: 'EleutherAI', lfm: 'Liquid AI', mercury: 'Inception', fuyu: 'Adept', zamba2: 'Zyphra', sea: 'AI Singapore', morph: 'Morph', relace: 'Relace', reka: 'Reka', rinna: 'rinna',
  l3: 'Community', 'l3.1': 'Community', 'l3.3': 'Community', dolphin: 'Community', mythomax: 'Community', magnum: 'Community', rocinante: 'Community', remm: 'Community', cydonia: 'Community', unslopnemo: 'Community', allamoe: 'Community', dracarys: 'Community', skyfall: 'Community',
  wizardlm: 'Microsoft', kosmos: 'Microsoft',
  'perplexity-sonar': 'Perplexity',
  cursor: 'Cursor',
}

// 顺序敏感：{"palmyra"} 必须在 {"palm"} 之前；与后端 catalog/display.go 保持一致。
const NAME_PREFIX_VENDOR: [string, string][] = [
  ['minimax-m3', 'MiniMax'], ['minimax', 'MiniMax'], ['abab', 'MiniMax'],
  ['gpt-', 'OpenAI'], ['text-embedding-', 'OpenAI'], ['chatgpt', 'OpenAI'], ['dall-e', 'OpenAI'], ['tts-', 'OpenAI'], ['whisper', 'OpenAI'], ['davinci', 'OpenAI'], ['codex', 'OpenAI'], ['o1', 'OpenAI'], ['o3', 'OpenAI'], ['o4', 'OpenAI'],
  ['claude-', 'Anthropic'], ['gemini-', 'Google'], ['gemma-', 'Google'],
  ['codegemma', 'Google'], ['recurrentgemma', 'Google'], ['diffusiongemma', 'Google'], ['deplot', 'Google'], ['lyria', 'Google'],
  ['qwen', 'Alibaba'], ['qwq', 'Alibaba'], ['wan2', 'Alibaba'],
  ['doubao', 'ByteDance'], ['skylark', 'ByteDance'], ['seed', 'ByteDance'], ['ui-tars', 'ByteDance'], ['longcat', 'Meituan'],
  ['deepseek', 'DeepSeek'], ['glm', 'Zhipu AI'], ['chatglm', 'Zhipu AI'],
  ['llama-', 'Meta'], ['llama2-', 'Meta'], ['llama3-', 'Meta'], ['codellama', 'Meta'], ['llama', 'Meta'],
  ['mimo-', '小米'], ['kimi-', 'Moonshot AI'], ['moonshot-', 'Moonshot AI'], ['grok-', 'xAI'],
  ['mistral', 'Mistral AI'], ['mixtral', 'Mistral AI'], ['codestral', 'Mistral AI'], ['ministral', 'Mistral AI'], ['devstral', 'Mistral AI'], ['voxtral', 'Mistral AI'], ['pixtral', 'Mistral AI'], ['mathstral', 'Mistral AI'],
  ['step-', 'StepFun'], ['baichuan', 'Baichuan'], ['yi-', '01.AI'],
  ['sonar', 'Perplexity'], ['sensenova', '商汤'], ['sensechat', '商汤'],
  ['command-', 'Cohere'], ['aya-', 'Cohere'],
  ['nemo', 'NVIDIA'], ['nv-', 'NVIDIA'], ['nvclip', 'NVIDIA'], ['neva', 'NVIDIA'], ['riva', 'NVIDIA'], ['cosmos', 'NVIDIA'], ['vila', 'NVIDIA'],
  ['phi-', 'Microsoft'], ['wizardlm', 'Microsoft'], ['kosmos', 'Microsoft'],
  ['palmyra', 'Writer'], ['palm', 'Google'],
  ['hunyuan', 'Tencent'], ['ernie', 'Baidu'], ['pangu', 'Huawei'], ['spark', 'iFlytek'], ['youdao', 'Youdao'],
  ['bge-', 'BAAI'], ['ling-', 'InclusionAI'], ['kat-', 'Kwaipilot'], ['dots', 'rednote'],
  ['hyperclova', 'Naver'], ['sea-lion', 'AI Singapore'], ['zamba', 'Zyphra'],
  ['jamba', 'AI21'], ['falcon', 'TII'], ['dbrx', 'Databricks'], ['granite', 'IBM'],
  ['olmo', 'AI2'], ['tulu', 'AI2'], ['starcoder', 'BigCode'], ['bloom', 'BigScience'],
  ['solar', 'Upstage'], ['hermes', 'Nous Research'], ['sakana', 'Sakana AI'], ['sarvam', 'Sarvam AI'],
  ['stockmark', 'Stockmark'], ['reka', 'Reka'], ['rinna', 'rinna'], ['mercury', 'Inception'], ['lfm', 'Liquid AI'], ['fuyu', 'Adept'],
  ['stable-diffusion', 'Stability AI'], ['stability', 'Stability AI'], ['arctic', 'Snowflake'],
  ['morph-', 'Morph'], ['relace-', 'Relace'],
  ['l3', 'Community'], ['dolphin', 'Community'], ['mythomax', 'Community'], ['magnum', 'Community'],
  ['rocinante', 'Community'], ['remm-', 'Community'], ['cydonia', 'Community'], ['unslop', 'Community'],
  ['allamoe', 'Community'], ['dracarys', 'Community'], ['skyfall', 'Community'],
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
  // 2026-09-07 (676): 默认厂商识别扩展的新厂商中文标签。
  NVIDIA: '英伟达',
  Baidu: '百度',
  Tencent: '腾讯',
  Huawei: '华为',
  iFlytek: '科大讯飞',
  Meituan: '美团',
  InclusionAI: '蚂蚁集团',
  BAAI: '智源 AI',
  Youdao: '网易有道',
  rednote: '小红书 hi lab',
  Kwaipilot: '快手 Kwaipilot',
  Community: '社区模型',
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
