import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createI18n } from 'vue-i18n'
import ClientConfigDialog from './ClientConfigDialog.vue'
import { getKeys, getClientConfigFeatured, listModels, revealKey } from '../api'
import { downloadFile } from '../composables/useClientConfig'
import zhCN from '../locales/zh-CN/clientConfigDialog'
import enUS from '../locales/en-US/clientConfigDialog'
vi.mock('vue-router',()=>({useRouter:()=>({push:vi.fn()})}))
vi.mock('../api',()=>({getKeys:vi.fn(),getClientConfigFeatured:vi.fn(),listModels:vi.fn(),revealKey:vi.fn(),applyForKey:vi.fn()}))
vi.mock('../composables/useClientConfig',async(importOriginal)=>({...await importOriginal<typeof import('../composables/useClientConfig')>(),auditAction:vi.fn(),downloadFile:vi.fn()}))
const key=(id:number,status='active',expires_at:string|null=null)=>({id,enabled:status==='active',status,expires_at,key_prefix:'sk-fixture',application_code:'test'})
const catalog={featured_models:Array.from({length:28},(_,i)=>'model-'+i),models:Array.from({length:28},(_,i)=>({id:'model-'+i,name:'model-'+i,contextWindow:32768}))}
const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': { clientConfigDialog: zhCN }, en: { clientConfigDialog: enUS } } })
beforeEach(()=>{
 i18n.global.locale.value = 'zh-CN'
 vi.clearAllMocks(); vi.mocked(getKeys).mockResolvedValue([key(1,'disabled'),key(2,'active','2000-01-01'),key(42)] as any)
 vi.mocked(getClientConfigFeatured).mockResolvedValue(catalog);vi.mocked(listModels).mockResolvedValue({items:[],total:0});vi.mocked(revealKey).mockResolvedValue({key_id:42,api_key:'sk-private-fixture'})
})
async function dialog(tool:'zcode'|'cursor'='zcode'){const view=mount(ClientConfigDialog,{props:{open:true,tool},global:{plugins:[i18n]}});await flushPromises();return view}
async function generate(view:any){await view.findAll('button').find((button:any)=>button.text()==='生成配置')!.trigger('click');await flushPromises()}
describe('client config dialog',()=>{
 it.each([
  { selector: 'input[type="url"]', value: 'not-a-url', error: 'invalidGatewayURL' as const },
  { selector: 'input[maxlength="64"]', value: 'invalid:name', error: 'invalidProviderName' as const },
 ])('localizes $error and updates it after a locale change without revealing a key', async ({ selector, value, error }) => {
  i18n.global.locale.value = 'en'
  const view = await dialog()
  const input = view.get(selector)
  await input.setValue(value)
  await view.findAll('button').find(button => button.text() === enUS.footer.generate)!.trigger('click')
  await flushPromises()
  expect(view.get('.alert-danger').text()).toBe(enUS.error[error])
  expect(revealKey).not.toHaveBeenCalled()
  i18n.global.locale.value = 'zh-CN'
  await flushPromises()
  expect(view.get('.alert-danger').text()).toBe(zhCN.error[error])
  expect(view.text()).not.toContain(enUS.error[error])
  expect((input.element as HTMLInputElement).value).toBe(value)
  expect(revealKey).not.toHaveBeenCalled()
  expect(view.find('.results-section').exists()).toBe(false)
  view.unmount()
 })
 it('updates labels and an existing error when the locale changes without resetting the form', async () => {
  vi.mocked(getClientConfigFeatured).mockRejectedValue(new Error('unavailable'))
  const view = await dialog()
  const provider = view.get('input[maxlength="64"]')
  await provider.setValue('my-provider')
  expect(view.get('.dialog-title').text()).toContain('配置生成器')
  expect(view.text()).toContain(zhCN.error.featuredLoadFailed)
  i18n.global.locale.value = 'en'
  await flushPromises()
  expect(view.get('.dialog-title').text()).toContain('Configuration Generator')
  expect(view.text()).toContain(enUS.error.featuredLoadFailed)
  expect(view.text()).not.toContain(zhCN.error.featuredLoadFailed)
  expect(provider.attributes('aria-label')).toBe(enUS.provider.label)
  expect((provider.element as HTMLInputElement).value).toBe('my-provider')
  expect(getClientConfigFeatured).toHaveBeenCalledTimes(1)
  view.unmount()
 })
 it('filters disabled/expired keys and exports all tenant featured models with alias metadata',async()=>{
  const view=await dialog();expect(view.findAll('select option')).toHaveLength(1);expect(getClientConfigFeatured).toHaveBeenCalledWith(42)
  await generate(view);expect(view.text()).toContain('已生成 28 个模型配置');expect(view.text()).not.toContain('sk-private-fixture')
  await view.findAll('button').find(button=>button.text()==='下载文件')!.trigger('click')
  const config=JSON.parse(vi.mocked(downloadFile).mock.calls[0][0]);const provider:any=Object.values(config.provider)[0]
  expect(provider.name).toBe('itestu');expect(Object.keys(provider.models)).toHaveLength(28);expect(provider.models['model-27'].limit.context).toBe(32768);expect(provider.options.apiKey).toBe('sk-private-fixture');view.unmount()
 })
 it('invalidates the old result when the destination changes',async()=>{
  const view=await dialog();await generate(view);await view.find('input[type="url"]').setValue('https://other.example/v1');expect(view.find('.results-section').exists()).toBe(false);view.unmount()
 })
 it('does not reveal keys for Cursor manual instructions',async()=>{
  const view=await dialog('cursor');await generate(view);expect(revealKey).not.toHaveBeenCalled();expect(view.text()).toContain('model-27');expect(view.text()).not.toContain('mcp.json');view.unmount()
 })
 it('drops a pending reveal result when the drawer closes',async()=>{
  let finish!:(value:any)=>void;vi.mocked(revealKey).mockReturnValue(new Promise(resolve=>{finish=resolve}))
  const view=await dialog();await view.findAll('button').find(button=>button.text()==='生成配置')!.trigger('click');await flushPromises();await view.setProps({open:false});finish({key_id:42,api_key:'sk-stale-fixture'});await flushPromises();await view.setProps({open:true});await flushPromises();expect(view.find('.results-section').exists()).toBe(false);expect(view.text()).not.toContain('sk-stale-fixture');view.unmount()
 })
 it('requires a successful featured load before revealing a key',async()=>{
  vi.mocked(getClientConfigFeatured).mockRejectedValue(new Error('unavailable'));const view=await dialog();await generate(view);expect(revealKey).not.toHaveBeenCalled();expect(view.text()).toContain('特色模型加载失败');view.unmount()
 })
 it('retries the all-model catalog and clears its error independently',async()=>{
  vi.mocked(listModels).mockRejectedValueOnce(new Error('unavailable')).mockResolvedValue({items:[{canonical_name:'retry-model',status:'active'}],total:1} as any)
  const view=await dialog();await view.find('input[value="all"]').setValue();await flushPromises();expect(view.text()).toContain('模型目录加载失败')
  await view.findAll('button').find(button=>button.text()==='重试')!.trigger('click');await flushPromises();expect(listModels).toHaveBeenCalledTimes(2);expect(getClientConfigFeatured).toHaveBeenCalledTimes(1);expect(view.text()).not.toContain('模型目录加载失败');expect(view.text()).toContain('retry-model');view.unmount()
 })
 it('masks JSON-escaped secrets in the preview while retaining them in the download',async()=>{
  const secret='sk-quoted"fixture\\newline\n';vi.mocked(revealKey).mockResolvedValue({key_id:42,api_key:secret})
  const view=await dialog();await generate(view);expect(view.text()).not.toContain('quoted');await view.findAll('button').find(button=>button.text()==='下载文件')!.trigger('click')
  expect((Object.values(JSON.parse(vi.mocked(downloadFile).mock.calls[0][0]).provider)[0] as any).options.apiKey).toBe(secret);view.unmount()
 })
})
