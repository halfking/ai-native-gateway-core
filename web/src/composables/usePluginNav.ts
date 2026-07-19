// usePluginNav.ts — 拉取并缓存插件菜单条目。
import { ref } from 'vue'
import { fetchPluginNav, type NavEntry } from '@/api/plugins'

const pluginNav = ref<NavEntry[]>([])
let loaded = false

export function usePluginNav() {
  async function load() {
    pluginNav.value = await fetchPluginNav()
    loaded = true
  }
  if (!loaded) {
    load().catch(() => { loaded = true })
  }
  return { pluginNav, reload: load }
}
