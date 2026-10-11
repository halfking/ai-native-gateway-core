import { execFileSync } from 'node:child_process'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import {
  generateShellScript,
  renderMiniMaxCodeProvider,
  renderZCodeConfig,
} from './useClientConfig'

const tempDirs: string[] = []

function makeHome(): string {
  const dir = mkdtempSync(join(tmpdir(), 'client-config-'))
  tempDirs.push(dir)
  return dir
}

function runScript(home: string, script: string): void {
  execFileSync('sh', ['-c', script], {
    env: { ...process.env, HOME: home },
    stdio: 'pipe',
  })
}

afterEach(() => {
  for (const dir of tempDirs.splice(0)) rmSync(dir, { recursive: true, force: true })
})

describe('client config generation scripts', () => {
  it('merges ZCode provider while preserving existing providers and backing up the file', () => {
    const home = makeHome()
    const configPath = join(home, '.zcode', 'v2', 'config.json')
    const configDir = join(home, '.zcode', 'v2')
    const initial = {
      provider: {
        existing: { name: 'existing-provider', options: { baseURL: 'https://old.example/v1' } },
        oldTest: { name: 'itestuu', options: { baseURL: 'https://old.example/v1' } },
      },
      unrelatedSetting: true,
    }
    const generated = JSON.stringify(renderZCodeConfig('sk-test-only', ['model-a'], 'https://llm.itestu.cn/v1'))

    mkdirSync(configDir, { recursive: true })
    writeFileSync(configPath, JSON.stringify(initial, null, 2))
    chmodSync(configPath, 0o644)
    const script = generateShellScript('zcode', 'macos', generated, 'sk-test-only', 'https://llm.itestu.cn/v1')
    runScript(home, script)

    const result = JSON.parse(readFileSync(configPath, 'utf8'))
    expect(result.unrelatedSetting).toBe(true)
    expect(result.provider.existing.name).toBe('existing-provider')
    expect(result.provider.oldTest).toBeUndefined()
    const newProvider = Object.values(result.provider).find((value: any) => value.name === 'itestuu') as any
    expect(newProvider.options.baseURL).toBe('https://llm.itestu.cn/v1')
    expect(newProvider.options.apiKey).toBe('sk-test-only')
    expect(statSync(configPath).mode & 0o777).toBe(0o600)

    const backups = readdirSync(configDir).filter((name) => name.startsWith('config.json.backup.'))
    expect(backups).toHaveLength(1)
    expect(readFileSync(join(configDir, backups[0]), 'utf8')).toContain('existing-provider')
  })

  it('merges MiniMax Code provider under custom_provider, preserving siblings and lock cleanup', () => {
    const home = makeHome()
    const configDir = join(home, '.minimax')
    const configPath = join(configDir, 'config.yaml')
    const initial = [
      'defaultModel: minimax/base-model',
      'custom_provider:',
      '  existing:',
      '    name: existing',
      '    api: openai-completions',
      '  itestuu:',
      '    name: itestuu',
      '    api: old-api',
      'other_setting: preserved',
      '',
    ].join('\n')
    const fragment = `custom_provider:\n${renderMiniMaxCodeProvider('sk-test-only', ['model:a'], 'https://llm.itestu.cn/v1')
      .split('\n').filter(Boolean).map((line) => `  ${line}`).join('\n')}\n`

    const script = generateShellScript('minimax_code', 'macos', fragment, 'sk-test-only', 'https://llm.itestu.cn/v1')
    mkdirSync(configDir, { recursive: true })
    writeFileSync(configPath, initial)
    runScript(home, script)

    const result = readFileSync(configPath, 'utf8')
    expect(result).toContain('defaultModel: minimax/base-model')
    expect(result).toContain('  existing:\n    name: existing')
    expect(result).toContain('other_setting: preserved')
    expect(result).toContain('    api: openai-completions')
    expect(result).toContain('baseURL: "https://llm.itestu.cn/v1"')
    expect(result).toContain('apiKey: "sk-test-only"')
    expect(result.match(/^custom_provider:$/gm)).toHaveLength(1)
    expect(result).not.toContain('\\ncustom_provider:')
    expect(result).toContain('  itestuu:\n    name: itestuu\n    kind: custom')
    expect(result.match(/^  itestuu:$/gm)).toHaveLength(1)
    expect(readdirSync(configDir).some((name) => name === 'config.yaml.lock')).toBe(false)
    expect(readdirSync(configDir).some((name) => name.startsWith('config.yaml.backup.'))).toBe(true)
    expect(statSync(configPath).mode & 0o777).toBe(0o600)
  })
})
