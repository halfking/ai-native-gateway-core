import { ref, computed } from 'vue'
import { localeRef } from '../i18n'
import { formatDateTime } from '../utils/datetime'
import { exportFile } from '../utils/exportFile'

export type ToolId = 'zcode' | 'opencode' | 'minimax_code' | 'cursor' | 'cherry_studio' | 'roocode'
export type OS = 'macos' | 'windows' | 'linux'
export type ModelScope = 'featured' | 'all' | 'custom'

export const TOOLS: { id: ToolId; name: string; icon: string; description: string }[] = [
  { id: 'zcode', name: 'ZCode', icon: '🔮', description: 'OpenAI 兼容供应商；合并写入现有配置' },
  { id: 'opencode', name: 'OpenCode', icon: '⚡', description: '终端 AI 编码 agent（opencode.ai）' },
  { id: 'minimax_code', name: 'MiniMax Code / CC Switch', icon: '🧠', description: '写入 MiniMax Code 自定义供应商；CC Switch 会同步识别' },
  { id: 'cursor', name: 'Cursor', icon: '⌨️', description: 'AI-first 代码编辑器，支持 OpenAI 兼容 API' },
  { id: 'cherry_studio', name: 'Cherry Studio', icon: '🍒', description: '多模型桌面客户端，支持批量导入提供商' },
  { id: 'roocode', name: 'Roo Code / VS Code', icon: '🧩', description: 'VS Code + Roo Code 扩展，配置 roo-cline' },
]

export const OS_INFO: Record<OS, { label: string; paths: Record<ToolId, string> }> = {
  macos: {
    label: 'macOS',
    paths: {
      zcode: '~/.zcode/v2/config.json',
      opencode: '~/.config/opencode/opencode.json',
      minimax_code: '~/.minimax/config.yaml',
      cursor: '~/.cursor/mcp.json（Models 设置）',
      cherry_studio: '应用内手动导入（不支持脚本）',
      roocode: '~/.vscode/settings.json（ROO_CLINE_* 设置）',
    },
  },
  windows: {
    label: 'Windows',
    paths: {
      zcode: '%APPDATA%/zcode/v2/config.json',
      opencode: '%APPDATA%/opencode/opencode.json',
      minimax_code: '%USERPROFILE%/.minimax/config.yaml',
      cursor: '%APPDATA%/Cursor/User/mcp.json（Models 设置）',
      cherry_studio: '应用内手动导入（不支持脚本）',
      roocode: '%APPDATA%/Code/User/settings.json',
    },
  },
  linux: {
    label: 'Linux',
    paths: {
      zcode: '~/.zcode/v2/config.json',
      opencode: '~/.config/opencode/opencode.json',
      minimax_code: '~/.minimax/config.yaml',
      cursor: '~/.config/Cursor/mcp.json（Models 设置）',
      cherry_studio: '应用内手动导入（不支持脚本）',
      roocode: '~/.config/Code/User/settings.json',
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
    providerName: 'itestuu',
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
    providerName: 'itestuu',
    baseURL: '',
    configPath: {
      macos: '~/.config/opencode/opencode.json',
      windows: '%APPDATA%/opencode/opencode.json',
      linux: '~/.config/opencode/opencode.json',
    },
    supportsScript: true,
  },
  minimax_code: {
    providerUUID: '',
    providerName: 'itestuu',
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
    providerName: 'itestuu',
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
    providerName: 'itestuu',
    baseURL: '',
    configPath: {
      macos: '设置 → 模型服务 → 添加自定义 → 导入 JSON',
      windows: '设置 → 模型服务 → 添加自定义 → 导入 JSON',
      linux: '设置 → 模型服务 → 添加自定义 → 导入 JSON',
    },
    supportsScript: false,
  },
  roocode: {
    providerUUID: '',
    providerName: 'itestuu',
    baseURL: '',
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

function buildZCodeOpenCodeModels(models: string[], modelInfo: ModelItem[] = []): Record<string, any> {
  const result: Record<string, any> = {}
  const byId = new Map(modelInfo.map((item) => [item.id, item]))
  for (const m of models) {
    const info = byId.get(m)
    const limit: Record<string, number> = {}
    if (info?.contextWindow && info.contextWindow > 0) limit.context = info.contextWindow
    if (info?.outputLimit && info.outputLimit > 0) limit.output = info.outputLimit
    result[m] = {
      name: m,
      ...(Object.keys(limit).length > 0 ? { limit } : {}),
      modalities: { input: ['text'], output: ['text'] },
    }
  }
  return result
}

export function renderZCodeConfig(apiKey: string, models: string[], baseURL: string, modelInfo: ModelItem[] = []): any {
  const tpl = TOOL_TEMPLATES.zcode
  return {
    $schema: 'https://opencode.ai/config.json',
    provider: {
      [tpl.providerUUID]: {
        name: tpl.providerName,
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

export function renderOpenCodeConfig(apiKey: string, models: string[], baseURL: string, modelInfo: ModelItem[] = []): any {
  const tpl = TOOL_TEMPLATES.opencode
  return {
    $schema: 'https://opencode.ai/config.json',
    provider: {
      [tpl.providerUUID]: {
        name: tpl.providerName,
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

export function renderCherryStudioConfig(apiKey: string, models: string[], baseURL: string): any {
  return {
    name: 'itestuu',
    api_base: baseURL,
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

function yamlScalar(value: string): string {
  // JSON double-quoted strings are valid YAML scalars and safely quote model IDs/keys.
  return JSON.stringify(value)
}

export function renderMiniMaxCodeProvider(apiKey: string, models: string[], baseURL: string, modelInfo: ModelItem[] = []): string {
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
    'itestuu:',
    '  name: itestuu',
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

export function generateShellScript(
  tool: ToolId,
  os: OS,
  configContent: string,
  apiKey: string,
  baseURL: string,
): string {
  if (tool === 'roocode') {
    const settingsPath = os === 'macos'
      ? '~/.vscode/settings.json'
      : os === 'windows'
        ? '$env:APPDATA\\Code\\User\\settings.json'
        : '~/.config/Code/User/settings.json'
    const isWin = os === 'windows'

    if (isWin) {
      return `:: Roo Code / VS Code 配置脚本 (Windows PowerShell)
:: 生成时间: ${formatDateTime(new Date(), { locale: localeRef.value })}

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
$Settings["roo-cline.openAiBaseUrl"] = "${baseURL}"
$Settings["roo-cline.openAiModelId"] = "auto"

$Settings | ConvertTo-Json -Depth 10 | Set-Content $SettingsPath -Encoding UTF8
Write-Host "✅ 配置已写入: $SettingsPath"
Write-Host "请重启 VS Code / Roo Code 使配置生效。"
`
    } else {
      return `#!/bin/bash
# Roo Code / VS Code 配置脚本 (macOS / Linux)
# 生成时间: ${formatDateTime(new Date(), { locale: localeRef.value })}

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
settings['roo-cline.openAiBaseUrl'] = '${baseURL}'
settings['roo-cline.openAiModelId'] = 'auto'

with open(path, 'w') as f:
    json.dump(settings, f, indent=2, ensure_ascii=False)
print('✅ 配置已写入:', path)
"
echo "请重启 VS Code / Roo Code 使配置生效。"
`
    }
  }

  if (tool === 'cursor' || tool === 'cherry_studio') {
    return '# 此客户端通过应用设置界面配置；请按“手动步骤”操作。\n'
  }

  if (tool === 'minimax_code') {
    const providerContent = configContent
      .replace(/^custom_provider:\s*\r?\n/, '')
      .split(/\r?\n/)
      .map(line => line.startsWith('  ') ? line.slice(2) : line)
      .join('\n')
      .trimEnd()
    const python = `import os, re, shutil, tempfile
from pathlib import Path

provider = ${JSON.stringify(providerContent)}
target = Path.home() / '.minimax' / 'config.yaml'
target.parent.mkdir(parents=True, exist_ok=True)
lock = target.with_name('config.yaml.lock')
try:
    lock.mkdir()
except FileExistsError:
    raise SystemExit('MiniMax Code 配置锁已存在；关闭配置编辑后重试。')
try:
    original = target.read_text(encoding='utf-8') if target.exists() else ''
    lines = original.splitlines()
    roots = [i for i, line in enumerate(lines) if line and not line[0].isspace() and not line.lstrip().startswith('#')]
    sections = [i for i in roots if re.match(r'^custom_provider\\s*:', lines[i])]
    if len(sections) > 1:
        raise SystemExit('检测到多个 custom_provider 根键，未修改配置。')
    if sections and lines[sections[0]].strip() != 'custom_provider:':
        raise SystemExit('custom_provider 不是块映射格式，未修改配置。')
    if sections:
        start = sections[0]
        end = next((i for i in roots if i > start), len(lines))
        section = lines[start + 1:end]
        spans = []
        i = 0
        while i < len(section):
            if re.match(r'^  [^ #][^:]*:\\s*(?:#.*)?$', section[i]):
                j = i + 1
                while j < len(section) and (not section[j].strip() or section[j].startswith('   ')):
                    j += 1
                block = section[i:j]
                key = section[i].strip().split(':', 1)[0].strip('"\\'')
                name = any(re.match(r'^    name:\\s*["\\']?itestuu["\\']?\\s*(?:#.*)?$', x) for x in block)
                if key == 'itestuu' or name:
                    spans.append((i, j))
                i = j
            else:
                i += 1
        if spans:
            for a, b in reversed(spans):
                del section[a:b]
        addition = ['  ' + line if line else '' for line in provider.splitlines()]
        if section and section[-1].strip():
            section.append('')
        section.extend(addition)
        lines[start + 1:end] = section
        updated = '\\n'.join(lines) + ('\\n' if original.endswith('\\n') or lines else '')
    else:
        suffix = '' if not original else ('\\n' if original.endswith('\\n') else '\\n\\n')
        updated = original + suffix + 'custom_provider:\\n' + '\\n'.join('  ' + line if line else '' for line in provider.splitlines()) + '\\n'
    if target.exists():
        stamp = __import__('datetime').datetime.now().strftime('%Y%m%d%H%M%S%f')
        backup = target.with_name(target.name + '.backup.' + stamp)
        shutil.copy2(target, backup)
        os.chmod(backup, 0o600)
        mode = 0o600
        print('备份已创建:', backup)
    else:
        mode = 0o600
    fd, temp_name = tempfile.mkstemp(prefix=target.name + '.', dir=str(target.parent))
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            stream.write(updated)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temp_name, mode)
        os.replace(temp_name, target)
    finally:
        if os.path.exists(temp_name): os.unlink(temp_name)
    print('MiniMax Code / CC Switch 配置已写入:', target)
finally:
    try: lock.rmdir()
    except OSError: pass
`
    if (os === 'windows') {
      return `$ErrorActionPreference = 'Stop'\n$python = Get-Command python3 -ErrorAction SilentlyContinue\nif (-not $python) { throw '需要 Python 3 才能安全合并 YAML 配置。' }\n@'\n${python}'@ | & $python.Source -\nif ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }\n`
    }
    return `#!/bin/sh\n# MiniMax Code / CC Switch：备份并增量合并 itestuu。\npython3 - <<'PY'\n${python}PY\n`
  }

  const python = `import json, os, shutil, tempfile
from pathlib import Path

incoming = json.loads(${JSON.stringify(configContent)})
if not isinstance(incoming.get('provider'), dict) or len(incoming['provider']) != 1:
    raise SystemExit('生成的 provider JSON 无效，未修改配置。')
provider_id, provider = next(iter(incoming['provider'].items()))
if os.name == 'nt':
    appdata = Path(os.environ.get('APPDATA') or (Path.home() / 'AppData' / 'Roaming'))
    target = appdata / ('zcode/v2/config.json' if ${JSON.stringify(tool)} == 'zcode' else 'opencode/opencode.json')
elif ${JSON.stringify(tool)} == 'zcode':
    target = Path.home() / '.zcode' / 'v2' / 'config.json'
else:
    target = Path.home() / '.config' / 'opencode' / 'opencode.json'
target.parent.mkdir(parents=True, exist_ok=True)
current = json.loads(target.read_text(encoding='utf-8')) if target.exists() else {}
if not isinstance(current, dict):
    raise SystemExit('现有配置根节点不是 JSON 对象，未修改配置。')
providers = current.setdefault('provider', {})
if not isinstance(providers, dict):
    raise SystemExit('现有 provider 不是对象，未修改配置。')
for key, value in list(providers.items()):
    if isinstance(value, dict) and value.get('name') == provider.get('name'):
        del providers[key]
providers[provider_id] = provider
if target.exists():
    stamp = __import__('datetime').datetime.now().strftime('%Y%m%d%H%M%S%f')
    backup = target.with_name(target.name + '.backup.' + stamp)
    shutil.copy2(target, backup)
    os.chmod(backup, 0o600)
    mode = 0o600
    print('备份已创建:', backup)
else:
    mode = 0o600
fd, temp_name = tempfile.mkstemp(prefix=target.name + '.', dir=str(target.parent))
try:
    with os.fdopen(fd, 'w', encoding='utf-8') as stream:
        json.dump(current, stream, ensure_ascii=False, indent=2)
        stream.write('\\n')
        stream.flush()
        os.fsync(stream.fileno())
    os.chmod(temp_name, mode)
    os.replace(temp_name, target)
finally:
    if os.path.exists(temp_name): os.unlink(temp_name)
print('配置已增量写入:', target)
`
  if (os === 'windows') {
    return `$ErrorActionPreference = 'Stop'\n$python = Get-Command python3 -ErrorAction SilentlyContinue\nif (-not $python) { throw '需要 Python 3 才能安全合并 JSON 配置。' }\n@'\n${python}'@ | & $python.Source -\nif ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }\n`
  }
  return `#!/bin/sh\n# ${tool === 'zcode' ? 'ZCode' : 'OpenCode'}：备份并增量合并 itestuu。\npython3 - <<'PY'\n${python}PY\n`
}

export function getManualSteps(tool: ToolId, os: OS, baseURL: string, models: string[] = []): string {
  const modelList = models.map((model) => `   - ${model}`).join('\n') || '   - 先在网关数据库配置特色模型'
  if (tool === 'zcode') {
    return `ZCode 将在 ${OS_INFO[os].paths.zcode} 中增量添加 itestuu 供应商。脚本会备份原文件并保留其他供应商。\n\nAPI 地址：${baseURL}\n特色模型：\n${modelList}\n\n写入后重启 ZCode，在模型选择器中选择 itestuu。`
  }
  if (tool === 'minimax_code') {
    return `MiniMax Code 将使用 OpenAI Chat Completions 兼容模式。脚本会备份并增量更新 ${OS_INFO[os].paths.minimax_code}，同时遵守 config.yaml.lock。\n\nAPI 地址：${baseURL}\n特色模型：\n${modelList}\n\n保存后在 MiniMax Code 的模型选择器中选择 itestuu。CC Switch 的 MiniMax Code 页面会从该 custom_provider 配置同步识别它。`
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
    return `OpenCode 会在 ${OS_INFO[os].paths.opencode} 中增量添加 itestuu 供应商，并保留其他 provider。\n\nAPI 地址：${baseURL}\n特色模型：\n${modelList}\n\n写入后重启 OpenCode，并选择 itestuu 下的模型验证。`
  }
  if (tool === 'cherry_studio') {
    return `Cherry Studio 配置步骤：

1. 打开 Cherry Studio → 设置 → 模型服务
2. 点击「添加自定义服务商」
3. 名称填「itestuu」，类型选「OpenAI 兼容」
4. API 地址填：${baseURL}
5. API Key 填入你的 sk-* 密钥
6. 模型列表填入（用英文逗号分隔）：
${models.join(', ')}
7. 保存后在左侧模型列表找到「itestuu」即可使用`
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
    await fetch('/api/client-configs/audit', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(params),
    })
  } catch {
    // best-effort, non-blocking
  }
}
