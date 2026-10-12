import installer from './clientConfigInstaller.py?raw'
import { req } from '../api/_core'
import { exportFile } from '../utils/exportFile'

export type ToolId = 'zcode' | 'opencode' | 'minimax_code' | 'cursor' | 'cherry_studio' | 'roocode'
export type OS = 'macos' | 'windows' | 'linux'
export type ModelScope = 'featured' | 'all' | 'custom'

export const TOOLS: { id: ToolId; name: string; icon: string; description: string }[] = [
  { id: 'zcode', name: 'ZCode', icon: '🔮', description: 'OpenAI 兼容供应商；合并写入现有配置' },
  { id: 'opencode', name: 'OpenCode v1', icon: '⚡', description: 'OpenCode v1 provider 配置；v2 需手动迁移' },
  { id: 'minimax_code', name: 'MiniMax Code / CC Switch', icon: '🧠', description: '写入 MiniMax Code 自定义供应商；CC Switch 需在应用内导入并确认' },
  { id: 'cursor', name: 'Cursor', icon: '⌨️', description: 'AI-first 代码编辑器，支持 OpenAI 兼容 API' },
  { id: 'cherry_studio', name: 'Cherry Studio', icon: '🍒', description: '多模型桌面客户端，支持批量导入提供商' },
  { id: 'roocode', name: 'Roo Code / VS Code', icon: '🧩', description: '在 Roo Code 设置面板选择 OpenAI Compatible' },
]

export const OS_INFO: Record<OS, { label: string; paths: Record<ToolId, string> }> = {
  macos: {
    label: 'macOS',
    paths: {
      zcode: '~/.zcode/v2/config.json',
      opencode: '~/.config/opencode/opencode.json',
      minimax_code: '~/.minimax/config.yaml',
      cursor: 'Cursor Settings → Models（应用内配置）',
      cherry_studio: '应用内手动导入（不支持脚本）',
      roocode: 'Roo Code 设置面板（应用内配置）',
    },
  },
  windows: {
    label: 'Windows',
    paths: {
      zcode: '%APPDATA%/zcode/v2/config.json',
      opencode: '%USERPROFILE%/.config/opencode/opencode.json',
      minimax_code: '%USERPROFILE%/.minimax/config.yaml',
      cursor: 'Cursor Settings → Models（应用内配置）',
      cherry_studio: '应用内手动导入（不支持脚本）',
      roocode: 'Roo Code 设置面板（应用内配置）',
    },
  },
  linux: {
    label: 'Linux',
    paths: {
      zcode: '~/.zcode/v2/config.json',
      opencode: '~/.config/opencode/opencode.json',
      minimax_code: '~/.minimax/config.yaml',
      cursor: 'Cursor Settings → Models（应用内配置）',
      cherry_studio: '应用内手动导入（不支持脚本）',
      roocode: 'Roo Code 设置面板（应用内配置）',
    },
  },
}

export interface ToolTemplate {
  providerUUID: string
  providerName: string
  baseURL: string
  configPath: Record<OS, string>
  supportsScript: boolean
}

function newProviderUUID(): string {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (token) => {
    const random = Math.floor(Math.random() * 16)
    return (token === 'x' ? random : (random & 0x3) | 0x8).toString(16)
  })
}

export const TOOL_TEMPLATES: Record<ToolId, ToolTemplate> = {
  zcode: {
    providerUUID: newProviderUUID(),
    providerName: 'itestu',
    baseURL: '',
    configPath: {
      macos: '~/.zcode/v2/config.json',
      windows: '%APPDATA%/zcode/v2/config.json',
      linux: '~/.zcode/v2/config.json',
    },
    supportsScript: true,
  },
  opencode: {
    providerUUID: newProviderUUID(),
    providerName: 'itestu',
    baseURL: '',
    configPath: {
      macos: '~/.config/opencode/opencode.json',
      windows: '%USERPROFILE%/.config/opencode/opencode.json',
      linux: '~/.config/opencode/opencode.json',
    },
    supportsScript: true,
  },
  minimax_code: {
    providerUUID: '',
    providerName: 'itestu',
    baseURL: '',
    configPath: {
      macos: '~/.minimax/config.yaml',
      windows: '%USERPROFILE%/.minimax/config.yaml',
      linux: '~/.minimax/config.yaml',
    },
    supportsScript: true,
  },
  cursor: {
    providerUUID: '',
    providerName: 'itestu',
    baseURL: '',
    configPath: {
      macos: 'Settings → Models → Add Custom Model',
      windows: 'Settings → Models → Add Custom Model',
      linux: 'Settings → Models → Add Custom Model',
    },
    supportsScript: false,
  },
  cherry_studio: {
    providerUUID: '',
    providerName: 'itestu',
    baseURL: '',
    configPath: {
      macos: '设置 → 模型服务 → 添加自定义',
      windows: '设置 → 模型服务 → 添加自定义',
      linux: '设置 → 模型服务 → 添加自定义',
    },
    supportsScript: false,
  },
  roocode: {
    providerUUID: '',
    providerName: 'itestu',
    baseURL: '',
    configPath: {
      macos: 'Roo Code 设置面板',
      windows: 'Roo Code 设置面板',
      linux: 'Roo Code 设置面板',
    },
    supportsScript: false,
  },
}

export interface ModelItem {
  id: string
  name: string
  contextWindow?: number
  outputLimit?: number
}

function buildZCodeOpenCodeModels(models: string[], modelInfo: ModelItem[] = [], completeLimit = false): Record<string, any> {
  const result: Record<string, any> = Object.create(null)
  const byId = new Map(modelInfo.map((item) => [item.id, item]))
  for (const m of models) {
    const info = byId.get(m)
    const limit: Record<string, number> = {}
    if (info?.contextWindow && info.contextWindow > 0) limit.context = info.contextWindow
    if (info?.outputLimit && info.outputLimit > 0) limit.output = info.outputLimit
    result[m] = {
      name: m,
      ...((completeLimit ? limit.context && limit.output : Object.keys(limit).length > 0) ? { limit } : {}),
      modalities: { input: ['text'], output: ['text'] },
    }
  }
  return result
}

export function renderZCodeConfig(apiKey: string, models: string[], baseURL: string, modelInfo: ModelItem[] = [], providerName = 'itestu'): any {
  const tpl = TOOL_TEMPLATES.zcode
  return {
    $schema: 'https://opencode.ai/config.json',
    provider: {
      [tpl.providerUUID]: {
        name: validateProviderName(providerName),
        kind: 'openai-compatible',
        options: {
          apiKey: apiKey,
          baseURL,
          apiKeyRequired: true,
        },
        source: 'custom',
        models: buildZCodeOpenCodeModels(models, modelInfo),
      },
    },
  }
}

export function renderOpenCodeConfig(apiKey: string, models: string[], baseURL: string, modelInfo: ModelItem[] = [], providerName = 'itestu'): any {
  return {
    $schema: 'https://opencode.ai/config.json',
    provider: {
      [validateProviderName(providerName)]: {
        name: validateProviderName(providerName),
        npm: '@ai-sdk/openai-compatible',
        options: {
          apiKey: apiKey,
          baseURL,
        },
        models: buildZCodeOpenCodeModels(models, modelInfo, true),
      },
    },
  }
}

function yamlScalar(value: string): string {
  // JSON double-quoted strings are valid YAML scalars and safely quote model IDs/keys.
  return JSON.stringify(value)
}

export function renderMiniMaxCodeProvider(apiKey: string, models: string[], baseURL: string, modelInfo: ModelItem[] = [], providerName = 'itestu'): string {
  const byId = new Map(modelInfo.map((item) => [item.id, item]))
  const modelBlocks = models.map((model) => {
    const info = byId.get(model)
    const lines = [
      `    ${yamlScalar(model)}:`,
      `      name: ${yamlScalar(model)}`,
      '      enabled: true',
      '      attachment: false',
      '      modalities:',
      '        input:',
      '          - text',
      '        output:',
      '          - text',
    ]
    if (info?.contextWindow && info.contextWindow > 0) {
      lines.push('      limit:', `        context: ${info.contextWindow}`)
    }
    if (info?.outputLimit && info.outputLimit > 0) {
      if (!lines.includes('      limit:')) lines.push('      limit:')
      lines.push(`        output: ${info.outputLimit}`)
    }
    return lines.join('\n')
  }).join('\n')

  return [
    `${validateProviderName(providerName)}:`,
    `  name: ${providerName}`,
    '  kind: custom',
    '  enabled: true',
    '  api: openai-completions',
    '  options:',
    `    baseURL: ${yamlScalar(baseURL)}`,
    '    authMode: api-key',
    `    apiKey: ${yamlScalar(apiKey)}`,
    '  models:',
    modelBlocks,
    '',
  ].join('\n')
}

export function generateShellScript(tool: ToolId, os: OS, configContent: string, _apiKey = '', _baseURL = ''): string {
  if (!TOOL_TEMPLATES[tool].supportsScript) return '# 请按应用内配置步骤操作。\n'
  // Hex encoding keeps user data out of Python syntax and shell/PowerShell delimiters.
  const hex = Array.from(new TextEncoder().encode(configContent), byte => byte.toString(16).padStart(2, '0')).join('')
  const python = `TOOL = ${JSON.stringify(tool)}\nCONFIG_HEX = '${hex}'\n${installer}`
  if (os === 'windows') {
    return `$ErrorActionPreference = 'Stop'\n$python = Get-Command python3 -ErrorAction SilentlyContinue\nif (-not $python) { $python = Get-Command python -ErrorAction SilentlyContinue }\nif (-not $python) { throw '需要 Python 3；MiniMax Code 还需要 PyYAML。' }\n@'\n${python}\n'@ | & $python.Source -\nif ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }\n`
  }
  return `#!/bin/sh\n# Python 3；MiniMax Code 还需要 PyYAML。关闭客户端配置编辑后执行。\npython3 - <<'PY'\n${python}\nPY\n`
}

export function validateProviderName(value: string): string {
  const name = value.trim()
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(name)) throw new Error('供应商名称须为 1–64 个英文字母、数字、下划线或连字符。')
  return name
}

export function normalizeGatewayURL(value: string): string {
  const url = new URL(value.trim())
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new Error('API 地址须为 HTTP(S)，且不能包含账号、查询参数或片段。')
  }
  return url.href.replace(/\/+$/, '')
}

export function getManualSteps(tool: ToolId, os: OS, baseURL: string, models: string[] = [], providerName = 'itestu'): string {
  const modelList = models.map((model) => `   - ${model}`).join('\n') || '   - 先在网关数据库配置特色模型'
  if (tool === 'zcode') {
    return `ZCode 将在 ${OS_INFO[os].paths.zcode} 中增量添加 ${providerName} 供应商。脚本会备份原文件并保留其他供应商。\n\nAPI 地址：${baseURL}\n特色模型：\n${modelList}\n\n写入后重启 ZCode，在模型选择器中选择 ${providerName}。`
  }
  if (tool === 'minimax_code') {
    return `MiniMax Code 将使用 OpenAI Chat Completions 兼容模式。脚本会备份并增量更新 ${OS_INFO[os].paths.minimax_code}，同时遵守 config.yaml.lock。\n\nAPI 地址：${baseURL}\n特色模型：\n${modelList}\n\n保存后在 MiniMax Code 的模型选择器中选择 ${providerName}。CC Switch：在 MiniMax Code 页面使用应用提供的导入/添加操作，并确认供应商、地址和模型列表。它有独立的供应商库，写入 YAML 不等于已导入。`
  }
  if (tool === 'cursor') {
    return `Cursor 配置步骤：

1. 打开 Cursor（Cmd/Ctrl+Shift+J）→ Settings → Models
2. 在 OpenAI API Key 区域填入选定的网关 API Key，并启用 Use OpenAI API Key
3. 启用 Override OpenAI Base URL，填入：${baseURL}
4. 在 Add Custom Model 中逐个添加以下数据库特色模型 ID：
${modelList}
5. 选中一个模型，在本地 Chat 中发送最小请求验证；先用普通 Chat/Ask 模式确认 BYOK 连通，再检查 Agent 等模式。

注意：Cursor 的 OpenAI Key 和 Base URL 是全局设置，不是独立 provider；自定义 Key 只支持标准聊天模型。Cursor 专用补全功能继续使用其内置模型。若模型名与 Cursor 内置项冲突，Cursor 会拒绝重复添加，需确认该内置项是否会使用此 OpenAI Base URL。详见 [Cursor API Keys 文档](https://docs.cursor.com/settings/api-keys)。`
  }
  if (tool === 'opencode') {
    return `OpenCode 会在 ${OS_INFO[os].paths.opencode} 中增量添加 ${providerName} 供应商，并保留其他 provider。\n\nAPI 地址：${baseURL}\n特色模型：\n${modelList}\n\n此脚本面向 OpenCode v1。v2 使用 providers/package/settings 结构，请按官方迁移文档手动配置。写入后重启 OpenCode，并选择 ${providerName} 下的模型验证。`
  }
  if (tool === 'cherry_studio') {
    return `Cherry Studio 配置步骤：

1. 打开 Cherry Studio → 设置 → 模型服务
2. 点击「添加自定义服务商」
3. 名称填「${providerName}」，类型选「OpenAI 兼容」
4. API 地址填：${baseURL}
5. API Key 填入你的 sk-* 密钥
6. 模型列表填入（用英文逗号分隔）：
${models.join(', ')}
7. 保存后在左侧模型列表找到「${providerName}」即可使用`
  }
  if (tool === 'roocode') {
    return `打开 Roo Code 设置面板 → API Provider 选择 OpenAI Compatible。\nBase URL：${baseURL}\nAPI Key：填入选定密钥。\nModel ID：选择下面的一个真实模型 ID（不要填写 auto）：\n${modelList}\n\n保存后验证普通请求和原生工具调用。详见 https://docs.roocode.com/providers/openai-compatible 。`
  }
  return ''
}

export function detectOS(): OS {
  const ua = navigator.userAgent
  if (ua.includes('Mac')) return 'macos'
  if (ua.includes('Win')) return 'windows'
  return 'linux'
}

// 2026-10-05（UI规范 19 §3.1）：走 exportFile 降级链（分享面→壳桥→blob）。
export function downloadFile(content: string, filename: string, mimeType: string = 'text/plain') {
  const blob = new Blob([content], { type: mimeType })
  return exportFile({ filename, blob })
}

export async function auditAction(params: {
  action: string
  tool: string
  os: string
  keyId: number
  modelCount: number
  modelScope: string
}) {
  try {
    await req('POST', '/api/client-configs/audit', {
      action: params.action, tool: params.tool, os: params.os,
      key_id: params.keyId, model_count: params.modelCount, model_scope: params.modelScope,
    })
  } catch {
    // best-effort, non-blocking
  }
}
