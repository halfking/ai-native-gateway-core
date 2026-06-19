import { ref, computed } from 'vue'

export type ToolId = 'zcode' | 'opencode' | 'cursor' | 'cherry_studio' | 'roocode'
export type OS = 'macos' | 'windows' | 'linux'
export type ModelScope = 'featured' | 'all' | 'custom'

export const TOOLS: { id: ToolId; name: string; icon: string; description: string }[] = [
  { id: 'zcode', name: 'ZCode', icon: '🔮', description: '智谱 AI 编程 IDE，基于 OpenCode 配置 schema' },
  { id: 'opencode', name: 'OpenCode', icon: '⚡', description: '终端 AI 编码 agent（opencode.ai）' },
  { id: 'cursor', name: 'Cursor', icon: '⌨️', description: 'AI-first 代码编辑器，支持 OpenAI 兼容 API' },
  { id: 'cherry_studio', name: 'Cherry Studio', icon: '🍒', description: '多模型桌面客户端，支持批量导入提供商' },
  { id: 'roocode', name: 'Roo Code / VS Code', icon: '🧩', description: 'VS Code + Roo Code 扩展，配置 roo-cline' },
]

export const OS_INFO: Record<OS, { label: string; paths: Record<ToolId, string> }> = {
  macos: {
    label: 'macOS',
    paths: {
      zcode: '~/.zcode/v2/config.json',
      opencode: '~/.config/opencode/config.json',
      cursor: '~/.cursor/mcp.json（Models 设置）',
      cherry_studio: '应用内手动导入（不支持脚本）',
      roocode: '~/.vscode/settings.json（ROO_CLINE_* 设置）',
    },
  },
  windows: {
    label: 'Windows',
    paths: {
      zcode: '%APPDATA%/zcode/v2/config.json',
      opencode: '%APPDATA%/opencode/config.json',
      cursor: '%APPDATA%/Cursor/User/mcp.json（Models 设置）',
      cherry_studio: '应用内手动导入（不支持脚本）',
      roocode: '%APPDATA%/Code/User/settings.json',
    },
  },
  linux: {
    label: 'Linux',
    paths: {
      zcode: '~/.zcode/v2/config.json',
      opencode: '~/.config/opencode/config.json',
      cursor: '~/.config/Cursor/mcp.json（Models 设置）',
      cherry_studio: '应用内手动导入（不支持脚本）',
      roocode: '~/.config/Code/User/settings.json',
    },
  },
}

export const FEATURED_MODELS = [
  'glm-4-flash',
  'glm-5.1',
  'minimax-m3',
  'claude-sonnet-4-6',
  'deepseek-v4-pro',
  'gpt-5.4',
  'auto',
  'minimax-m2.7-highspeed',
]

export interface ToolTemplate {
  providerUUID: string
  providerName: string
  baseURL: string
  configPath: Record<OS, string>
  supportsScript: boolean
}

export const TOOL_TEMPLATES: Record<ToolId, ToolTemplate> = {
  zcode: {
    providerUUID: 'kx-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 6),
    providerName: 'kaixuan-gateway',
    baseURL: 'https://llmgo.kxpms.cn/v1',
    configPath: {
      macos: '~/.zcode/v2/config.json',
      windows: '%APPDATA%/zcode/v2/config.json',
      linux: '~/.zcode/v2/config.json',
    },
    supportsScript: true,
  },
  opencode: {
    providerUUID: 'kx-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 6),
    providerName: 'kaixuan-gateway',
    baseURL: 'https://llmgo.kxpms.cn/v1',
    configPath: {
      macos: '~/.config/opencode/config.json',
      windows: '%APPDATA%/opencode/config.json',
      linux: '~/.config/opencode/config.json',
    },
    supportsScript: true,
  },
  cursor: {
    providerUUID: '',
    providerName: 'kaixuan-gateway',
    baseURL: 'https://llmgo.kxpms.cn/v1',
    configPath: {
      macos: 'Settings → Models → Add Custom Model',
      windows: 'Settings → Models → Add Custom Model',
      linux: 'Settings → Models → Add Custom Model',
    },
    supportsScript: false,
  },
  cherry_studio: {
    providerUUID: '',
    providerName: 'kaixuan-gateway',
    baseURL: 'https://llmgo.kxpms.cn/v1',
    configPath: {
      macos: '设置 → 模型服务 → 添加自定义 → 导入 JSON',
      windows: '设置 → 模型服务 → 添加自定义 → 导入 JSON',
      linux: '设置 → 模型服务 → 添加自定义 → 导入 JSON',
    },
    supportsScript: false,
  },
  roocode: {
    providerUUID: '',
    providerName: 'kaixuan-gateway',
    baseURL: 'https://llmgo.kxpms.cn/v1',
    configPath: {
      macos: '~/.vscode/settings.json',
      windows: '%APPDATA%/Code/User/settings.json',
      linux: '~/.config/Code/User/settings.json',
    },
    supportsScript: true,
  },
}

export interface ModelItem {
  id: string
  name: string
  contextWindow?: number
  outputLimit?: number
}

function buildZCodeOpenCodeModels(models: string[]): Record<string, any> {
  const result: Record<string, any> = {}
  for (const m of models) {
    result[m] = {
      name: m,
      limit: { context: 200000, output: 64000 },
      modalities: { input: ['text'], output: ['text'] },
    }
  }
  return result
}

export function renderZCodeConfig(apiKey: string, models: string[]): any {
  const tpl = TOOL_TEMPLATES.zcode
  return {
    $schema: 'https://opencode.ai/config.json',
    provider: {
      [tpl.providerUUID]: {
        name: tpl.providerName,
        kind: 'openai-compatible',
        options: {
          apiKey: apiKey,
          baseURL: tpl.baseURL,
          apiKeyRequired: true,
        },
        source: 'custom',
        models: buildZCodeOpenCodeModels(models),
      },
    },
  }
}

export function renderOpenCodeConfig(apiKey: string, models: string[]): any {
  const tpl = TOOL_TEMPLATES.opencode
  return {
    $schema: 'https://opencode.ai/config.json',
    provider: {
      [tpl.providerUUID]: {
        name: tpl.providerName,
        kind: 'openai-compatible',
        options: {
          apiKey: apiKey,
          baseURL: tpl.baseURL,
          apiKeyRequired: true,
        },
        source: 'custom',
        models: buildZCodeOpenCodeModels(models),
      },
    },
  }
}

export function renderCherryStudioConfig(apiKey: string, models: string[]): any {
  return {
    name: 'kaixuan-gateway',
    api_base: 'https://llmgo.kxpms.cn/v1',
    api_key: apiKey,
    models: models.map(m => ({ name: m, label: m })),
  }
}

export function renderRooCodeSettings(apiKey: string, baseURL: string): any {
  return {
    'roo-cline.openAiApiKey': apiKey,
    'roo-cline.openAiBaseUrl': baseURL,
    'roo-cline.openAiModelId': 'auto',
  }
}

export function generateShellScript(
  tool: ToolId,
  os: OS,
  configContent: string,
  apiKey: string
): string {
  const tpl = TOOL_TEMPLATES[tool]
  const path = tpl.configPath[os]
  const timestamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19)

  if (tool === 'roocode') {
    const settingsPath = os === 'macos'
      ? '~/.vscode/settings.json'
      : os === 'windows'
        ? '$env:APPDATA\\Code\\User\\settings.json'
        : '~/.config/Code/User/settings.json'
    const isWin = os === 'windows'

    if (isWin) {
      return `:: Roo Code / VS Code 配置脚本 (Windows PowerShell)
:: 生成时间: ${new Date().toLocaleString('zh-CN')}

$SettingsPath = "$env:APPDATA\\Code\\User\\settings.json"
$BackupPath = "$SettingsPath.backup.$(Get-Date -Format 'yyyyMMddHHmmss')"

if (Test-Path $SettingsPath) {
    Copy-Item $SettingsPath $BackupPath -Force
    Write-Host "✅ 备份已创建: $BackupPath"
}

$Settings = @{}
if (Test-Path $SettingsPath) {
    $content = Get-Content $SettingsPath -Raw
    if ($content) { $Settings = $content | ConvertFrom-Json -AsHashtable -ErrorAction SilentlyContinue @{} }
}

$Settings["roo-cline.openAiApiKey"] = "${apiKey}"
$Settings["roo-cline.openAiBaseUrl"] = "https://llmgo.kxpms.cn/v1"
$Settings["roo-cline.openAiModelId"] = "auto"

$Settings | ConvertTo-Json -Depth 10 | Set-Content $SettingsPath -Encoding UTF8
Write-Host "✅ 配置已写入: $SettingsPath"
Write-Host "请重启 VS Code / Roo Code 使配置生效。"
`
    } else {
      return `#!/bin/bash
# Roo Code / VS Code 配置脚本 (macOS / Linux)
# 生成时间: ${new Date().toLocaleString('zh-CN')}

SETTINGS_PATH="${settingsPath}"
BACKUP_PATH="${settingsPath}.backup.$(date +%Y%m%d%H%M%S)"

if [ -f "$SETTINGS_PATH" ]; then
    cp "$SETTINGS_PATH" "$BACKUP_PATH"
    echo "✅ 备份已创建: $BACKUP_PATH"
fi

# 读取现有配置或创建空对象
if [ -f "$SETTINGS_PATH" ]; then
    SETTINGS=$(cat "$SETTINGS_PATH")
else
    SETTINGS="{}"
fi

# 追加/更新 roo-cline 配置（使用 Python 处理 JSON）
python3 -c "
import json, sys, os

path = os.path.expanduser('${settingsPath}')
settings = {}
if os.path.exists(path):
    with open(path) as f:
        try: settings = json.load(f)
        except: settings = {}

settings['roo-cline.openAiApiKey'] = '${apiKey}'
settings['roo-cline.openAiBaseUrl'] = 'https://llmgo.kxpms.cn/v1'
settings['roo-cline.openAiModelId'] = 'auto'

with open(path, 'w') as f:
    json.dump(settings, f, indent=2, ensure_ascii=False)
print('✅ 配置已写入:', path)
"
echo "请重启 VS Code / Roo Code 使配置生效。"
`
    }
  }

  // ZCode / OpenCode shell script (macOS/Linux)
  const configPath = path.replace('~/', '$HOME/').replace('%APPDATA%', '$APPDATA')
  const isWin = os === 'windows'
  const dirPart = isWin ? '%APPDATA%\\\\zcode\\\\v2' : '$HOME/.zcode/v2'
  const filePart = isWin ? '%APPDATA%\\\\zcode\\\\v2\\\\config.json' : '$HOME/.zcode/v2/config.json'

  if (isWin) {
    return `:: ZCode / OpenCode 配置脚本 (Windows PowerShell)
:: 生成时间: ${new Date().toLocaleString('zh-CN')}
:: 路径: ${path}

$CONFIG_DIR = "${dirPart.replace('\\\\', '\\\\\\\\')}"
$CONFIG_FILE = "${filePart.replace('\\\\', '\\\\\\\\')}"
$BACKUP_FILE = "$CONFIG_FILE.backup.$(Get-Date -Format 'yyyyMMddHHmmss')"

New-Item -ItemType Directory -Force -Path $CONFIG_DIR | Out-Null

if (Test-Path $CONFIG_FILE) {
    Copy-Item $CONFIG_FILE $BACKUP_FILE -Force
    Write-Host "✅ 备份已创建: $BACKUP_FILE"
}

$CONFIG_CONTENT = @'
${JSON.stringify(JSON.parse(configContent), null, 2)}
'@

$CONFIG_CONTENT | Set-Content $CONFIG_FILE -Encoding UTF8
Write-Host "✅ 配置已写入: $CONFIG_FILE"
Write-Host "请重启 ZCode 使配置生效。"
`
  } else {
    return `#!/bin/bash
# ZCode / OpenCode 配置脚本 (macOS / Linux)
# 生成时间: ${new Date().toLocaleString('zh-CN')}
# 路径: ${path}

CONFIG_DIR="${path.replace('~/', '$HOME/').replace('/.zcode/', '/.zcode/v2/').split('/').slice(0, -1).join('/') || '$HOME/.zcode/v2'}"
CONFIG_FILE="${path.replace('~/', '$HOME/')}"
BACKUP_FILE="$CONFIG_FILE.backup.$(date +%Y%m%d%H%M%S)"

mkdir -p "$CONFIG_DIR"

if [ -f "$CONFIG_FILE" ]; then
    cp "$CONFIG_FILE" "$BACKUP_FILE"
    echo "✅ 备份已创建: $BACKUP_FILE"
fi

cat > "$CONFIG_FILE" << 'ZCODE_CONFIG'
${JSON.stringify(JSON.parse(configContent), null, 2)}
ZCODE_CONFIG

echo "✅ 配置已写入: $CONFIG_FILE"
echo "请重启 ZCode / OpenCode 使配置生效。"
`
  }
}

export function getManualSteps(tool: ToolId, os: OS): string {
  const baseURL = 'https://llmgo.kxpms.cn/v1'
  if (tool === 'cursor') {
    return `Cursor 配置步骤：

1. 打开 Cursor（Cmd/Ctrl+Shift+J）→ Settings → Models
2. 找到「OpenAI API Key」填入你的 sk-* 密钥
3. 开启「Override OpenAI Base URL」，填入：${baseURL}
4. 点击「Verify」验证连通性
5. 在「Add Custom Model」中添加以下模型 ID（按需选择）：
   - glm-4-flash / glm-5.1 / minimax-m3 / claude-sonnet-4-6
   - deepseek-v4-pro / gpt-5.4 / auto / minimax-m2.7-highspeed
6. 可选 Header：X-Client-Profile: cursor`
  }
  if (tool === 'cherry_studio') {
    return `Cherry Studio 配置步骤：

1. 打开 Cherry Studio → 设置 → 模型服务
2. 点击「添加自定义服务商」
3. 名称填「开轩网关」，类型选「OpenAI 兼容」
4. API 地址填：${baseURL}
5. API Key 填入你的 sk-* 密钥
6. 模型列表填入（用英文逗号分隔）：
   glm-4-flash, glm-5.1, minimax-m3, claude-sonnet-4-6, deepseek-v4-pro, auto
7. 保存后在左侧模型列表找到「开轩网关」即可使用`
  }
  if (tool === 'roocode') {
    const path = OS_INFO[os].paths.roocode
    return `Roo Code / VS Code 配置步骤：

1. 打开 VS Code → 扩展 → 搜索安装「Roo Code」
2. 打开 VS Code 设置（settings.json）
3. 找到以下 3 个 key 填入对应值：
   - roo-cline.openAiApiKey：你的 sk-* 密钥
   - roo-cline.openAiBaseUrl：${baseURL}
   - roo-cline.openAiModelId：auto（支持自动路由）
4. 或者直接在 VS Code 中使用命令面板（Cmd+Shift+P）运行：
   Roo Code: Open Settings 手动编辑 JSON
5. 建议同时安装「Roo Cline」扩展以获得更好体验`
  }
  return ''
}

export function detectOS(): OS {
  const ua = navigator.userAgent
  if (ua.includes('Mac')) return 'macos'
  if (ua.includes('Win')) return 'windows'
  return 'linux'
}

export function downloadFile(content: string, filename: string, mimeType: string = 'text/plain') {
  const blob = new Blob([content], { type: mimeType })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
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
    await fetch('/api/client-configs/audit', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(params),
    })
  } catch {
    // best-effort, non-blocking
  }
}
