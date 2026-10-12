import { execFileSync } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, dirname, join } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { generateShellScript, renderMiniMaxCodeProvider, renderZCodeConfig, renderOpenCodeConfig, getManualSteps, OS_INFO, normalizeGatewayURL, validateProviderName, auditAction } from './useClientConfig'

const homes: string[] = []
const URL = 'https://llm.itestu.cn/v1'
// A temporary HOME hides user-installed PyYAML on macOS. Keep the real module
// search path available to fixture scripts without touching the user's config.
const yamlSite = execFileSync('python3', ['-c', 'import pathlib,yaml; print(pathlib.Path(yaml.__file__).parent.parent)'], {encoding:'utf8'}).trim()
const pythonPath = [yamlSite, process.env.PYTHONPATH].filter(Boolean).join(delimiter)
function home() { const path = mkdtempSync(join(tmpdir(), 'client-config-')); homes.push(path); return path }
function run(path: string, script: string) { execFileSync('sh', ['-c', script], { env: { ...process.env, HOME: path, PYTHONPATH: pythonPath, OPENCODE_CONFIG: '', XDG_CONFIG_HOME: '', MINIMAX_PROFILE: '', MINIMAX_DATA_DIR: '', MAVIS_DATA_DIR: '' }, stdio: 'pipe' }) }
function jsonScript(config = renderZCodeConfig('sk-test', ['model-a'], URL)) { return generateShellScript('zcode', 'macos', JSON.stringify(config)) }
function yamlScript(key = 'sk-test') {
 const provider = renderMiniMaxCodeProvider(key, ['model:a'], URL, [{id:'model:a',name:'model:a',contextWindow:32000}])
 return generateShellScript('minimax_code', 'macos', 'custom_provider:\n' + provider.split('\n').filter(Boolean).map(l => '  ' + l).join('\n') + '\n')
}
function write(path: string, text: string) { mkdirSync(dirname(path), {recursive:true}); writeFileSync(path, text); chmodSync(path, 0o644) }
function backups(path: string) { return readdirSync(dirname(path)).filter(n => n.startsWith(path.split('/').at(-1)! + '.backup.')) }
function yaml(text: string) { return JSON.parse(execFileSync('python3', ['-c', 'import json,sys,yaml; print(json.dumps(yaml.safe_load(sys.stdin.read())))'], {input:text,encoding:'utf8'})) }
afterEach(() => { homes.splice(0).forEach(path => rmSync(path, {recursive:true,force:true})); vi.restoreAllMocks() })

describe('safe client installers', () => {
 it('keeps provider identity, defaults, unrelated options and models across regenerations', () => {
  const dir=home(), path=join(dir,'.zcode/v2/config.json')
  const initial={model:'stable/model-a', provider:{stable:{name:'itestu',kind:'openai-compatible',options:{baseURL:URL,headers:{custom:'value'}},models:{'model-a':{custom:true}, extra:{name:'extra'}}},other:{name:'itestuu',options:{baseURL:'https://other.example/v1'}}}}
  write(path,JSON.stringify(initial)); const config=renderZCodeConfig('sk-test',['model-a'],URL)
  run(dir,jsonScript(config)); config.provider.fresh = Object.values(config.provider)[0]; delete config.provider[Object.keys(config.provider)[0]]
  run(dir,jsonScript(config))
  const result=JSON.parse(readFileSync(path,'utf8'))
  expect(Object.keys(result.provider)).toEqual(['stable','other']); expect(result.model).toBe('stable/model-a')
  expect(result.provider.stable.options.headers).toEqual({custom:'value'}); expect(result.provider.stable.models.extra.name).toBe('extra'); expect(result.provider.stable.models['model-a'].custom).toBe(true)
  expect(result.provider.stable.options.apiKey).toBe('sk-test'); expect(statSync(path).mode & 0o777).toBe(0o600)
  expect(backups(path)).toHaveLength(2); backups(path).forEach(n=>expect(statSync(join(dirname(path),n)).mode & 0o777).toBe(0o600))
 })
 it.each(['{"provider":{"id":{"name":"itestu","options":{"baseURL":"https://other.example/v1"}}}}','{"provider":{},"provider":{}}','{broken','[]'])('refuses conflicts or invalid JSON without changing bytes: %s', initial => {
  const dir=home(),path=join(dir,'.zcode/v2/config.json'); write(path,initial)
  expect(()=>run(dir,jsonScript())).toThrow(); expect(readFileSync(path,'utf8')).toBe(initial); expect(backups(path)).toHaveLength(0); expect(existsSync(path+'.gateway-setup.lock')).toBe(false)
 })
 it('refuses an occupied lock and symlinks', () => {
  const dir=home(),path=join(dir,'.zcode/v2/config.json'); write(path,'{}'); mkdirSync(path+'.gateway-setup.lock')
  expect(()=>run(dir,jsonScript())).toThrow(); expect(readFileSync(path,'utf8')).toBe('{}'); rmSync(path+'.gateway-setup.lock',{recursive:true}); rmSync(path); write(join(dir,'actual'),'{}'); symlinkSync(join(dir,'actual'),path)
  expect(()=>run(dir,jsonScript())).toThrow(); expect(readFileSync(join(dir,'actual'),'utf8')).toBe('{}')
 })
 it('round-trips malicious quotes, backslashes, Unicode and shell delimiters as data', () => {
  const dir=home(),key=`sk-'"\\\nPY\n$(touch ${dir}/injected)\n'@\n中文`
  run(dir,jsonScript(renderZCodeConfig(key,['model"\\\n中文'],URL)))
  const provider:any=Object.values(JSON.parse(readFileSync(join(dir,'.zcode/v2/config.json'),'utf8')).provider)[0]
  expect(provider.options.apiKey).toBe(key); expect(provider.models['model"\\\n中文']).toBeDefined(); expect(existsSync(join(dir,'injected'))).toBe(false)
 })
 it('structurally merges YAML, keeps sibling bytes and repeats without duplicates', () => {
  const dir=home(),path=join(dir,'.minimax/config.yaml')
  const initial='# root comment\ndefaultModel: itestu/model:a\ncustom_provider:\n  other:\n    name: other # sibling comment\n    options: {baseURL: https://old.example/v1}\n  itestu:\n    name: itestu\n    options:\n      baseURL: '+URL+'\n      headers: {custom: value}\n    models:\n      extra: {name: extra}\nother_setting: preserved' // no final newline
  write(path,initial); run(dir,yamlScript()); run(dir,yamlScript())
  const text=readFileSync(path,'utf8'), result=yaml(text)
  expect(text).toContain('    name: other # sibling comment'); expect(result.other_setting).toBe('preserved'); expect(result.defaultModel).toBe('itestu/model:a'); expect(Object.keys(result.custom_provider)).toEqual(['other','itestu'])
  expect(result.custom_provider.itestu.options.headers.custom).toBe('value'); expect(result.custom_provider.itestu.options.apiKey).toBe('sk-test'); expect(result.custom_provider.itestu.models['model:a'].limit.context).toBe(32000); expect(result.custom_provider.itestu.models.extra.name).toBe('extra'); expect(backups(path)).toHaveLength(2); expect(existsSync(join(dirname(path),'config.yaml.lock'))).toBe(false)
 })
 it.each(['custom_provider:\n  other: {name: first}\n  other: {name: second}\n', 'custom_provider: {}\n','custom_provider:\n  itestu: {}\n  itestu: {}\n','custom_provider: [invalid\n','custom_provider:\n  itestu:\n    name: itestu\n    options: {baseURL: https://other.example/v1}\n'])('rejects unsafe YAML: %s', initial=>{
  const dir=home(),path=join(dir,'.minimax/config.yaml'); write(path,initial)
  expect(()=>run(dir,yamlScript())).toThrow(); expect(readFileSync(path,'utf8')).toBe(initial); expect(backups(path)).toHaveLength(0); expect(existsSync(join(dirname(path),'config.yaml.lock'))).toBe(false)
 })
 it('adds YAML section to config without a final newline, preserving escaped keys',()=>{
  const dir=home(),path=join(dir,'.minimax/config.yaml'),key='sk-"\\中文\nnew'; write(path,'setting: preserved'); run(dir,yamlScript(key))
  const result=yaml(readFileSync(path,'utf8')); expect(result.setting).toBe('preserved'); expect(result.custom_provider.itestu.options.apiKey).toBe(key)
 })
 it('uses the OpenCode v1 npm adapter and protects v2 files',()=>{
  const config=renderOpenCodeConfig('sk-test',['model'],URL); expect(config.provider.itestu.npm).toBe('@ai-sdk/openai-compatible'); expect(config.provider.itestu.kind).toBeUndefined(); expect(config.provider.itestu.source).toBeUndefined(); const incomplete=renderOpenCodeConfig('sk-test',['model'],URL,[{id:'model',name:'model',contextWindow:32000}]); expect(incomplete.provider.itestu.models.model.limit).toBeUndefined()
  const dir=home(),path=join(dir,'.config/opencode/opencode.json'); write(path,'{"providers":{}}'); expect(()=>run(dir,generateShellScript('opencode','macos',JSON.stringify(config)))).toThrow(); expect(readFileSync(path,'utf8')).toBe('{"providers":{}}')
 })
 it('makes manual clients use their settings panel and an actual model ID',()=>{
  expect(OS_INFO.macos.paths.cursor).not.toContain('mcp.json'); expect(getManualSteps('roocode','macos',URL,['model-a'])).toContain('model-a'); expect(generateShellScript('roocode','windows','{}')).not.toContain('Set-Content'); expect(getManualSteps('minimax_code','macos',URL)).toContain('独立的供应商库')
 })
 it('rejects unsafe URLs and invalid provider names',()=>{
  expect(normalizeGatewayURL(URL+'/')).toBe(URL); for(const value of ['ftp://host','https://user:pass@host/v1','https://host/v1?key=secret','https://host/v1#x']) expect(()=>normalizeGatewayURL(value)).toThrow()
  expect(validateProviderName('itestu')).toBe('itestu'); for(const name of ['','a:b','a\nPY','a/b']) expect(()=>validateProviderName(name)).toThrow()
 })
 it('uses authenticated API plumbing and snake_case audit fields',async()=>{
  const fetch=vi.spyOn(globalThis,'fetch').mockResolvedValue(new Response('{"ok":true}',{headers:{'Content-Type':'application/json'}}))
  await auditAction({action:'generate',tool:'minimax_code',os:'macos',keyId:42,modelCount:28,modelScope:'featured'})
  const options=fetch.mock.calls[0][1]!; expect(JSON.parse(options.body as string)).toEqual({action:'generate',tool:'minimax_code',os:'macos',key_id:42,model_count:28,model_scope:'featured'})
 })
})
